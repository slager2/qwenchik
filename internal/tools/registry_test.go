package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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
