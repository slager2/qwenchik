//go:build !unix

package workerlock

import "errors"

var ErrAlreadyRunning = errors.New("another qagent worker is already using this database")

type Lock struct{}

func Acquire(string) (*Lock, error) {
	return nil, errors.New("qagent worker process locking is unsupported on this platform")
}

func (*Lock) Close() error { return nil }
