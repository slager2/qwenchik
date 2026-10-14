package config

import (
	"strings"
	"testing"
)

func TestValidateRejectsNonLoopbackBindAndLLM(t *testing.T) {
	cfg := Config{
		BindAddr: "0.0.0.0:8080", DatabasePath: "./data/test.db", ArtifactsPath: "./data/artifacts",
		LLMBaseURL: "http://127.0.0.1:8000/v1", LLMModel: "test", LLMContextLength: 8192,
		LLMMaxOutputTokens: 1024, LLMTemperature: 0.2, LLMTopP: 0.8, LLMRequestTimeoutSecs: 30,
		LLMMaxConcurrency: 1, MaxSteps: 8, TaskTimeoutSeconds: 3600, MaxFetchBytes: 1024,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback bind validation error = %v", err)
	}
	cfg.BindAddr = "127.0.0.1:8080"
	cfg.LLMBaseURL = "https://api.example.com/v1"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "remote LLM") {
		t.Fatalf("remote LLM validation error = %v", err)
	}
}
