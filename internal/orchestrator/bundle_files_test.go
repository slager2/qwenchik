package orchestrator

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestRoleContractFilesMatchEmbed(t *testing.T) {
	raw, err := os.ReadFile("bundle.go")
	if err != nil {
		t.Fatal(err)
	}
	directive := regexp.MustCompile(`(?m)^//go:embed (.+)$`).FindSubmatch(raw)
	if directive == nil {
		t.Fatal("go:embed directive not found")
	}
	embedded := strings.Fields(string(directive[1]))
	if !slices.Equal(embedded, roleContractFiles) {
		t.Fatalf("embed %q != roleContractFiles %q", embedded, roleContractFiles)
	}
	// Phase files split out of worker.go must stay in the prompt fingerprint.
	for _, name := range []string{"plan_phase.go", "act_phase.go", "verify_phase.go", "synthesis.go", "model_io.go"} {
		if !slices.Contains(roleContractFiles, name) {
			t.Errorf("%s missing from roleContractFiles", name)
		}
	}
}
