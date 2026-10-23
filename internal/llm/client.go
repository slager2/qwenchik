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
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Messages              []ChatMessage
	Model                 string   `json:"-"`
	BaseURL               string   `json:"-"`
	Stop                  []string `json:"-"`
	ContextWindow         int      `json:"-"`
	MaxTokens             int
	Temperature           float64
	TemperatureSet        bool
	TopP                  float64
	TopPSet               bool
	JSONMode              bool
	JSONSchema            json.RawMessage
	BeforeFallbackAttempt func() error `json:"-"`
}

// ChatTelemetry carries inference metrics gathered by a chat transport.
type ChatTelemetry struct{ domain.InferenceTelemetry }

type ChatResponse struct {
	Content      string
	PromptTokens int
	OutputTokens int
	Telemetry    *ChatTelemetry
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
