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

func TestValidateJSONSchemaEnforcesIdentifierPattern(t *testing.T) {
	schema := []byte(`{"type":"string","pattern":"^[a-zA-Z0-9_-]{1,64}$"}`)
	for _, raw := range []string{`"search_1"`, `"source-2"`} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err != nil {
			t.Fatalf("valid identifier rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`"bad.id"`, `"источник"`, `"search 1"`, `""`} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err == nil {
			t.Fatalf("structurally invalid identifier accepted: %s", raw)
		}
	}
	if err := ValidateJSONSchema([]byte(`{"type":"string","pattern":"["}`), []byte(`"x"`)); err == nil {
		t.Fatal("invalid schema pattern did not fail closed")
	}
}
