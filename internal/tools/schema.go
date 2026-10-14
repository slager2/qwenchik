package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

func ValidateJSONSchema(schemaJSON, valueJSON []byte) error {
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		return fmt.Errorf("invalid tool schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(valueJSON))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON value must contain exactly one value")
	}
	return validateSchemaNode(schema, value, "$", 0)
}

func validateSchemaNode(schema map[string]any, value any, path string, depth int) error {
	if depth > 32 {
		return fmt.Errorf("%s exceeds maximum schema validation depth", path)
	}
	typeName, _ := schema["type"].(string)
	if typeName != "" && !matchesJSONType(typeName, value) {
		return fmt.Errorf("%s must be %s", path, typeName)
	}
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if candidate == value {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		required := stringSet(schema["required"])
		for key := range required {
			if _, exists := typed[key]; !exists {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		additionalAllowed, explicitlySet := schema["additionalProperties"].(bool)
		for key, child := range typed {
			propertySchema, exists := properties[key]
			if !exists {
				if explicitlySet && !additionalAllowed {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			childSchema, ok := propertySchema.(map[string]any)
			if ok {
				if err := validateSchemaNode(childSchema, child, path+"."+key, depth+1); err != nil {
					return err
				}
			}
		}
	case []any:
		if max, ok := schemaNumber(schema["maxItems"]); ok && float64(len(typed)) > max {
			return fmt.Errorf("%s exceeds maxItems", path)
		}
		if min, ok := schemaNumber(schema["minItems"]); ok && float64(len(typed)) < min {
			return fmt.Errorf("%s is below minItems", path)
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, child := range typed {
				if err := validateSchemaNode(itemSchema, child, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
		}
	case string:
		runeLength := float64(utf8.RuneCountInString(typed))
		if max, ok := schemaNumber(schema["maxLength"]); ok && runeLength > max {
			return fmt.Errorf("%s exceeds maxLength", path)
		}
		if min, ok := schemaNumber(schema["minLength"]); ok && runeLength < min {
			return fmt.Errorf("%s is below minLength", path)
		}
	case json.Number:
		number, err := typed.Float64()
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return fmt.Errorf("%s must be a finite number", path)
		}
		if typeName == "integer" {
			if _, err := typed.Int64(); err != nil {
				return fmt.Errorf("%s must be an integer", path)
			}
		}
		if min, ok := schemaNumber(schema["minimum"]); ok && number < min {
			return fmt.Errorf("%s is below minimum", path)
		}
		if max, ok := schemaNumber(schema["maximum"]); ok && number > max {
			return fmt.Errorf("%s exceeds maximum", path)
		}
	}
	return nil
}

func matchesJSONType(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func schemaNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case string:
		result, err := strconv.ParseFloat(typed, 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func stringSet(value any) map[string]struct{} {
	items, _ := value.([]any)
	result := make(map[string]struct{}, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok {
			result[value] = struct{}{}
		}
	}
	return result
}
