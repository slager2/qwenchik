package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/runtime/resources"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

var contextAnchors = []struct{ ID, Quote string }{{"ctx-begin", "The calibration mineral is basalt."}, {"ctx-middle", "The calibration instrument is a spectrometer."}, {"ctx-end", "The calibration container is amber glass."}}

func (s *Server) SetContextProbe(enabled bool, window, output int, check func(context.Context) error) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.contextProbeEnabled = enabled
	s.contextProbeWindow = window
	s.contextProbeOutput = output
	s.contextProbeResourceCheck = check
}

func contextProbeRequest(window, output, fill int) llm.ChatRequest {
	line := "Background log: this record describes routine sample inventory and is unrelated to the requested calibration anchors.\n"
	background := strings.Repeat(line, fill/len(line)+1)[:fill]
	mid := len(background) / 2
	text := fmt.Sprintf("CONTROLLED SYNTHETIC CONTEXT FIXTURE. Source [%s]: %s\n%s\nSource [%s]: %s\n%s\nSource [%s]: %s\nReturn the exact three calibration findings with their source IDs and exact source quotes. Ignore the unrelated background. This tests retention, not independent factual truth.", contextAnchors[0].ID, contextAnchors[0].Quote, background[:mid], contextAnchors[1].ID, contextAnchors[1].Quote, background[mid:], contextAnchors[2].ID, contextAnchors[2].Quote)
	schema := json.RawMessage(`{"type":"object","properties":{"findings":{"type":"array","minItems":3,"maxItems":3,"items":{"type":"object","properties":{"id":{"type":"string","enum":["ctx-begin","ctx-middle","ctx-end"]},"quote":{"type":"string","minLength":1,"maxLength":256}},"required":["id","quote"],"additionalProperties":false}}},"required":["findings"],"additionalProperties":false}`)
	no := false
	return llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "system", Content: "Extract the three requested facts from the supplied synthetic fixture. Return only the schema-bound JSON, no reasoning. Cite only the fixture's source IDs and verbatim quotes."}, {Role: "user", Content: text}}, JSONSchema: schema, ContextWindow: window, ContextOutputReserve: output, MaxTokens: output, Temperature: 0, TemperatureSet: true, TopP: 1, TopPSet: true, EnableThinking: &no, CachePrompt: &no}
}

func (s *Server) contextProbe(w http.ResponseWriter, r *http.Request) {
	s.healthMu.RLock()
	enabled, window, output, check := s.contextProbeEnabled, s.contextProbeWindow, s.contextProbeOutput, s.contextProbeResourceCheck
	s.healthMu.RUnlock()
	if !enabled {
		writeError(w, 404, "context_probe_disabled", "Context probe отключён.", false)
		return
	}
	if !s.contextProbeMu.TryLock() {
		writeError(w, 409, "context_probe_busy", "Другой diagnostic probe уже выполняется.", true)
		return
	}
	defer s.contextProbeMu.Unlock()
	var input struct {
		Context int `json:"context"`
	}
	if decodeRequest(w, r, &input) != nil {
		return
	}
	if input.Context != window || (window != 32768 && window != 65536 && window != 98304 && window != 131072) {
		writeError(w, 400, "context_probe_mismatch", "Probe context должен совпадать с текущим настроенным rung.", false)
		return
	}
	if worker, ok := s.worker.(interface{ Busy() bool }); ok && worker.Busy() {
		writeError(w, 409, "inference_busy", "Worker занят; probe не запускается.", true)
		return
	}
	inspector, ok := s.llmClient.(interface {
		CheckContextRequest(context.Context, llm.ChatRequest) (domain.ContextBudget, error)
	})
	probe, guarded := s.llmClient.(interface {
		TryComplete(context.Context, llm.ChatRequest) (llm.ChatResponse, error)
	})
	if !ok || !guarded || check == nil {
		writeError(w, 503, "context_probe_unavailable", "Нет shared guard, context inspector или resource check.", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	started := time.Now()
	receipt := map[string]any{"type": "context_load_probe", "protocol_version": 1, "context": window, "output_budget": output, "cache_prompt": false, "fixture": "calibration-anchors-v1", "synthetic": true, "independent_factual_accuracy": nil, "status": "failed", "observed_at": started.UTC(), "target_exercised": false}
	finish := func(code string) {
		receipt["error_code"] = code
		receipt["wall_seconds"] = time.Since(started).Seconds()
		writeJSON(w, 200, receipt)
	}
	if err := check(ctx); err != nil {
		receipt["status"] = "resource_limited"
		finish("resource_preflight")
		return
	}
	// Binary-search actual rendered tokens, not a byte estimate. No arbitrary
	// prompt or URL is accepted; generation stays in the existing shared guard.
	lo, hi := 0, min(2<<20, window*8)
	var selected llm.ChatRequest
	var budget domain.ContextBudget
	for attempt := 0; attempt < 22 && lo <= hi; attempt++ {
		if err := ctx.Err(); err != nil {
			finish("preparation_timeout")
			return
		}
		mid := (lo + hi) / 2
		request := contextProbeRequest(window, output, mid)
		d, err := inspector.CheckContextRequest(ctx, request)
		if err != nil && !errors.Is(err, llm.ErrContextBudget) {
			receipt["context_budget"] = d
			finish("context_unverified")
			return
		}
		if d.PromptTokens == nil || d.AvailableInputTokens == nil {
			finish("token_count_unavailable")
			return
		}
		if *d.PromptTokens <= *d.AvailableInputTokens {
			selected = request
			budget = d
			lo = mid + 1
			if *d.AvailableInputTokens-*d.PromptTokens <= 64 {
				break
			}
		} else {
			hi = mid - 1
		}
	}
	if len(selected.Messages) == 0 {
		finish("target_not_prepared")
		return
	}
	receipt["context_budget"] = budget
	receipt["target_prompt_tokens"] = *budget.AvailableInputTokens
	sum := sha256.Sum256([]byte(selected.Messages[1].Content))
	receipt["input_sha256"] = hex.EncodeToString(sum[:])
	selected.RuntimeFingerprint = budget.RuntimeFingerprint
	if err := check(ctx); err != nil {
		receipt["status"] = "resource_limited"
		finish("resource_before_generation")
		return
	}
	genCtx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	watchCtx, cancelWatch := context.WithCancel(genCtx)
	defer cancelWatch()
	watched := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				watched <- nil
				return
			case <-ticker.C:
				if err := check(watchCtx); err != nil {
					if watchCtx.Err() != nil && genCtx.Err() == nil {
						watched <- nil
						return
					}
					stop(err)
					watched <- err
					return
				}
			}
		}
	}()
	response, err := probe.TryComplete(genCtx, selected)
	cancelWatch()
	resourceErr := <-watched
	receipt["inference"] = response.Telemetry
	receipt["provider_prompt_tokens"] = nil
	receipt["provider_output_tokens"] = nil
	if response.PromptTokens > 0 {
		receipt["provider_prompt_tokens"] = response.PromptTokens
	}
	if response.OutputTokens > 0 {
		receipt["provider_output_tokens"] = response.OutputTokens
	}
	receipt["cache_protocol_verified"] = nil
	if response.Telemetry != nil && response.Telemetry.CachedPromptTokens.Value != nil {
		receipt["cache_protocol_verified"] = *response.Telemetry.CachedPromptTokens.Value == 0
	}
	receipt["output"] = response.Content
	if resourceErr != nil {
		receipt["status"] = "resource_limited"
		finish("resource_guard")
		return
	}
	if finalErr := check(ctx); finalErr != nil {
		receipt["status"] = "resource_limited"
		finish("resource_final_check")
		return
	}
	if err != nil {
		code := "model_error"
		if errors.Is(err, llm.ErrInferenceBusy) {
			code = "inference_busy"
		}
		if errors.Is(err, llm.ErrModelTimeout) {
			code = "timeout"
		}
		if errors.Is(err, resources.ErrModelOOM) {
			code = "model_oom"
		}
		if cause := context.Cause(genCtx); cause != nil && !errors.Is(cause, context.DeadlineExceeded) {
			code = "resource_guard"
			receipt["status"] = "resource_limited"
		}
		finish(code)
		return
	}
	parity := math.Abs(float64(response.PromptTokens-*budget.PromptTokens)) <= 2
	target := parity && *budget.AvailableInputTokens-*budget.PromptTokens <= 128
	receipt["template_count_parity"] = parity
	receipt["target_exercised"] = target
	var answer struct {
		Findings []struct {
			ID    string `json:"id"`
			Quote string `json:"quote"`
		} `json:"findings"`
	}
	valid := 0
	seen := map[string]bool{}
	decoder := json.NewDecoder(strings.NewReader(response.Content))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&answer)
	trailing := decoder.Decode(new(any))
	if decodeErr == nil && errors.Is(trailing, io.EOF) && len(answer.Findings) == 3 {
		for _, finding := range answer.Findings {
			for _, anchor := range contextAnchors {
				if finding.ID == anchor.ID && finding.Quote == anchor.Quote && !seen[finding.ID] {
					valid++
					seen[finding.ID] = true
				}
			}
		}
	}
	if len(seen) != 3 {
		valid = 0
	}
	receipt["fixture_reference_score"] = float64(valid) / 3
	receipt["fixture_retention_passed"] = valid == 3
	if target && valid == 3 && receipt["cache_protocol_verified"] == true {
		receipt["status"] = "passed"
		finish("")
		return
	}
	if receipt["cache_protocol_verified"] != true {
		finish("uncached_prefill_not_established")
		return
	}
	finish("target_or_retention_not_established")
}
