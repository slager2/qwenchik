package resources

import (
	"testing"
	"time"
)

func TestModelRSSGuardIsSeparateFromAgentRSSAndRequiresRecoveryMargin(t *testing.T) {
	p := DefaultPolicy()
	age := int64(30000)
	healthy := true
	agent := int64(64 << 20)
	model := p.ModelRSSMaxBytes + 1
	s := Snapshot{SnapshotAt: time.Now().UTC(), StaleAfterMS: &age, LlamaHealthy: &healthy, RSSBytes: &agent, ModelRSSBytes: &model}
	if violation := Check(s, p); violation == nil || violation.Code != "high_model_rss" {
		t.Fatal("model RSS escaped guard", violation)
	}
	model = p.ModelRSSMaxBytes
	if violation := CheckRecovery(s, p, "high_model_rss"); violation == nil || violation.Code != "high_model_rss" {
		t.Fatal("recovery ignored hysteresis")
	}
	model = p.ModelRSSMaxBytes * 8 / 10
	if violation := CheckRecovery(s, p, "high_model_rss"); violation != nil {
		t.Fatal("cleared model guard did not recover", violation)
	}
	s.ModelRSSBytes = nil
	if violation := CheckRecovery(s, p, "high_model_rss"); violation == nil || violation.Code != "telemetry_unavailable" {
		t.Fatal("unknown model RSS allowed recovery")
	}
}
