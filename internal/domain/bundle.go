package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

type AgentBundle struct {
	BundleID            string   `json:"bundle_id"`
	Version             int      `json:"version"`
	ModelProfile        string   `json:"model_profile"`
	PromptHash          string   `json:"prompt_hash"`
	RoleSchemaVersion   int      `json:"role_schema_version"`
	EnabledTools        []string `json:"enabled_tools"`
	MemoryPolicyVersion int      `json:"memory_policy_version"`
	BudgetProfile       string   `json:"budget_profile"`
	EvaluatorVersion    string   `json:"evaluator_version"`
	BundleHash          string   `json:"bundle_hash"`
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
