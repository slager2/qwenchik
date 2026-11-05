package orchestrator

import "github.com/slager2/qwenchik/internal/llm"

// Sampler presets follow the Qwen3.5 model card for non-thinking (instruct)
// mode: top_k=20, min_p=0, and presence_penalty=1.5 for general free text
// (https://huggingface.co/Qwen/Qwen3.5-4B). Without them llama.cpp applies
// its own defaults (top_k=40, min_p=0.05).
//
// Temperature and top_p are deliberately not overridden: research sessions
// pin them in the model snapshot (LLM_TEMPERATURE/LLM_TOP_P), and roles that
// need determinism already request temperature 0 explicitly.
const (
	qwenTopK                = 20
	qwenMinP                = 0.0
	qwenChatPresencePenalty = 1.5
)

// withStructuredSampling is for schema-bound JSON roles. It never adds a
// presence penalty: JSON repeats keys, quotes and IDs by design, and
// penalizing repeated tokens would push the model away from valid structure.
func withStructuredSampling(request llm.ChatRequest) llm.ChatRequest {
	topK, minP := qwenTopK, qwenMinP
	request.TopK, request.MinP = &topK, &minP
	return request
}

// withChatSampling is for free-text conversational replies.
func withChatSampling(request llm.ChatRequest) llm.ChatRequest {
	request = withStructuredSampling(request)
	presence := qwenChatPresencePenalty
	request.PresencePenalty = &presence
	return request
}
