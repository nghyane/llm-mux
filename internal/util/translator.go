// Package util provides utility functions for JSON manipulation and common operations.
package util

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Walk recursively traverses a JSON structure to find all occurrences of a specific field.
func Walk(value gjson.Result, path, field string, paths *[]string) {
	switch value.Type {
	case gjson.JSON:
		// For JSON objects and arrays, iterate through each child
		value.ForEach(func(key, val gjson.Result) bool {
			var childPath string
			if path == "" {
				childPath = key.String()
			} else {
				childPath = path + "." + key.String()
			}
			if key.String() == field {
				*paths = append(*paths, childPath)
			}
			Walk(val, childPath, field, paths)
			return true
		})
	case gjson.String, gjson.Number, gjson.True, gjson.False, gjson.Null:
		// Terminal types - no further traversal needed
	}
}

// RenameKey renames a key in a JSON string by moving its value to a new key path.
func RenameKey(jsonStr, oldKeyPath, newKeyPath string) (string, error) {
	value := gjson.Get(jsonStr, oldKeyPath)

	if !value.Exists() {
		return "", fmt.Errorf("old key '%s' does not exist", oldKeyPath)
	}

	interimJson, err := sjson.SetRaw(jsonStr, newKeyPath, value.Raw)
	if err != nil {
		return "", fmt.Errorf("failed to set new key '%s': %w", newKeyPath, err)
	}

	finalJson, err := sjson.Delete(interimJson, oldKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to delete old key '%s': %w", oldKeyPath, err)
	}

	return finalJson, nil
}

func DeleteKey(jsonStr, keyName string) string {
	paths := make([]string, 0)
	Walk(gjson.Parse(jsonStr), "", keyName, &paths)
	for _, p := range paths {
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}
