package stream

import (
	"bytes"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/preprocess"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/sjson"
)

var (
	FormatOpenAI = provider.FromString("openai")
	FormatGemini = provider.FromString("gemini")
	FormatCodex  = provider.FromString("codex")
	FormatClaude = provider.FromString("claude")
)

func ExtractUsageFromEvents(events []ir.UnifiedEvent) *ir.Usage {
	var lastUsage *ir.Usage
	for i := range events {
		if events[i].Usage != nil {
			lastUsage = events[i].Usage
		}
	}
	return lastUsage
}

type TranslationResult struct {
	Payload              []byte                 // Translated payload
	EstimatedInputTokens int64                  // Pre-calculated input token count (0 if not applicable)
	IR                   *ir.UnifiedChatRequest // Parsed IR (for advanced use cases)
}

type StreamTranslationResult struct {
	Chunks [][]byte  // Translated SSE chunks
	Usage  *ir.Usage // Usage extracted from IR events (nil if not present in this chunk)
}

func TranslateToGeminiWithTokens(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) (*TranslationResult, error) {
	irReq, err := ConvertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}

	geminiJSON, err := translator.ConvertRequest("gemini", irReq)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{
		Payload: ApplyPayloadConfigToIR(cfg, model, geminiJSON),
		IR:      irReq,
	}

	if from.String() == "claude" {
		result.EstimatedInputTokens = util.CountTokensFromIR(model, irReq)
	}

	return result, nil
}

func TranslateToGeminiCLIWithTokens(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) (*TranslationResult, error) {
	fromStr := from.String()
	isClaudeModel := strings.Contains(model, "claude")

	if (fromStr == "gemini" || fromStr == "gemini-cli") && !isClaudeModel {
		cliPayload, _ := sjson.SetRawBytes([]byte(`{}`), "request", payload)
		return &TranslationResult{
			Payload:              ApplyPayloadConfigToIR(cfg, model, cliPayload),
			EstimatedInputTokens: 0,
		}, nil
	}

	irReq, err := ConvertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}

	if isClaudeModel && (fromStr == "gemini" || fromStr == "gemini-cli") {
		irReq.Messages = to_ir.MergeConsecutiveModelThinking(irReq.Messages)
	}

	convertedJSON, err := translator.ConvertRequest("vertex-envelope", irReq)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{
		Payload: ApplyPayloadConfigToIR(cfg, model, convertedJSON),
		IR:      irReq,
	}

	if fromStr == "claude" {
		result.EstimatedInputTokens = util.CountGeminiTokensFromIR(irReq)
		if irReq.Thinking != nil && irReq.Thinking.ThinkingBudget != nil && *irReq.Thinking.ThinkingBudget > 0 {
			result.EstimatedInputTokens += util.ThinkingModeOverhead
		}
	}

	return result, nil
}

func ConvertRequestToIR(from provider.Format, model string, payload []byte, metadata map[string]any) (*ir.UnifiedChatRequest, error) {
	payload = SanitizeUndefinedValues(payload)

	formatStr := from.String()
	irReq, err := translator.ParseRequest(formatStr, payload)
	if err != nil {
		return nil, err
	}

	if model != "" {
		irReq.Model = model
	}

	if metadata != nil {
		if irReq.Metadata == nil {
			irReq.Metadata = make(map[string]any)
		}
		for k, v := range metadata {
			irReq.Metadata[k] = v
		}
	}

	if metadata != nil {
		budgetOverride, includeOverride, hasOverride := ExtractThinkingFromMetadata(metadata)
		if hasOverride {
			if irReq.Thinking == nil {
				irReq.Thinking = &ir.ThinkingConfig{}
			}
			if budgetOverride != nil {
				b := int32(*budgetOverride)
				irReq.Thinking.ThinkingBudget = &b
			}
			if includeOverride != nil {
				irReq.Thinking.IncludeThoughts = *includeOverride
			}
		}
	}

	NormalizeIRLimits(irReq.Model, irReq)
	ApplyThinkingToIR(irReq.Model, irReq)
	preprocess.Apply(irReq)

	return irReq, nil
}

func NormalizeIRLimits(model string, req *ir.UnifiedChatRequest) {
	if model == "" {
		return
	}

	info := registry.GetGlobalRegistry().GetModelInfo(model)
	if info == nil {
		return
	}

	if req.Thinking != nil && req.Thinking.ThinkingBudget != nil && info.Thinking != nil {
		budget := int(*req.Thinking.ThinkingBudget)

		if budget == -1 && !info.Thinking.DynamicAllowed {
			budget = (info.Thinking.Min + info.Thinking.Max) / 2
		}

		if budget == 0 && !info.Thinking.ZeroAllowed {
			budget = info.Thinking.Min
		}

		if budget > 0 {
			if budget < info.Thinking.Min {
				budget = info.Thinking.Min
			}
			if budget > info.Thinking.Max {
				budget = info.Thinking.Max
			}
		}

		b := int32(budget)
		req.Thinking.ThinkingBudget = &b
	}

	if req.MaxTokens != nil {
		limit := info.OutputTokenLimit
		if limit == 0 {
			limit = info.MaxCompletionTokens
		}
		if limit > 0 && *req.MaxTokens > limit {
			*req.MaxTokens = limit
		}
	}
}

func TranslateToGeminiCLI(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	result, err := TranslateToGeminiCLIWithTokens(cfg, from, model, payload, streaming, metadata)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

func ExtractThinkingFromMetadata(metadata map[string]any) (budget *int, include *bool, hasOverride bool) {
	if metadata == nil {
		return nil, nil, false
	}

	if v, ok := metadata["thinking_budget"].(int); ok {
		budget = &v
		hasOverride = true
	}
	if v, ok := metadata["include_thoughts"].(bool); ok {
		include = &v
		hasOverride = true
	}

	return budget, include, hasOverride
}

func TranslateToCodex(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := ConvertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return from_ir.ToOpenAIRequestFmt(irReq, from_ir.FormatResponsesAPI)
}

func TranslateToClaude(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := ConvertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return translator.ConvertRequest("claude", irReq)
}

func TranslateToOpenAI(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	fromStr := from.String()
	if fromStr == "openai" || fromStr == "cline" {
		return ApplyPayloadConfigToIR(cfg, model, payload), nil
	}

	irReq, err := ConvertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	openaiJSON, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		return nil, err
	}
	return ApplyPayloadConfigToIR(cfg, model, openaiJSON), nil
}

func TranslateToGemini(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	result, err := TranslateToGeminiWithTokens(cfg, from, model, payload, streaming, metadata)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

// FilterSSEUsageMetadata filters usage metadata from SSE payload
func FilterSSEUsageMetadata(payload []byte) []byte {
	// This is a placeholder - the actual implementation should be imported from executor
	// For now, just return the payload as-is
	return payload
}

// JsonPayload extracts JSON payload from SSE line
func JsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}

	// Check if it starts with "data:" prefix
	dataPrefix := []byte("data:")
	if bytes.HasPrefix(trimmed, dataPrefix) {
		trimmed = bytes.TrimSpace(trimmed[len(dataPrefix):])
	}

	if len(trimmed) == 0 {
		return nil
	}

	// Check if it's valid JSON (starts with { or [)
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return nil
	}

	return trimmed
}

// SanitizeUndefinedValues removes undefined JavaScript values from JSON
func SanitizeUndefinedValues(payload []byte) []byte {
	// Simple passthrough for now - actual implementation may need more logic
	return payload
}

// ApplyPayloadConfigToIR applies configuration to the payload
func ApplyPayloadConfigToIR(cfg *config.Config, model string, payload []byte) []byte {
	// Placeholder - actual implementation depends on config structure
	return payload
}

// ApplyThinkingToIR applies thinking configuration to the IR request
func ApplyThinkingToIR(model string, req *ir.UnifiedChatRequest) {
	// Get model info from registry
	info := registry.GetGlobalRegistry().GetModelInfo(model)
	if info == nil || info.Thinking == nil {
		return
	}

	// If thinking is not set but model supports it, initialize
	if req.Thinking == nil && info.Thinking != nil {
		// Don't auto-enable thinking - let the request decide
		return
	}
}
