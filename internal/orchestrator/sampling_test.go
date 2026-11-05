package orchestrator

import (
	"testing"

	"github.com/slager2/qwenchik/internal/llm"
)

func TestSamplingPresetsKeepPinnedTemperatureAndAvoidJSONPenalty(t *testing.T) {
	base := llm.ChatRequest{Temperature: 0.65, TopP: 0.55}
	structured := withStructuredSampling(base)
	if structured.Temperature != 0.65 || structured.TopP != 0.55 || structured.TemperatureSet || structured.TopPSet {
		t.Fatal("structured preset overrode the session-pinned temperature/top_p")
	}
	if structured.TopK == nil || *structured.TopK != 20 || structured.MinP == nil || *structured.MinP != 0 {
		t.Fatalf("structured preset lacks Qwen top_k/min_p: %+v", structured)
	}
	if structured.PresencePenalty != nil {
		t.Fatal("presence penalty would penalize repeated JSON structure tokens")
	}
	chat := withChatSampling(base)
	if chat.PresencePenalty == nil || *chat.PresencePenalty != 1.5 || chat.TopK == nil || *chat.TopK != 20 {
		t.Fatalf("chat preset differs from the Qwen non-thinking preset: %+v", chat)
	}
	if base.TopK != nil || base.PresencePenalty != nil {
		t.Fatal("preset mutated its input")
	}
}
