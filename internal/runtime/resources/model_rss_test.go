package resources

import (
	"io/fs"
	"testing"
	"time"

	"github.com/slager2/qwenchik/internal/domain"
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

func TestModelRSSStatsCountOnlyBoundedCollectionWork(t *testing.T) {
	p := &HostProvider{
		policy:   DefaultPolicy(),
		procPIDs: func(int) ([]string, error) { return []string{"4242"}, nil },
		readFile: func(path string, _ int) ([]byte, error) {
			switch path {
			case "/proc/4242/comm":
				return []byte("llama-server\n"), nil
			case "/proc/4242/status":
				return []byte("VmRSS: 400 kB\n"), nil
			default:
				return nil, fs.ErrNotExist
			}
		},
	}
	first := p.llamaServerRSS(time.Now().UTC())
	second := p.llamaServerRSS(time.Now().UTC().Add(time.Second))
	if first.Value == nil || second.Value == nil {
		t.Fatalf("RSS samples are unavailable: first=%+v second=%+v", first, second)
	}
	stats := p.Stats()
	if stats.ModelRSSCacheMisses != 1 || stats.ModelRSSCacheHits != 1 || stats.ModelRSSScans != 1 {
		t.Fatalf("unexpected RSS collection stats: %+v", stats)
	}
}

func BenchmarkPolicyCheck(b *testing.B) {
	policy := DefaultPolicy()
	healthy := true
	freeRAM := policy.MinFreeRAMBytes
	appRSS := policy.ProcessRSSMaxBytes / 2
	modelRSS := policy.ModelRSSMaxBytes / 2
	freeVRAM := policy.MinFreeVRAMMB
	gpuTemp := policy.GPUTempMaxC
	cpuTemp := policy.CPUTempMaxC
	at := time.Now().UTC()
	snapshot := Snapshot{
		FreeRAMBytes:  &freeRAM,
		RSSBytes:      &appRSS,
		ModelRSSBytes: &modelRSS,
		GPUFreeVRAMMB: &freeVRAM,
		GPUTempC:      &gpuTemp,
		LlamaHealthy:  &healthy,
		System:        &domain.SystemTelemetry{CPUTemperature: domain.NewExactMetric(cpuTemp, domain.MetricUnitCelsius, at)},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if violation := Check(snapshot, policy); violation != nil {
			b.Fatalf("healthy snapshot violated policy: %+v", violation)
		}
	}
}

func BenchmarkCachedModelRSS(b *testing.B) {
	p := &HostProvider{
		policy:   DefaultPolicy(),
		procPIDs: func(int) ([]string, error) { return []string{"4242"}, nil },
		readFile: func(path string, _ int) ([]byte, error) {
			switch path {
			case "/proc/4242/comm":
				return []byte("llama-server\n"), nil
			case "/proc/4242/status":
				return []byte("VmRSS: 400 kB\n"), nil
			default:
				return nil, fs.ErrNotExist
			}
		},
	}
	if got := p.llamaServerRSS(time.Now().UTC()); got.Value == nil {
		b.Fatalf("warm-up RSS sample is unavailable: %+v", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := p.llamaServerRSS(time.Now().UTC()); got.Value == nil {
			b.Fatalf("cached RSS sample is unavailable: %+v", got)
		}
	}
}
