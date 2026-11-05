package orchestrator

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/tools"
)

// roleContractFiles hold the role prompts, schemas and validation contracts
// fingerprinted into PromptHash. worker.go was split by phase, so every phase
// file is listed; TestRoleContractFilesMatchEmbed keeps this list and the
// embed directive identical.
//
//go:embed worker.go plan_phase.go act_phase.go verify_phase.go synthesis.go model_io.go checkpoint_recovery.go citations.go schemas.go chat.go context.go
var roleContractSource embed.FS

var roleContractFiles = []string{"worker.go", "plan_phase.go", "act_phase.go", "verify_phase.go", "synthesis.go", "model_io.go", "checkpoint_recovery.go", "citations.go", "schemas.go", "chat.go", "context.go"}

func ResearchAgentBundle(model string, registry *tools.Registry) domain.AgentBundle {
	digest := sha256.New()
	for _, name := range roleContractFiles {
		data, err := roleContractSource.ReadFile(name)
		if err != nil {
			panic(err)
		}
		digest.Write([]byte(name))
		digest.Write([]byte{0})
		digest.Write(data)
	}
	descriptors := registry.Descriptors()
	encoded, _ := json.Marshal(descriptors)
	digest.Write(encoded)
	names := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		names = append(names, descriptor.Name)
	}
	bundle := domain.AgentBundle{BundleID: "research-default", Version: 1, ModelProfile: model, PromptHash: hex.EncodeToString(digest.Sum(nil)), RoleSchemaVersion: 1, EnabledTools: names, MemoryPolicyVersion: 1, BudgetProfile: "snapshotted-research-v1", EvaluatorVersion: "baseline-v1"}
	bundle.BundleHash = bundle.Hash()
	return bundle
}
