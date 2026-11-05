package llm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/slager2/qwenchik/internal/domain"
)

var (
	ErrModelTimeout       = errors.New("local model request timed out")
	ErrStreamInterrupted  = errors.New("local model stream interrupted")
	ErrOutputTruncated    = errors.New("local model output truncated")
	ErrInvalidModelOutput = errors.New("local model returned invalid output")
	ErrInferenceBusy      = errors.New("local model inference slot is busy")
	ErrContextMismatch    = errors.New("context_mismatch")
	ErrContextUnverified  = errors.New("server context is unverified")
	ErrContextBudget      = errors.New("rendered prompt exceeds context budget")
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Messages             []ChatMessage
	Model                string   `json:"-"`
	BaseURL              string   `json:"-"`
	Stop                 []string `json:"-"`
	ContextWindow        int      `json:"-"`
	ContextOutputReserve int      `json:"-"`
	RuntimeFingerprint   string   `json:"-"`
	// RuntimeIdentity is the pinned session runtime identity used for durable
	// invocation identity. Unlike RuntimeFingerprint it never forces a live
	// metadata refresh, so it can be attached to every request of a run.
	RuntimeIdentity string `json:"-"`
	EnableThinking  *bool  `json:"-"`
	CachePrompt     *bool  `json:"-"`
	MaxTokens       int
	Temperature     float64
	TemperatureSet  bool
	TopP            float64
	TopPSet         bool
	// Optional llama.cpp sampler overrides. Nil keeps the server default, so
	// requests and invocation hashes are unchanged unless a role opts in.
	TopK                  *int     `json:",omitempty"`
	MinP                  *float64 `json:",omitempty"`
	PresencePenalty       *float64 `json:",omitempty"`
	JSONMode              bool
	JSONSchema            json.RawMessage
	BeforeFallbackAttempt func() error `json:"-"`
}

// ChatTelemetry carries inference metrics gathered by a chat transport.
type ChatTelemetry struct{ domain.InferenceTelemetry }

type ChatResponse struct {
	ContextBudget *domain.ContextBudget `json:"context_budget,omitempty"`
	Content       string
	PromptTokens  int
	OutputTokens  int
	Telemetry     *ChatTelemetry
}

type ChatEvent struct {
	Text      string
	Err       error
	Telemetry *ChatTelemetry
}

type Client interface {
	Complete(context.Context, ChatRequest) (ChatResponse, error)
	Stream(context.Context, ChatRequest) (<-chan ChatEvent, error)
}
