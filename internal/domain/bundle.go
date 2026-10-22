package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

type AgentBundle struct {
	BaseBundleHash      string     `json:"base_bundle_hash,omitempty"`
	BasePromptHash      string     `json:"base_prompt_hash,omitempty"`
	Hints               *RoleHints `json:"hints,omitempty"`
	BundleID            string     `json:"bundle_id"`
	Version             int        `json:"version"`
	ModelProfile        string     `json:"model_profile"`
	PromptHash          string     `json:"prompt_hash"`
	RoleSchemaVersion   int        `json:"role_schema_version"`
	EnabledTools        []string   `json:"enabled_tools"`
	MemoryPolicyVersion int        `json:"memory_policy_version"`
	BudgetProfile       string     `json:"budget_profile"`
	EvaluatorVersion    string     `json:"evaluator_version"`
	BundleHash          string     `json:"bundle_hash"`
}

func (bundle AgentBundle) Hash() string {
	bundle.BundleHash = ""
	bundle.EnabledTools = append([]string{}, bundle.EnabledTools...)
	sort.Strings(bundle.EnabledTools)
	encoded, _ := json.Marshal(bundle)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (bundle AgentBundle) Validate() error {
	if bundle.Version == 1 {
		if bundle.Hints != nil || bundle.BaseBundleHash != "" || bundle.BasePromptHash != "" {
			return errors.New("baseline bundle cannot contain a candidate patch")
		}
	} else {
		base, err := hex.DecodeString(bundle.BaseBundleHash)
		prompt, promptErr := hex.DecodeString(bundle.BasePromptHash)
		if err != nil || len(base) != 32 || promptErr != nil || len(prompt) != 32 || bundle.Hints == nil || bundle.PromptHash != EffectivePromptHash(bundle.BasePromptHash, *bundle.Hints) || (CandidatePatch{Hints: *bundle.Hints}).Validate() != nil {
			return errors.New("invalid refined bundle lineage")
		}
	}
	digest, err := hex.DecodeString(bundle.PromptHash)
	if err != nil || len(digest) != 32 || bundle.BundleID != "research-default" || bundle.Version < 1 || bundle.RoleSchemaVersion != 1 || bundle.MemoryPolicyVersion != 1 || bundle.ModelProfile == "" || len(bundle.ModelProfile) > 128 || bundle.BudgetProfile != "snapshotted-research-v1" || bundle.EvaluatorVersion != "baseline-v1" || len(bundle.EnabledTools) > 32 || bundle.BundleHash != bundle.Hash() {
		return errors.New("invalid agent bundle")
	}
	seen := map[string]bool{}
	for _, tool := range bundle.EnabledTools {
		if tool == "" || len(tool) > 128 || seen[tool] {
			return errors.New("invalid bundle tool set")
		}
		seen[tool] = true
	}
	return nil
}
