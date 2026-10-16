package cli

import (
	"bytes"
	"strings"
	"testing"
)

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
