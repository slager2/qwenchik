package openai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) WithContextBudget(configured int) *Client {
	c.contextConfigured = configured
	return c
}

func (c *Client) metadataJSON(ctx context.Context, method, endpoint string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if len(raw) > 2<<20 {
			return llm.ErrContextBudget
		}
		body = bytes.NewReader(raw)
	}
	r, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	c.setHeaders(r)
	response, err := c.httpClient.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("context metadata endpoint unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return llm.ErrContextBudget
	}
	return json.Unmarshal(raw, output)
}

func (c *Client) InspectContext(ctx context.Context, request llm.ChatRequest) (domain.ContextBudget, error) {
	request, err := c.resolveRequest(request)
	if err != nil {
		return domain.ContextBudget{}, err
	}
	configured := c.contextConfigured
	if configured == 0 {
		configured = request.ContextWindow
	}
	output := request.ContextOutputReserve
	if output <= 0 {
		output = c.defaultMaxTokens
	}
	if output <= 0 {
		output = request.MaxTokens
	}
	output = max(output, c.prepare(request, false).MaxTokens)
	d := domain.NewContextBudget(configured, request.ContextWindow, output, nil)
	d.ObservedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	root := strings.TrimSuffix(request.BaseURL, "/v1")
	var props struct {
		Default struct {
			Context int `json:"n_ctx"`
		} `json:"default_generation_settings"`
		Slots    int    `json:"total_slots"`
		Alias    string `json:"model_alias"`
		Path     string `json:"model_path"`
		Template string `json:"chat_template"`
		Build    string `json:"build_info"`
		FType    string `json:"model_ftype"`
	}
	if err := c.metadataJSON(ctx, "GET", root+"/props", nil, &props); err != nil {
		d.Reason = "runtime_metadata_unavailable"
		return d, llm.ErrContextUnverified
	}
	if props.Default.Context < 1 || props.Slots != 1 || props.Alias != request.Model {
		d.Reason = "runtime_metadata_ambiguous"
		return d, llm.ErrContextUnverified
	}
	var models struct {
		Data []struct {
			ID   string `json:"id"`
			Meta struct {
				Context  int   `json:"n_ctx"`
				Training int   `json:"n_ctx_train"`
				Size     int64 `json:"size"`
			} `json:"meta"`
		} `json:"data"`
	}
	if err := c.metadataJSON(ctx, "GET", request.BaseURL+"/models", nil, &models); err != nil {
		d.Reason = "model_metadata_unavailable"
		return d, llm.ErrContextUnverified
	}
	found := false
	training := 0
	size := int64(0)
	for _, m := range models.Data {
		if m.ID == request.Model {
			found = true
			training = m.Meta.Training
			size = m.Meta.Size
			if m.Meta.Context > 0 && m.Meta.Context != props.Default.Context {
				d.Reason = "runtime_metadata_disagrees"
				return d, llm.ErrContextUnverified
			}
			break
		}
	}
	if !found {
		d.Reason = "model_identity_unverified"
		return d, llm.ErrContextUnverified
	}
	if props.Template == "" || props.Build == "" || props.Path == "" || props.FType == "" || size <= 0 {
		d.Reason = "runtime_identity_incomplete"
		return d, llm.ErrContextUnverified
	}
	server := props.Default.Context
	d = domain.NewContextBudget(configured, request.ContextWindow, output, &server)
	d.ObservedAt = time.Now().UTC()
	d.ServerSource = "props.default_generation_settings.n_ctx:single_slot"
	if training > 0 {
		d.TrainingContext = &training
	}
	identity, _ := json.Marshal([]any{props.Alias, props.Path, props.Template, props.Build, props.FType, training, size})
	sum := sha256.Sum256(identity)
	d.RuntimeFingerprint = hex.EncodeToString(sum[:])
	d.RuntimeBuild = props.Build
	d.ModelQuantization = props.FType
	templateSum := sha256.Sum256([]byte(props.Template))
	d.ChatTemplateHash = hex.EncodeToString(templateSum[:])
	if request.RuntimeFingerprint != "" && request.RuntimeFingerprint != d.RuntimeFingerprint {
		d.ContextStatus = "context_mismatch"
		d.Decision = "blocked"
		d.Reason = "runtime_identity_changed"
	}
	if d.ContextStatus != "matched" {
		return d, llm.ErrContextMismatch
	}
	return d, nil
}

func (c *Client) CheckContextRequest(ctx context.Context, request llm.ChatRequest) (domain.ContextBudget, error) {
	d, err := c.InspectContext(ctx, request)
	if err != nil {
		return d, err
	}
	request, err = c.resolveRequest(request)
	if err != nil {
		return d, err
	}
	if len(request.Messages) == 0 {
		return d, nil
	}
	countCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	body := c.prepare(request, false)
	var rendered struct {
		Prompt string `json:"prompt"`
	}
	root := strings.TrimSuffix(request.BaseURL, "/v1")
	input := map[string]any{"messages": body.Messages, "add_generation_prompt": true, "chat_template_kwargs": body.ChatTemplateKwargs, "response_format": body.ResponseFormat}
	if err := c.metadataJSON(countCtx, "POST", root+"/apply-template", input, &rendered); err != nil || rendered.Prompt == "" {
		d.Reason = "template_render_unavailable"
		d.Decision = "unverified"
		return d, llm.ErrContextUnverified
	}
	var tokenized struct {
		Tokens []int `json:"tokens"`
	}
	if err := c.metadataJSON(countCtx, "POST", root+"/tokenize", map[string]any{"content": rendered.Prompt, "add_special": true, "parse_special": true}, &tokenized); err != nil || len(tokenized.Tokens) == 0 {
		d.Reason = "tokenizer_unavailable"
		d.Decision = "unverified"
		return d, llm.ErrContextUnverified
	}
	n := len(tokenized.Tokens)
	d.PromptTokens = &n
	d.CountMethod = "server_template_tokenize"
	d.Decision = "fits"
	if d.AvailableInputTokens == nil || n > *d.AvailableInputTokens {
		d.Decision = "exceeds"
		d.Reason = "rendered_prompt_too_large"
		return d, llm.ErrContextBudget
	}
	return d, nil
}
