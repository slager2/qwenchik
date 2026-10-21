package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/runtime/resources"
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
	Stop               []string          `json:"stop,omitempty"`
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
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	resp, err := c.send(ctx, request, false)
	if err != nil {
		return llm.ChatResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return llm.ChatResponse{}, requestError(ctx, err)
	}
	if len(data) > maxResponseBytes {
		return llm.ChatResponse{}, fmt.Errorf("%w: response exceeds size limit", llm.ErrInvalidModelOutput)
	}
	var decoded completionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return llm.ChatResponse{}, fmt.Errorf("%w: malformed completion JSON", llm.ErrInvalidModelOutput)
	}
	if len(decoded.Choices) != 1 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		return llm.ChatResponse{}, fmt.Errorf("%w: expected one nonempty completion", llm.ErrInvalidModelOutput)
	}
	result := llm.ChatResponse{Content: decoded.Choices[0].Message.Content, PromptTokens: decoded.Usage.PromptTokens, OutputTokens: decoded.Usage.CompletionTokens}
	if decoded.Choices[0].FinishReason == "length" {
		return result, llm.ErrOutputTruncated
	}
	if reason := decoded.Choices[0].FinishReason; reason != "" && reason != "stop" {
		return result, fmt.Errorf("%w: unsupported completion finish reason", llm.ErrInvalidModelOutput)
	}
	return result, nil
}

func (c *Client) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	resp, err := c.send(ctx, request, true)
	if err != nil {
		return nil, err
	}
	mediaType, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != "text/event-stream" {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("%w: expected text/event-stream", llm.ErrInvalidModelOutput)
	}
	channel := make(chan llm.ChatEvent, 16)
	go c.readStream(ctx, resp.Body, channel)
	return channel, nil
}

func (c *Client) readStream(ctx context.Context, body io.ReadCloser, events chan<- llm.ChatEvent) {
	defer close(events)
	defer body.Close()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	scanner := bufio.NewScanner(io.LimitReader(body, maxResponseBytes*8))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	emit := func(event llm.ChatEvent) bool {
		select {
		case events <- event:
			return true
		case <-ctx.Done():
			return false
		}
	}
	hasText := false
	process := func(data string) bool {
		if data == "[DONE]" {
			if !hasText {
				emit(llm.ChatEvent{Err: fmt.Errorf("%w: empty stream", llm.ErrInvalidModelOutput)})
			}
			return true
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			emit(llm.ChatEvent{Err: fmt.Errorf("%w: malformed stream event", llm.ErrInvalidModelOutput)})
			return true
		}
		if len(chunk.Choices) == 0 {
			if len(chunk.Usage) > 0 && string(chunk.Usage) != "null" {
				return false
			}
			emit(llm.ChatEvent{Err: fmt.Errorf("%w: stream event has no choices", llm.ErrInvalidModelOutput)})
			return true
		}
		if len(chunk.Choices) != 1 {
			emit(llm.ChatEvent{Err: fmt.Errorf("%w: expected one stream choice", llm.ErrInvalidModelOutput)})
			return true
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			hasText = hasText || strings.TrimSpace(choice.Delta.Content) != ""
			if !emit(llm.ChatEvent{Text: choice.Delta.Content}) {
				return true
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			switch *choice.FinishReason {
			case "length":
				emit(llm.ChatEvent{Err: llm.ErrOutputTruncated})
			case "stop":
				if !hasText {
					emit(llm.ChatEvent{Err: fmt.Errorf("%w: empty stream", llm.ErrInvalidModelOutput)})
				}
			default:
				emit(llm.ChatEvent{Err: fmt.Errorf("%w: unsupported stream finish reason", llm.ErrInvalidModelOutput)})
			}
			return true
		}
		return false
	}
	var data []string
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				if process(strings.Join(data, "\n")) {
					return
				}
				data = nil
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := scanner.Err(); err != nil {
		emit(llm.ChatEvent{Err: errors.Join(llm.ErrStreamInterrupted, requestError(ctx, err))})
		return
	}
	if len(data) > 0 && process(strings.Join(data, "\n")) {
		return
	}
	emit(llm.ChatEvent{Err: llm.ErrStreamInterrupted})
}

type HTTPError struct {
	StatusCode int
	cause      error
}

func (e *HTTPError) Error() string { return fmt.Sprintf("local LLM returned HTTP %d", e.StatusCode) }
func (e *HTTPError) Unwrap() error { return e.cause }
func (e *HTTPError) HTTPStatusCode() int { return e.StatusCode }

type requestFailure struct{ cause error }

func (e *requestFailure) Error() string { return "local LLM request failed" }
func (e *requestFailure) Unwrap() error { return e.cause }

func requestError(ctx context.Context, err error) error {
	var endpointError *url.Error
	if errors.As(err, &endpointError) {
		err = &requestFailure{cause: err}
	}
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return errors.Join(llm.ErrModelTimeout, err)
	}
	return err
}

func schemaUnsupported(data []byte) bool {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Param   string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return false
	}
	e := envelope.Error
	if e.Code == "unsupported_json_schema" || e.Code == "unsupported_response_format" {
		return true
	}
	message := strings.ToLower(e.Message)
	format := strings.Contains(message, "json_schema") || strings.Contains(message, "json schema") || strings.Contains(message, "response_format") || e.Param == "response_format"
	unsupported := strings.Contains(message, "unsupported") || strings.Contains(message, "not supported") || strings.Contains(message, "does not support") || strings.Contains(message, "not implemented")
	return format && unsupported
}

func (c *Client) send(ctx context.Context, request llm.ChatRequest, stream bool) (*http.Response, error) {
	request, err := c.resolveRequest(request)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		body, err := json.Marshal(c.prepare(request, stream))
		if err != nil {
			return nil, fmt.Errorf("encode LLM request: %w", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, request.BaseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, &requestFailure{cause: err}
		}
		c.setHeaders(req)
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, requestError(ctx, err)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}
		fallback := false
		var cause error
		if resp.StatusCode == 500 || resp.StatusCode == 503 || resp.StatusCode == 507 {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
			if readErr == nil {
				cause = resources.ClassifyModelHTTPError(resp.StatusCode, data)
			}
		}
		if attempt == 0 && len(request.JSONSchema) > 0 && (resp.StatusCode == 400 || resp.StatusCode == 422) {
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if readErr != nil {
				_ = resp.Body.Close()
				return nil, requestError(ctx, readErr)
			}
			fallback = schemaUnsupported(data)
		}
		_ = resp.Body.Close()
		if !fallback {
			return nil, &HTTPError{StatusCode: resp.StatusCode, cause: cause}
		}
		if request.BeforeFallbackAttempt != nil {
			if err := request.BeforeFallbackAttempt(); err != nil {
				return nil, err
			}
		}
		request.JSONSchema = nil
		request.JSONMode = true
	}
	return nil, llm.ErrInvalidModelOutput
}

func (c *Client) prepare(request llm.ChatRequest, stream bool) requestBody {
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.defaultMaxTokens
	}
	temperature := request.Temperature
	if !request.TemperatureSet && temperature == 0 {
		temperature = c.temperature
	}
	topP := request.TopP
	if !request.TopPSet && topP == 0 {
		topP = c.topP
	}
	model := request.Model
	if model == "" {
		model = c.model
	}
	body := requestBody{
		Model: model, Messages: request.Messages, MaxTokens: maxTokens,
		Temperature: temperature, TopP: topP, Stop: append([]string(nil), request.Stop...), Stream: stream,
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
