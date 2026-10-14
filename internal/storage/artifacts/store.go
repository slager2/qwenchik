package artifacts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Store struct {
	root     string
	maxBytes int64
}

func New(root string, maxBytes int64) (*Store, error) {
	if root == "" {
		return nil, errors.New("artifact root is required")
	}
	if maxBytes < 1 {
		maxBytes = 10_000_000
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	return &Store{root: absolute, maxBytes: maxBytes}, nil
}

func (s *Store) Put(ctx context.Context, taskID, name, mimeType string, content []byte) (domain.Artifact, string, error) {
	if !safeSegment.MatchString(taskID) {
		return domain.Artifact{}, "", errors.New("invalid task identifier")
	}
	if name == "" || len(name) > 200 || filepath.Base(name) != name {
		return domain.Artifact{}, "", errors.New("invalid artifact name")
	}
	if mimeType == "" || len(mimeType) > 200 {
		return domain.Artifact{}, "", errors.New("invalid artifact MIME type")
	}
	if int64(len(content)) > s.maxBytes {
		return domain.Artifact{}, "", errors.New("artifact exceeds configured size limit")
	}
	if err := ctx.Err(); err != nil {
		return domain.Artifact{}, "", err
	}
	contentHash := sha256.Sum256(content)
	hashText := hex.EncodeToString(contentHash[:])
	identity := sha256.Sum256([]byte(taskID + "\x00" + mimeType + "\x00" + hashText))
	id := hex.EncodeToString(identity[:])
	taskDir := filepath.Join(s.root, taskID)
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return domain.Artifact{}, "", fmt.Errorf("create task artifact directory: %w", err)
	}
	temporary, err := os.CreateTemp(taskDir, ".artifact-*")
	if err != nil {
		return domain.Artifact{}, "", fmt.Errorf("create temporary artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := func() { _ = os.Remove(temporaryPath) }
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		cleanup()
		return domain.Artifact{}, "", err
	}
	if _, err := io.Copy(temporary, bytes.NewReader(content)); err != nil {
		_ = temporary.Close()
		cleanup()
		return domain.Artifact{}, "", fmt.Errorf("write artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		cleanup()
		return domain.Artifact{}, "", err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return domain.Artifact{}, "", err
	}
	if err := ctx.Err(); err != nil {
		cleanup()
		return domain.Artifact{}, "", err
	}
	finalPath := filepath.Join(taskDir, id)
	if existingFile, err := os.Open(finalPath); err == nil {
		existing, readErr := io.ReadAll(io.LimitReader(existingFile, s.maxBytes+1))
		closeErr := existingFile.Close()
		if readErr != nil {
			cleanup()
			return domain.Artifact{}, "", readErr
		}
		if closeErr != nil {
			cleanup()
			return domain.Artifact{}, "", closeErr
		}
		if int64(len(existing)) > s.maxBytes {
			cleanup()
			return domain.Artifact{}, "", errors.New("existing artifact exceeds configured size limit")
		}
		existingHash := sha256.Sum256(existing)
		if hex.EncodeToString(existingHash[:]) != hashText {
			cleanup()
			return domain.Artifact{}, "", errors.New("existing artifact content does not match its deterministic identifier")
		}
		cleanup()
		record := domain.Artifact{ID: id, TaskID: taskID, Name: name, MIMEType: mimeType, SHA256: hashText, SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC()}
		return record, filepath.Join(taskID, id), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return domain.Artifact{}, "", err
	}
	if err := os.Rename(temporaryPath, finalPath); err != nil {
		cleanup()
		return domain.Artifact{}, "", fmt.Errorf("commit artifact: %w", err)
	}
	record := domain.Artifact{ID: id, TaskID: taskID, Name: name, MIMEType: mimeType, SHA256: hashText, SizeBytes: int64(len(content)), CreatedAt: time.Now().UTC()}
	return record, filepath.Join(taskID, id), nil
}

func (s *Store) Read(ctx context.Context, taskID, artifactID string) ([]byte, error) {
	if !safeSegment.MatchString(taskID) || !safeSegment.MatchString(artifactID) {
		return nil, errors.New("invalid artifact identifier")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.root, taskID, artifactID)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, s.maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > s.maxBytes {
		return nil, errors.New("artifact exceeds configured size limit")
	}
	return content, nil
}
