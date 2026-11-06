package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/tools"
)

type clarifyModel struct {
	content  string
	err      error
	block    bool
	requests []llm.ChatRequest
}

func (m *clarifyModel) Complete(ctx context.Context, request llm.ChatRequest) (llm.ChatResponse, error) {
	m.requests = append(m.requests, request)
	if m.block {
		<-ctx.Done()
		return llm.ChatResponse{}, ctx.Err()
	}
	return llm.ChatResponse{Content: m.content}, m.err
}

func (m *clarifyModel) Stream(context.Context, llm.ChatRequest) (<-chan llm.ChatEvent, error) {
	return nil, errors.New("not used")
}

func TestClarifyReturnsNormalizedOptions(t *testing.T) {
	model := &clarifyModel{content: `{"interpretations":[{"title":"С нуля самостоятельно","goal":"План самообучения биоинформатике с нуля."},{"title":"  Переход из IT ","goal":"План перехода  в биоинформатику для программиста."},{"title":"Дубль","goal":"план самообучения биоинформатике с нуля."}],"questions":[{"question":"Сколько часов в неделю?","options":["5","10","20+","10"]},{"question":"Бесполезный","options":["да"," "]}]}`}
	worker := New(nil, model, &tools.Registry{}, Options{TopP: 0.8})
	got := worker.Clarify(context.Background(), "сделай мне роадмап по биоинформатике")
	if got.Status != domain.ClarificationStatusReady || got.Reason != "" {
		t.Fatalf("status = %+v", got)
	}
	if len(got.Interpretations) != 2 || got.Interpretations[1].Title != "Переход из IT" || got.Interpretations[1].Goal != "План перехода в биоинформатику для программиста." || got.Interpretations[0].ID != "i1" {
		t.Fatalf("interpretations = %+v", got.Interpretations)
	}
	if len(got.Questions) != 1 || strings.Join(got.Questions[0].Options, ",") != "5,10,20+" || got.Questions[0].ID != "q1" {
		t.Fatalf("questions = %+v", got.Questions)
	}
	request := model.requests[0]
	if !request.TemperatureSet || request.Temperature != 0 || request.TopK == nil || request.PresencePenalty != nil || request.MaxTokens > clarifyMaxTokens {
		t.Fatalf("sampling = %+v", request)
	}
	if !strings.Contains(request.Messages[0].Content, "untrusted") || !strings.Contains(request.Messages[1].Content, `"request_untrusted"`) {
		t.Fatalf("prompt does not mark the request as untrusted: %+v", request.Messages)
	}
	var schema map[string]any
	if err := json.Unmarshal(request.JSONSchema, &schema); err != nil || schema["additionalProperties"] != false {
		t.Fatalf("schema = %s (%v)", request.JSONSchema, err)
	}
}

func TestClarifyFallsBackWithoutBlocking(t *testing.T) {
	for name, test := range map[string]struct {
		model  *clarifyModel
		reason string
	}{
		"model error":     {&clarifyModel{err: errors.New("connection refused")}, "model_unavailable"},
		"invalid json":    {&clarifyModel{content: `{"interpretations":[]`}, "invalid_output"},
		"schema mismatch": {&clarifyModel{content: `{"interpretations":[{"title":"a","goal":"b","extra":1}],"questions":[]}`}, "invalid_output"},
		"empty result":    {&clarifyModel{content: `{"interpretations":[{"title":" ","goal":" "}],"questions":[]}`}, "invalid_output"},
	} {
		worker := New(nil, test.model, &tools.Registry{}, Options{})
		got := worker.Clarify(context.Background(), "What is WAL?")
		if got.Status != domain.ClarificationStatusSkipped || got.Reason != test.reason || got.Interpretations == nil || got.Questions == nil {
			t.Fatalf("%s: %+v", name, got)
		}
	}
	worker := New(nil, &clarifyModel{}, &tools.Registry{}, Options{})
	if got := worker.Clarify(context.Background(), "   "); got.Reason != "invalid_request" {
		t.Fatalf("blank request: %+v", got)
	}
}

func TestClarifyTimesOut(t *testing.T) {
	model := &clarifyModel{block: true}
	worker := New(nil, model, &tools.Registry{}, Options{})
	previous := clarifyTimeout
	clarifyTimeout = 20 * time.Millisecond
	defer func() { clarifyTimeout = previous }()
	got := worker.Clarify(context.Background(), "What is WAL?")
	if got.Status != domain.ClarificationStatusSkipped || got.Reason != "timeout" {
		t.Fatalf("timeout: %+v", got)
	}
}
