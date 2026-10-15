package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrInvalidMemory = errors.New("invalid memory")

func invalidMemory(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidMemory, fmt.Sprintf(format, args...))
}

type MemoryStatus string

const (
	MemoryCandidate  MemoryStatus = "candidate"
	MemoryAccepted   MemoryStatus = "accepted"
	MemoryStale      MemoryStatus = "stale"
	MemorySuperseded MemoryStatus = "superseded"
	MemoryRejected   MemoryStatus = "rejected"
)

type Memory struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	Status           MemoryStatus   `json:"status"`
	Text             string         `json:"text"`
	Metadata         map[string]any `json:"metadata"`
	Scope            string         `json:"scope"`
	Confidence       *float64       `json:"confidence,omitempty"`
	SourceEvidenceID string         `json:"source_evidence_id,omitempty"`
	SourceTaskID     string         `json:"source_task_id,omitempty"`
	UserEditable     bool           `json:"user_editable"`
	RetentionPolicy  string         `json:"retention_policy"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
}

type MemoryMatch struct {
	Memory    Memory `json:"memory"`
	Relevance int    `json:"relevance"`
}

func (m Memory) Validate() error {
	if len(m.Kind) == 0 || len(m.Kind) > 64 {
		return invalidMemory("memory kind must contain 1 to 64 characters")
	}
	for _, r := range m.Kind {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' {
			return invalidMemory("memory kind must use lowercase letters, digits, '_' or '-'")
		}
	}
	if text := strings.TrimSpace(m.Text); text == "" || len(text) > 4000 || !utf8.ValidString(m.Text) {
		return invalidMemory("memory text must contain 1 to 4000 bytes")
	}
	if m.Status != MemoryCandidate && m.Status != MemoryAccepted && m.Status != MemoryStale && m.Status != MemorySuperseded && m.Status != MemoryRejected {
		return invalidMemory("invalid memory status %q", m.Status)
	}
	if m.Scope != "global" && m.Scope != "task" {
		return invalidMemory("invalid memory scope %q", m.Scope)
	}
	if m.Scope == "task" && m.SourceTaskID == "" {
		return invalidMemory("task-scoped memory requires a source task")
	}
	if m.SourceEvidenceID != "" && m.SourceTaskID == "" {
		return invalidMemory("evidence-backed memory requires a source task")
	}
	if m.RetentionPolicy != "until_deleted" {
		return invalidMemory("unsupported memory retention policy %q", m.RetentionPolicy)
	}
	if m.Confidence != nil && (math.IsNaN(*m.Confidence) || math.IsInf(*m.Confidence, 0) || *m.Confidence < 0 || *m.Confidence > 1) {
		return invalidMemory("memory confidence must be between 0 and 1")
	}
	if m.Metadata != nil {
		encoded, err := json.Marshal(m.Metadata)
		if err != nil {
			return fmt.Errorf("%w: encode memory metadata: %v", ErrInvalidMemory, err)
		}
		if len(encoded) > 16<<10 {
			return invalidMemory("memory metadata exceeds 16384 bytes")
		}
	}
	return nil
}
