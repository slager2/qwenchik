//go:build linux

package keepawake

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func fakeProcess(token string, mode string) (*process, <-chan struct{}, *atomic.Int32) {
	childIn, parentIn := io.Pipe()
	parentOut, childOut := io.Pipe()
	done := make(chan struct{})
	kills := &atomic.Int32{}
	var once sync.Once
	finish := func() { once.Do(func() { _ = childIn.Close(); _ = childOut.Close(); close(done) }) }
	go func() {
		defer finish()
		r := bufio.NewReader(childIn)
		_, _ = r.ReadString('\n')
		switch mode {
		case "ready":
			_, _ = io.WriteString(childOut, "READY "+token+"\n")
		case "wrong":
			_, _ = io.WriteString(childOut, "READY "+strings.Repeat("0", 64)+"\n")
		case "exit":
			return
		}
		_, _ = io.Copy(io.Discard, r)
	}()
	return &process{input: parentIn, output: parentOut, wait: func() error { <-done; return nil }, kill: func() { kills.Add(1); finish() }}, done, kills
}

func TestAcquireRequiresVerifiedReadiness(t *testing.T) {
	for _, mode := range []string{"ready", "wrong", "silent", "exit"} {
		t.Run(mode, func(t *testing.T) {
			var done <-chan struct{}
			var kills *atomic.Int32
			p := &linuxProvider{timeout: 30 * time.Millisecond, launch: func(token string) (*process, error) {
				proc, d, k := fakeProcess(token, mode)
				done, kills = d, k
				return proc, nil
			}}
			release, status, err := p.Acquire(context.Background(), "run-secret-untrusted")
			if mode == "ready" {
				if err != nil || !status.Available || !status.Active || status.FailureReason != "" {
					t.Fatalf("acquired %+v %v", status, err)
				}
			} else if !errors.Is(err, ErrInhibitorUnavailable) || status.Available || status.Active || status.FailureReason == "" {
				t.Fatalf("failure %+v %v", status, err)
			}
			if strings.Contains(status.FailureReason, "run-secret") {
				t.Fatal("run ID leaked")
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); release() }()
			}
			wg.Wait()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cleanup did not wait")
			}
			if kills.Load() != 1 {
				t.Fatalf("kill count %d", kills.Load())
			}
		})
	}
}

func TestUnavailableAndCanceled(t *testing.T) {
	p := &linuxProvider{timeout: time.Second, launch: func(string) (*process, error) { return nil, errors.New("secret stderr") }}
	release, status, err := p.Acquire(context.Background(), "run")
	release()
	if !errors.Is(err, ErrInhibitorUnavailable) || status.Available || status.Active || strings.Contains(status.FailureReason, "secret") {
		t.Fatalf("unavailable %+v %v", status, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.launch = func(string) (*process, error) { t.Fatal("launched with canceled context"); return nil, nil }
	_, status, err = p.Acquire(ctx, "run")
	if !errors.Is(err, context.Canceled) || status.Active {
		t.Fatalf("canceled %+v %v", status, err)
	}
}

func TestCancelDuringAcquisitionAndAfterReady(t *testing.T) {
	for _, mode := range []string{"silent", "ready"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var done <-chan struct{}
			p := &linuxProvider{timeout: time.Second, launch: func(token string) (*process, error) {
				proc, d, _ := fakeProcess(token, mode)
				done = d
				return proc, nil
			}}
			if mode == "silent" {
				time.AfterFunc(20*time.Millisecond, cancel)
			}
			release, status, err := p.Acquire(ctx, "run")
			if mode == "silent" {
				if !errors.Is(err, context.Canceled) || status.Active {
					t.Fatalf("cancel %+v %v", status, err)
				}
			} else {
				if err != nil || !status.Active {
					t.Fatalf("ready %+v %v", status, err)
				}
				cancel()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("context cleanup leaked child")
			}
			release()
		})
	}
}

func directHelper(t *testing.T, token string) (*process, *exec.Cmd) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, helperArg)
	proc, err := startHelperCommand(cmd, token)
	if err != nil {
		t.Fatal(err)
	}
	return proc, cmd
}

func TestNativeReexecAndReap(t *testing.T) {
	var cmd *exec.Cmd
	p := &linuxProvider{timeout: 3 * time.Second, launch: func(token string) (*process, error) {
		var proc *process
		proc, cmd = directHelper(t, token)
		return proc, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, status, err := p.Acquire(ctx, "run")
	if err != nil || !status.Active {
		t.Fatalf("native helper %+v %v", status, err)
	}
	if cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL || !cmd.SysProcAttr.Setpgid {
		t.Fatal("missing process protection")
	}
	release()
	release()
	if cmd.ProcessState == nil || !cmd.ProcessState.Exited() && cmd.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child not reaped: %v", cmd.ProcessState)
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child still exists: %v", err)
	}
}

func TestNativeHelperExitsWhenParentPipeCloses(t *testing.T) {
	token := strings.Repeat("a", 64)
	proc, cmd := directHelper(t, token)
	t.Cleanup(func() { proc.kill(); _ = proc.input.Close(); _ = proc.output.Close() })
	if _, err := io.WriteString(proc.input, token+"\n"); err != nil {
		t.Fatal(err)
	}
	expected := "READY " + token + "\n"
	buf := make([]byte, len(expected))
	if _, err := io.ReadFull(proc.output, buf); err != nil || string(buf) != expected {
		t.Fatalf("ready %q %v", buf, err)
	}
	_ = proc.input.Close()
	done := make(chan error, 1)
	go func() { done <- proc.wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		proc.kill()
		<-done
		t.Fatal("orphan after parent pipe closed")
	}
	if !cmd.ProcessState.Success() {
		t.Fatalf("helper failed: %v", cmd.ProcessState)
	}
}

func TestNativeHelperRejectsInvalidToken(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, helperArg)
	cmd.Env = helperEnvironment("invalid")
	if err := cmd.Run(); err == nil {
		t.Fatal("invalid helper token accepted")
	}
	if ctx.Err() != nil {
		t.Fatal("helper started application instead of rejecting token")
	}
	if cmd.ProcessState.ExitCode() != 126 {
		t.Fatalf("unexpected exit %v", cmd.ProcessState)
	}
}

func TestStderrBounded(t *testing.T) {
	b := &discardBounded{remaining: 16}
	for i := 0; i < 3; i++ {
		if n, err := b.Write([]byte(strings.Repeat("s", 100))); n != 100 || err != nil {
			t.Fatalf("write %d %v", n, err)
		}
	}
	if len(b.data) != 16 || b.remaining != 0 {
		t.Fatalf("stderr exceeded cap: %d", len(b.data))
	}
}
