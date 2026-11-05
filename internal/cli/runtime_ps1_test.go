package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// scripts/qwenchik.ps1 launches llama-server directly on Windows, so it keeps
// its own copy of the tuning flags; keep it identical to runtimeModelPolicyArgs.
func TestWindowsLauncherModelArgsMatchRuntimePolicy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "qwenchik.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)\$ModelPolicyArgs\s*=\s*@\((.*?)\)`).FindSubmatch(raw)
	if block == nil {
		t.Fatal("$ModelPolicyArgs not found in scripts/qwenchik.ps1")
	}
	var got []string
	for _, match := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(block[1], -1) {
		got = append(got, string(match[1]))
	}
	if want := runtimeModelPolicyArgs(); !slices.Equal(got, want) {
		t.Fatalf("qwenchik.ps1 $ModelPolicyArgs = %q, want runtimeModelPolicyArgs %q", got, want)
	}
}
