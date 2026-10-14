package llm

import "context"

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Messages    []ChatMessage
	MaxTokens   int
	Temperature float64
	TopP        float64
	JSONMode    bool
}

type ChatResponse struct {
	Content      string
	PromptTokens int
	OutputTokens int
}

type ChatEvent struct {
	Text string
	Err  error
}

type Client interface {
	Complete(context.Context, ChatRequest) (ChatResponse, error)
	Stream(context.Context, ChatRequest) (<-chan ChatEvent, error)
}
