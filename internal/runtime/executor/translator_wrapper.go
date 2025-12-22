package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Cached format constants - used by executors for TranslateTokenCount
var (
	formatOpenAI = sdktranslator.FromString("openai")
	formatGemini = sdktranslator.FromString("gemini")
	formatCodex  = sdktranslator.FromString("codex")
	formatClaude = sdktranslator.FromString("claude")
)

// =============================================================================
// Stream Conversion Infrastructure
// =============================================================================

// StreamState holds unified state for all streaming conversions.
// This reduces code duplication by providing a single state structure
// that can be used across different streaming translation functions.
type StreamState struct {
	ClaudeState         *from_ir.ClaudeStreamState
	ToolCallIndex       int  // Track tool call index across chunks for OpenAI format
	ReasoningCharsAccum int  // Track accumulated reasoning characters (for estimation)
	FinishSent          bool // Track if finish event was already sent (prevent duplicates)
	HasToolCalls        bool // Track if any tool calls were seen (for correct finish_reason)
	ToolSchemaCtx       *ir.ToolSchemaContext
}

// NewStreamState creates a new stream state with optional tool schema context.
func NewStreamState() *StreamState {
	return &StreamState{
		ClaudeState: from_ir.NewClaudeStreamState(),
	}
}

// EventPreprocessor is called before each event is converted.
// It allows format-specific state tracking and event modification.
// Returns true if the event should be skipped (not converted).
type EventPreprocessor func(event *ir.UnifiedEvent, state *StreamState) (skip bool)

// extractUsageFromEvents extracts usage from IR events (typically from Finish event).
// Returns nil if no usage is found in events.
func extractUsageFromEvents(events []ir.UnifiedEvent) *ir.Usage {
	for i := range events {
		if events[i].Type == ir.EventTypeFinish && events[i].Usage != nil {
			return events[i].Usage
		}
	}
	return nil
}

// convertEventsToOpenAI converts IR events to OpenAI format chunks.
// This consolidates the repeated openai/cline case logic.
func convertEventsToOpenAI(events []ir.UnifiedEvent, model, messageID string, state *StreamState, preprocessor EventPreprocessor) ([][]byte, error) {
	chunks := make([][]byte, 0, len(events))

	for i := range events {
		event := &events[i]

		// Apply preprocessor if provided
		if preprocessor != nil {
			if skip := preprocessor(event, state); skip {
				continue
			}
		}

		// Determine tool call index
		idx := 0
		if event.Type == ir.EventTypeToolCall {
			idx = state.ToolCallIndex
			state.ToolCallIndex++
		}

		chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
		if err != nil {
			return nil, err
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}

// convertEventsToClaude converts IR events to Claude format chunks.
func convertEventsToClaude(events []ir.UnifiedEvent, model, messageID string, state *StreamState, preprocessor EventPreprocessor) ([][]byte, error) {
	chunks := make([][]byte, 0, len(events))

	if state.ClaudeState == nil {
		state.ClaudeState = from_ir.NewClaudeStreamState()
	}

	for _, event := range events {
		// Apply preprocessor if provided
		if preprocessor != nil {
			if skip := preprocessor(&event, state); skip {
				continue
			}
		}

		claudeChunks, err := from_ir.ToClaudeSSE(event, model, messageID, state.ClaudeState)
		if err != nil {
			return nil, err
		}
		if claudeChunks != nil {
			chunks = append(chunks, claudeChunks)
		}
	}
	return chunks, nil
}

// convertEventsToOllama converts IR events to Ollama format chunks.
func convertEventsToOllama(events []ir.UnifiedEvent, model string, preprocessor EventPreprocessor, state *StreamState) ([][]byte, error) {
	chunks := make([][]byte, 0, len(events))

	for _, event := range events {
		// Apply preprocessor if provided (even for Ollama, for consistency)
		if preprocessor != nil {
			if skip := preprocessor(&event, state); skip {
				continue
			}
		}

		chunk, err := from_ir.ToOllamaChatChunk(event, model)
		if err != nil {
			return nil, err
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}

// convertEventsToGemini converts IR events to Gemini format chunks.
func convertEventsToGemini(events []ir.UnifiedEvent, model string) ([][]byte, error) {
	chunks := make([][]byte, 0, len(events))

	for _, event := range events {
		chunk, err := from_ir.ToGeminiChunk(event, model)
		if err != nil {
			return nil, err
		}
		if chunk != nil {
			chunks = append(chunks, chunk)
		}
	}
	return chunks, nil
}

// geminiPreprocessor handles state tracking for Gemini-sourced streams.
// Tracks tool calls, reasoning accumulation, and finish event handling.
func geminiPreprocessor(event *ir.UnifiedEvent, state *StreamState) bool {
	// Track tool calls across chunks for correct finish_reason
	if event.Type == ir.EventTypeToolCall {
		state.HasToolCalls = true
	}

	// Track reasoning content for token estimation
	if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
		state.ReasoningCharsAccum += len(event.Reasoning)
	}

	// Handle finish event with deduplication and token estimation
	if event.Type == ir.EventTypeFinish {
		if state.FinishSent {
			return true // skip duplicate finish
		}
		state.FinishSent = true

		// Override finish_reason if tool calls were seen
		if state.HasToolCalls {
			event.FinishReason = ir.FinishReasonToolCalls
		}

		// Estimate reasoning tokens if provider didn't provide them
		if state.ReasoningCharsAccum > 0 {
			if event.Usage == nil {
				event.Usage = &ir.Usage{}
			}
			if event.Usage.ThoughtsTokenCount == 0 {
				event.Usage.ThoughtsTokenCount = int32((state.ReasoningCharsAccum + 2) / 3)
			}
		}
	}

	return false // don't skip
}

// geminiPreprocessorNoFinishDedup is like geminiPreprocessor but without finish deduplication.
// Used for TranslateGeminiResponseStream which doesn't need FinishSent tracking.
func geminiPreprocessorNoFinishDedup(event *ir.UnifiedEvent, state *StreamState) bool {
	// Track tool calls across chunks for correct finish_reason
	if event.Type == ir.EventTypeToolCall {
		state.HasToolCalls = true
	}

	// Track reasoning content for token estimation
	if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
		state.ReasoningCharsAccum += len(event.Reasoning)
	}

	// Handle finish event
	if event.Type == ir.EventTypeFinish {
		// Override finish_reason if tool calls were seen
		if state.HasToolCalls {
			event.FinishReason = ir.FinishReasonToolCalls
		}

		// Estimate reasoning tokens if provider didn't provide them
		if state.ReasoningCharsAccum > 0 {
			if event.Usage == nil {
				event.Usage = &ir.Usage{}
			}
			if event.Usage.ThoughtsTokenCount == 0 {
				event.Usage.ThoughtsTokenCount = int32((state.ReasoningCharsAccum + 2) / 3)
			}
		}
	}

	return false // don't skip
}

// openaiPreprocessor handles state tracking for OpenAI-sourced streams.
// Tracks reasoning accumulation for token estimation.
func openaiPreprocessor(event *ir.UnifiedEvent, state *StreamState) bool {
	// Track reasoning content for token estimation
	if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
		state.ReasoningCharsAccum += len(event.Reasoning)
	}

	// On finish, ensure reasoning_tokens is set if we had reasoning content
	if event.Type == ir.EventTypeFinish && state.ReasoningCharsAccum > 0 {
		if event.Usage == nil {
			event.Usage = &ir.Usage{}
		}
		if event.Usage.ThoughtsTokenCount == 0 {
			event.Usage.ThoughtsTokenCount = int32((state.ReasoningCharsAccum + 2) / 3)
		}
	}

	return false // don't skip
}

// claudePreprocessor handles state tracking for Claude-sourced streams.
func claudePreprocessor(event *ir.UnifiedEvent, state *StreamState) bool {
	// Track tool calls across chunks
	if event.Type == ir.EventTypeToolCall {
		state.HasToolCalls = true
	}
	return false // don't skip
}

// =============================================================================
// Translation Result Types
// =============================================================================

// TranslationResult contains the translated payload and estimated token count.
// This allows callers to get both translation and token counting in a single operation.
type TranslationResult struct {
	Payload              []byte                 // Translated payload
	EstimatedInputTokens int64                  // Pre-calculated input token count (0 if not applicable)
	IR                   *ir.UnifiedChatRequest // Parsed IR (for advanced use cases)
}

// StreamTranslationResult contains translated chunks and extracted usage from streaming response.
// This eliminates duplicate parsing by extracting usage during translation.
type StreamTranslationResult struct {
	Chunks [][]byte  // Translated SSE chunks
	Usage  *ir.Usage // Usage extracted from IR events (nil if not present in this chunk)
}

// =============================================================================
// Gemini Translation with Token Counting
// =============================================================================

// TranslateToGeminiWithTokens converts request to Gemini format and counts tokens.
// This is the optimized path - translation and token counting share the same IR.
// Token counting is only performed for Claude source format (where input_tokens is needed).
// For other formats, EstimatedInputTokens will be 0.
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

	// Count tokens for Claude format (needed for message_start event)
	if from.String() == "claude" {
		result.EstimatedInputTokens = util.CountTokensFromIR(model, irReq)
	}

	return result, nil
}

// TranslateToGeminiCLIWithTokens converts request to Gemini CLI format and counts tokens.
// Similar to TranslateToGeminiWithTokens but for Gemini CLI/Antigravity format.
func TranslateToGeminiCLIWithTokens(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) (*TranslationResult, error) {
	// Early passthrough for gemini formats (except Claude models)
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

	geminiJSON, err := (&from_ir.GeminiCLIProvider{}).ConvertRequest(irReq)
	if err != nil {
		return nil, err
	}

	result := &TranslationResult{
		Payload: applyPayloadConfigToIR(cfg, model, geminiJSON),
		IR:      irReq,
	}

	// Count tokens for Claude format
	if fromStr == "claude" {
		result.EstimatedInputTokens = util.CountTokensFromIR(model, irReq)
	}

	return result, nil
}

// sanitizeUndefinedValues removes "[undefined]" literal strings from JSON payload.
// Some clients (e.g., Cherry Studio) send these invalid values which cause upstream API errors.
func sanitizeUndefinedValues(payload []byte) []byte {
	// Quick check - skip expensive parsing if no "[undefined]" present
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
				continue // Skip undefined values
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

// convertRequestToIR converts a request payload to unified format.
// This is the shared logic used by all translators.
// Returns error if format is unsupported.
func convertRequestToIR(from sdktranslator.Format, model string, payload []byte, metadata map[string]any) (*ir.UnifiedChatRequest, error) {
	// Sanitize payload to remove "[undefined]" values from buggy clients
	payload = sanitizeUndefinedValues(payload)

	var irReq *ir.UnifiedChatRequest
	var err error

	// Determine source format and convert to IR
	switch from.String() {
	case "openai", "cline", "codex", "openai-response": // OpenAI-compatible formats (auto-detects Chat Completions vs Responses API)
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

	// Override model if specified
	if model != "" {
		irReq.Model = model
	}

	// Store metadata for provider-specific handling (merge with existing)
	if metadata != nil {
		if irReq.Metadata == nil {
			irReq.Metadata = make(map[string]any)
		}
		for k, v := range metadata {
			irReq.Metadata[k] = v
		}
	}

	// Apply thinking overrides from metadata if present
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

	return irReq, nil
}

// TranslateToGeminiCLI converts request to Gemini CLI format using canonical IR translator.
// Note: Antigravity uses the same format as Gemini CLI, so this function works for both.
// This is a convenience wrapper around TranslateToGeminiCLIWithTokens that discards token count.
func TranslateToGeminiCLI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	result, err := TranslateToGeminiCLIWithTokens(cfg, from, model, payload, streaming, metadata)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

// extractThinkingFromMetadata extracts thinking config overrides from request metadata
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

// applyPayloadConfigToIR applies YAML payload config rules to the generated JSON
func applyPayloadConfigToIR(cfg *config.Config, model string, payload []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}

	// Apply default rules (only set if missing)
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

	// Apply override rules (always set)
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

// matchesPayloadRule checks if a payload rule matches the given model and protocol
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

// matchesPattern checks if a model name matches a pattern (supports wildcards)
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

// TranslateToCodex converts request to OpenAI Responses API format (Codex).
// metadata contains additional context like thinking overrides from request metadata.
func TranslateToCodex(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return from_ir.ToOpenAIRequestFmt(irReq, from_ir.FormatResponsesAPI)
}

// TranslateCodexResponseNonStream converts Codex (Responses API) non-streaming response to target format.
// Returns nil if new translator is disabled (caller should use old translator as fallback).
func TranslateCodexResponseNonStream(cfg *config.Config, to sdktranslator.Format, codexResponse []byte, model string) ([]byte, error) {
	// Early passthrough for codex format
	toStr := to.String()
	if toStr == "codex" || toStr == "openai-response" {
		return codexResponse, nil
	}

	// Step 1: Parse Codex response to IR (auto-detects Responses API format)
	messages, usage, err := to_ir.ParseOpenAIResponse(codexResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	messageID := "resp-" + model

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		return nil, nil
	}
}

// CodexStreamState maintains state for Codex (Responses API) streaming conversions.
type CodexStreamState struct {
	ResponsesState *from_ir.ResponsesStreamState
	ClaudeState    *from_ir.ClaudeStreamState
	ToolCallIndex  int
}

// TranslateCodexResponseStream converts Codex (Responses API) streaming chunk to target format.
func TranslateCodexResponseStream(cfg *config.Config, to sdktranslator.Format, codexChunk []byte, model string, messageID string, state *CodexStreamState) ([][]byte, error) {
	// Early passthrough for codex format
	toStr := to.String()
	if toStr == "codex" || toStr == "openai-response" {
		return [][]byte{codexChunk}, nil
	}

	// Step 1: Parse Codex chunk to IR events
	events, err := to_ir.ParseOpenAIChunk(codexChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &CodexStreamState{}
	}
	ss := &StreamState{
		ClaudeState:   state.ClaudeState,
		ToolCallIndex: state.ToolCallIndex,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, nil)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return nil, nil
	}

	// Sync state back
	state.ToolCallIndex = ss.ToolCallIndex
	state.ClaudeState = ss.ClaudeState

	return chunks, err
}

// TranslateCodexResponseStreamWithUsage converts Codex streaming chunk and extracts usage.
// This eliminates duplicate parsing by returning both translated chunks and usage in one operation.
func TranslateCodexResponseStreamWithUsage(cfg *config.Config, to sdktranslator.Format, codexChunk []byte, model string, messageID string, state *CodexStreamState) (*StreamTranslationResult, error) {
	// Early passthrough for codex format
	toStr := to.String()
	if toStr == "codex" || toStr == "openai-response" {
		return &StreamTranslationResult{Chunks: [][]byte{codexChunk}}, nil
	}

	// Step 1: Parse Codex chunk to IR events
	events, err := to_ir.ParseOpenAIChunk(codexChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return &StreamTranslationResult{}, nil
	}

	// Extract usage from events before conversion
	usage := extractUsageFromEvents(events)

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &CodexStreamState{}
	}
	ss := &StreamState{
		ClaudeState:   state.ClaudeState,
		ToolCallIndex: state.ToolCallIndex,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, nil)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return &StreamTranslationResult{}, nil
	}

	// Sync state back
	state.ToolCallIndex = ss.ToolCallIndex
	state.ClaudeState = ss.ClaudeState

	return &StreamTranslationResult{Chunks: chunks, Usage: usage}, err
}

// TranslateToClaude converts request to Claude API format.
func TranslateToClaude(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	// Note: We always parse to IR even for "claude" format to enable thinking block injection
	// for history turns that may have been stripped by the client.

	irReq, err := convertRequestToIR(from, model, payload, metadata)
	if err != nil {
		return nil, err
	}
	return (&from_ir.ClaudeProvider{}).ConvertRequest(irReq)
}

// TranslateToOpenAI converts request to OpenAI Chat Completions API format.
// metadata contains additional context like thinking overrides from request metadata.
func TranslateToOpenAI(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	// Early passthrough for openai formats - preserves native OpenAI request structure
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

// TranslateToGemini converts request to Gemini (AI Studio API) format.
// metadata contains additional context like thinking overrides from request metadata.
// This is a convenience wrapper around TranslateToGeminiWithTokens that discards token count.
func TranslateToGemini(cfg *config.Config, from sdktranslator.Format, model string, payload []byte, streaming bool, metadata map[string]any) ([]byte, error) {
	result, err := TranslateToGeminiWithTokens(cfg, from, model, payload, streaming, metadata)
	if err != nil {
		return nil, err
	}
	return result.Payload, nil
}

// TranslateGeminiCLIResponseNonStream converts Gemini CLI non-streaming response to target format.
func TranslateGeminiCLIResponseNonStream(cfg *config.Config, to sdktranslator.Format, geminiResponse []byte, model string) ([]byte, error) {
	// Early passthrough for gemini formats
	toStr := to.String()
	if toStr == "gemini" || toStr == "gemini-cli" {
		// Unwrap Antigravity envelope if present: {"response": {...}, "traceId": "..."}
		if responseWrapper := gjson.GetBytes(geminiResponse, "response"); responseWrapper.Exists() {
			return []byte(responseWrapper.Raw), nil
		}
		return geminiResponse, nil
	}

	// For multiple candidates, use the candidate parser (OpenAI format only)
	if (toStr == "openai" || toStr == "cline") && hasMultipleCandidates(geminiResponse) {
		candidates, usage, meta, err := to_ir.ParseGeminiResponseCandidates(geminiResponse, nil)
		if err != nil {
			return nil, err
		}

		messageID := "chatcmpl-" + model
		var openaiMeta *ir.OpenAIMeta
		if meta != nil {
			if meta.ResponseID != "" {
				messageID = meta.ResponseID
			}
			openaiMeta = &ir.OpenAIMeta{
				ResponseID:         meta.ResponseID,
				CreateTime:         meta.CreateTime,
				NativeFinishReason: meta.NativeFinishReason,
				GroundingMetadata:  meta.GroundingMetadata,
			}
			if usage != nil {
				openaiMeta.ThoughtsTokenCount = usage.ThoughtsTokenCount
			}
		}
		return from_ir.ToOpenAIChatCompletionCandidates(candidates, usage, model, messageID, openaiMeta)
	}

	// Step 1: Parse Gemini CLI response to IR (single candidate) with meta for grounding
	messages, usage, meta, err := to_ir.ParseGeminiResponseMetaWithContext(geminiResponse, nil)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	messageID := "chatcmpl-" + model
	var openaiMeta *ir.OpenAIMeta
	if meta != nil {
		if meta.ResponseID != "" {
			messageID = meta.ResponseID
		}
		openaiMeta = meta
	}

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletionMeta(messages, usage, model, messageID, openaiMeta)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		return nil, nil
	}
}

// GeminiCLIStreamState maintains state for stateful streaming conversions (e.g., Claude tool calls).
type GeminiCLIStreamState struct {
	ClaudeState          *from_ir.ClaudeStreamState
	ToolCallIndex        int // Track tool call index across chunks for OpenAI format
	ReasoningTokensCount int // Track accumulated reasoning tokens for final usage chunk
	ReasoningCharsAccum  int // Track accumulated reasoning characters (for estimation if provider doesn't give count)

	ToolSchemaCtx *ir.ToolSchemaContext // Schema context for normalizing tool call parameters
	FinishSent    bool                  // Track if finish event was already sent (prevent duplicates)
	HasToolCalls  bool                  // Track if any tool calls were seen across chunks (for correct finish_reason)
}

// NewAntigravityStreamState creates a new stream state with tool schema context for Antigravity provider.
// Antigravity has a known issue where Gemini ignores tool parameter schemas and returns
// different parameter names (e.g., "path" instead of "target_file").
// This function extracts the expected schema from the original request to normalize responses.
// Uses gjson for efficient extraction without full JSON unmarshaling.
func NewAntigravityStreamState(originalRequest []byte) *GeminiCLIStreamState {
	state := &GeminiCLIStreamState{
		ClaudeState: from_ir.NewClaudeStreamState(),
	}

	// Extract tool schemas efficiently using gjson (no full unmarshal)
	if len(originalRequest) > 0 {
		tools := gjson.GetBytes(originalRequest, "tools").Array()
		if len(tools) > 0 {
			state.ToolSchemaCtx = ir.NewToolSchemaContextFromGJSON(tools)
		}
	}

	return state
}

// TranslateGeminiCLIResponseStream converts Gemini CLI streaming chunk to target format.
// state parameter is optional but recommended for stateful conversions (e.g., Claude tool calls).
func TranslateGeminiCLIResponseStream(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) ([][]byte, error) {
	// Early passthrough for gemini formats (no IR conversion needed)
	toStr := to.String()
	if toStr == "gemini" || toStr == "gemini-cli" {
		// Unwrap Antigravity envelope if present: {"response": {...}, "traceId": "..."}
		if responseWrapper := gjson.GetBytes(geminiChunk, "response"); responseWrapper.Exists() {
			return [][]byte{[]byte(responseWrapper.Raw)}, nil
		}
		return [][]byte{geminiChunk}, nil
	}

	// Step 1: Parse Gemini CLI chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &GeminiCLIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         state.ClaudeState,
		ToolCallIndex:       state.ToolCallIndex,
		ReasoningCharsAccum: state.ReasoningCharsAccum,
		FinishSent:          state.FinishSent,
		HasToolCalls:        state.HasToolCalls,
		ToolSchemaCtx:       state.ToolSchemaCtx,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers with preprocessor
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, geminiPreprocessor)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return nil, nil
	}

	// Sync state back
	state.ClaudeState = ss.ClaudeState
	state.ToolCallIndex = ss.ToolCallIndex
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum
	state.FinishSent = ss.FinishSent
	state.HasToolCalls = ss.HasToolCalls

	return chunks, err
}

// TranslateGeminiCLIResponseStreamWithUsage converts Gemini CLI streaming chunk and extracts usage.
// This eliminates duplicate parsing by returning both translated chunks and usage in one operation.
func TranslateGeminiCLIResponseStreamWithUsage(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) (*StreamTranslationResult, error) {
	// Early passthrough for gemini formats (no IR conversion needed)
	toStr := to.String()
	if toStr == "gemini" || toStr == "gemini-cli" {
		if responseWrapper := gjson.GetBytes(geminiChunk, "response"); responseWrapper.Exists() {
			return &StreamTranslationResult{Chunks: [][]byte{[]byte(responseWrapper.Raw)}}, nil
		}
		return &StreamTranslationResult{Chunks: [][]byte{geminiChunk}}, nil
	}

	// Step 1: Parse Gemini CLI chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return &StreamTranslationResult{}, nil
	}

	// Extract usage from events before conversion
	usage := extractUsageFromEvents(events)

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &GeminiCLIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         state.ClaudeState,
		ToolCallIndex:       state.ToolCallIndex,
		ReasoningCharsAccum: state.ReasoningCharsAccum,
		FinishSent:          state.FinishSent,
		HasToolCalls:        state.HasToolCalls,
		ToolSchemaCtx:       state.ToolSchemaCtx,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers with preprocessor
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, geminiPreprocessor)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return &StreamTranslationResult{}, nil
	}

	// Sync state back
	state.ClaudeState = ss.ClaudeState
	state.ToolCallIndex = ss.ToolCallIndex
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum
	state.FinishSent = ss.FinishSent
	state.HasToolCalls = ss.HasToolCalls

	return &StreamTranslationResult{Chunks: chunks, Usage: usage}, err
}

// TranslateGeminiResponseNonStream converts Gemini (AI Studio) non-streaming response to target format.
func TranslateGeminiResponseNonStream(cfg *config.Config, to sdktranslator.Format, geminiResponse []byte, model string) ([]byte, error) {
	// Early passthrough for gemini format
	toStr := to.String()
	if toStr == "gemini" {
		// Unwrap Antigravity envelope if present: {"response": {...}, "traceId": "..."}
		if responseWrapper := gjson.GetBytes(geminiResponse, "response"); responseWrapper.Exists() {
			return []byte(responseWrapper.Raw), nil
		}
		return geminiResponse, nil
	}

	// For multiple candidates, use the new candidate parser (OpenAI format only)
	if (toStr == "openai" || toStr == "cline") && hasMultipleCandidates(geminiResponse) {
		candidates, usage, meta, err := to_ir.ParseGeminiResponseCandidates(geminiResponse, nil)
		if err != nil {
			return nil, err
		}

		messageID := "chatcmpl-" + model
		var openaiMeta *ir.OpenAIMeta
		if meta != nil {
			messageID = meta.ResponseID
			openaiMeta = &ir.OpenAIMeta{
				ResponseID:         meta.ResponseID,
				CreateTime:         meta.CreateTime,
				NativeFinishReason: meta.NativeFinishReason,
				GroundingMetadata:  meta.GroundingMetadata,
			}
			if usage != nil {
				openaiMeta.ThoughtsTokenCount = usage.ThoughtsTokenCount
			}
		}
		return from_ir.ToOpenAIChatCompletionCandidates(candidates, usage, model, messageID, openaiMeta)
	}

	// Step 1: Parse Gemini response to IR with metadata (single candidate)
	messages, usage, meta, err := to_ir.ParseGeminiResponseMeta(geminiResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	messageID := "chatcmpl-" + model
	if meta != nil && meta.ResponseID != "" {
		messageID = meta.ResponseID
	}

	switch toStr {
	case "openai", "cline":
		var openaiMeta *ir.OpenAIMeta
		if meta != nil {
			openaiMeta = &ir.OpenAIMeta{
				ResponseID:         meta.ResponseID,
				CreateTime:         meta.CreateTime,
				NativeFinishReason: meta.NativeFinishReason,
				Logprobs:           meta.Logprobs,
				GroundingMetadata:  meta.GroundingMetadata,
			}
			if usage != nil {
				openaiMeta.ThoughtsTokenCount = usage.ThoughtsTokenCount
			}
		}
		return from_ir.ToOpenAIChatCompletionMeta(messages, usage, model, messageID, openaiMeta)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	default:
		return nil, nil
	}
}

// hasMultipleCandidates checks if response has more than one candidate.
// Uses gjson's efficient array traversal - stops after finding 2nd element.
func hasMultipleCandidates(response []byte) bool {
	// Unwrap Antigravity envelope (zero-copy)
	parsed, _ := ir.UnwrapAntigravityEnvelope(response)
	// Check if candidates.1 exists (0-indexed, so .1 means 2nd element)
	return parsed.Get("candidates.1").Exists()
}

// TranslateGeminiResponseStream converts Gemini (AI Studio) streaming chunk to target format.
func TranslateGeminiResponseStream(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) ([][]byte, error) {
	// Early passthrough for gemini format (no IR conversion needed)
	toStr := to.String()
	if toStr == "gemini" {
		// Unwrap Antigravity envelope if present: {"response": {...}, "traceId": "..."}
		if responseWrapper := gjson.GetBytes(geminiChunk, "response"); responseWrapper.Exists() {
			return [][]byte{[]byte(responseWrapper.Raw)}, nil
		}
		return [][]byte{geminiChunk}, nil
	}

	// Step 1: Parse Gemini chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &GeminiCLIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         state.ClaudeState,
		ToolCallIndex:       state.ToolCallIndex,
		ReasoningCharsAccum: state.ReasoningCharsAccum,
		HasToolCalls:        state.HasToolCalls,
		ToolSchemaCtx:       state.ToolSchemaCtx,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers with preprocessor
	// Note: Uses geminiPreprocessorNoFinishDedup since this function doesn't track FinishSent
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, geminiPreprocessorNoFinishDedup)
	case "claude":
		// Claude also tracks tool calls in this function
		chunks, err = convertEventsToClaude(events, model, messageID, ss, claudePreprocessor)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return nil, nil
	}

	// Sync state back
	state.ClaudeState = ss.ClaudeState
	state.ToolCallIndex = ss.ToolCallIndex
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum
	state.HasToolCalls = ss.HasToolCalls

	return chunks, err
}

// TranslateGeminiResponseStreamWithUsage converts Gemini streaming chunk and extracts usage.
// This eliminates duplicate parsing by returning both translated chunks and usage in one operation.
func TranslateGeminiResponseStreamWithUsage(cfg *config.Config, to sdktranslator.Format, geminiChunk []byte, model string, messageID string, state *GeminiCLIStreamState) (*StreamTranslationResult, error) {
	// Early passthrough for gemini format (no IR conversion needed)
	toStr := to.String()
	if toStr == "gemini" {
		if responseWrapper := gjson.GetBytes(geminiChunk, "response"); responseWrapper.Exists() {
			return &StreamTranslationResult{Chunks: [][]byte{[]byte(responseWrapper.Raw)}}, nil
		}
		return &StreamTranslationResult{Chunks: [][]byte{geminiChunk}}, nil
	}

	// Step 1: Parse Gemini chunk to IR events (with schema context if available)
	var events []ir.UnifiedEvent
	var err error
	if state != nil && state.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithContext(geminiChunk, state.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunk(geminiChunk)
	}
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return &StreamTranslationResult{}, nil
	}

	// Extract usage from events before conversion
	usage := extractUsageFromEvents(events)

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &GeminiCLIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         state.ClaudeState,
		ToolCallIndex:       state.ToolCallIndex,
		ReasoningCharsAccum: state.ReasoningCharsAccum,
		HasToolCalls:        state.HasToolCalls,
		ToolSchemaCtx:       state.ToolSchemaCtx,
	}
	if ss.ClaudeState == nil {
		ss.ClaudeState = from_ir.NewClaudeStreamState()
	}

	// Step 3: Convert using unified helpers with preprocessor
	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks, err = convertEventsToOpenAI(events, model, messageID, ss, geminiPreprocessorNoFinishDedup)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, claudePreprocessor)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return &StreamTranslationResult{}, nil
	}

	// Sync state back
	state.ClaudeState = ss.ClaudeState
	state.ToolCallIndex = ss.ToolCallIndex
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum
	state.HasToolCalls = ss.HasToolCalls

	return &StreamTranslationResult{Chunks: chunks, Usage: usage}, err
}
func TranslateClaudeResponseNonStream(cfg *config.Config, to sdktranslator.Format, claudeResponse []byte, model string) ([]byte, error) {
	// Step 1: Parse Claude response to IR
	messages, usage, err := to_ir.ParseClaudeResponse(claudeResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()
	messageID := "msg-" + model // Simple ID generation

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	case "claude":
		// Passthrough - already in Claude format
		return claudeResponse, nil
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}
}

// TranslateClaudeResponseStream converts Claude streaming chunk to target format.
func TranslateClaudeResponseStream(cfg *config.Config, to sdktranslator.Format, claudeChunk []byte, model string, messageID string, state *from_ir.ClaudeStreamState) ([][]byte, error) {
	toStr := to.String()

	// Early passthrough for claude format
	if toStr == "claude" {
		return [][]byte{claudeChunk}, nil
	}

	// Step 1: Parse Claude chunk to IR events
	events, err := to_ir.ParseClaudeChunk(claudeChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Convert using unified helpers
	// Note: Claude source uses event.ToolCallIndex instead of state tracking
	ss := &StreamState{}
	if state != nil {
		ss.HasToolCalls = state.HasToolCalls
	}

	// Custom preprocessor for Claude source - uses event.ToolCallIndex directly
	claudeSourcePreprocessor := func(event *ir.UnifiedEvent, s *StreamState) bool {
		if event.Type == ir.EventTypeToolCall {
			s.HasToolCalls = true
		}
		return false
	}

	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		// For Claude source, tool call index comes from the event itself
		chunks = make([][]byte, 0, len(events))
		for _, event := range events {
			claudeSourcePreprocessor(&event, ss)
			idx := event.ToolCallIndex // Claude provides index in the event
			chunk, err := from_ir.ToOpenAIChunk(event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return nil, nil
	}

	// Sync state back
	if state != nil {
		state.HasToolCalls = ss.HasToolCalls
	}

	return chunks, err
}

// TranslateClaudeResponseStreamWithUsage converts Claude streaming chunk and extracts usage.
// This eliminates duplicate parsing by returning both translated chunks and usage in one operation.
func TranslateClaudeResponseStreamWithUsage(cfg *config.Config, to sdktranslator.Format, claudeChunk []byte, model string, messageID string, state *from_ir.ClaudeStreamState) (*StreamTranslationResult, error) {
	toStr := to.String()

	// Early passthrough for claude format
	if toStr == "claude" {
		return &StreamTranslationResult{Chunks: [][]byte{claudeChunk}}, nil
	}

	// Step 1: Parse Claude chunk to IR events
	events, err := to_ir.ParseClaudeChunk(claudeChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return &StreamTranslationResult{}, nil
	}

	// Extract usage from events before conversion
	usage := extractUsageFromEvents(events)

	// Step 2: Convert using unified helpers
	ss := &StreamState{}
	if state != nil {
		ss.HasToolCalls = state.HasToolCalls
	}

	claudeSourcePreprocessor := func(event *ir.UnifiedEvent, s *StreamState) bool {
		if event.Type == ir.EventTypeToolCall {
			s.HasToolCalls = true
		}
		return false
	}

	var chunks [][]byte
	switch toStr {
	case "openai", "cline":
		chunks = make([][]byte, 0, len(events))
		for _, event := range events {
			claudeSourcePreprocessor(&event, ss)
			idx := event.ToolCallIndex
			chunk, err := from_ir.ToOpenAIChunk(event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	default:
		return &StreamTranslationResult{}, nil
	}

	// Sync state back
	if state != nil {
		state.HasToolCalls = ss.HasToolCalls
	}

	return &StreamTranslationResult{Chunks: chunks, Usage: usage}, err
}

// OpenAIStreamState maintains state for OpenAI → OpenAI streaming conversions.
type OpenAIStreamState struct {
	ReasoningCharsAccum int // Track accumulated reasoning characters for token estimation
}

// TranslateOpenAIResponseStream converts OpenAI streaming chunk to target format.
// This is used for OpenAI-compatible providers (like Ollama) to ensure reasoning_tokens is properly set.
func TranslateOpenAIResponseStream(cfg *config.Config, to sdktranslator.Format, openaiChunk []byte, model string, messageID string, state *OpenAIStreamState) ([][]byte, error) {
	// Step 1: Parse OpenAI chunk to IR events
	events, err := to_ir.ParseOpenAIChunk(openaiChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return nil, nil
	}

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &OpenAIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         from_ir.NewClaudeStreamState(),
		ReasoningCharsAccum: state.ReasoningCharsAccum,
	}

	// Step 3: Convert using unified helpers
	toStr := to.String()
	var chunks [][]byte

	switch toStr {
	case "openai", "cline":
		// OpenAI source uses event.ToolCallIndex, but needs reasoning tracking
		chunks = make([][]byte, 0, len(events))
		for i := range events {
			event := &events[i]

			// Apply openai preprocessor for reasoning tracking
			openaiPreprocessor(event, ss)

			// Use ToolCallIndex from event for proper tool call indexing
			idx := event.ToolCallIndex
			chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "gemini", "gemini-cli":
		chunks, err = convertEventsToGemini(events, model)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	default:
		return nil, nil
	}

	// Sync state back
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum

	return chunks, err
}

// TranslateOpenAIResponseStreamWithUsage converts OpenAI streaming chunk and extracts usage.
// This eliminates duplicate parsing by returning both translated chunks and usage in one operation.
func TranslateOpenAIResponseStreamWithUsage(cfg *config.Config, to sdktranslator.Format, openaiChunk []byte, model string, messageID string, state *OpenAIStreamState) (*StreamTranslationResult, error) {
	// Step 1: Parse OpenAI chunk to IR events
	events, err := to_ir.ParseOpenAIChunk(openaiChunk)
	if err != nil {
		return nil, err
	}

	if len(events) == 0 {
		return &StreamTranslationResult{}, nil
	}

	// Extract usage from events before conversion
	usage := extractUsageFromEvents(events)

	// Step 2: Initialize unified state from legacy state
	if state == nil {
		state = &OpenAIStreamState{}
	}
	ss := &StreamState{
		ClaudeState:         from_ir.NewClaudeStreamState(),
		ReasoningCharsAccum: state.ReasoningCharsAccum,
	}

	// Step 3: Convert using unified helpers
	toStr := to.String()
	var chunks [][]byte

	switch toStr {
	case "openai", "cline":
		chunks = make([][]byte, 0, len(events))
		for i := range events {
			event := &events[i]
			openaiPreprocessor(event, ss)
			idx := event.ToolCallIndex
			chunk, err := from_ir.ToOpenAIChunk(*event, model, messageID, idx)
			if err != nil {
				return nil, err
			}
			if chunk != nil {
				chunks = append(chunks, chunk)
			}
		}
	case "gemini", "gemini-cli":
		chunks, err = convertEventsToGemini(events, model)
	case "ollama":
		chunks, err = convertEventsToOllama(events, model, nil, ss)
	case "claude":
		chunks, err = convertEventsToClaude(events, model, messageID, ss, nil)
	default:
		return &StreamTranslationResult{}, nil
	}

	// Sync state back
	state.ReasoningCharsAccum = ss.ReasoningCharsAccum

	return &StreamTranslationResult{Chunks: chunks, Usage: usage}, err
}

// TranslateOpenAIResponseNonStream converts OpenAI non-streaming response to target format.
func TranslateOpenAIResponseNonStream(cfg *config.Config, to sdktranslator.Format, openaiResponse []byte, model string) ([]byte, error) {
	// Step 1: Parse OpenAI response to IR
	messages, usage, err := to_ir.ParseOpenAIResponse(openaiResponse)
	if err != nil {
		return nil, err
	}

	// Step 2: Convert IR to target format
	toStr := to.String()
	messageID := "chatcmpl-" + model // Simple ID generation

	switch toStr {
	case "openai", "cline":
		return from_ir.ToOpenAIChatCompletion(messages, usage, model, messageID)
	case "gemini", "gemini-cli":
		return from_ir.ToGeminiResponse(messages, usage, model)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, model)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, model, messageID)
	default:
		// Unsupported target format, return nil to trigger fallback
		return nil, nil
	}
}

// TranslateTokenCount converts token count response to target format.
// This delegates to sdktranslator.TranslateTokenCount since token count
// translation doesn't require IR-based conversion.
func TranslateTokenCount(ctx context.Context, to, from sdktranslator.Format, count int64, usageJSON []byte) string {
	return sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
}
