package ir

import "strings"

func IsGemini3(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-3")
}

func IsGemini3Flash(model string) bool {
	lower := strings.ToLower(model)
	return strings.HasPrefix(lower, "gemini-3") && strings.Contains(lower, "flash")
}

func IsClaude(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

func IsThinkingModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "thinking")
}

func ModelMayHaveThinking(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "2.5") ||
		strings.HasPrefix(lower, "gemini-3") ||
		strings.Contains(lower, "thinking") ||
		strings.Contains(lower, "claude")
}

// EffortToBudget converts reasoning_effort to thinkingBudget.
// Returns (budget, include) where -1 means use model default.
func EffortToBudget(effort string) (budget int, include bool) {
	switch strings.ToLower(effort) {
	case "none":
		return 0, false
	case "minimal":
		return 128, true
	case "low":
		return 1024, true
	case "medium":
		return 8192, true
	case "high":
		return 32768, true
	case "xhigh":
		return 65536, true
	default:
		return -1, true
	}
}

func BudgetToEffort(budget int, defaultForZero string) string {
	if budget <= 0 {
		return defaultForZero
	}
	if budget <= 1024 {
		return "low"
	}
	if budget <= 8192 {
		return "medium"
	}
	return "high"
}

// EffortToThinkingLevel converts reasoning_effort to Gemini 3 thinkingLevel.
// OpenAI "medium" maps to Gemini "HIGH" per Gemini 3 docs.
func EffortToThinkingLevel(model, effort string) ThinkingLevel {
	isFlash := IsGemini3Flash(model)

	switch strings.ToLower(effort) {
	case "none", "minimal":
		if isFlash {
			return ThinkingLevelMinimal
		}
		return ThinkingLevelLow
	case "low":
		return ThinkingLevelLow
	case "medium", "high", "xhigh":
		return ThinkingLevelHigh
	default:
		return DefaultThinkingLevel(model)
	}
}

// BudgetToThinkingLevel converts thinkingBudget to Gemini 3 thinkingLevel.
// Flash: ≤128→MINIMAL, ≤1024→LOW, ≤8192→MEDIUM, >8192→HIGH
// Pro: ≤1024→LOW, >1024→HIGH
func BudgetToThinkingLevel(model string, budget int) ThinkingLevel {
	isFlash := IsGemini3Flash(model)

	switch {
	case budget <= 128:
		if isFlash {
			return ThinkingLevelMinimal
		}
		return ThinkingLevelLow
	case budget <= 1024:
		return ThinkingLevelLow
	case budget <= 8192:
		if isFlash {
			return ThinkingLevelMedium
		}
		return ThinkingLevelHigh
	default:
		return ThinkingLevelHigh
	}
}

func DefaultThinkingLevel(model string) ThinkingLevel {
	if IsGemini3Flash(model) {
		return ThinkingLevelMedium
	}
	return ThinkingLevelHigh
}

func ThinkingLevelToBudget(level ThinkingLevel) int {
	switch level {
	case ThinkingLevelMinimal:
		return 128
	case ThinkingLevelLow:
		return 1024
	case ThinkingLevelMedium:
		return 8192
	case ThinkingLevelHigh:
		return 32768
	default:
		return 8192
	}
}

func IsValidThoughtSignature(ts []byte) bool {
	if len(ts) == 0 {
		return false
	}
	switch string(ts) {
	case "[undefined]", "undefined", "null", "[null]", "":
		return false
	}
	return true
}

const DummyThoughtSignature = "context_engineering_is_the_way_to_go"
