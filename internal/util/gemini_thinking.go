package util

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
)

const (
	GeminiThinkingBudgetMetadataKey  = "gemini_thinking_budget"
	GeminiIncludeThoughtsMetadataKey = "gemini_include_thoughts"
	GeminiOriginalModelMetadataKey   = "gemini_original_model"
)

func ParseGeminiThinkingSuffix(model string) (string, *int, *bool, bool) {
	if model == "" {
		return model, nil, nil, false
	}
	lower := strings.ToLower(model)
	if !strings.HasPrefix(lower, "gemini-") {
		return model, nil, nil, false
	}

	if strings.HasSuffix(lower, "-nothinking") {
		base := model[:len(model)-len("-nothinking")]
		budgetValue := 0
		if strings.HasPrefix(lower, "gemini-2.5-pro") {
			budgetValue = 128
		}
		include := false
		return base, &budgetValue, &include, true
	}

	if strings.HasSuffix(lower, "-reasoning") {
		base := model[:len(model)-len("-reasoning")]
		budgetValue := -1
		include := true
		return base, &budgetValue, &include, true
	}

	idx := strings.LastIndex(lower, "-thinking-")
	if idx == -1 {
		return model, nil, nil, false
	}

	digits := model[idx+len("-thinking-"):]
	if digits == "" {
		return model, nil, nil, false
	}
	end := len(digits)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			end = i
			break
		}
	}
	if end == 0 {
		return model, nil, nil, false
	}
	valueStr := digits[:end]
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return model, nil, nil, false
	}
	base := model[:idx]
	budgetValue := value
	return base, &budgetValue, nil, true
}

func NormalizeGeminiThinkingModel(modelName string) (string, map[string]any) {
	baseModel, budget, include, matched := ParseGeminiThinkingSuffix(modelName)
	if !matched {
		return baseModel, nil
	}
	metadata := map[string]any{
		GeminiOriginalModelMetadataKey: modelName,
	}
	if budget != nil {
		metadata[GeminiThinkingBudgetMetadataKey] = *budget
	}
	if include != nil {
		metadata[GeminiIncludeThoughtsMetadataKey] = *include
	}
	return baseModel, metadata
}

func ApplyGeminiThinkingConfig(body []byte, budget *int, includeThoughts *bool) []byte {
	if budget == nil && includeThoughts == nil {
		return body
	}
	updated := body
	if budget != nil {
		valuePath := "generationConfig.thinkingConfig.thinkingBudget"
		rewritten, err := sjson.SetBytes(updated, valuePath, *budget)
		if err == nil {
			updated = rewritten
		}
	}
	if includeThoughts != nil {
		valuePath := "generationConfig.thinkingConfig.include_thoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *includeThoughts)
		if err == nil {
			updated = rewritten
		}
	}
	return updated
}

func GeminiThinkingFromMetadata(metadata map[string]any) (*int, *bool, bool) {
	if len(metadata) == 0 {
		return nil, nil, false
	}

	var budgetPtr *int
	var includePtr *bool
	matched := false

	if raw, ok := metadata[GeminiThinkingBudgetMetadataKey]; ok {
		if v := toInt(raw); v != nil {
			budgetPtr = v
			matched = true
		}
	}

	if raw, ok := metadata[GeminiIncludeThoughtsMetadataKey]; ok {
		if v := toBool(raw); v != nil {
			includePtr = v
			matched = true
		}
	}

	return budgetPtr, includePtr, matched
}

func toInt(v any) *int {
	var result int
	switch x := v.(type) {
	case int:
		result = x
	case int32:
		result = int(x)
	case int64:
		result = int(x)
	case float64:
		result = int(x)
	case json.Number:
		if val, err := x.Int64(); err == nil {
			result = int(val)
		} else {
			return nil
		}
	default:
		return nil
	}
	return &result
}

func toBool(v any) *bool {
	var result bool
	switch x := v.(type) {
	case bool:
		result = x
	case string:
		if parsed, err := strconv.ParseBool(x); err == nil {
			result = parsed
		} else {
			return nil
		}
	case int, int32, int64:
		result = x != 0
	case float64:
		result = x != 0
	case json.Number:
		if val, err := x.Int64(); err == nil {
			result = val != 0
		} else {
			return nil
		}
	default:
		return nil
	}
	return &result
}

// StripThinkingConfigIfUnsupported removes thinkingConfig for models that don't support it.
func StripThinkingConfigIfUnsupported(model string, body []byte) []byte {
	if ModelSupportsThinking(model) || len(body) == 0 {
		return body
	}
	updated := body
	updated, _ = sjson.DeleteBytes(updated, "request.generationConfig.thinkingConfig")
	updated, _ = sjson.DeleteBytes(updated, "generationConfig.thinkingConfig")
	return updated
}
