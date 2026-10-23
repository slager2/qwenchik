//go:build windows

package workerlock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const workerLockTestPathEnv = "QWENCHIK_WORKERLOCK_TEST_PATH"
const workerLockTestHoldEnv = "QWENCHIK_WORKERLOCK_TEST_HOLD"

func TestWindowsWorkerLockChildProcess(t *testing.T) {
	path := os.Getenv(workerLockTestPathEnv)
	if path == "" {
		return
	}

	lock, err := Acquire(path)
	if errors.Is(err, ErrAlreadyRunning) {
		return
	}
	if err != nil {
		t.Fatalf("acquire lock in child process: %v", err)
	}
	_ = lock.Close()
	t.Fatal("child process acquired a lock already held by another process")
}

func TestWindowsWorkerLockHoldingChildProcess(t *testing.T) {
	path := os.Getenv(workerLockTestPathEnv)
	if path == "" || os.Getenv(workerLockTestHoldEnv) != "1" {
		return
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire lock in child process: %v", err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatalf("signal lock acquisition: %v", err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsWorkerLockExcludesAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsWorkerLockChildProcess$")
	cmd.Env = append(os.Environ(), workerLockTestPathEnv+"="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child process lock check failed: %v\n%s", err, output)
	}
}

func TestWindowsWorkerLockContextCancellationReleasesProcessLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.lock")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsWorkerLockHoldingChildProcess$")
	cmd.Env = append(os.Environ(), workerLockTestPathEnv+"="+path, workerLockTestHoldEnv+"=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer cancel()

	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "locked\n" {
			err = fmt.Errorf("unexpected child output %q", line)
		}
		ready <- err
	}()

	select {
	case err := <-ready:
		if err != nil {
			cancel()
			_ = cmd.Wait()
			t.Fatalf("wait for child lock: %v", err)
		}
	case <-time.After(10 * time.Second):
		cancel()
		_ = cmd.Wait()
		t.Fatal("child process did not acquire the lock")
	}
	_ = stdin

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("child process exited successfully instead of being canceled")
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire lock after child cancellation: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsWorkerLockReleaseReacquireAndCloseSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second lock error = %v, want ErrAlreadyRunning", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	var nilLock *Lock
	if err := nilLock.Close(); err != nil {
		t.Fatalf("close nil lock: %v", err)
	}

	second, err := Acquire(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsWorkerLockRejectsReparsePaths(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target.lock")
	targetLock, err := Acquire(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := targetLock.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "reparse.lock")
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("creating a Windows symlink requires additional privileges: %v", err)
		}
		t.Fatalf("create file symlink: %v", err)
	}
	if lock, err := Acquire(link); err == nil {
		_ = lock.Close()
		t.Fatal("worker lock followed a reparse point")
	}
}

func TestWindowsWorkerLockRejectsAncestorReparsePathWithoutOpeningTarget(t *testing.T) {
	root := t.TempDir()
	externalTarget := t.TempDir()
	link := filepath.Join(root, "redirect")
	if err := os.Symlink(externalTarget, link); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skipf("creating a Windows directory symlink requires additional privileges: %v", err)
		}
		t.Fatalf("create ancestor directory symlink: %v", err)
	}

	externalLockPath := filepath.Join(externalTarget, "worker.lock")
	if lock, err := Acquire(filepath.Join(link, "worker.lock")); err == nil {
		_ = lock.Close()
		t.Fatal("worker lock followed an ancestor reparse point")
	}
	if _, err := os.Lstat(externalLockPath); !os.IsNotExist(err) {
		t.Fatalf("external target was opened or modified; lock file stat error = %v", err)
	}
}

func TestWindowsWorkerLockAllowsSafeCurrentDirectoryComponent(t *testing.T) {
	if err := rejectParentTraversal(filepath.Join(".", "data", "worker.lock")); err != nil {
		t.Fatalf("safe current-directory component rejected: %v", err)
	}
	if err := rejectParentTraversal(filepath.Join("..", "worker.lock")); err == nil {
		t.Fatal("parent traversal accepted")
	}
}
