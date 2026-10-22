package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

type RoleHints struct {
	Actor     string `json:"actor"`
	Finalizer string `json:"finalizer"`
}

type CandidatePatch struct {
	Hints     RoleHints `json:"hints"`
	Rationale string    `json:"rationale"`
}

func (patch CandidatePatch) Validate() error {
	if patch.Hints.Actor == "" && patch.Hints.Finalizer == "" {
		return errors.New("candidate must change a bounded operational hint")
	}
	for _, value := range []string{patch.Hints.Actor, patch.Hints.Finalizer, patch.Rationale} {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 1000 {
			return errors.New("candidate hint exceeds bound")
		}
	}
	return nil
}

func EffectivePromptHash(base string, hints RoleHints) string {
	encoded, _ := json.Marshal(hints)
	hash := sha256.Sum256(append([]byte(base+"\n"), encoded...))
	return hex.EncodeToString(hash[:])
}

func (bundle AgentBundle) BaseHash() string {
	if bundle.BaseBundleHash != "" {
		return bundle.BaseBundleHash
	}
	return bundle.BundleHash
}

type RefinementJob struct {
	Base          AgentBundle          `json:"base"`
	Model         ModelProfileSnapshot `json:"model"`
	SourceTaskIDs []string             `json:"source_task_ids"`
	DeadlineAt    time.Time            `json:"deadline_at"`
	CandidateHash string               `json:"candidate_hash,omitempty"`
}

type RefinementEvaluation struct {
	Version         string `json:"version"`
	BaselinePassed  int    `json:"baseline_passed"`
	CandidatePassed int    `json:"candidate_passed"`
	CaseCount       int    `json:"case_count"`
	BaselineMS      int64  `json:"baseline_ms"`
	CandidateMS     int64  `json:"candidate_ms"`
	Passed          bool   `json:"passed"`
}

type BundleCandidate struct {
	Bundle     AgentBundle           `json:"bundle"`
	ParentHash string                `json:"parent_hash"`
	TaskID     string                `json:"task_id,omitempty"`
	Status     string                `json:"status"`
	Active     bool                  `json:"active"`
	Patch      *CandidatePatch       `json:"patch,omitempty"`
	Evaluation *RefinementEvaluation `json:"evaluation,omitempty"`
}
