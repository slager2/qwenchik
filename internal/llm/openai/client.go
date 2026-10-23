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
	"unicode/utf8"

	"github.com/slager2/qwenchik/internal/domain"
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
	StreamOptions      *streamOptions    `json:"stream_options,omitempty"`
	ResponseFormat     *responseFormat   `json:"response_format,omitempty"`
	ChatTemplateKwargs map[string]bool   `json:"chat_template_kwargs,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
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
		FinishReason json.RawMessage `json:"finish_reason"`
		Message      struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage   json.RawMessage `json:"usage"`
	Timings json.RawMessage `json:"timings"`
}

func completionFinishReason(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || rawIsNull(raw) {
		return "", true
	}
	var reason string
	if err := json.Unmarshal(raw, &reason); err != nil {
		return "", false
	}
	return reason, true
}

func (c *Client) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	requestForTelemetry := c.telemetryRequest(request)
	attemptStarted := time.Now()
	resp, err := c.send(ctx, request, false)
	if err != nil {
		return telemetryFailure(requestForTelemetry, statusForError(ctx, err)), err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		requestErr := requestError(ctx, err)
		return telemetryFailure(requestForTelemetry, statusForError(ctx, requestErr)), requestErr
	}
	if len(data) > maxResponseBytes {
		requestErr := fmt.Errorf("%w: response exceeds size limit", llm.ErrInvalidModelOutput)
		return telemetryFailure(requestForTelemetry, domain.InferenceStatusFailed), requestErr
	}
	var decoded completionResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		requestErr := fmt.Errorf("%w: malformed completion JSON", llm.ErrInvalidModelOutput)
		return telemetryFailure(requestForTelemetry, domain.InferenceStatusFailed), requestErr
	}
	at := time.Now()
	telemetry := newChatTelemetry(attemptStarted, requestForTelemetry, domain.InferenceStatusLoading)
	applyProviderTelemetry(&telemetry, decoded.Usage, decoded.Timings, at)
	result := llm.ChatResponse{Telemetry: &telemetry}
	if metric := telemetry.PromptTokens; metric.Source != domain.MetricSourceUnavailable && metric.Value != nil {
		result.PromptTokens = int(*metric.Value)
	}
	if metric := telemetry.OutputTokens; metric.Source != domain.MetricSourceUnavailable && metric.Value != nil {
		result.OutputTokens = int(*metric.Value)
	}
	if len(decoded.Choices) != 1 || strings.TrimSpace(decoded.Choices[0].Message.Content) == "" {
		telemetry.Status = domain.InferenceStatusFailed
		telemetry.UpdatedAt = time.Now()
		result.Telemetry = &telemetry
		return result, fmt.Errorf("%w: expected one nonempty completion", llm.ErrInvalidModelOutput)
	}
	result.Content = decoded.Choices[0].Message.Content
	reason, validReason := completionFinishReason(decoded.Choices[0].FinishReason)
	if !validReason {
		telemetry.Status = domain.InferenceStatusFailed
		telemetry.UpdatedAt = time.Now()
		result.Telemetry = &telemetry
		return result, fmt.Errorf("%w: unsupported completion finish reason", llm.ErrInvalidModelOutput)
	}
	if reason == "length" {
		telemetry.Status = domain.InferenceStatusFailed
		telemetry.UpdatedAt = time.Now()
		result.Telemetry = &telemetry
		return result, llm.ErrOutputTruncated
	}
	if reason != "" && reason != "stop" {
		telemetry.Status = domain.InferenceStatusFailed
		telemetry.UpdatedAt = time.Now()
		result.Telemetry = &telemetry
		return result, fmt.Errorf("%w: unsupported completion finish reason", llm.ErrInvalidModelOutput)
	}
	telemetry.UpdatedAt = time.Now()
	telemetry.Status = domain.InferenceStatusCompleted
	result.Telemetry = &telemetry
	return result, nil
}

func (c *Client) Stream(ctx context.Context, request llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	started := time.Now()
	requestForTelemetry := c.telemetryRequest(request)
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
	go c.readStream(ctx, resp.Body, channel, requestForTelemetry, started)
	return channel, nil
}

func (c *Client) telemetryRequest(request llm.ChatRequest) llm.ChatRequest {
	if request.MaxTokens <= 0 {
		request.MaxTokens = c.defaultMaxTokens
	}
	return request
}

func telemetryFailure(request llm.ChatRequest, status domain.InferenceStatus) llm.ChatResponse {
	telemetry := newChatTelemetry(time.Now(), request, status)
	return llm.ChatResponse{Telemetry: &telemetry}
}

func statusForError(ctx context.Context, err error) domain.InferenceStatus {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return domain.InferenceStatusCancelled
	}
	return domain.InferenceStatusFailed
}

type streamProgress struct {
	telemetry     llm.ChatTelemetry
	started       time.Time
	firstOutputAt time.Time
	outputRunes   int
}

func (progress *streamProgress) updateEstimate(at time.Time) {
	if progress.firstOutputAt.IsZero() {
		return
	}
	if progress.telemetry.OutputTokens.Source != domain.MetricSourceExact {
		tokens := int64((progress.outputRunes + 3) / 4)
		if tokens < 1 {
			tokens = 1
		}
		progress.telemetry.OutputTokens = domain.NewEstimatedMetric(tokens, domain.MetricUnitTokens, at)
	}
	if progress.telemetry.OutputTokensPerSecond.Source == domain.MetricSourceExact || progress.telemetry.OutputTokens.Value == nil {
		return
	}
	seconds := at.Sub(progress.firstOutputAt).Seconds()
	if seconds <= 0 {
		return
	}
	progress.telemetry.OutputTokensPerSecond = domain.NewEstimatedMetric(float64(*progress.telemetry.OutputTokens.Value)/seconds, domain.MetricUnitTokensPerSecond, at)
}

func (progress *streamProgress) snapshot(at time.Time) *llm.ChatTelemetry {
	value := progress.telemetry
	value.UpdatedAt = at
	return &value
}

func (c *Client) readStream(ctx context.Context, body io.ReadCloser, events chan<- llm.ChatEvent, request llm.ChatRequest, started time.Time) {
	defer close(events)
	defer body.Close()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	progress := streamProgress{
		telemetry: newChatTelemetry(started, request, domain.InferenceStatusLoading),
		started:   started,
	}
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
	emitCancellation := func() {
		at := time.Now()
		progress.telemetry.Status = domain.InferenceStatusCancelled
		progress.telemetry.UpdatedAt = at
		progress.updateEstimate(at)
		event := llm.ChatEvent{Telemetry: progress.snapshot(at)}
		select {
		case events <- event:
		default:
		}
	}
	finish := func(status domain.InferenceStatus, err error) {
		at := time.Now()
		progress.updateEstimate(at)
		progress.telemetry.Status = status
		progress.telemetry.UpdatedAt = at
		event := llm.ChatEvent{Err: err, Telemetry: progress.snapshot(at)}
		if ctx.Err() != nil {
			emitCancellation()
			return
		}
		if !emit(event) && ctx.Err() != nil {
			emitCancellation()
		}
	}
	progress.telemetry.Status = domain.InferenceStatusLoading
	if !emit(llm.ChatEvent{Telemetry: progress.snapshot(time.Now())}) {
		emitCancellation()
		return
	}
	progress.telemetry.Status = domain.InferenceStatusPrefilling
	progress.telemetry.UpdatedAt = time.Now()
	if !emit(llm.ChatEvent{Telemetry: progress.snapshot(time.Now())}) {
		emitCancellation()
		return
	}

	hasText := false
	finishReason := ""
	terminalError := func() error {
		switch finishReason {
		case "length":
			return llm.ErrOutputTruncated
		case "", "stop":
			if !hasText {
				return fmt.Errorf("%w: empty stream", llm.ErrInvalidModelOutput)
			}
			return nil
		default:
			return fmt.Errorf("%w: unsupported stream finish reason", llm.ErrInvalidModelOutput)
		}
	}
	process := func(data string) (bool, error) {
		if data == "[DONE]" {
			return true, nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage   json.RawMessage `json:"usage"`
			Timings json.RawMessage `json:"timings"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			return true, fmt.Errorf("%w: malformed stream event", llm.ErrInvalidModelOutput)
		}
		applyProviderTelemetry(&progress.telemetry, chunk.Usage, chunk.Timings, time.Now())
		if len(chunk.Error) > 0 && !rawIsNull(chunk.Error) {
			return true, fmt.Errorf("%w: malformed stream event", llm.ErrInvalidModelOutput)
		}
		if len(chunk.Choices) == 0 {
			if !rawIsNull(chunk.Usage) || !rawIsNull(chunk.Timings) {
				return false, nil
			}
			return true, fmt.Errorf("%w: stream event has no choices", llm.ErrInvalidModelOutput)
		}
		if len(chunk.Choices) != 1 {
			return true, fmt.Errorf("%w: expected one stream choice", llm.ErrInvalidModelOutput)
		}
		choice := chunk.Choices[0]
		if choice.Delta.Content != "" {
			at := time.Now()
			progress.outputRunes += utf8.RuneCountInString(choice.Delta.Content)
			if strings.TrimSpace(choice.Delta.Content) != "" {
				hasText = true
				if progress.firstOutputAt.IsZero() {
					progress.firstOutputAt = at
					progress.telemetry.TTFTMS = domain.NewExactMetric(float64(at.Sub(progress.started).Microseconds())/1000, domain.MetricUnitMilliseconds, at)
					progress.telemetry.Status = domain.InferenceStatusGenerating
					progress.updateEstimate(at)
					if !emit(llm.ChatEvent{Telemetry: progress.snapshot(at)}) {
						return true, nil
					}
				}
			}
			progress.updateEstimate(at)
			if !emit(llm.ChatEvent{Text: choice.Delta.Content, Telemetry: progress.snapshot(at)}) {
				return true, nil
			}
		}
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			if *choice.FinishReason != "stop" && *choice.FinishReason != "length" {
				return true, fmt.Errorf("%w: unsupported stream finish reason", llm.ErrInvalidModelOutput)
			}
			finishReason = *choice.FinishReason
		}
		return false, nil
	}
	finishFromResult := func(err error) {
		if ctx.Err() != nil {
			emitCancellation()
			return
		}
		if err == nil {
			err = terminalError()
		}
		status := domain.InferenceStatusCompleted
		if err != nil {
			status = domain.InferenceStatusFailed
		}
		finish(status, err)
	}
	var data []string
	for scanner.Scan() {
		if ctx.Err() != nil {
			emitCancellation()
			return
		}
		line := scanner.Text()
		if line == "" {
			if len(data) > 0 {
				stop, err := process(strings.Join(data, "\n"))
				data = nil
				if stop {
					finishFromResult(err)
					return
				}
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		emitCancellation()
		return
	}
	if err := scanner.Err(); err != nil {
		finish(domain.InferenceStatusFailed, errors.Join(llm.ErrStreamInterrupted, requestError(ctx, err)))
		return
	}
	if len(data) > 0 {
		stop, err := process(strings.Join(data, "\n"))
		if stop {
			finishFromResult(err)
			return
		}
	}
	if finishReason != "" {
		finishFromResult(nil)
		return
	}
	finish(domain.InferenceStatusFailed, llm.ErrStreamInterrupted)
}

type HTTPError struct {
	StatusCode int
	cause      error
}

func (e *HTTPError) Error() string       { return fmt.Sprintf("local LLM returned HTTP %d", e.StatusCode) }
func (e *HTTPError) Unwrap() error       { return e.cause }
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
	if stream {
		body.StreamOptions = &streamOptions{IncludeUsage: true}
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
