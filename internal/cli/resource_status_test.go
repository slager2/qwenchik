package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slager2/qwenchik/internal/config"
)

func TestDoctorDelegationPreservesSafeRuntimeWithoutExtraProbes(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/doctor" {
			t.Errorf("unexpected probe: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"ok","checks":{"llm":{"status":"ok"}},"research_limits":{"configured":true},"pdf_extractor":{"isolated_available":false,"fallback_allowed":false},"runtime":{"resources":{"free_ram_bytes":1024,"gpu_free_vram_mb":null,"power_source":"AC","warnings":["GPU VRAM unknown; configured guard cannot evaluate this metric"],"api_key":"private-fixture"},"sleep":{"provider":"systemd-inhibit","active":true,"power_source":"AC","failure_reason":"private-fixture"},"lease_owner":"private-fixture"}}`))
	}))
	defer server.Close()
	report := runningAppDoctor(context.Background(), config.Config{BindAddr: strings.TrimPrefix(server.URL, "http://")})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || strings.Contains(string(encoded), "private-fixture") || strings.Contains(string(encoded), "lease_owner") || !strings.Contains(string(encoded), `"gpu_free_vram_mb":null`) || !strings.Contains(string(encoded), `"active":true`) {
		t.Fatalf("invalid runtime projection: %s", encoded)
	}
}
