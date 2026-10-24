package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"

	"github.com/slager2/qwenchik/internal/tools"
)

func planOutputSchema(maxSteps int, allowedKinds []string) json.RawMessage {
	return planOutputSchemaWithRequiredKinds(maxSteps, allowedKinds, nil)
}

// Required kinds are semantic constraints checked by validateTaskPlan after
// lossless normalization, not positional JSON-schema constraints.
func planOutputSchemaWithRequiredKinds(maxSteps int, allowedKinds, _ []string) json.RawMessage {
	idSchema := boundedStringSchema(1, 64)
	idSchema["pattern"] = "^[a-zA-Z0-9_-]{1,64}$"
	dependencyItems := map[string]any{"type": "string", "maxLength": 64, "pattern": "^[a-zA-Z0-9_-]{1,64}$"}
	step := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":               idSchema,
			"kind":             map[string]any{"type": "string", "enum": allowedKinds},
			"description":      boundedStringSchema(1, 1000),
			"dependencies":     boundedArraySchema(dependencyItems, 0, min(4, maxSteps)),
			"expected_output":  boundedStringSchema(0, 500),
			"success_criteria": boundedArraySchema(boundedStringSchema(1, 500), 1, 10),
		},
		"required":             []string{"id", "kind", "description", "dependencies", "expected_output", "success_criteria"},
		"additionalProperties": false,
	}
	subtasksSchema := boundedArraySchema(step, 1, maxSteps)
	maxStepsSchema := map[string]any{"type": "integer", "minimum": 1, "maximum": maxSteps}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"schema_version":    map[string]any{"type": "integer", "minimum": 1, "maximum": 1},
			"goal":              boundedStringSchema(1, 1000),
			"subtasks":          subtasksSchema,
			"max_steps":         maxStepsSchema,
			"final_deliverable": boundedStringSchema(1, 1000),
		},
		"required":             []string{"schema_version", "goal", "subtasks", "max_steps", "final_deliverable"},
		"additionalProperties": false,
	}
	return encodeSchema(schema)
}

func actionOutputSchema(descriptors []tools.Descriptor, fetchURLs []string) json.RawMessage {
	toolCalls := make([]any, 0, len(descriptors))
	for _, descriptor := range descriptors {
		argumentsSchema := strictToolArgumentsSchema(descriptor.ArgumentsSchema)
		if descriptor.Name == "fetch_url" && len(fetchURLs) > 0 {
			if properties, ok := argumentsSchema["properties"].(map[string]any); ok {
				properties["url"] = map[string]any{"type": "string", "enum": fetchURLs}
			}
		}
		toolCalls = append(toolCalls, map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":      map[string]any{"type": "string", "enum": []string{descriptor.Name}},
				"arguments": argumentsSchema,
			},
			"required":             []string{"name", "arguments"},
			"additionalProperties": false,
		})
	}
	var toolCallSchema any = map[string]any{"anyOf": toolCalls}
	if len(toolCalls) == 1 {
		toolCallSchema = toolCalls[0]
	}
	return encodeSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"status":    map[string]any{"type": "string", "enum": []string{"continue"}},
			"summary":   boundedStringSchema(1, 500),
			"tool_call": toolCallSchema,
		},
		"required":             []string{"status", "summary", "tool_call"},
		"additionalProperties": false,
	})
}

func strictToolArgumentsSchema(raw json.RawMessage) map[string]any {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil || schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
	}
	strictifyJSONSchema(schema)
	return schema
}

func strictifyJSONSchema(schema map[string]any) {
	if properties, ok := schema["properties"].(map[string]any); ok {
		required := make(map[string]struct{})
		if list, ok := schema["required"].([]any); ok {
			for _, item := range list {
				if name, ok := item.(string); ok {
					required[name] = struct{}{}
				}
			}
		} else if list, ok := schema["required"].([]string); ok {
			for _, name := range list {
				required[name] = struct{}{}
			}
		}
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		sort.Strings(names)
		strictProperties := make(map[string]any, len(properties))
		for _, name := range names {
			child, ok := properties[name].(map[string]any)
			if !ok {
				child = map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}, "additionalProperties": false}
			}
			strictifyJSONSchema(child)
			if _, isRequired := required[name]; !isRequired {
				child = map[string]any{"anyOf": []any{child, map[string]any{"type": "null"}}}
			}
			strictProperties[name] = child
		}
		schema["properties"] = strictProperties
		schema["required"] = names
		schema["additionalProperties"] = false
	}
	if item, ok := schema["items"].(map[string]any); ok {
		strictifyJSONSchema(item)
	}
	if prefixItems, ok := schema["prefixItems"].([]any); ok {
		for _, item := range prefixItems {
			if child, ok := item.(map[string]any); ok {
				strictifyJSONSchema(child)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if alternatives, ok := schema[key].([]any); ok {
			for _, alternative := range alternatives {
				if child, ok := alternative.(map[string]any); ok {
					strictifyJSONSchema(child)
				}
			}
		}
	}
}

func normalizeToolArguments(raw json.RawMessage, descriptor tools.Descriptor) (json.RawMessage, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("tool arguments must contain exactly one JSON value")
	}
	var schema map[string]any
	if err := json.Unmarshal(descriptor.ArgumentsSchema, &schema); err != nil {
		return nil, err
	}
	stripOptionalNulls(value, schema)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := tools.ValidateJSONSchema(descriptor.ArgumentsSchema, encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func stripOptionalNulls(value any, schema map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required := make(map[string]struct{})
		if values, ok := schema["required"].([]any); ok {
			for _, item := range values {
				if name, ok := item.(string); ok {
					required[name] = struct{}{}
				}
			}
		}
		for name, child := range typed {
			childSchema, ok := properties[name].(map[string]any)
			if !ok {
				continue
			}
			if child == nil {
				if _, isRequired := required[name]; !isRequired {
					delete(typed, name)
				}
				continue
			}
			stripOptionalNulls(child, childSchema)
		}
	case []any:
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for _, item := range typed {
				stripOptionalNulls(item, itemSchema)
			}
		}
	}
}

func verificationOutputSchema(catalog []verificationCriterion, contradictions ...bool) json.RawMessage {
	supplementaryIDSchema := boundedStringSchema(1, 16)
	if len(catalog) > 0 {
		supplementaryIDSchema["enum"] = criterionIDs(catalog)
	}
	assessmentSchema := func(criterion verificationCriterion) map[string]any {
		evidenceItem := boundedStringSchema(1, 128)
		if len(criterion.EvidenceIDs) > 0 {
			evidenceItem["enum"] = criterion.EvidenceIDs
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"met":          map[string]any{"type": "boolean"},
				"evidence_ids": boundedArraySchema(evidenceItem, 0, len(criterion.EvidenceIDs)),
				"rationale":    boundedStringSchema(1, 500),
			},
			"required":             []string{"met", "evidence_ids", "rationale"},
			"additionalProperties": false,
		}
	}
	properties := make(map[string]any, len(catalog))
	required := make([]string, 0, len(catalog))
	for _, criterion := range catalog {
		properties[criterion.ID] = assessmentSchema(criterion)
		required = append(required, criterion.ID)
	}
	output := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"ready_to_answer":        map[string]any{"type": "boolean"},
			"summary":                boundedStringSchema(1, 1000),
			"missing":                boundedArraySchema(boundedStringSchema(1, 500), 0, 20),
			"supplementary_criteria": boundedArraySchema(supplementaryIDSchema, 0, len(catalog)),
			"criterion_assessments":  map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false},
		},
		"required":             []string{"ready_to_answer", "summary", "missing", "supplementary_criteria", "criterion_assessments"},
		"additionalProperties": false,
	}
	if len(contradictions) > 0 && contradictions[0] {
		output["properties"].(map[string]any)["contradictions"] = boundedArraySchema(boundedStringSchema(1, 500), 0, 8)
	}
	return encodeSchema(output)
}

func criterionIDs(catalog []verificationCriterion) []string {
	ids := make([]string, 0, len(catalog))
	for _, criterion := range catalog {
		ids = append(ids, criterion.ID)
	}
	return ids
}

func boundedStringSchema(minLength, maxLength int) map[string]any {
	return map[string]any{"type": "string", "minLength": minLength, "maxLength": maxLength}
}

func boundedArraySchema(itemSchema map[string]any, minItems, maxItems int) map[string]any {
	return map[string]any{"type": "array", "items": itemSchema, "minItems": minItems, "maxItems": maxItems}
}

func encodeSchema(schema map[string]any) json.RawMessage {
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic("encode static model-output schema: " + err.Error())
	}
	return encoded
}
