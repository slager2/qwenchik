package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/tools"
)

func TestStructuredOutputSchemasConstrainRoleEnvelopes(t *testing.T) {
	tests := []struct {
		name    string
		schema  json.RawMessage
		valid   string
		invalid string
	}{
		{
			name:    "planner",
			schema:  planOutputSchema(4, []string{"analysis", "search", "fetch"}),
			valid:   `{"schema_version":1,"goal":"research","subtasks":[{"id":"step_1","kind":"search","description":"Find sources","dependencies":[],"expected_output":"hits","success_criteria":["source results are recorded"]}],"max_steps":1,"final_deliverable":"summary"}`,
			invalid: `{"schema_version":1,"goal":"research","subtasks":[{"id":"step_1","kind":"browser","description":"Open page","dependencies":[],"expected_output":"text","success_criteria":["source results are recorded"]}],"max_steps":1,"final_deliverable":"summary"}`,
		},
		{
			name: "action",
			schema: actionOutputSchema([]tools.Descriptor{{
				Name: "text.inspect", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
			}}, nil),
			valid:   `{"status":"continue","summary":"Counted words","tool_call":{"name":"text.inspect","arguments":{"text":"one two"}}}`,
			invalid: `{"status":"continue","summary":"Counted words","tool_call":{"name":"run_command_sandbox","arguments":{"command":"id"}}}`,
		},
		{
			name:    "verifier",
			schema:  verificationOutputSchema(),
			valid:   `{"complete":true,"summary":"The result is usable.","missing":[],"criterion_assessments":[{"step_id":"step_1","criterion_index":0,"met":true,"evidence_ids":[],"rationale":"The step result supports the criterion."}]}`,
			invalid: `{"complete":true,"summary":"The result is usable.","missing":[]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := tools.ValidateJSONSchema(test.schema, []byte(test.valid)); err != nil {
				t.Fatalf("valid role response rejected: %v", err)
			}
			if err := tools.ValidateJSONSchema(test.schema, []byte(test.invalid)); err == nil {
				t.Fatal("invalid role response passed schema validation")
			}
		})
	}
}

func TestWorkerPreservesLargeInputBudgetAndCountsPromptOverhead(t *testing.T) {
	model := &scriptedLLM{responses: []string{`{"ok":true}`}}
	worker := New(nil, model, tools.NewRegistry(), Options{MaxInputBytes: 100_000, MaxOutputTokens: 128})
	if worker.options.MaxInputBytes != 100_000 {
		t.Fatalf("worker input budget = %d, want configured 100000 bytes", worker.options.MaxInputBytes)
	}
	if !worker.requestFits("system", "user", json.RawMessage(`{"type":"object"}`)) {
		t.Fatal("small prompt unexpectedly exceeds configured budget")
	}
	worker.options.MaxInputBytes = 700
	_, err := worker.complete(context.Background(), strings.Repeat("s", 200), strings.Repeat("u", 200), json.RawMessage(strings.Repeat("x", 100)))
	if !errors.Is(err, errInputBudgetExceeded) {
		t.Fatalf("oversized system/user/schema input error = %v, want context budget error", err)
	}
	if model.requests != 0 {
		t.Fatalf("oversized request reached the model: %d requests", model.requests)
	}
}

func TestActionSchemaBindsArgumentsToSelectedTool(t *testing.T) {
	fetchDescriptor := tools.Descriptor{
		Name: "fetch_url", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"original_query":{"type":"string"}},"required":["url"],"additionalProperties":false}`),
	}
	schema := actionOutputSchema([]tools.Descriptor{
		{Name: "text.inspect", ArgumentsSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`)},
		fetchDescriptor,
	}, []string{"https://example.com"})
	if err := tools.ValidateJSONSchema(schema, []byte(`{"status":"continue","summary":"Fetch source","tool_call":{"name":"fetch_url","arguments":{"url":"https://example.com","original_query":null}}}`)); err != nil {
		t.Fatalf("valid fetch action rejected: %v", err)
	}
	if err := tools.ValidateJSONSchema(schema, []byte(`{"status":"continue","summary":"Fetch source","tool_call":{"name":"fetch_url","arguments":{"url":"https://other.example/","original_query":null}}}`)); err == nil {
		t.Fatal("URL outside the current unused-source set passed the action schema")
	}
	normalized, err := normalizeToolArguments(json.RawMessage(`{"url":"https://example.com","original_query":null}`), fetchDescriptor)
	if err != nil || string(normalized) != `{"url":"https://example.com"}` {
		t.Fatalf("optional null argument normalized to %s, err=%v", normalized, err)
	}
	if err := tools.ValidateJSONSchema(schema, []byte(`{"status":"continue","summary":"Inspect text","tool_call":{"name":"text.inspect","arguments":{"url":"https://example.com"}}}`)); err == nil {
		t.Fatal("arguments for another tool passed the action schema")
	}
	if err := tools.ValidateJSONSchema(schema, []byte(`{"status":"continue","summary":"Fetch source","tool_call":{"name":"fetch_url","arguments":{"url":"https://example.com","command":"id"}}}`)); err == nil {
		t.Fatal("extra tool argument passed the action schema")
	}
}

func TestResearchPlanSchemaRequiresConditionalSearchRetryAndTwoFetches(t *testing.T) {
	requiredKinds := []string{"search", "search", "fetch", "fetch"}
	schema := planOutputSchemaWithRequiredKinds(12, []string{"analysis", "search", "fetch", "code_execution", "external_action"}, requiredKinds)
	searchIDs := []string{"search_1", "search_retry_1"}
	steps := make([]map[string]any, 0, len(requiredKinds))
	for index, searchID := range searchIDs {
		dependencies := []string{}
		description := "Search for the core topic"
		if index == 1 {
			dependencies = []string{"search_1"}
			description = "Broaden the first query only if it returned no results"
		}
		steps = append(steps, map[string]any{
			"id": searchID, "kind": "search", "description": description,
			"dependencies": dependencies, "expected_output": "search hits", "success_criteria": []string{"the search step result is recorded"},
		})
	}
	steps = append(steps, map[string]any{
		"id": "search_2", "kind": "search", "description": "Search an additional user-requested research dimension",
		"dependencies": []string{}, "expected_output": "additional search hits", "success_criteria": []string{"the additional research question is searched"},
	})
	searchIDs = append(searchIDs, "search_2")
	for index := 1; index <= 2; index++ {
		steps = append(steps, map[string]any{
			"id": fmt.Sprintf("fetch_%d", index), "kind": "fetch", "description": fmt.Sprintf("Fetch a distinct research source %d", index),
			"dependencies": searchIDs, "expected_output": "evidence", "success_criteria": []string{"a source with evidence is stored"},
		})
	}
	plan := map[string]any{"schema_version": 1, "goal": "research a technical topic", "subtasks": steps, "max_steps": 5, "final_deliverable": "answer"}
	valid, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateJSONSchema(schema, valid); err != nil {
		t.Fatalf("valid required research plan rejected: %v", err)
	}
	invalidSteps := append([]map[string]any(nil), steps...)
	invalidSteps[1], invalidSteps[2] = invalidSteps[2], invalidSteps[1]
	plan["subtasks"] = invalidSteps
	invalid, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateJSONSchema(schema, invalid); err == nil {
		t.Fatal("plan with incorrect retry/fetch order passed the schema")
	}
	steps = append(steps, map[string]any{
		"id": "code_1", "kind": "code_execution", "description": "Run a bounded task-specific calculation",
		"dependencies": []string{"fetch_1"}, "expected_output": "calculation result", "success_criteria": []string{"calculation result is recorded"},
	})
	plan["subtasks"] = steps
	plan["max_steps"] = 7
	validWithAdditionalAction, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateJSONSchema(schema, validWithAdditionalAction); err != nil {
		t.Fatalf("required research plan rejected an additional supported action: %v", err)
	}
}

func TestResearchPlanSchemaCanFetchSuppliedURLBeforeDiscovery(t *testing.T) {
	requiredKinds := []string{"fetch", "search", "search", "fetch"}
	schema := planOutputSchemaWithRequiredKinds(8, []string{"analysis", "search", "fetch", "code_execution", "external_action"}, requiredKinds)
	searchIDs := []string{"search_1", "search_retry_1"}
	steps := []map[string]any{{
		"id": "fetch_1", "kind": "fetch", "description": "Fetch the user-provided URL first",
		"dependencies": []string{}, "expected_output": "user source evidence", "success_criteria": []string{"user source evidence is stored"},
	}}
	for index, searchID := range searchIDs {
		dependencies := []string{"fetch_1"}
		description := fmt.Sprintf("Investigate a distinct question %d", index+1)
		if index == 1 {
			dependencies = []string{"fetch_1", "search_1"}
			description = "Broaden the discovery query only if the first search has no results"
		}
		steps = append(steps, map[string]any{
			"id": searchID, "kind": "search", "description": description,
			"dependencies": dependencies, "expected_output": "discovery hits", "success_criteria": []string{"discovery results are recorded"},
		})
	}
	for index := 2; index <= 2; index++ {
		steps = append(steps, map[string]any{
			"id": fmt.Sprintf("fetch_%d", index), "kind": "fetch", "description": fmt.Sprintf("Fetch a discovered source %d", index-1),
			"dependencies": searchIDs, "expected_output": "source evidence", "success_criteria": []string{"source evidence is stored"},
		})
	}
	plan := map[string]any{"schema_version": 1, "goal": "compare supplied and discovered sources", "subtasks": steps, "max_steps": 4, "final_deliverable": "cited comparison"}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := tools.ValidateJSONSchema(schema, encoded); err != nil {
		t.Fatalf("URL-first research plan rejected: %v", err)
	}
}

func TestStructuredOutputRepairKeepsSchemaAndRetriesOnce(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	for _, test := range []struct {
		name      string
		responses []string
		wantErr   bool
	}{
		{name: "repairs once", responses: []string{`{"ok":"yes"}`, `{"ok":true}`}},
		{name: "fails closed after one repair", responses: []string{`{"ok":"yes"}`, `{"ok":false,"extra":1}`}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &scriptedLLM{responses: test.responses}
			worker := &Worker{client: model, options: Options{MaxInputBytes: 1024, MaxOutputTokens: 128}}
			var result struct {
				OK bool `json:"ok"`
			}
			err := worker.completeJSONWithRepair(context.Background(), "Return JSON only.", "{\"request\":\"test\"}", schema, &result)
			if test.wantErr {
				if !errors.Is(err, errInvalidModelOutput) {
					t.Fatalf("error = %v, want invalid model output", err)
				}
			} else if err != nil || !result.OK {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
			if len(model.seen) != 2 {
				t.Fatalf("model requests = %d, want initial plus one repair", len(model.seen))
			}
			for i, request := range model.seen {
				if string(request.JSONSchema) != string(schema) || request.JSONMode {
					t.Fatalf("request %d schema mode = JSONSchema %s, JSONMode %t", i, request.JSONSchema, request.JSONMode)
				}
			}
			if !strings.Contains(model.seen[1].Messages[1].Content, "validation_error") {
				t.Fatalf("repair request omitted the bounded validation reason: %s", model.seen[1].Messages[1].Content)
			}
		})
	}
}
