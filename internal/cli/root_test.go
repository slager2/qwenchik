package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/slager2/qwenchik/internal/config"
)

func TestMaxInputBytesScalesWithConfiguredContext(t *testing.T) {
	for _, test := range []struct {
		name    string
		context int
		output  int
		want    int
	}{
		{name: "16k context", context: 16_384, output: 4_096, want: 40_960},
		{name: "32k context", context: 32_768, output: 4_096, want: 106_496},
		{name: "default 128k context", context: 131_072, output: 4_096, want: 499_712},
		{name: "less than reserve", context: 4_000, output: 3_000, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := maxInputBytes(config.Config{LLMContextLength: test.context, LLMMaxOutputTokens: test.output})
			if got != test.want {
				t.Fatalf("maxInputBytes() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestMaxInputBytesSaturatesInsteadOfOverflowing(t *testing.T) {
	got := maxInputBytes(config.Config{LLMContextLength: int(^uint(0) >> 1), LLMMaxOutputTokens: 1})
	if got != int(^uint(0)>>1) {
		t.Fatalf("overflowing context budget = %d, want max int", got)
	}
}

func TestSearchProviderCommandsNeverExposeTavilyKey(t *testing.T) {
	const testKey = "test-tavily-secret-value"
	t.Setenv("TAVILY_API_KEY", testKey)
	t.Setenv("SEARXNG_URL", "")
	t.Setenv("APP_BIND_ADDR", "127.0.0.1:8080")
	t.Setenv("LLM_BASE_URL", "http://127.0.0.1:8000/v1")

	for _, command := range []string{"providers", "config"} {
		t.Run(command, func(t *testing.T) {
			root := NewRoot()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs([]string{"--json", command})
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			got := output.String()
			if strings.Contains(got, testKey) || strings.Contains(got, "api_key") {
				t.Fatalf("%s output exposed Tavily credential: %s", command, got)
			}
			if command == "providers" && (!strings.Contains(got, `"type":"searxng"`) || !strings.Contains(got, `"mode":"tavily"`)) {
				t.Fatalf("providers output omitted configured Tavily: %s", got)
			}
			if command == "config" && !strings.Contains(got, `"tavily_configured":true`) {
				t.Fatalf("config output omitted Tavily configured status: %s", got)
			}
		})
	}
}
