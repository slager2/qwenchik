package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
)

const maxResponseBytes = 4 << 20

type Client struct {
	baseURL          string
	apiKey           string
	model            string
	thinking         bool
	httpClient       *http.Client
	defaultMaxTokens int
	temperature      float64
	topP             float64
}

func New(baseURL, apiKey, model string, timeout time.Duration, maxTokens int, temperature, topP float64, thinking bool) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		apiKey:   apiKey,
		model:    model,
		thinking: thinking,
		httpClient: &http.Client{
			Timeout:       timeout,
			Transport:     &http.Transport{Proxy: nil},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
		defaultMaxTokens: maxTokens,
		temperature:      temperature,
		topP:             topP,
	}
}

type requestBody struct {
	Model              string            `json:"model"`
	Messages           []llm.ChatMessage `json:"messages"`
	MaxTokens          int               `json:"max_tokens"`
	Temperature        float64           `json:"temperature"`
	TopP               float64           `json:"top_p"`
	Stream             bool              `json:"stream"`
	ResponseFormat     *responseFormat   `json:"response_format,omitempty"`
	ChatTemplateKwargs map[string]bool   `json:"chat_template_kwargs,omitempty"`
}

type responseFormat struct {
	Type       string              `json:"type"`
	JSONSchema *jsonSchemaResponse `json:"json_schema,omitempty"`
}

type jsonSchemaResponse struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	body, err := json.Marshal(c.prepare(request, false))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("encode LLM request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return llm.ChatResponse{}, err
	}
	c.setHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("local LLM request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return llm.ChatResponse{}, fmt.Errorf("local LLM returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return llm.ChatResponse{}, fmt.Errorf("read local LLM response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return llm.ChatResponse{}, errors.New("local LLM response exceeds 4 MiB limit")
	}
	var decoded completionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("decode local LLM response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return llm.ChatResponse{}, errors.New("local LLM returned no choices")
	}
	return llm.ChatResponse{Content: decoded.Choices[0].Message.Content, PromptTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens}, nil
}

func (c *Client) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	body, err := json.Marshal(c.prepare(request, true))
	if err != nil {
		return nil, fmt.Errorf("encode LLM request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("local LLM stream request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("local LLM returned HTTP %d", resp.StatusCode)
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		_ = resp.Body.Close()
		return nil, errors.New("local LLM stream response is not text/event-stream")
	}
	channel := make(chan llm.ChatEvent, 16)
	go c.readStream(ctx, resp.Body, channel)
	return channel, nil
}

func (c *Client) readStream(ctx context.Context, body io.ReadCloser, events chan<- llm.ChatEvent) {
	defer close(events)
	defer body.Close()
	scanner := bufio.NewScanner(io.LimitReader(body, maxResponseBytes*8))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			select {
			case events <- llm.ChatEvent{Err: fmt.Errorf("decode local LLM stream chunk: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
		if len(chunk.Choices) != 0 && chunk.Choices[0].Delta.Content != "" {
			select {
			case events <- llm.ChatEvent{Text: chunk.Choices[0].Delta.Content}:
			case <-ctx.Done():
				return
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		select {
		case events <- llm.ChatEvent{Err: fmt.Errorf("read local LLM stream: %w", err)}:
		case <-ctx.Done():
		}
	}
}

func (c *Client) prepare(request llm.ChatRequest, stream bool) requestBody {
	maxTokens := request.MaxTokens
	if maxTokens <= 0 || maxTokens > c.defaultMaxTokens {
		maxTokens = c.defaultMaxTokens
	}
	temperature := request.Temperature
	if temperature == 0 {
		temperature = c.temperature
	}
	topP := request.TopP
	if topP == 0 {
		topP = c.topP
	}
	body := requestBody{
		Model: c.model, Messages: request.Messages, MaxTokens: maxTokens,
		Temperature: temperature, TopP: topP, Stream: stream,
		ChatTemplateKwargs: map[string]bool{"enable_thinking": c.thinking},
	}
	if len(request.JSONSchema) != 0 {
		body.ResponseFormat = &responseFormat{
			Type: "json_schema",
			JSONSchema: &jsonSchemaResponse{
				Name: "qagent_response", Strict: true, Schema: append(json.RawMessage(nil), request.JSONSchema...),
			},
		}
	} else if request.JSONMode {
		body.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	return body
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}
