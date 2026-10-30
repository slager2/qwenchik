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

const defaultRuntimeMetadataCacheTTL = 5 * time.Second

// runtimeMetadataCache contains only runtime identity/configuration data.
// Rendered prompts and token counts are deliberately not cached: they depend
// on the individual request and must continue to be verified on every call.
type runtimeMetadataCache struct {
	root        string
	props       runtimePropsMetadata
	models      runtimeModelsMetadata
	fingerprint string
	expiresAt   time.Time
}

type runtimePropsMetadata struct {
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

type runtimeModelsMetadata struct {
	Data []struct {
		ID   string `json:"id"`
		Meta struct {
			Context  int   `json:"n_ctx"`
			Training int   `json:"n_ctx_train"`
			Size     int64 `json:"size"`
		} `json:"meta"`
	} `json:"data"`
}

type runtimeMetadataError struct {
	source string
	err    error
}

func (e *runtimeMetadataError) Error() string { return e.err.Error() }
func (e *runtimeMetadataError) Unwrap() error { return e.err }

func (c *Client) WithContextBudget(configured int) *Client {
	c.contextConfigured = configured
	return c
}

// WithRuntimeMetadataTTL overrides the short-lived cache used for /props and
// /models context admission metadata. A non-positive duration disables reuse,
// which is useful for diagnostics or runtimes that change configuration often.
func (c *Client) WithRuntimeMetadataTTL(ttl time.Duration) *Client {
	if ttl < 0 {
		ttl = 0
	}
	c.metadataMu.Lock()
	c.metadataCacheTTL = ttl
	c.metadataCache = nil
	c.metadataMu.Unlock()
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

func (c *Client) cachedRuntimeMetadata(ctx context.Context, request llm.ChatRequest, root string) (runtimeMetadataCache, error) {
	now := time.Now()
	// A caller-provided fingerprint is a pin to a specific runtime identity.
	// Revalidate it against the live metadata instead of trusting a possibly
	// stale cache entry. Requests without a pin may use the short TTL cache.
	forceRefresh := request.RuntimeFingerprint != ""
	if !forceRefresh {
		c.metadataMu.Lock()
		cached := c.metadataCache
		if cached != nil && cached.root == root && cached.expiresAt.After(now) {
			value := *cached
			c.metadataMu.Unlock()
			return value, nil
		}
		c.metadataMu.Unlock()
	}

	// Serialize misses so a burst of admission checks does not fan out into
	// duplicate metadata requests. Re-check after waiting for the in-flight
	// fetch, since another caller may have filled the cache already.
	c.metadataFetchMu.Lock()
	defer c.metadataFetchMu.Unlock()
	if !forceRefresh {
		c.metadataMu.Lock()
		cached := c.metadataCache
		if cached != nil && cached.root == root && cached.expiresAt.After(time.Now()) {
			value := *cached
			c.metadataMu.Unlock()
			return value, nil
		}
		c.metadataMu.Unlock()
	}

	var props runtimePropsMetadata
	if err := c.metadataJSON(ctx, http.MethodGet, root+"/props", nil, &props); err != nil {
		c.invalidateRuntimeMetadata(root)
		return runtimeMetadataCache{}, &runtimeMetadataError{source: "runtime", err: err}
	}
	var models runtimeModelsMetadata
	if err := c.metadataJSON(ctx, http.MethodGet, strings.TrimRight(request.BaseURL, "/")+"/models", nil, &models); err != nil {
		c.invalidateRuntimeMetadata(root)
		return runtimeMetadataCache{}, &runtimeMetadataError{source: "model", err: err}
	}

	// The fingerprint is computed after model lookup/identity validation in
	// InspectContext. It is filled there before publication, so a malformed or
	// incomplete response can never remain cached after validation fails.
	value := runtimeMetadataCache{root: root, props: props, models: models}
	// Publish the raw pair before returning so concurrent callers waiting on
	// metadataFetchMu can reuse the same response while the first caller runs
	// the semantic validation below. Invalid responses are removed by the
	// validation branches in InspectContext.
	c.metadataMu.Lock()
	if c.metadataCacheTTL > 0 {
		value.expiresAt = time.Now().Add(c.metadataCacheTTL)
		c.metadataCache = &value
	}
	c.metadataMu.Unlock()
	return value, nil
}

func (c *Client) publishRuntimeMetadata(value runtimeMetadataCache) {
	c.metadataMu.Lock()
	defer c.metadataMu.Unlock()
	if c.metadataCacheTTL <= 0 {
		c.metadataCache = nil
		return
	}
	// A changed identity invalidates the prior entry. The fresh value is still
	// published below so the next request does not repeat the same refresh.
	if c.metadataCache != nil && c.metadataCache.root == value.root && c.metadataCache.fingerprint != "" && value.fingerprint != "" && c.metadataCache.fingerprint != value.fingerprint {
		c.metadataCache = nil
	}
	value.expiresAt = time.Now().Add(c.metadataCacheTTL)
	c.metadataCache = &value
}

func (c *Client) invalidateRuntimeMetadata(root string) {
	c.metadataMu.Lock()
	defer c.metadataMu.Unlock()
	if c.metadataCache == nil || c.metadataCache.root == root {
		c.metadataCache = nil
	}
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
	metadata, err := c.cachedRuntimeMetadata(ctx, request, root)
	if err != nil {
		d.Reason = "runtime_metadata_unavailable"
		var metadataErr *runtimeMetadataError
		if errors.As(err, &metadataErr) && metadataErr.source == "model" {
			d.Reason = "model_metadata_unavailable"
		}
		return d, llm.ErrContextUnverified
	}
	props := metadata.props
	if props.Default.Context < 1 || props.Slots != 1 || props.Alias != request.Model {
		c.invalidateRuntimeMetadata(root)
		d.Reason = "runtime_metadata_ambiguous"
		return d, llm.ErrContextUnverified
	}
	models := metadata.models
	found := false
	training := 0
	size := int64(0)
	for _, m := range models.Data {
		if m.ID == request.Model {
			found = true
			training = m.Meta.Training
			size = m.Meta.Size
			if m.Meta.Context > 0 && m.Meta.Context != props.Default.Context {
				c.invalidateRuntimeMetadata(root)
				d.Reason = "runtime_metadata_disagrees"
				return d, llm.ErrContextUnverified
			}
			break
		}
	}
	if !found {
		c.invalidateRuntimeMetadata(root)
		d.Reason = "model_identity_unverified"
		return d, llm.ErrContextUnverified
	}
	if props.Template == "" || props.Build == "" || props.Path == "" || props.FType == "" || size <= 0 {
		c.invalidateRuntimeMetadata(root)
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
	metadata.fingerprint = d.RuntimeFingerprint
	c.publishRuntimeMetadata(metadata)
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
		// A failed runtime verification must not leave a previously cached
		// identity trusted for the next admission attempt.
		c.invalidateRuntimeMetadata(root)
		d.Reason = "template_render_unavailable"
		d.Decision = "unverified"
		return d, llm.ErrContextUnverified
	}
	var tokenized struct {
		Tokens []int `json:"tokens"`
	}
	if err := c.metadataJSON(countCtx, "POST", root+"/tokenize", map[string]any{"content": rendered.Prompt, "add_special": true, "parse_special": true}, &tokenized); err != nil || len(tokenized.Tokens) == 0 {
		c.invalidateRuntimeMetadata(root)
		d.Reason = "tokenizer_unavailable"
		d.Decision = "unverified"
		return d, llm.ErrContextUnverified
	}
	n := len(tokenized.Tokens)
	d.PromptTokens = &n
	d.CountMethod = "server_template_tokenize"
	d.Decision = "fits"
	if d.AvailableInputTokens == nil || n > *d.AvailableInputTokens {
		// Treat an over-budget prompt as a failed admission, not as evidence
		// that the runtime identity can remain cached indefinitely. The next
		// request will refresh /props and /models before retrying.
		c.invalidateRuntimeMetadata(root)
		d.Decision = "exceeds"
		d.Reason = "rendered_prompt_too_large"
		return d, llm.ErrContextBudget
	}
	return d, nil
}
