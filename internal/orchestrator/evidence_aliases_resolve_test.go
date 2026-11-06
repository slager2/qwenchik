package orchestrator

import "testing"

func TestEvidenceAliasResolveToleratesWrappedAliases(t *testing.T) {
	a, b := "0b9a3f7e-1111-4222-8333-444455556666", "1c9a3f7e-1111-4222-8333-444455556666"
	numbered := newNumberedEvidenceAliases([]string{a, b})
	for _, input := range []string{"2", " 2 ", "[2]", `{"evidence_id": "2"}`, "source 2", b} {
		if got := numbered.resolve(input); got != b {
			t.Errorf("resolve(%q) = %q, want %q", input, got, b)
		}
	}
	// Ambiguous or unknown values are returned unchanged for validation to reject.
	for _, input := range []string{"[1][2]", "7", "none"} {
		if got := numbered.resolve(input); got != input {
			t.Errorf("resolve(%q) = %q, want unchanged", input, got)
		}
	}
	lettered := newEvidenceAliases([]string{a, b})
	if got := lettered.resolve(`"E1"`); got != a {
		t.Errorf("resolve(\"E1\") = %q, want %q", got, a)
	}
}
