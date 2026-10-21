//go:build linux

package keepawake

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type process struct {
	input  io.WriteCloser
	output io.ReadCloser
	wait   func() error
	kill   func()
}

type linuxProvider struct {
	launch  func(string) (*process, error)
	timeout time.Duration
}

func newPlatformProvider() Provider {
	return &linuxProvider{launch: launchInhibitor, timeout: 3 * time.Second}
}

func (p *linuxProvider) Acquire(ctx context.Context, _ string) (func(), Status, error) {
	status := Status{Provider: "systemd-inhibit", PowerSource: PowerUnknown}
	noop := func() {}
	if err := ctx.Err(); err != nil {
		status.FailureReason = "acquisition canceled"
		return noop, status, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		status.FailureReason = "helper token generation failed"
		return noop, status, ErrInhibitorUnavailable
	}
	token := hex.EncodeToString(random[:])
	proc, err := p.launch(token)
	if err != nil {
		status.FailureReason = "inhibitor could not be started"
		return noop, status, ErrInhibitorUnavailable
	}
	done := make(chan struct{})
	go func() { _ = proc.wait(); close(done) }()
	var once sync.Once
	stop := make(chan struct{})
	release := func() {
		once.Do(func() {
			close(stop)
			_ = proc.input.Close()
			proc.kill()
			_ = proc.output.Close()
			<-done
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			release()
		case <-done:
			release()
		case <-stop:
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		if _, err := io.WriteString(proc.input, token+"\n"); err != nil {
			ready <- false
			return
		}
		expected := "READY " + token + "\n"
		buf := make([]byte, len(expected))
		_, err := io.ReadFull(proc.output, buf)
		ready <- err == nil && string(buf) == expected
	}()
	timer := time.NewTimer(p.timeout)
	defer timer.Stop()
	failed := "inhibitor helper did not become ready"
	select {
	case ok := <-ready:
		if ok {
			select {
			case <-done:
				ok = false
			default:
			}
			if ctx.Err() != nil {
				ok = false
			}
		}
		if ok {
			status.Available, status.Active = true, true
			return release, status, nil
		}
	case <-ctx.Done():
		failed = "acquisition canceled"
	case <-done:
		failed = "inhibitor exited before acquisition"
	case <-timer.C:
		failed = "inhibitor acquisition timed out"
	}
	release()
	status.FailureReason = failed
	if ctx.Err() != nil {
		return noop, status, ctx.Err()
	}
	return noop, status, ErrInhibitorUnavailable
}

func launchInhibitor(token string) (*process, error) {
	inhibitor, err := exec.LookPath("/usr/bin/systemd-inhibit")
	if err != nil {
		inhibitor, err = exec.LookPath("/bin/systemd-inhibit")
	}
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(inhibitor, "--what=sleep:idle", "--mode=block", "--who=Qwenchik", "--why=Active local research run", "--", executable, helperArg)
	return startHelperCommand(cmd, token)
}

func startHelperCommand(cmd *exec.Cmd, token string) (*process, error) {
	cmd.Env = helperEnvironment(token)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, err
	}
	cmd.Stderr = &discardBounded{remaining: 4096}
	cmd.WaitDelay = 200 * time.Millisecond
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, err
	}
	return &process{input: input, output: output, wait: cmd.Wait, kill: func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }}, nil
}

type discardBounded struct {
	remaining int
	data      []byte
}

func (b *discardBounded) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	b.remaining -= len(p)
	b.data = append(b.data, p...)
	return n, nil
}
