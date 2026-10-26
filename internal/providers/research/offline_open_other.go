//go:build !unix

package research

import (
	"errors"
	"os"
)

func openOfflineCorpus(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("corpus must be a regular file")
	}
	return os.Open(path)
}
