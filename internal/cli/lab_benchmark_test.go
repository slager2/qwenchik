package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/slager2/qwenchik/internal/domain"
	"github.com/slager2/qwenchik/internal/providers/research"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBenchmarkManifestHasFixedHeldoutCasesAndImportedSources(t *testing.T) {
	cases, err := labCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 12 {
		t.Fatalf("cases=%d", len(cases))
	}
	corpus, err := research.LoadOfflineCorpus("testdata/lab-corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	if corpus.SHA256() != labHash(labCorpusJSON) {
		t.Fatal("embedded corpus bytes differ")
	}
	heldout := 0
	for _, c := range cases {
		if c.Split == "heldout" {
			heldout++
		}
		for _, u := range c.SourceURLs {
			if err := corpus.ValidateURL(context.Background(), u); err != nil {
				t.Fatalf("%s missing %s", c.ID, u)
			}
		}
	}
	if heldout != 4 {
		t.Fatalf("heldout=%d", heldout)
	}
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"lab", "benchmark", "--print-corpus"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), labCorpusJSON) {
		t.Fatal("printed corpus changed bytes")
	}
}
func TestBenchmarkScoresOwnedReferencesWithoutInventingFactualAccuracy(t *testing.T) {
	c := labBenchmarkCase{ID: "case", Split: "heldout", Concepts: []string{"FASTA", "FASTQ"}, SourceURLs: []string{"https://a.example/source", "https://b.example/source"}}
	evidence := []domain.Evidence{{ID: "owned", TaskID: "run", CanonicalURL: c.SourceURLs[0], ContentSHA256: "hash", TextArtifactID: "artifact"}, {ID: "foreign", TaskID: "other", CanonicalURL: c.SourceURLs[1], ContentSHA256: "hash", TextArtifactID: "artifact"}}
	r := scoreLabCase(c, "FASTA [owned] and [foreign]", evidence, "run")
	if r.ConceptHitRate != .5 || r.SourceRecall != .5 || r.CitationReferenceValidity == nil || *r.CitationReferenceValidity != .5 {
		t.Fatalf("scores=%+v", r)
	}
	if r.IndependentFactualAccuracy != nil || r.TotalOutputTokens != nil {
		t.Fatal("invented unavailable measurements")
	}
	r = scoreLabCase(c, "No citations", nil, "run")
	if r.CitationReferenceValidity != nil || r.SourceRecall != 0 {
		t.Fatal("empty evidence scored as validated")
	}
}
func TestBenchmarkComparisonRejectsChangedSnapshots(t *testing.T) {
	cases, _ := labCases()
	b := labBenchmarkResult{ScoringComplete: true, CaseID: cases[0].ID, Variant: "baseline", DatasetSHA256: labHash(labCasesJSON), CorpusSHA256: labHash(labCorpusJSON), ModelProfileHash: "same-model", BudgetHash: "same-budget", Status: domain.StatusCompleted}
	a := b
	a.Variant = "adaptive"
	a.SourceRecall = 1
	base := map[string]labBenchmarkResult{b.CaseID: b}
	adaptive := map[string]labBenchmarkResult{a.CaseID: a}
	rows, err := compareLabResults(base, adaptive)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	a.ModelProfileHash = "different"
	adaptive[a.CaseID] = a
	if _, err := compareLabResults(base, adaptive); err == nil {
		t.Fatal("compared different model profiles")
	}
}
func TestBenchmarkCancelsOwnRunOnPolicyMismatch(t *testing.T) {
	cancelled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/conversations":
			_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
		case "/api/conversations/conversation/messages":
			_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
		case "/api/runs/run/runtime":
			_, _ = w.Write([]byte(`{"task_id":"run","status":"running","privacy_mode":"local_inference_external_search"}`))
		case "/api/runs/run/cancel":
			cancelled = true
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cases, _ := labCases()
	result, err := runLabCase(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[0], "baseline", time.Minute)
	if err == nil || result.ErrorCode != "benchmark_policy_mismatch" || !cancelled {
		t.Fatalf("result=%+v err=%v cancelled=%v", result, err, cancelled)
	}
}

func TestBenchmarkRejectsIncompleteCollectionAndCleansAmbiguousSubmission(t *testing.T) {
	for _, mode := range []string{"messages_missing", "evidence_failed", "ambiguous_submission"} {
		t.Run(mode, func(t *testing.T) {
			deleted := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "POST" && r.URL.Path == "/api/conversations":
					_ = json.NewEncoder(w).Encode(domain.Conversation{ID: "conversation"})
				case r.Method == "DELETE" && r.URL.Path == "/api/conversations/conversation":
					deleted = true
					_, _ = w.Write([]byte(`{}`))
				case r.Method == "POST" && r.URL.Path == "/api/conversations/conversation/messages":
					if mode == "ambiguous_submission" {
						_, _ = w.Write([]byte(`invalid`))
						return
					}
					_ = json.NewEncoder(w).Encode(domain.SubmitResponse{Run: domain.Run{ID: "run", AssistantMessageID: "answer"}})
				case r.URL.Path == "/api/runs/run/runtime":
					_ = json.NewEncoder(w).Encode(labRuntime{RunID: "run", ResearchSessionID: "session", PrivacyMode: domain.PrivacyOffline, CorpusSHA256: labHash(labCorpusJSON), SectionedReportEnabled: true, ModelProfileHash: "model", Status: domain.StatusCompleted})
				case r.Method == "GET" && r.URL.Path == "/api/conversations/conversation/messages":
					if mode == "messages_missing" {
						_, _ = w.Write([]byte(`[]`))
						return
					}
					_ = json.NewEncoder(w).Encode([]domain.ConversationMessage{{ID: "answer", RunID: "run", Role: "assistant", Content: "answer"}})
				case r.URL.Path == "/api/runs/run/evidence":
					http.Error(w, "unavailable", 503)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cases, _ := labCases()
			row, err := runLabCase(context.Background(), &labClient{base: server.URL, client: server.Client()}, cases[0], "baseline", time.Minute)
			if err == nil || row.ErrorCode == "" || row.ScoringComplete {
				t.Fatalf("incomplete benchmark accepted: %+v %v", row, err)
			}
			if mode == "ambiguous_submission" && !deleted {
				t.Fatal("ambiguous committed submission was not cleaned up")
			}
		})
	}
}
