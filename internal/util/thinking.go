package util

import (
	"github.com/nghyane/llm-mux/internal/registry"
)

// DefaultThinkingBudget is the safe default budget for auto-enabling thinking.
// NOTE: For models with Thinking metadata, use GetAutoAppliedThinkingConfig which
// reads from registry for single source of truth. This constant is only a fallback.
const DefaultThinkingBudget = 1024

// ModelSupportsThinking reports whether the given model has Thinking capability
// according to the model registry metadata (provider-agnostic).
func ModelSupportsThinking(model string) bool {
	if model == "" {
		return false
	}
	if info := registry.GetGlobalRegistry().GetModelInfo(model); info != nil {
		return info.Thinking != nil
	}
	return false
}

// GetModelThinkingMin returns the minimum thinking budget for a model
// from registry metadata. Returns 0 if model doesn't support thinking.
func GetModelThinkingMin(model string) int {
	if model == "" {
		return 0
	}
	if info := registry.GetGlobalRegistry().GetModelInfo(model); info != nil && info.Thinking != nil {
		return info.Thinking.Min
	}
	return 0
}

// GetDefaultThinkingBudget returns the appropriate default thinking budget for a model
// by reading from registry metadata. Uses model's Min as default if available,
// otherwise falls back to DefaultThinkingBudget.
func GetDefaultThinkingBudget(model string) int {
	if min := GetModelThinkingMin(model); min > 0 {
		// Use model's minimum as default (single source of truth)
		return min
	}
	return DefaultThinkingBudget
}

// GetAutoAppliedThinkingConfig returns the default thinking configuration for a model
// if it should be auto-applied. Returns (budget, include_thoughts, should_apply).
// Uses registry metadata for single source of truth on budget values.
func GetAutoAppliedThinkingConfig(model string) (int, bool, bool) {
	if ModelSupportsThinking(model) {
		budget := GetDefaultThinkingBudget(model)
		return budget, true, true
	}
	return 0, false, false
}
