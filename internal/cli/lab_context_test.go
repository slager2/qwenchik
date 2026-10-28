package cli

import (
	"github.com/slager2/qwenchik/internal/domain"
	"path/filepath"
	"testing"
)

func TestContextReceiptRejectsIncompleteAndResourceLimitedMeasurements(t *testing.T) {
	ram := int64(4 << 30)
	rss := int64(1 << 30)
	vram := int64(2000)
	temp := float64(60)
	for _, mode := range []string{"valid", "resource_limited", "paused", "summaries_missing", "sample_unknown", "collection_error"} {
		t.Run(mode, func(t *testing.T) {
			samples := []contextResourceSample{{RAMFree: &ram, ModelRSS: &rss, GPUFree: &vram, GPUTemperature: &temp}, {RAMFree: &ram, ModelRSS: &rss, GPUFree: &vram, GPUTemperature: &temp}}
			probe := map[string]any{"status": "passed"}
			row := labBenchmarkResult{ScoringComplete: true, Status: domain.StatusCompleted, OutputBytes: 100}
			r := map[string]any{"probe": probe, "research": row, "summary_collection_complete": true, "sample_errors": 0, "samples_truncated": false}
			switch mode {
			case "resource_limited":
				probe["status"] = "resource_limited"
			case "paused":
				row.Status = domain.StatusPaused
				row.ScoringComplete = false
				r["research"] = row
			case "summaries_missing":
				r["summary_collection_complete"] = false
			case "sample_unknown":
				samples[0].ModelRSS = nil
			case "collection_error":
				r["inference_collection_error"] = "unavailable"
			}
			reasons := contextReceiptReasons(r, true, samples)
			if (len(reasons) == 0) != (mode == "valid") {
				t.Fatalf("mode=%s reasons=%v", mode, reasons)
			}
		})
	}
}

func TestContextReceiptNeverOverwritesAnExistingExperiment(t *testing.T) {
	p := filepath.Join(t.TempDir(), "receipt.json")
	if err := writeContextReceipt(p, map[string]any{"type": "first"}); err != nil {
		t.Fatal(err)
	}
	if err := writeContextReceipt(p, map[string]any{"type": "second"}); err == nil {
		t.Fatal("overwrote existing measurement")
	}
}
