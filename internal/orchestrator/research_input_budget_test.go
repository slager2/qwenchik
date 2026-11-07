package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/storage/sqlite"
)

func TestResearchInputBytesScalesWithContextUnderPinLimit(t *testing.T) {
	available := func(context int) int { return context - 5120 - 2048 }
	small, medium, large := researchInputBytes(available(65536)), researchInputBytes(available(131072)), researchInputBytes(available(262144))
	if small != available(65536)*researchBytesPerToken || medium != available(131072)*researchBytesPerToken {
		t.Fatalf("budget does not scale with context: 64k=%d 128k=%d", small, medium)
	}
	if medium <= 2*small-small/4 || medium > researchMaxInputBytes {
		t.Fatalf("128k budget %d not ~2x the 64k budget %d within the pin cap %d", medium, small, researchMaxInputBytes)
	}
	if large != researchMaxInputBytes || researchMaxInputBytes+32<<10 != sqlite.MaxRoleContextInputBytes {
		t.Fatalf("budget %d escapes the pin limit %d", large, sqlite.MaxRoleContextInputBytes)
	}
	if researchInputBytes(int(^uint(0)>>1)) != researchMaxInputBytes || researchInputBytes(0) != 1 {
		t.Fatal("budget overflows or vanishes at the extremes")
	}
}
