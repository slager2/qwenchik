package tools

import (
	"testing"
)

func TestValidateJSONSchemaChecksObjectFieldsAndBounds(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"query":{"type":"string","maxLength":8},"count":{"type":"integer","minimum":1,"maximum":3}},"required":["query"],"additionalProperties":false}`)
	for _, raw := range []string{`{"query":"qwen","count":2}`, `{"query":"qwen"}`} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err != nil {
			t.Fatalf("valid JSON rejected: %v", err)
		}
	}
	for _, raw := range []string{`{"count":2}`, `{"query":"too-long-query"}`, `{"query":"qwen","count":4}`, `{"query":"qwen","unexpected":true}`, `[]`} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err == nil {
			t.Errorf("invalid JSON accepted: %s", raw)
		}
	}
}
