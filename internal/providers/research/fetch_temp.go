package research

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ownedFetchName = regexp.MustCompile(`^[a-f0-9]{16}-[a-f0-9]{32}$`)

type fetchTemp struct {
	file *os.File
	root *os.File
	name string
	once sync.Once
}

func (t *fetchTemp) cleanup() {
	t.once.Do(func() { _ = syscall.Unlinkat(int(t.root.Fd()), t.name); _ = t.file.Close(); _ = t.root.Close() })
}

func openFetchRoot(path string) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		err = syscall.Mkdirat(fd, part, 0700)
		if err != nil && err != syscall.EEXIST {
			syscall.Close(fd)
			return nil, err
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	root := os.NewFile(uintptr(fd), absolute)
	info, err := root.Stat()
	if err != nil {
		root.Close()
		return nil, err
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != uint32(os.Geteuid()) {
		root.Close()
		return nil, errors.New("fetch temp root must be private and owned")
	}
	if err := root.Chmod(0700); err != nil {
		root.Close()
		return nil, err
	}
	return root, nil
}

func createFetchTemp(ctx context.Context, p FetchPolicy, task string, reserve int64) (*fetchTemp, error) {
	root, err := openFetchRoot(p.TempRoot)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			root.Close()
		}
	}()
	lockFD, err := syscall.Openat(int(root.Fd()), ".fetch-quota.lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(lockFD), "quota")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("invalid fetch quota lock")
	}
	if err := lockFetchQuota(ctx,lockFD); err != nil {
		return nil, err
	}
	defer syscall.Flock(lockFD, syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(task))
	prefix := fmt.Sprintf("fetch-%d-", os.Geteuid())
	taskPrefix := prefix + hex.EncodeToString(digest[:8]) + "-"
	names, err := root.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var global, local int64
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) || !ownedFetchName.MatchString(strings.TrimPrefix(name, prefix)) {
			continue
		}
		fd, e := syscall.Openat(int(root.Fd()), name, syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if e != nil {
			continue
		}
		file := os.NewFile(uintptr(fd), name)
		info, e := file.Stat()
		if e != nil || !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			file.Close()
			continue
		}
		if syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			_ = syscall.Unlinkat(int(root.Fd()), name)
			file.Close()
			continue
		}
		global += min(info.Size(), p.TempGlobalBytes-global)
		if strings.HasPrefix(name, taskPrefix) {
			local += min(info.Size(), p.TempTaskBytes-local)
		}
		file.Close()
	}
	if reserve == 0 {
		return nil, nil
	}
	if reserve > p.TempGlobalBytes-global || reserve > p.TempTaskBytes-local {
		return nil, errors.New("fetch temporary disk quota exceeded")
	}
	var space syscall.Statfs_t
	if err := syscall.Fstatfs(int(root.Fd()), &space); err != nil {
		return nil, err
	}
	if space.Bsize <= 0 || (uint64(reserve)+uint64(global))/uint64(space.Bsize)+1 > space.Bavail {
		return nil, errors.New("insufficient fetch temporary disk space")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name := taskPrefix + hex.EncodeToString(random[:])
	fd, err := syscall.Openat(int(root.Fd()), name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lease := &fetchTemp{file: os.NewFile(uintptr(fd), filepath.Join(root.Name(), name)), root: root, name: name}
	if err := lease.file.Chmod(0600); err != nil {
		lease.cleanup()
		return nil, err
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lease.cleanup()
		return nil, err
	}
	if err := lease.file.Truncate(reserve); err != nil {
		lease.cleanup()
		return nil, err
	}
	ok = true
	return lease, nil
}

func lockFetchQuota(ctx context.Context,fd int) error {
	for {
		if err:=ctx.Err(); err!=nil { return err }
		err:=syscall.Flock(fd,syscall.LOCK_EX|syscall.LOCK_NB)
		if err==nil { return nil }
		if err!=syscall.EWOULDBLOCK && err!=syscall.EAGAIN && err!=syscall.EINTR { return err }
		timer:=time.NewTimer(5*time.Millisecond)
		select { case <-ctx.Done(): timer.Stop(); return ctx.Err(); case <-timer.C: }
	}
}
