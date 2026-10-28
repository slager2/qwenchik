package domain

import "testing"

func TestContextBudgetKeepsConfiguredServerSessionAndReservesDistinct(t *testing.T) {
	server := 65536
	d := NewContextBudget(65536, 65536, 4096, &server)
	if d.ContextStatus != "matched" || *d.AvailableInputTokens != 59392 || d.ReservedSystemTokens != 2048 {
		t.Fatalf("budget arithmetic: %+v", d)
	}
	d = NewContextBudget(131072, 65536, 4096, &server)
	if d.ContextStatus != "context_mismatch" || d.ConfiguredContext != 131072 || *d.ServerReportedContext != 65536 {
		t.Fatal("silently capped configuration")
	}
	d = NewContextBudget(65536, 32768, 4096, &server)
	if d.ContextStatus != "context_mismatch" || *d.SessionContext != 32768 {
		t.Fatal("silently increased saved session")
	}
	d = NewContextBudget(131072, 131072, 4096, nil)
	if d.ServerReportedContext != nil || d.AvailableInputTokens != nil || d.ContextStatus != "unavailable" {
		t.Fatal("invented server capacity")
	}
}
