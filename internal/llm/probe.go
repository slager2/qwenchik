package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var doctorJSONSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)

// ProbeStructuredOutput checks that a local model can complete a bounded,
// strict JSON Schema request without exposing the response or any task data.
func ProbeStructuredOutput(ctx context.Context, client Client) error {
	if client == nil {
		return errors.New("local model client is required")
	}
	response, err := client.Complete(ctx, ChatRequest{
		Messages: []ChatMessage{
			{Role: "system", Content: "Return exactly the requested JSON object. Do not include explanations."},
			{Role: "user", Content: "Set ok to true."},
		},
		MaxTokens: 24, TopP: 1, JSONSchema: doctorJSONSchema,
	})
	if err != nil {
		return fmt.Errorf("strict JSON Schema request failed: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(response.Content))
	decoder.DisallowUnknownFields()
	var result struct {
		OK bool `json:"ok"`
	}
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("local model did not return the required JSON object: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err == nil {
		return errors.New("local model returned trailing JSON data")
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("local model returned trailing non-JSON data: %w", err)
	}
	if !result.OK {
		return errors.New("local model JSON Schema smoke returned ok=false")
	}
	return nil
}
