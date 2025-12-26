// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for JSON manipulation, proxy configuration,
// and other common operations used across the application.
package util

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Walk recursively traverses a JSON structure to find all occurrences of a specific field.
// It builds paths to each occurrence and adds them to the provided paths slice.
// Parameters:
//   - value: The gjson.Result object to traverse
//   - path: The current path in the JSON structure (empty string for root)
//   - field: The field name to search for
//   - paths: Pointer to a slice where found paths will be stored
//
// The function works recursively, building dot-notation paths to each occurrence
// of the specified field throughout the JSON structure.
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

// RenameKey renames a key in a JSON string by moving its value to a new key path
// and then deleting the old key path.
// Parameters:
//   - jsonStr: The JSON string to modify
//   - oldKeyPath: The dot-notation path to the key that should be renamed
//   - newKeyPath: The dot-notation path where the value should be moved to
//
// Returns:
//   - string: The modified JSON string with the key renamed
//   - error: An error if the operation fails
//
// The function performs the rename in two steps:
// 1. Sets the value at the new key path
// 2. Deletes the old key path
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
