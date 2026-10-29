package sqlite

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"strings"
)

// Checkpoints are immutable audit records, so a research run can write a full
// state hundreds of times. Large states contain repeated role inputs and
// source notes; storing their JSON verbatim turns normal long runs into a
// multi-hundred-megabyte SQLite file. Keep small/legacy rows as plain JSON and
// transparently compress only rows where compression actually saves space.
const (
	checkpointStoragePrefix    = "gz1:"
	checkpointCompressionLimit = 64 << 10
)

func encodeCheckpointStorage(encoded []byte) (string, error) {
	if len(encoded) < checkpointCompressionLimit {
		return string(encoded), nil
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return "", err
	}
	if _, err := writer.Write(encoded); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	stored := checkpointStoragePrefix + base64.RawStdEncoding.EncodeToString(compressed.Bytes())
	if len(stored) >= len(encoded) {
		return string(encoded), nil
	}
	return stored, nil
}

func decodeCheckpointStorage(stored string) ([]byte, error) {
	if !strings.HasPrefix(stored, checkpointStoragePrefix) {
		return []byte(stored), nil
	}
	compressed, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, checkpointStoragePrefix))
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	decoded, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return decoded, nil
}
