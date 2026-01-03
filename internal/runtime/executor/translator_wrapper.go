package executor

import (
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
	formatOpenAI = provider.FromString("openai")
	formatGemini = provider.FromString("gemini")
	formatCodex  = provider.FromString("codex")
	formatClaude = provider.FromString("claude")
)

func extractUsageFromEvents(events []ir.UnifiedEvent) *ir.Usage {
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
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}

	geminiJSON, err := translator.ConvertRequest("gemini", irReq)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{
		Payload: applyPayloadConfigToIR(cfg, model, geminiJSON),
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
			Payload:              applyPayloadConfigToIR(cfg, model, cliPayload),
			EstimatedInputTokens: 0,
		}, nil
	}

	irReq, err := convertRequestToIR(from, model, payload, metadata)
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
		Payload: applyPayloadConfigToIR(cfg, model, convertedJSON),
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

func convertRequestToIR(from provider.Format, model string, payload []byte, metadata map[string]any) (*ir.UnifiedChatRequest, error) {
	payload = sanitizeUndefinedValues(payload)

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
		budgetOverride, includeOverride, hasOverride := extractThinkingFromMetadata(metadata)
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

	normalizeIRLimits(irReq.Model, irReq)
	preprocess.Apply(irReq)

	return irReq, nil
}

func normalizeIRLimits(model string, req *ir.UnifiedChatRequest) {
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

func extractThinkingFromMetadata(metadata map[string]any) (budget *int, include *bool, hasOverride bool) {
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
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return from_ir.ToOpenAIRequestFmt(irReq, from_ir.FormatResponsesAPI)
}

func TranslateToClaude(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return translator.ConvertRequest("claude", irReq)
}

func TranslateToOpenAI(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	fromStr := from.String()
	if fromStr == "openai" || fromStr == "cline" {
		return applyPayloadConfigToIR(cfg, model, payload), nil
	}

	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	openaiJSON, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		return nil, err
	}
	return applyPayloadConfigToIR(cfg, model, openaiJSON), nil
}

func TranslateToGemini(cfg *config.Config, from provider.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	result, err := TranslateToGeminiWithTokens(cfg, from, model, payload, streaming, metadata)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

func hasMultipleCandidates(response []byte) bool {
	parsed, _ := ir.UnwrapAntigravityEnvelope(response)
	return parsed.Get("candidates.1").Exists()
}
