package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/slager2/qwenchik/internal/policy"
)

func TestRegistryRejectsUnknownAndDisallowedTools(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Execute(context.Background(), "analyst", "sh", json.RawMessage(`{"command":"id"}`)); !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("unknown tool error = %v", err)
	}
	if _, err := registry.Execute(context.Background(), "planner", "text.inspect", json.RawMessage(`{"text":"hello"}`)); !errors.Is(err, ErrToolDenied) {
		t.Fatalf("disallowed role error = %v", err)
	}
}

func TestTextInspectValidatesArguments(t *testing.T) {
	registry := NewRegistry()
	result, err := registry.Execute(context.Background(), "analyst", "text.inspect", json.RawMessage(`{"text":"one two\nтри"}`))
	if err != nil {
		t.Fatal(err)
	}
	values := result.(map[string]any)
	if values["words"] != 3 || values["lines"] != 2 {
		t.Fatalf("unexpected text.inspect result: %#v", values)
	}
	if _, err := registry.Execute(context.Background(), "analyst", "text.inspect", json.RawMessage(`{"text":"x","command":"id"}`)); err == nil {
		t.Fatal("unknown arguments must be rejected")
	}
	if _, err := registry.Execute(context.Background(), "analyst", "text.inspect", json.RawMessage(`{"text":null}`)); err == nil {
		t.Fatal("null text must be rejected")
	}
}

func TestExternalToolNeverRunsWithoutApproval(t *testing.T) {
	registry := NewRegistry(policy.New(map[string][]string{"analyst": {"external.submit"}}))
	runs := 0
	if err := registry.Register(Descriptor{
		Name: "external.submit", Description: "test side effect",
		ArgumentsSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`),
		RequiredCapability: "external.submit", RiskClass: "external_side_effect", TimeoutSeconds: 1,
		MaxOutputBytes: 100, AllowedRoles: []string{"analyst"},
	}, func(context.Context, json.RawMessage) (any, error) {
		runs++
		return map[string]any{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "analyst", "external.submit", json.RawMessage(`{}`)); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("external tool error = %v, want ErrApprovalRequired", err)
	}
	if runs != 0 {
		t.Fatalf("external executor ran %d times without an approval", runs)
	}
}
