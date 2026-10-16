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

func TestValidateJSONSchemaChecksAnyOfBranches(t *testing.T) {
	schema := []byte(`{"anyOf":[{"type":"object","properties":{"name":{"type":"string","enum":["text.inspect"]},"arguments":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}},"required":["name","arguments"],"additionalProperties":false},{"type":"object","properties":{"name":{"type":"string","enum":["fetch_url"]},"arguments":{"type":"object","properties":{"url":{"type":"string"}},"required":["url"],"additionalProperties":false}},"required":["name","arguments"],"additionalProperties":false}]}`)
	for _, raw := range []string{
		`{"name":"text.inspect","arguments":{"text":"hello"}}`,
		`{"name":"fetch_url","arguments":{"url":"https://example.com"}}`,
	} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err != nil {
			t.Errorf("valid branch rejected: %v", err)
		}
	}
	for _, raw := range []string{
		`{"name":"text.inspect","arguments":{"url":"https://example.com"}}`,
		`{"name":"fetch_url","arguments":{"url":"https://example.com","command":"id"}}`,
	} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err == nil {
			t.Errorf("invalid branch accepted: %s", raw)
		}
	}
}
