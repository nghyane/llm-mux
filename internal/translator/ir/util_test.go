package ir

import (
	"testing"
)

func TestCleanJsonSchemaForGemini_RemovesExclusiveMinMax(t *testing.T) {
	// Test case based on the bug:
	// Unknown name "exclusiveMaximum" at 'request.tools[0].function_declarations[43].parameters.properties[0].value'
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"page": map[string]any{
				"type":             "integer",
				"description":      "Page number for pagination",
				"minimum":          1,
				"maximum":          10,
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 11,
			},
			"limit": map[string]any{
				"type":             "number",
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 100,
			},
		},
		"required": []any{"page"},
	}

	result := CleanJsonSchemaForGemini(schema)

	// Check top-level structure preserved
	if result["type"] != "object" {
		t.Errorf("Expected type 'object', got %v", result["type"])
	}

	props := result["properties"].(map[string]any)

	// Check 'page' property
	pageProp := props["page"].(map[string]any)
	if _, exists := pageProp["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from 'page' property")
	}
	if _, exists := pageProp["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from 'page' property")
	}
	if _, exists := pageProp["minimum"]; exists {
		t.Error("minimum should be removed from 'page' property")
	}
	if _, exists := pageProp["maximum"]; exists {
		t.Error("maximum should be removed from 'page' property")
	}
	// Description should be preserved
	if pageProp["description"] != "Page number for pagination" {
		t.Errorf("description should be preserved, got %v", pageProp["description"])
	}

	// Check 'limit' property
	limitProp := props["limit"].(map[string]any)
	if _, exists := limitProp["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from 'limit' property")
	}
	if _, exists := limitProp["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from 'limit' property")
	}

	// Required should be preserved
	if result["required"] == nil {
		t.Error("required field should be preserved")
	}
}

func TestCleanJsonSchemaForGemini_DeeplyNestedProperties(t *testing.T) {
	// Test deeply nested schema (3 levels)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{
								"type":             "integer",
								"exclusiveMinimum": 0,
								"exclusiveMaximum": 100,
							},
						},
					},
				},
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	// Navigate to deeply nested property
	props := result["properties"].(map[string]any)
	config := props["config"].(map[string]any)
	configProps := config["properties"].(map[string]any)
	settings := configProps["settings"].(map[string]any)
	settingsProps := settings["properties"].(map[string]any)
	value := settingsProps["value"].(map[string]any)

	if _, exists := value["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from deeply nested 'value' property")
	}
	if _, exists := value["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from deeply nested 'value' property")
	}
	if value["type"] != "integer" {
		t.Errorf("type should be preserved, got %v", value["type"])
	}
}

func TestCleanJsonSchemaForGemini_ArrayItems(t *testing.T) {
	// Test schema with array items containing unsupported fields
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"numbers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":             "integer",
					"exclusiveMinimum": 0,
					"exclusiveMaximum": 100,
					"minimum":          1,
					"maximum":          99,
				},
				"minItems": 1,
				"maxItems": 10,
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	props := result["properties"].(map[string]any)
	numbers := props["numbers"].(map[string]any)

	// Check array-level fields removed
	if _, exists := numbers["minItems"]; exists {
		t.Error("minItems should be removed")
	}
	if _, exists := numbers["maxItems"]; exists {
		t.Error("maxItems should be removed")
	}

	// Check items schema cleaned
	items := numbers["items"].(map[string]any)
	if _, exists := items["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from items")
	}
	if _, exists := items["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from items")
	}
	if _, exists := items["minimum"]; exists {
		t.Error("minimum should be removed from items")
	}
	if _, exists := items["maximum"]; exists {
		t.Error("maximum should be removed from items")
	}
}

func TestCleanJsonSchemaForGemini_PreservesValidFields(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"description": "A test schema",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name field",
			},
			"age": map[string]any{
				"type":        "integer",
				"description": "The age field",
			},
		},
		"required": []any{"name"},
	}

	result := CleanJsonSchemaForGemini(schema)

	if result["type"] != "object" {
		t.Errorf("type should be preserved")
	}
	if result["description"] != "A test schema" {
		t.Errorf("description should be preserved")
	}
	if result["required"] == nil {
		t.Errorf("required should be preserved")
	}

	props := result["properties"].(map[string]any)
	nameProp := props["name"].(map[string]any)
	if nameProp["type"] != "string" {
		t.Errorf("name.type should be preserved")
	}
	if nameProp["description"] != "The name field" {
		t.Errorf("name.description should be preserved")
	}
}

func TestCleanJsonSchemaForGemini_NilSchema(t *testing.T) {
	result := CleanJsonSchemaForGemini(nil)
	if result != nil {
		t.Errorf("Expected nil for nil input, got %v", result)
	}
}

func TestCleanJsonSchemaForGemini_RemovesRefAndDefs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"$ref": "#/$defs/MyType",
		"$defs": map[string]any{
			"MyType": map[string]any{
				"type": "string",
			},
		},
		"properties": map[string]any{
			"field": map[string]any{
				"$ref": "#/$defs/MyType",
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	if _, exists := result["$ref"]; exists {
		t.Error("$ref should be removed at top level")
	}
	if _, exists := result["$defs"]; exists {
		t.Error("$defs should be removed at top level")
	}

	props := result["properties"].(map[string]any)
	field := props["field"].(map[string]any)
	if _, exists := field["$ref"]; exists {
		t.Error("$ref should be removed from nested property")
	}
}
