package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

var supportedSchemaKeywords = map[string]bool{
	"$schema": true, "title": true, "description": true, "type": true,
	"properties": true, "required": true, "additionalProperties": true,
	"enum": true, "items": true, "minLength": true, "maxLength": true,
	"minimum": true, "maximum": true, "minItems": true, "maxItems": true,
}

func ValidateSchema(raw json.RawMessage) error {
	var schema any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	root, ok := schema.(map[string]any)
	if !ok {
		return fmt.Errorf("root must be an object")
	}
	return validateSchemaNode("$", root)
}

func ValidateValue(raw json.RawMessage, value any) error {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("invalid schema JSON: %w", err)
	}
	if err := validateSchemaNode("$", schema); err != nil {
		return err
	}
	return validateAgainst("$", schema, normalizeValue(value))
}

func validateSchemaNode(path string, schema map[string]any) error {
	for keyword := range schema {
		if !supportedSchemaKeywords[keyword] {
			return fmt.Errorf("%s: unsupported keyword %q; plugin schemas fail closed", path, keyword)
		}
	}
	if rawType, exists := schema["type"]; exists {
		typeName, ok := rawType.(string)
		if !ok || !validSchemaType(typeName) {
			return fmt.Errorf("%s.type must be one of object, array, string, number, integer, boolean, null", path)
		}
	}
	if properties, exists := schema["properties"]; exists {
		propertyMap, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.properties must be an object", path)
		}
		for name, child := range propertyMap {
			childSchema, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s.properties.%s must be an object", path, name)
			}
			if err := validateSchemaNode(path+".properties."+name, childSchema); err != nil {
				return err
			}
		}
	}
	if required, exists := schema["required"]; exists {
		items, ok := required.([]any)
		if !ok {
			return fmt.Errorf("%s.required must be an array", path)
		}
		for _, item := range items {
			if _, ok := item.(string); !ok {
				return fmt.Errorf("%s.required values must be strings", path)
			}
		}
	}
	if additional, exists := schema["additionalProperties"]; exists {
		if _, ok := additional.(bool); !ok {
			return fmt.Errorf("%s.additionalProperties must be a boolean", path)
		}
	}
	if enum, exists := schema["enum"]; exists {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("%s.enum must be a non-empty array", path)
		}
	}
	for _, keyword := range []string{"minLength", "maxLength", "minItems", "maxItems"} {
		if value, exists := schema[keyword]; exists {
			number, ok := value.(float64)
			if !ok || number < 0 || math.Trunc(number) != number {
				return fmt.Errorf("%s.%s must be a non-negative integer", path, keyword)
			}
		}
	}
	for _, keyword := range []string{"minimum", "maximum"} {
		if value, exists := schema[keyword]; exists {
			if _, ok := value.(float64); !ok {
				return fmt.Errorf("%s.%s must be a number", path, keyword)
			}
		}
	}
	if items, exists := schema["items"]; exists {
		child, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("%s.items must be an object", path)
		}
		if err := validateSchemaNode(path+".items", child); err != nil {
			return err
		}
	}
	return nil
}

func validateAgainst(path string, schema map[string]any, value any) error {
	if enum, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(normalizeValue(candidate), value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s must be one of %s", path, renderEnum(enum))
		}
	}
	typeName, _ := schema["type"].(string)
	if typeName != "" && !valueMatchesType(value, typeName) {
		return fmt.Errorf("%s must be %s, got %s", path, typeName, valueType(value))
	}
	switch typed := value.(type) {
	case map[string]any:
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, item := range required {
				name := item.(string)
				if _, exists := typed[name]; !exists {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		additional, restrict := schema["additionalProperties"].(bool)
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, declared := properties[key]
			if !declared {
				if restrict && !additional {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			if err := validateAgainst(path+"."+key, child.(map[string]any), typed[key]); err != nil {
				return err
			}
		}
	case []any:
		if err := checkLength(path, len(typed), schema, "minItems", "maxItems"); err != nil {
			return err
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range typed {
				if err := validateAgainst(fmt.Sprintf("%s[%d]", path, i), itemSchema, item); err != nil {
					return err
				}
			}
		}
	case string:
		if err := checkLength(path, len([]rune(typed)), schema, "minLength", "maxLength"); err != nil {
			return err
		}
	case float64:
		if minimum, ok := numberKeyword(schema, "minimum"); ok && typed < minimum {
			return fmt.Errorf("%s must be >= %v", path, minimum)
		}
		if maximum, ok := numberKeyword(schema, "maximum"); ok && typed > maximum {
			return fmt.Errorf("%s must be <= %v", path, maximum)
		}
	}
	return nil
}

func normalizeValue(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized any
	if json.Unmarshal(raw, &normalized) != nil {
		return value
	}
	return normalized
}

func validSchemaType(value string) bool {
	return strings.Contains(" object array string number integer boolean null ", " "+value+" ")
}

func valueMatchesType(value any, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && math.Trunc(n) == n
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return true
	}
}

func valueType(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func checkLength(path string, length int, schema map[string]any, minKey, maxKey string) error {
	if minimum, ok := integerKeyword(schema, minKey); ok && length < minimum {
		return fmt.Errorf("%s length must be >= %d", path, minimum)
	}
	if maximum, ok := integerKeyword(schema, maxKey); ok && length > maximum {
		return fmt.Errorf("%s length must be <= %d", path, maximum)
	}
	return nil
}

func integerKeyword(schema map[string]any, key string) (int, bool) {
	number, ok := numberKeyword(schema, key)
	return int(number), ok && math.Trunc(number) == number && number >= 0
}

func numberKeyword(schema map[string]any, key string) (float64, bool) {
	value, ok := schema[key].(float64)
	return value, ok
}

func renderEnum(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value)
	}
	return strings.Join(parts, ", ")
}
