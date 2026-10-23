//go:build windows

package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsFetchTempQuotaPrivacyAndCleanup(t *testing.T) {
	policy := smallFetchPolicy(t)
	policy.TempRoot = filepath.Join(t.TempDir(), "private-fetch")
	policy.TempTaskBytes = 1024
	policy.TempGlobalBytes = 2048
	lease, err := createFetchTemp(context.Background(), policy, "task", 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.cleanup()
	path := lease.file.Name()
	markerPath := filepath.Join(policy.TempRoot, lease.markerName)
	if _, err := createFetchTemp(context.Background(), policy, "task", 1); err == nil {
		t.Fatal("task quota was bypassed")
	}
	if err := verifyFetchPrivateACL(windows.Handle(lease.file.Fd())); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active lease was removed: %v", err)
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("active lease marker was removed: %v", err)
	}
	lease.cleanup()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fetch temporary survived cleanup: %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fetch provenance marker survived cleanup: %v", err)
	}
}

func TestWindowsFetchTempPreservesUnmarkedMatchingFile(t *testing.T) {
	policy := smallFetchPolicy(t)
	policy.TempRoot = filepath.Join(t.TempDir(), "private-fetch")
	policy.TempTaskBytes = 100
	policy.TempGlobalBytes = 1000
	const task = "orphan"
	root, err := openFetchRoot(policy.TempRoot)
	if err != nil {
		t.Fatal(err)
	}
	name := windowsTestFetchName(t, task)
	handle, err := createFetchFileAt(root, name)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), name)
	if err := file.Truncate(100); err != nil {
		file.Close()
		root.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		root.Close()
		t.Fatal(err)
	}
	root.Close()

	if _, err := createFetchTemp(context.Background(), policy, task, 1); err == nil {
		t.Fatal("unmarked file was not conservatively charged against quota")
	}
	if _, err := os.Stat(filepath.Join(policy.TempRoot, name)); err != nil {
		t.Fatalf("unmarked matching file was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(policy.TempRoot, fetchMarkerName(name))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected provenance marker for unmarked file: %v", err)
	}
}

func TestWindowsFetchTempRecoversValidStaleMarker(t *testing.T) {
	policy := smallFetchPolicy(t)
	policy.TempRoot = filepath.Join(t.TempDir(), "private-fetch")
	policy.TempTaskBytes = 1024
	policy.TempGlobalBytes = 2048
	const task = "stale"
	root, err := openFetchRoot(policy.TempRoot)
	if err != nil {
		t.Fatal(err)
	}
	name := windowsTestFetchName(t, task)
	handle, err := createFetchFileAt(root, name)
	if err != nil {
		root.Close()
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(handle), name)
	if err := file.Truncate(100); err != nil {
		file.Close()
		root.Close()
		t.Fatal(err)
	}
	markerName, err := createFetchMarker(root, name, handle)
	if err != nil {
		file.Close()
		root.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		root.Close()
		t.Fatal(err)
	}
	root.Close()

	if _, err := createFetchTemp(context.Background(), policy, task, 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(policy.TempRoot, name), filepath.Join(policy.TempRoot, markerName)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale lease entry survived recovery (%s): %v", filepath.Base(path), err)
		}
	}
}

func windowsTestFetchName(t *testing.T, task string) string {
	t.Helper()
	prefix, err := fetchOwnerPrefix()
	if err != nil {
		t.Fatal(err)
	}
	taskHash := sha256.Sum256([]byte(task))
	return prefix + hex.EncodeToString(taskHash[:8]) + "-" + strings.Repeat("a", 32)
}
