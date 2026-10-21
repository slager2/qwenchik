package artifacts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Store struct {
	root     string
	maxBytes int64
}

func openDirectory(path string, create bool) (*os.File, error) {
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
		if create {
			if e := syscall.Mkdirat(fd, part, 0700); e != nil && e != syscall.EEXIST {
				syscall.Close(fd)
				return nil, e
			}
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), absolute), nil
}

func New(root string, maxBytes int64) (*Store, error) {
	if root == "" {
		return nil, errors.New("artifact root is required")
	}
	if maxBytes < 1 {
		maxBytes = 10_000_000
	}
	if maxBytes == math.MaxInt64 {
		return nil, errors.New("invalid artifact limit")
	}
	directory, err := openDirectory(root, true)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	return &Store{root: directory.Name(), maxBytes: maxBytes}, nil
}

func (s *Store) taskDirectory(task string, create bool) (*os.File, error) {
	if !safeSegment.MatchString(task) {
		return nil, errors.New("invalid task identifier")
	}
	return openDirectory(filepath.Join(s.root, task), create)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (s *Store) Put(ctx context.Context, taskID, name, mimeType string, content []byte) (domain.Artifact, string, error) {
	return s.PutReader(ctx, taskID, name, mimeType, bytes.NewReader(content))
}

func (s *Store) PutReader(ctx context.Context, taskID, name, mimeType string, content io.Reader) (domain.Artifact, string, error) {
	if name == "" || name == "." || name == ".." || len(name) > 200 || filepath.Base(name) != name {
		return domain.Artifact{}, "", errors.New("invalid artifact name")
	}
	if mimeType == "" || len(mimeType) > 200 || content == nil {
		return domain.Artifact{}, "", errors.New("invalid artifact MIME type or reader")
	}
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, "", err
	}
	dir, err := s.taskDirectory(taskID, true)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	defer dir.Close()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return domain.Artifact{}, "", err
	}
	temporaryName := ".artifact-" + hex.EncodeToString(random[:])
	fd, err := syscall.Openat(int(dir.Fd()), temporaryName, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	temporary := os.NewFile(uintptr(fd), temporaryName)
	defer temporary.Close()
	defer syscall.Unlinkat(int(dir.Fd()), temporaryName)
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(contextReader{ctx, content}, s.maxBytes+1))
	if err != nil {
		return domain.Artifact{}, "", err
	}
	if n > s.maxBytes {
		return domain.Artifact{}, "", errors.New("artifact exceeds configured size limit")
	}
	if err := temporary.Sync(); err != nil {
		return domain.Artifact{}, "", err
	}
	hashText := hex.EncodeToString(hash.Sum(nil))
	identity := sha256.Sum256([]byte(taskID + "\x00" + mimeType + "\x00" + hashText))
	id := hex.EncodeToString(identity[:])
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, "", err
	}
	lockFD, err := syscall.Openat(int(dir.Fd()), ".artifact.lock", syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return domain.Artifact{}, "", err
	}
	defer syscall.Close(lockFD)
	if err := lockArtifact(ctx,lockFD); err != nil {
		return domain.Artifact{}, "", err
	}
	defer syscall.Flock(lockFD, syscall.LOCK_UN)
	existingFD, err := syscall.Openat(int(dir.Fd()), id, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err == nil {
		existing := os.NewFile(uintptr(existingFD), id)
		defer existing.Close()
		info, e := existing.Stat()
		if e != nil {
			return domain.Artifact{}, "", e
		}
		if !info.Mode().IsRegular() {
			return domain.Artifact{}, "", errors.New("artifact is not a regular file")
		}
		h := sha256.New()
		count, e := io.Copy(h, io.LimitReader(contextReader{ctx, existing}, s.maxBytes+1))
		if e != nil {
			return domain.Artifact{}, "", e
		}
		if count != n || hex.EncodeToString(h.Sum(nil)) != hashText {
			return domain.Artifact{}, "", errors.New("existing artifact content does not match its deterministic identifier")
		}
	} else if err != syscall.ENOENT {
		return domain.Artifact{}, "", err
	} else {
		if err := ctx.Err(); err != nil {
			return domain.Artifact{}, "", err
		}
		if err := syscall.Renameat(int(dir.Fd()), temporaryName, int(dir.Fd()), id); err != nil {
			return domain.Artifact{}, "", fmt.Errorf("commit artifact: %w", err)
		}
	}
	record := domain.Artifact{ID: id, TaskID: taskID, Name: name, MIMEType: mimeType, SHA256: hashText, SizeBytes: n, CreatedAt: time.Now().UTC()}
	return record, filepath.Join(taskID, id), nil
}

func lockArtifact(ctx context.Context,fd int) error {
	for {
		if err:=ctx.Err(); err!=nil { return err }
		err:=syscall.Flock(fd,syscall.LOCK_EX|syscall.LOCK_NB)
		if err==nil { return nil }
		if err!=syscall.EWOULDBLOCK && err!=syscall.EAGAIN && err!=syscall.EINTR { return err }
		timer:=time.NewTimer(5*time.Millisecond)
		select { case <-ctx.Done(): timer.Stop(); return ctx.Err(); case <-timer.C: }
	}
}

func (s *Store) Read(ctx context.Context, taskID, artifactID string) ([]byte, error) {
	if !safeSegment.MatchString(artifactID) {
		return nil, errors.New("invalid artifact identifier")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.taskDirectory(taskID, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := syscall.Openat(int(dir.Fd()), artifactID, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), artifactID)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("artifact is not a regular file")
	}
	content, err := io.ReadAll(io.LimitReader(contextReader{ctx, file}, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > s.maxBytes {
		return nil, errors.New("artifact exceeds configured size limit")
	}
	return content, nil
}
