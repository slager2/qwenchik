package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/policy"
	"github.com/slager2/qwenchik/internal/storage/artifacts"
)

type workingSetTestRepository struct {
	mu        sync.Mutex
	evidence  []domain.Evidence
	artifacts []domain.Artifact
	snapshot  domain.ContextSnapshot
	tasks     []string
	patches   int
	err       error
}

func (r *workingSetTestRepository) Evidence(_ context.Context, task string) ([]domain.Evidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, task)
	return append([]domain.Evidence(nil), r.evidence...), r.err
}
func (r *workingSetTestRepository) Artifacts(_ context.Context, task string) ([]domain.Artifact, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, task)
	return append([]domain.Artifact(nil), r.artifacts...), r.err
}
func (r *workingSetTestRepository) ContextSnapshot(_ context.Context, task string) (domain.ContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, task)
	return r.snapshot, r.err
}
func (r *workingSetTestRepository) PatchWorkingSet(_ context.Context, task string, updates map[string]string) (domain.ContextSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, task)
	if r.err != nil {
		return domain.ContextSnapshot{}, r.err
	}
	r.patches++
	if r.snapshot.WorkingSet == nil {
		r.snapshot.WorkingSet = map[string]string{}
	}
	for key, value := range updates {
		r.snapshot.WorkingSet[key] = value
	}
	return r.snapshot, nil
}

type workingSetTestReader struct {
	store *artifacts.Store
	mu    sync.Mutex
	bytes int
	calls int
}

func (r *workingSetTestReader) ReadRange(ctx context.Context, task, id string, offset int64, limit int) ([]byte, error) {
	content, err := r.store.ReadRange(ctx, task, id, offset, limit)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.bytes += len(content)
	return content, err
}

func workingSetFixture(t *testing.T) (*Registry, *workingSetTestRepository, *workingSetTestReader, context.Context) {
	t.Helper()
	ctx := WithTaskID(context.Background(), "owned")
	store, err := artifacts.New(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := "prefix 東京 😀 alpha beta suffix"
	artifact, _, err := store.Put(ctx, "owned", "text.txt", "text/plain", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	foreign, _, err := store.Put(ctx, "foreign", "secret.txt", "text/plain", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	repo := &workingSetTestRepository{
		artifacts: []domain.Artifact{artifact, foreign},
		evidence: []domain.Evidence{
			{ID: "b", TaskID: "owned", HTTPStatus: 200, ExtractionMethod: "html", TextArtifactID: artifact.ID, Excerpt: "alpha", QuoteSpans: []domain.QuoteSpan{{Text: "alpha", Start: 12, End: 17, CoordinateSpace: "extracted_text_runes"}}},
			{ID: "a", TaskID: "owned", HTTPStatus: 200, ExtractionMethod: "html", TextArtifactID: artifact.ID, Excerpt: "東京 😀 alpha beta", QuoteSpans: []domain.QuoteSpan{{Text: "東京 😀", Start: 7, End: 11, CoordinateSpace: "extracted_text_runes"}}},
			{ID: "legacy", TaskID: "owned", HTTPStatus: 200, ExtractionMethod: "html", TextArtifactID: artifact.ID, Excerpt: "beta", QuoteSpans: []domain.QuoteSpan{{Text: "beta", Start: 0, End: 4}}},
			{ID: "foreign", TaskID: "foreign", HTTPStatus: 200, ExtractionMethod: "html", TextArtifactID: foreign.ID, Excerpt: "secret"},
			{ID: "rejected", TaskID: "owned", HTTPStatus: 403, ExtractionMethod: "html", TextArtifactID: artifact.ID, Excerpt: "alpha beta"},
			{ID: "flagged", TaskID: "owned", HTTPStatus: 200, ExtractionMethod: "html", TextArtifactID: artifact.ID, QualityFlags: []string{"empty_text"}, Excerpt: "alpha beta"},
		},
		snapshot: domain.ContextSnapshot{Version: 1, Goal: "protected goal", Constraints: []string{"keep"}, DoneCriteria: []string{"citations"}, AcceptedEvidenceIDs: []string{"a"}, BudgetRemaining: map[string]int64{"steps": 3}, NextAction: "review", WorkingSet: map[string]string{"notes": "old"}},
	}
	reader := &workingSetTestReader{store: store}
	cfg := WorkingSetTools{Repository: repo, Artifacts: reader}
	grants := []string{"task.evidence.read", "task.artifact.read", "task.context.read", "task.working_set.write"}
	registry := NewRegistry(policy.New(map[string][]string{"researcher": grants, "analyst": grants, "coder": grants}))
	if err := RegisterWorkingSetTools(registry, &cfg); err != nil {
		t.Fatal(err)
	}
	return registry, repo, reader, ctx
}

func TestWorkingSetPolicyAndRegistration(t *testing.T) {
	_, repo, reader, ctx := workingSetFixture(t)
	cfg := NewWorkingSetTools(repo, reader)
	registry := NewRegistry()
	if err := RegisterWorkingSetTools(registry, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, descriptor := range registry.Descriptors() {
		if descriptor.Name == "text.inspect" {
			continue
		}
		if !descriptor.Idempotent || descriptor.MaxOutputBytes > 32<<10 || descriptor.RiskClass != "read" {
			t.Fatalf("descriptor: %+v", descriptor)
		}
		_, decision := registry.Decide("researcher", descriptor.Name)
		if decision.Action != policy.Deny {
			t.Fatalf("default policy granted %s", descriptor.Name)
		}
	}
	if _, err := registry.Execute(ctx, "researcher", "context_select", json.RawMessage(`{}`)); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("default execution: %v", err)
	}
	if err := RegisterWorkingSetTools(registry, &cfg); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := RegisterWorkingSetTools(nil, &cfg); err == nil {
		t.Fatal("nil registry accepted")
	}
	if err := RegisterWorkingSetTools(NewRegistry(), &WorkingSetTools{}); err == nil {
		t.Fatal("missing dependencies accepted")
	}
}

func TestWorkingSetQueryDeterministicAndTaskScoped(t *testing.T) {
	registry, repo, reader, ctx := workingSetFixture(t)
	for i := 0; i < 5; i++ {
		value, err := registry.Execute(ctx, "analyst", "evidence_query", json.RawMessage(`{"query":"BETA alpha alpha"}`))
		if err != nil {
			t.Fatal(err)
		}
		records := value.([]workingSetRecord)
		if len(records) != 3 || records[0].EvidenceID != "a" || records[0].Score != 1 || records[1].EvidenceID != "b" || records[1].Score != .5 || records[2].EvidenceID != "legacy" {
			t.Fatalf("results: %+v", records)
		}
	}
	if reader.calls != 0 {
		t.Fatal("lexical query read artifacts")
	}
	for _, tool := range []string{"evidence_query", "fact_list", "citation_check"} {
		for _, id := range []string{"foreign", "rejected", "flagged", "invented"} {
			raw := `{"evidence_ids":["` + id + `"]}`
			if tool == "evidence_query" {
				raw = `{"query":"alpha","evidence_ids":["` + id + `"]}`
			}
			if _, err := registry.Execute(ctx, "researcher", tool, json.RawMessage(raw)); !errors.Is(err, ErrUnknownTarget) {
				t.Fatalf("%s %s: %v", tool, id, err)
			}
		}
	}
	for _, task := range repo.tasks {
		if task != "owned" {
			t.Fatalf("read other task %q", task)
		}
	}
}

func TestWorkingSetFactsVerifyUnicodeCoordinatesAndLegacyUnknown(t *testing.T) {
	registry, repo, reader, ctx := workingSetFixture(t)
	repo.evidence[0].QuoteSpans = append(repo.evidence[0].QuoteSpans, domain.QuoteSpan{Text: "fabricated", Start: 0, End: 10, CoordinateSpace: "extracted_text_runes"})
	value, err := registry.Execute(ctx, "researcher", "fact_list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	records := value.([]workingSetRecord)
	if len(records) != 3 {
		t.Fatalf("records %+v", records)
	}
	if records[0].QuoteSpan == nil || records[0].QuoteSpan.Start != 7 || records[0].Excerpt != "東京 😀" || records[0].Locator != "exact" {
		t.Fatalf("unicode span %+v", records[0])
	}
	if records[2].Locator != "unknown" || records[2].QuoteSpan != nil {
		t.Fatalf("legacy coordinates invented: %+v", records[2])
	}
	for _, record := range records {
		if record.Kind != "extractive_observation" || record.Status != "source_claim" {
			t.Fatalf("unsupported fact %+v", record)
		}
	}
	if reader.bytes > 64<<10 {
		t.Fatal("read budget exceeded")
	}
}

func TestWorkingSetCitationMembershipDoesNotAssertSupport(t *testing.T) {
	registry, _, reader, ctx := workingSetFixture(t)
	value, err := registry.Execute(ctx, "researcher", "citation_check", json.RawMessage(`{"evidence_ids":["a","a"],"quote":"東京 😀"}`))
	if err != nil {
		t.Fatal(err)
	}
	records := value.([]map[string]any)
	if len(records) != 1 || records[0]["citation_support"] != "unknown" || records[0]["quote_exact"] != true {
		t.Fatalf("citations %+v", records)
	}
	if _, err := registry.Execute(ctx, "researcher", "citation_check", json.RawMessage(`{"evidence_ids":["a"],"quote":"東京  😀"}`)); err == nil {
		t.Fatal("inexact quote accepted")
	}
	value, err = registry.Execute(ctx, "researcher", "citation_check", json.RawMessage(`{"evidence_ids":[]}`))
	if err != nil || len(value.([]map[string]any)) != 0 {
		t.Fatalf("empty request disclosed evidence: %v %v", value, err)
	}
	if reader.bytes > 64<<10 {
		t.Fatal("read budget exceeded")
	}
}

func TestWorkingSetArtifactReadChecksOwnershipBeforeIO(t *testing.T) {
	registry, repo, reader, ctx := workingSetFixture(t)
	id := repo.artifacts[0].ID
	value, err := registry.Execute(ctx, "analyst", "artifact_read", json.RawMessage(`{"artifact_id":"`+id+`","offset":0,"limit":6}`))
	if err != nil {
		t.Fatal(err)
	}
	record := value.(map[string]any)
	if record["content"] != "prefix" || record["next_offset"] != int64(6) || record["eof"] != false {
		t.Fatalf("range %+v", record)
	}
	for _, id := range []string{repo.artifacts[1].ID, "../secret", "https://example.org/file"} {
		before := reader.calls
		if _, err := registry.Execute(ctx, "researcher", "artifact_read", json.RawMessage(`{"artifact_id":"`+id+`"}`)); !errors.Is(err, ErrUnknownTarget) {
			t.Fatalf("target %q: %v", id, err)
		}
		if reader.calls != before {
			t.Fatal("unowned artifact reached reader")
		}
	}
}

func TestWorkingSetContextPreservesProtectedStateAndFailsNarrowLimit(t *testing.T) {
	registry, repo, _, ctx := workingSetFixture(t)
	value, err := registry.Execute(ctx, "researcher", "context_select", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(value, repo.snapshot) {
		t.Fatalf("snapshot changed: %+v", value)
	}
	if _, err := registry.Execute(ctx, "analyst", "context_select", json.RawMessage(`{"max_bytes":10}`)); err == nil {
		t.Fatal("silently truncated snapshot")
	}
	repo.snapshot.Goal = strings.Repeat("x", 13000)
	if _, err := registry.Execute(ctx, "analyst", "context_select", json.RawMessage(`{}`)); err == nil {
		t.Fatal("default context budget ignored")
	}
	if _, err := registry.Execute(ctx, "analyst", "context_select", json.RawMessage(`{"max_bytes":32768}`)); err != nil {
		t.Fatal(err)
	}
}

func TestWorkingSetUpdatesBoundedProtectedAndStableIdentity(t *testing.T) {
	registry, repo, _, ctx := workingSetFixture(t)
	before := repo.snapshot
	for _, key := range []string{"goal", "Goal", "constraints", "done_criteria", "budget", "evidence_ids", "accepted_evidence_ids", "pending_action", "next_action", "summary", "working_set", "task_id", "id", "completed_steps", "file_changes", "open_questions", "a.b", "1note"} {
		arguments, _ := json.Marshal(map[string]any{"updates": map[string]string{key: "replacement"}})
		if _, err := registry.Execute(ctx, "researcher", "working_set_update", arguments); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("reserved key %q: %v", key, err)
		}
	}
	if repo.patches != 0 {
		t.Fatal("invalid update reached storage")
	}
	a, err := registry.Execute(ctx, "researcher", "working_set_update", json.RawMessage(`{"updates":{"notes":"reviewed","scratch":"one"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := registry.Execute(ctx, "researcher", "working_set_update", json.RawMessage(`{"updates":{"scratch":"one","notes":"reviewed"}}`))
	if err != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("unstable identity: %v %v %v", a, b, err)
	}
	if repo.snapshot.Goal != before.Goal || !reflect.DeepEqual(repo.snapshot.Constraints, before.Constraints) || repo.snapshot.NextAction != before.NextAction || !reflect.DeepEqual(repo.snapshot.BudgetRemaining, before.BudgetRemaining) {
		t.Fatal("protected fields changed")
	}
}

func TestWorkingSetStrictArgumentsAndCancellation(t *testing.T) {
	registry, repo, _, ctx := workingSetFixture(t)
	cases := []struct{ name, raw string }{
		{"evidence_query", `{"query":"alpha","task_id":"foreign"}`},
		{"evidence_query", `{"query":"!!!"}`},
		{"fact_list", `{"max_results":9}`},
		{"artifact_read", `{"artifact_id":"x","limit":65537}`},
		{"artifact_read", `{"artifact_id":"x","offset":-1}`},
		{"context_select", `{"path":"/tmp/secret"}`},
		{"working_set_update", `{"updates":{"notes":1}}`},
		{"working_set_update", `{"updates":{}}`},
		{"working_set_update", `{"updates":{"notes":null}}`},
		{"citation_check", `{"evidence_ids":["a"],"url":"https://example.org"}`},
	}
	for _, tc := range cases {
		if _, err := registry.Execute(ctx, "researcher", tc.name, json.RawMessage(tc.raw)); !errors.Is(err, ErrInvalidArguments) {
			t.Fatalf("%s %s: %v", tc.name, tc.raw, err)
		}
	}
	if _, err := registry.Execute(context.Background(), "researcher", "context_select", json.RawMessage(`{}`)); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("untrusted task: %v", err)
	}
	if _, err := registry.Execute(ctx, "coder", "context_select", json.RawMessage(`{}`)); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("role: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := registry.Execute(cancelled, "researcher", "working_set_update", json.RawMessage(`{"updates":{"notes":"never"}}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if repo.patches != 0 {
		t.Fatal("cancelled update wrote")
	}
}

func TestWorkingSetResultAndReadBudgets(t *testing.T) {
	registry, repo, reader, ctx := workingSetFixture(t)
	text := strings.Repeat("東京😀", 600) + " alpha"
	artifact, _, err := reader.store.Put(ctx, "owned", "long.txt", "text/plain", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	repo.artifacts = append(repo.artifacts, artifact)
	repo.evidence = nil
	ids := []string{}
	for i := 0; i < 17; i++ {
		id := fmt.Sprintf("e%02d", i)
		ids = append(ids, id)
		repo.evidence = append(repo.evidence, domain.Evidence{ID: id, TaskID: "owned", HTTPStatus: 200, ExtractionMethod: "text", TextArtifactID: artifact.ID, Excerpt: text, QuoteSpans: []domain.QuoteSpan{{Text: text, Start: 0, End: len([]rune(text)), CoordinateSpace: "extracted_text_runes"}}})
	}
	for _, name := range []string{"evidence_query", "fact_list"} {
		raw := `{}`
		if name == "evidence_query" {
			raw = `{"query":"alpha"}`
		}
		value, err := registry.Execute(ctx, "researcher", name, json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		records := value.([]workingSetRecord)
		encoded, _ := json.Marshal(records)
		if len(records) == 0 || len(records) > 8 || len(encoded) > 32<<10 {
			t.Fatalf("unbounded %s: records=%d bytes=%d", name, len(records), len(encoded))
		}
		for _, record := range records {
			if len([]rune(record.Excerpt)) > 2000 {
				t.Fatal("excerpt limit exceeded")
			}
		}
	}
	if reader.bytes > 64<<10 {
		t.Fatalf("fact read budget: %d", reader.bytes)
	}
	arguments, _ := json.Marshal(map[string]any{"evidence_ids": ids})
	if _, err := registry.Execute(ctx, "researcher", "citation_check", arguments); !errors.Is(err, ErrInvalidArguments) {
		t.Fatalf("17 IDs accepted: %v", err)
	}
	reader.bytes = 0
	arguments, _ = json.Marshal(map[string]any{"evidence_ids": ids[:16], "quote": "alpha"})
	if _, err := registry.Execute(ctx, "researcher", "citation_check", arguments); err == nil {
		t.Fatal("oversized quote verification accepted")
	}
	if reader.bytes > 64<<10 {
		t.Fatalf("citation read budget: %d", reader.bytes)
	}
	oversized, _, err := reader.store.Put(ctx, "owned", "oversized.txt", "text/plain", []byte(strings.Repeat("x", 65537)))
	if err != nil {
		t.Fatal(err)
	}
	repo.artifacts = append(repo.artifacts, oversized)
	repo.evidence[0].TextArtifactID = oversized.ID
	before := reader.calls
	value, err := registry.Execute(ctx, "researcher", "fact_list", json.RawMessage(`{"evidence_ids":["e00"]}`))
	if err != nil || len(value.([]workingSetRecord)) != 0 || reader.calls != before {
		t.Fatalf("oversized fact artifact was read: %v %v", value, err)
	}
}
