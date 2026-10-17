package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/config"
)

func effectiveModelContext(ctx context.Context, cfg config.Config) int {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.LLMBaseURL, "/")+"/models", nil)
	if err != nil {
		return cfg.LLMContextLength
	}
	if cfg.LLMAPIKey != "" {
		request.Header.Set("Authorization", "Bearer "+cfg.LLMAPIKey)
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return cfg.LLMContextLength
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cfg.LLMContextLength
	}
	var body struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				Context int `json:"n_ctx"`
			} `json:"meta"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body) != nil {
		return cfg.LLMContextLength
	}
	for _, model := range body.Data {
		if model.ID != cfg.LLMModel && len(body.Data) != 1 {
			continue
		}
		if model.Meta.Context > 0 && model.Meta.Context < cfg.LLMContextLength {
			return model.Meta.Context
		}
	}
	return cfg.LLMContextLength
}
