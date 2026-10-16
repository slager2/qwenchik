package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/slager2/qwenchik/internal/tools"
)

func planOutputSchema(maxSteps int, allowedKinds []string) json.RawMessage {
	return planOutputSchemaWithRequiredKinds(maxSteps, allowedKinds, nil)
}

func planOutputSchemaWithRequiredKinds(maxSteps int, allowedKinds, requiredKinds []string) json.RawMessage {
	stepSchema := func(kinds, ids, dependencyIDs []string, minDependencies, maxDependencies int) map[string]any {
		kindSchema := map[string]any{"type": "string", "enum": allowedKinds}
		if len(kinds) != 0 {
			kindSchema["enum"] = kinds
		}
		idSchema := boundedStringSchema(1, 64)
		idSchema["pattern"] = "^[a-zA-Z0-9_-]{1,64}$"
		if len(ids) != 0 {
			idSchema["enum"] = ids
		}
		dependencyItems := map[string]any{"type": "string", "maxLength": 64, "pattern": "^[a-zA-Z0-9_-]{1,64}$"}
		if len(dependencyIDs) != 0 {
			dependencyItems["enum"] = dependencyIDs
		}
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":               idSchema,
				"kind":             kindSchema,
				"description":      boundedStringSchema(1, 1000),
				"dependencies":     boundedArraySchema(dependencyItems, minDependencies, maxDependencies),
				"expected_output":  boundedStringSchema(0, 500),
				"success_criteria": boundedArraySchema(boundedStringSchema(1, 500), 1, 10),
			},
			"required":             []string{"id", "kind", "description", "dependencies", "expected_output", "success_criteria"},
			"additionalProperties": false,
		}
	}
	step := stepSchema(nil, nil, nil, 0, min(4, maxSteps))
	subtasksSchema := boundedArraySchema(step, 1, maxSteps)
	maxStepsSchema := map[string]any{"type": "integer", "minimum": 1, "maximum": maxSteps}
	if len(requiredKinds) != 0 {
		minimumSearches, minimumFetches := 0, 0
		for _, kind := range requiredKinds {
			switch kind {
			case "search":
				minimumSearches++
			case "fetch":
				minimumFetches++
			}
		}
		urlFirst := requiredKinds[0] == "fetch" && minimumSearches > 0
		if minimumSearches > 0 {
			alternatives := make([]any, 0, 6)
			nonResearchKinds := make([]string, 0, len(allowedKinds))
			for _, kind := range allowedKinds {
				if kind != "search" && kind != "fetch" {
					nonResearchKinds = append(nonResearchKinds, kind)
				}
			}
			nonResearchStep := stepSchema(nonResearchKinds, nil, nil, 0, min(4, maxSteps))
			if len(nonResearchKinds) == 0 {
				nonResearchStep = stepSchema([]string{"__no_additional_research_action__"}, nil, nil, 0, min(4, maxSteps))
			}
			maximumSearches := min(3, maxSteps-minimumFetches)
			maximumFetches := min(4, maxSteps-minimumSearches)
			for searchCount := minimumSearches; searchCount <= maximumSearches; searchCount++ {
				for fetchCount := minimumFetches; fetchCount <= maximumFetches; fetchCount++ {
					prefix := researchPlanPrefix(searchCount, fetchCount, urlFirst, stepSchema)
					mandatorySteps := len(prefix)
					if mandatorySteps > maxSteps {
						continue
					}
					alternatives = append(alternatives, map[string]any{
						"type": "array", "minItems": mandatorySteps, "maxItems": maxSteps,
						"prefixItems": prefix, "items": nonResearchStep,
					})
				}
			}
			subtasksSchema = map[string]any{"anyOf": alternatives}
		} else {
			prefix := make([]any, 0, len(requiredKinds))
			for index, kind := range requiredKinds {
				id := fmt.Sprintf("%s_%d", kind, index+1)
				maxDependencies := 0
				if kind == "fetch" {
					maxDependencies = 4
				}
				prefix = append(prefix, stepSchema([]string{kind}, []string{id}, nil, 0, maxDependencies))
			}
			subtasksSchema = map[string]any{"type": "array", "minItems": len(requiredKinds), "maxItems": maxSteps, "prefixItems": prefix, "items": step}
		}
		maxStepsSchema = map[string]any{"type": "integer", "minimum": len(requiredKinds), "maximum": maxSteps}
	}
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

func researchPlanPrefix(searchCount, fetchCount int, urlFirst bool, stepSchema func([]string, []string, []string, int, int) map[string]any) []any {
	prefix := make([]any, 0, searchCount+fetchCount)
	fetchIndex := 0
	if urlFirst {
		fetchIndex++
		id := fmt.Sprintf("fetch_%d", fetchIndex)
		prefix = append(prefix, stepSchema([]string{"fetch"}, []string{id}, nil, 0, 4))
	}
	for index := 1; index <= searchCount; index++ {
		id := fmt.Sprintf("search_%d", index)
		prefix = append(prefix, stepSchema([]string{"search"}, []string{id}, nil, 0, 4))
	}
	for fetchIndex < fetchCount {
		fetchIndex++
		id := fmt.Sprintf("fetch_%d", fetchIndex)
		prefix = append(prefix, stepSchema([]string{"fetch"}, []string{id}, nil, 0, 4))
	}
	return prefix
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

func verificationOutputSchema() json.RawMessage {
	assessment := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"step_id":         boundedStringSchema(1, 64),
			"criterion_index": map[string]any{"type": "integer", "minimum": 0, "maximum": 9},
			"met":             map[string]any{"type": "boolean"},
			"evidence_ids":    boundedArraySchema(boundedStringSchema(1, 128), 0, 30),
			"rationale":       boundedStringSchema(1, 500),
		},
		"required":             []string{"step_id", "criterion_index", "met", "evidence_ids", "rationale"},
		"additionalProperties": false,
	}
	return encodeSchema(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"complete":              map[string]any{"type": "boolean"},
			"summary":               boundedStringSchema(1, 1000),
			"missing":               boundedArraySchema(boundedStringSchema(1, 500), 0, 20),
			"criterion_assessments": boundedArraySchema(assessment, 0, 120),
		},
		"required":             []string{"complete", "summary", "missing", "criterion_assessments"},
		"additionalProperties": false,
	})
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
