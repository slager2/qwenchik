package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/config"
	"github.com/slager2/qwenchik/internal/workerlock"
)

func TestDoctorModelDelegatesToRunningServerWithoutDirectInference(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected direct inference", 500)
	}))
	defer model.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/doctor" {
			t.Errorf("path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"checks":{"llm":{"status":"ok","message":"Inference smoke deferred while busy"}}}`)
	}))
	defer app.Close()
	result := doctorModel(context.Background(), config.Config{BindAddr: strings.TrimPrefix(app.URL, "http://"), LLMBaseURL: model.URL}, true)
	if result.Status != "ok" || calls.Load() != 0 {
		t.Fatalf("check=%#v direct=%d", result, calls.Load())
	}
}

func TestDoctorModelDelegationFailureNeverFallsBackToInference(t *testing.T) {
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer model.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "invalid fixture", 500) }))
	defer app.Close()
	result := doctorModel(context.Background(), config.Config{BindAddr: strings.TrimPrefix(app.URL, "http://"), LLMBaseURL: model.URL}, true)
	if result.Status != "warning" || calls.Load() != 0 {
		t.Fatalf("check=%#v direct=%d", result, calls.Load())
	}
}

func TestDoctorModelStandaloneHonorsWorkerOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	lock, e := workerlock.Acquire(path + ".worker.lock")
	if e != nil {
		t.Fatal(e)
	}
	defer lock.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	result := doctorModel(context.Background(), config.Config{DatabasePath: path, LLMBaseURL: server.URL}, false)
	if result.Status != "warning" || calls.Load() != 0 {
		t.Fatalf("check=%#v direct=%d", result, calls.Load())
	}
}

func TestDoctorModelStandaloneStructuredSmoke(t *testing.T) {
	const modelName = "local-qwen"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `{"data":[]}`)
		case "/chat/completions":
			calls.Add(1)
			var request struct {
				Model          string  `json:"model"`
				MaxTokens      int     `json:"max_tokens"`
				Temperature    float64 `json:"temperature"`
				TopP           float64 `json:"top_p"`
				ResponseFormat struct {
					Type string `json:"type"`
				} `json:"response_format"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode structured smoke request: %v", err)
			}
			if r.Method != http.MethodPost || request.Model != modelName || request.MaxTokens != 24 || request.Temperature != 0 || request.TopP != 1 || request.ResponseFormat.Type != "json_schema" {
				t.Errorf("unexpected structured smoke method=%s request=%#v", r.Method, request)
			}
			fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	result := doctorModel(context.Background(), config.Config{DatabasePath: filepath.Join(t.TempDir(), "test.db"), LLMBaseURL: server.URL, LLMModel: modelName}, false)
	if result.Status != "ok" || calls.Load() != 1 {
		t.Fatalf("check=%#v calls=%d", result, calls.Load())
	}
}

func TestDoctorDelegatesOneSafeRunningPolicySnapshotWithoutLocalProbes(t *testing.T) {
	var healthCalls, doctorCalls, modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelCalls.Add(1) }))
	defer model.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/health":
			healthCalls.Add(1)
			fmt.Fprint(w, `{"worker":"attached"}`)
		case "/api/doctor":
			doctorCalls.Add(1)
			fmt.Fprint(w, `{"status":"warning","checks":{"app":{"status":"ok","message":"private-fixture"},"llm":{"status":"ok","message":"private-fixture"},"pdf_extractor":{"status":"warning","message":"private-fixture"},"private-fixture":{"status":"error"}},"research_limits":{"configured":true,"task_fetch_budget_bytes":134217728,"source_fetch_bytes":16777216,"pdf_bytes":67108864,"pdf_pages":20,"pdf_allow_unsandboxed_fallback":false,"api_key":"private-fixture","fetch_temp_dir":"private-fixture","pdf_memory_bytes":-1,"pdf_text_runes":"private-fixture"},"pdf_extractor":{"isolated_available":false,"isolated_status":"private-fixture","fallback_allowed":false,"stderr":"private-fixture"},"api_key":"private-fixture"}`)
		default:
			t.Errorf("unexpected app path %s", r.URL.Path)
		}
	}))
	defer app.Close()
	root := t.TempDir()
	cfg := config.Config{BindAddr: strings.TrimPrefix(app.URL, "http://"), LLMBaseURL: model.URL, MaxFetchBytes: 7, DatabasePath: filepath.Join(root, "must-not-open.db"), ArtifactsPath: filepath.Join(root, "must-not-create-artifacts")}
	cfg.PDFPolicy.MaxInputBytes, cfg.PDFPolicy.AllowUnsandboxedFallback = 1, true
	report := doctor(context.Background(), cfg)
	if report.Status != "warning" || report.ResearchLimits["task_fetch_budget_bytes"] != float64(134217728) || report.ResearchLimits["pdf_bytes"] != float64(67108864) || report.ResearchLimits["pdf_allow_unsandboxed_fallback"] != false || report.PDFExtractor["isolated_available"] != false || report.PDFExtractor["isolated_status"] != "unavailable" || report.PDFExtractor["fallback_allowed"] != false {
		t.Fatalf("running policy was replaced or misreported: %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil || strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "api_key") || report.ResearchLimits["pdf_memory_bytes"] != nil || report.ResearchLimits["pdf_text_runes"] != nil {
		t.Fatal("delegated doctor exposed unallowlisted or invalid fields")
	}
	if healthCalls.Load() != 1 || doctorCalls.Load() != 1 || modelCalls.Load() != 0 {
		t.Fatalf("health=%d doctor=%d direct model=%d", healthCalls.Load(), doctorCalls.Load(), modelCalls.Load())
	}
	for _, path := range []string{cfg.DatabasePath, cfg.ArtifactsPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("delegated doctor ran local storage checks")
		}
	}
	client := doctorDelegationClient()
	if client.Timeout != 34*time.Second || client.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("delegation lost its bounded direct-local client")
	}
}

func TestDoctorDelegationCancellationNeverSubstitutesLocalPolicyOrInference(t *testing.T) {
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelCalls.Add(1) }))
	defer model.Close()
	started := make(chan struct{})
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/health" {
			fmt.Fprint(w, `{"worker":"attached"}`)
			return
		}
		close(started)
		<-r.Context().Done()
	}))
	defer app.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	cfg := config.Config{BindAddr: strings.TrimPrefix(app.URL, "http://"), LLMBaseURL: model.URL, MaxFetchBytes: 123}
	report := doctor(ctx, cfg)
	if report.Status != "warning" || report.Checks["llm"].Status != "warning" || report.ResearchLimits != nil || report.PDFExtractor != nil || modelCalls.Load() != 0 {
		t.Fatalf("cancelled delegation invented local readiness: %#v", report)
	}
	report = doctor(ctx, cfg)
	if report.Status != "warning" || report.ResearchLimits != nil || modelCalls.Load() != 0 {
		t.Fatal("already cancelled context started a standalone probe")
	}
}

func TestDoctorDelegationRejectsMalformedOversizedAndRedirectResponses(t *testing.T) {
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { modelCalls.Add(1) }))
	defer model.Close()
	for _, kind := range []string{"malformed", "oversized", "redirect", "http_error", "missing_policy"} {
		t.Run(kind, func(t *testing.T) {
			app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch kind {
				case "malformed":
					fmt.Fprint(w, `{"checks":`)
				case "oversized":
					fmt.Fprintf(w, `{"checks":{"llm":{"status":"ok"}},"private":"%s"}`, strings.Repeat("x", 1<<20))
				case "redirect":
					http.Redirect(w, r, model.URL, http.StatusFound)
				case "http_error":
					http.Error(w, "private-fixture", http.StatusInternalServerError)
				case "missing_policy":
					fmt.Fprint(w, `{"status":"ok","checks":{"llm":{"status":"ok"}}}`)
				}
			}))
			defer app.Close()
			report := runningAppDoctor(context.Background(), config.Config{BindAddr: strings.TrimPrefix(app.URL, "http://"), LLMBaseURL: model.URL, MaxFetchBytes: 123})
			if report.Status != "warning" || report.ResearchLimits != nil || report.PDFExtractor != nil || modelCalls.Load() != 0 {
				t.Fatalf("unavailable delegation substituted local readiness: %#v", report)
			}
		})
	}
}
