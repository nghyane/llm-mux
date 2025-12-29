package executor

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	formatOpenAI = sdktranslator.FromString("openai")
	formatGemini = sdktranslator.FromString("gemini")
	formatCodex  = sdktranslator.FromString("codex")
	formatClaude = sdktranslator.FromString("claude")
)

func extractUsageFromEvents(events []ir.UnifiedEvent) *ir.Usage {
	for i := range events {
		if events[i].Type == ir.EventTypeFinish && events[i].Usage != nil {
			return events[i].Usage
		}
	}
	return nil
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

func TranslateToGeminiWithTokens(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) (*TranslationResult, error) {
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}

	geminiJSON, err := (&from_ir.GeminiProvider{}).ConvertRequest(irReq)
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

func TranslateToGeminiCLIWithTokens(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) (*TranslationResult, error) {
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

	if isClaudeModel {
		if strings.HasSuffix(model, "-thinking") {
			if irReq.Thinking == nil {
				budget := int32(1024)
				irReq.Thinking = &ir.ThinkingConfig{
					ThinkingBudget:  &budget,
					IncludeThoughts: true,
				}
			}
		} else if irReq.Thinking != nil {
			if thinkingModel := model + "-thinking"; registry.GetGlobalRegistry().GetModelInfo(thinkingModel) != nil {
				irReq.Model = thinkingModel
			}
		}
	}

	geminiJSON, err := (&from_ir.GeminiCLIProvider{}).ConvertRequest(irReq)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{
		Payload: applyPayloadConfigToIR(cfg, model, geminiJSON),
		IR:      irReq,
	}

	if fromStr == "claude" {
		result.EstimatedInputTokens = util.CountTokensFromIR(model, irReq)
	}

	return result, nil
}

func sanitizeUndefinedValues(payload []byte) []byte {
	if !strings.Contains(string(payload), "[undefined]") {
		return payload
	}
	result := gjson.ParseBytes(payload)
	if !result.IsObject() && !result.IsArray() {
		return payload
	}
	cleaned := cleanUndefinedRecursive(result.Value())
	if cleaned == nil {
		return payload
	}
	out, err := json.Marshal(cleaned)
	if err != nil {
		return payload
	}
	return out
}

func cleanUndefinedRecursive(v any) any {
	switch val := v.(type) {
	case map[string]any:
		cleaned := make(map[string]any)
		for k, child := range val {
			if str, ok := child.(string); ok && str == "[undefined]" {
				continue
			}
			if cleanedChild := cleanUndefinedRecursive(child); cleanedChild != nil {
				cleaned[k] = cleanedChild
			}
		}
		if len(cleaned) == 0 {
			return nil
		}
		return cleaned
	case []any:
		var cleaned []any
		for _, item := range val {
			if str, ok := item.(string); ok && str == "[undefined]" {
				continue
			}
			if cleanedItem := cleanUndefinedRecursive(item); cleanedItem != nil {
				cleaned = append(cleaned, cleanedItem)
			}
		}
		return cleaned
	default:
		return v
	}
}

func convertRequestToIR(from sdktranslator.Format, model string, payload []byte, metadata map[string]any) (*ir.UnifiedChatRequest, error) {
	payload = sanitizeUndefinedValues(payload)

	var irReq *ir.UnifiedChatRequest
	var err error

	switch from.String() {
	case "openai", "cline", "codex", "openai-response":
		irReq, err = to_ir.ParseOpenAIRequest(payload)
	case "ollama":
		irReq, err = to_ir.ParseOllamaRequest(payload)
	case "claude":
		irReq, err = to_ir.ParseClaudeRequest(payload)
	case "gemini", "gemini-cli":
		irReq, err = to_ir.ParseGeminiRequest(payload)
	default:
		return nil, fmt.Errorf("unsupported source format: %s", from.String())
	}

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

func TranslateToGeminiCLI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
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

func applyPayloadConfigToIR(cfg *config.Config, model string, payload []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}

	for _, rule := range cfg.Payload.Default {
		if matchesPayloadRule(rule, model, "gemini") {
			for path, value := range rule.Params {
				fullPath := "request." + path
				if !gjson.GetBytes(payload, fullPath).Exists() {
					payload, _ = sjson.SetBytes(payload, fullPath, value)
				}
			}
		}
	}

	for _, rule := range cfg.Payload.Override {
		if matchesPayloadRule(rule, model, "gemini") {
			for path, value := range rule.Params {
				fullPath := "request." + path
				payload, _ = sjson.SetBytes(payload, fullPath, value)
			}
		}
	}

	return payload
}

func matchesPayloadRule(rule config.PayloadRule, model, protocol string) bool {
	for _, m := range rule.Models {
		if m.Protocol != "" && m.Protocol != protocol {
			continue
		}
		if matchesPattern(m.Name, model) {
			return true
		}
	}
	return false
}

func matchesPattern(pattern, name string) bool {
	if pattern == name {
		return true
	}
	if pattern == "*" {
		return true
	}
	if strings.HasPrefix(pattern, "*") && strings.HasSuffix(pattern, "*") {
		return strings.Contains(name, pattern[1:len(pattern)-1])
	}
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(name, pattern[1:])
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return false
}

func TranslateToCodex(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return from_ir.ToOpenAIRequestFmt(irReq, from_ir.FormatResponsesAPI)
}

func TranslateToClaude(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return (&from_ir.ClaudeProvider{}).ConvertRequest(irReq)
}

func TranslateToOpenAI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
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

func TranslateToGemini(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
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
