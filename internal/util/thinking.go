package util

import (
	"github.com/nghyane/llm-mux/internal/registry"
)

// DefaultThinkingBudget is the safe default budget for auto-enabling thinking.
// Provide a fixed value (e.g. 1024) instead of dynamic (-1) because some upstream
// providers (e.g. Antigravity/Google) rely on fixed budgets for mapped models like Claude.
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

// GetAutoAppliedThinkingConfig returns the default thinking configuration for a model
// if it should be auto-applied. Returns (budget, include_thoughts, should_apply).
func GetAutoAppliedThinkingConfig(model string) (int, bool, bool) {
	if ModelSupportsThinking(model) {
		return DefaultThinkingBudget, true, true
	}
	return 0, false, false
}
