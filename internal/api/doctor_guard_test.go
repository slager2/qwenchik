package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/llm"
	"github.com/slager2/qwenchik/internal/llm/openai"
)

func TestDoctorUsesSharedGuardAndDoesNotCacheBusyAsFailure(t *testing.T) {
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[]}`)
			return
		}
		requests.Add(1)
		var in struct {
			Stream bool `json:"stream"`
		}
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			return
		}
		if in.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`)
	}))
	defer model.Close()
	server, store := newTestServer(t)
	defer store.Close()
	server.llmURL = model.URL + "/v1"
	guard := llm.NewGuardedClient(openai.New(model.URL+"/v1", "", "fixture", time.Second, 128, 0, 1, false))
	server.SetLLMClient(guard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, e := guard.Stream(ctx, llm.ChatRequest{})
	if e != nil {
		t.Fatal(e)
	}
	var firstTextEvent llm.ChatEvent
	for event := range stream {
		if event.Err != nil {
			t.Fatal(event.Err)
		}
		if event.Text != "" {
			firstTextEvent = event
			break
		}
	}
	if firstTextEvent.Text != "partial" {
		t.Fatalf("first text event = %#v", firstTextEvent)
	}
	response := conversationRequest(t, server.Handler(), "GET", "/api/doctor", "", "", 200)
	if !strings.Contains(response.Body.String(), "deferred while the inference slot is busy") || requests.Load() != 1 {
		t.Fatalf("doctor bypassed guard: %s requests=%d", response.Body.String(), requests.Load())
	}
	server.llmProbeMu.Lock()
	cached := !server.llmProbeAt.IsZero() || server.llmProbeError != nil
	server.llmProbeMu.Unlock()
	if cached {
		t.Fatal("busy was cached")
	}
	cancel()
	for range stream {
	}
	response = conversationRequest(t, server.Handler(), "GET", "/api/doctor", "", "", 200)
	if !strings.Contains(response.Body.String(), "passed the bounded structured JSON smoke request") || requests.Load() != 2 {
		t.Fatalf("slot/cache remained busy: %s requests=%d", response.Body.String(), requests.Load())
	}
	conversationRequest(t, server.Handler(), "GET", "/api/doctor", "", "", 200)
	if requests.Load() != 2 {
		t.Fatal("successful probe cache ignored")
	}
}

func TestDoctorWithoutInjectedClientNeverCreatesBypassClient(t *testing.T) {
	var requests atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			requests.Add(1)
		}
		fmt.Fprint(w, `{"data":[]}`)
	}))
	defer model.Close()
	s, store := newTestServer(t)
	defer store.Close()
	s.llmURL = model.URL
	response := conversationRequest(t, s.Handler(), "GET", "/api/doctor", "", "", 200)
	if requests.Load() != 0 || !strings.Contains(response.Body.String(), "deferred") {
		t.Fatal(response.Body.String())
	}
}
