package cli

import (
	"context"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/llm/openai"
)

func effectiveModelContext(ctx context.Context, cfg config.Config) int {
	d, err := discoverModelContext(ctx, cfg)
	if err != nil || d.ServerReportedContext == nil {
		return 0
	}
	return *d.ServerReportedContext
}

func discoverModelContext(ctx context.Context, cfg config.Config) (domain.ContextBudget, error) {
	client := openai.New(cfg.LLMBaseURL, cfg.LLMAPIKey, cfg.LLMModel, 3*time.Second, cfg.LLMMaxOutputTokens, cfg.LLMTemperature, cfg.LLMTopP, cfg.LLMEnableThinking).WithContextBudget(cfg.LLMContextLength)
	return client.InspectContext(ctx, llm.ChatRequest{Model: cfg.LLMModel, ContextWindow: cfg.LLMContextLength, ContextOutputReserve: cfg.LLMMaxOutputTokens})
}
