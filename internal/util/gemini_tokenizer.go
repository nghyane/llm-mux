// Package util provides utility functions for the llm-mux project.
package util

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"strings"
	"sync"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"google.golang.org/genai"
	"google.golang.org/genai/tokenizer"
)

// ImageTokenCost is the fixed token cost per image in Gemini models.
// This is an approximation based on Gemini's standard image processing.
const ImageTokenCost = 258

// AudioTokenCostGemini is the estimated token cost for audio content.
// Gemini processes audio at approximately 25 tokens per second.
const AudioTokenCostGemini = 300

// VideoTokenCostGemini is the estimated token cost for video content.
// Video includes both visual frames and audio, estimated at ~2000 tokens base.
const VideoTokenCostGemini = 2000

// DocTokenCostGemini is the estimated token cost for file references.
const DocTokenCostGemini = 500

// Token estimation constants for Claude via Antigravity.
// Based on observed differences between local estimate and actual API response.
const (
	AntigravityBaseOverhead   = 7    // Server-side wrapper tokens
	AntigravityPerMsgOverhead = 0    // Per-message overhead (included in base for simple cases)
	AntigravityScalingFactor  = 0.08 // 8% buffer for large inputs
	LargeInputThreshold       = 100  // Apply scaling above this threshold
)

// ThinkingModeOverhead is added when thinking/reasoning mode is enabled.
const ThinkingModeOverhead = 29

// tokenizerCache caches LocalTokenizer instances by normalized model name.
// This avoids repeated tokenizer initialization which is expensive.
var (
	tokenizerCache   = make(map[string]*tokenizer.LocalTokenizer)
	tokenizerCacheMu sync.RWMutex
)

// partsPool reduces allocations for genai.Part slices during token counting.
var partsPool = sync.Pool{
	New: func() any {
		// Pre-allocate with typical capacity
		parts := make([]*genai.Part, 0, 16)
		return &parts
	},
}

// =============================================================================
// Public API
// =============================================================================

// CountTokensFromIR counts tokens directly from a unified IR request.
// This is the primary token counting function - efficient and accurate.
// It acts as a dispatcher:
// - Gemini models: Uses google.golang.org/genai/tokenizer (native accuracy)
// - OpenAI/Claude/Qwen: Uses tiktoken-go (o200k_base/cl100k_base)
// Returns 0 if counting fails (non-blocking, fail-safe).
func CountTokensFromIR(model string, req *ir.UnifiedChatRequest) int64 {
	if req == nil {
		return 0
	}

	model = strings.ToLower(model)

	// Dispatch to appropriate tokenizer
	if isGeminiModel(model) {
		return countGeminiTokens(model, req)
	}

	// For Claude, OpenAI, Qwen, etc.
	return CountTiktokenTokens(model, req)
}

// CountGeminiTokensFromIR always uses Gemini tokenizer regardless of model name.
// Use this when requests are translated to Gemini format (e.g., Claude via Antigravity/Vertex).
// The backend (Gemini API) will tokenize using Gemini's tokenizer, so we must match that.
// Adds AntigravityOverhead to account for server-side system prompt/wrapper tokens.
func CountGeminiTokensFromIR(req *ir.UnifiedChatRequest) int64 {
	if req == nil {
		return 0
	}
	baseTokens := countGeminiTokens("gemini-2.0-flash", req)
	msgCount := int64(len(req.Messages))

	overhead := int64(AntigravityBaseOverhead) + (msgCount * AntigravityPerMsgOverhead)

	if baseTokens > LargeInputThreshold {
		overhead += int64(float64(baseTokens) * AntigravityScalingFactor)
	}

	return baseTokens + overhead
}

// mediaCounts tracks non-text media elements for token estimation.
type mediaCounts struct {
	images int
	audios int
	videos int
	files  int // Files without inline data (URL/ID references)
}

// total returns the estimated token count for all media.
func (m mediaCounts) total() int64 {
	return int64(m.images*ImageTokenCost) +
		int64(m.audios*AudioTokenCostGemini) +
		int64(m.videos*VideoTokenCostGemini) +
		int64(m.files*DocTokenCostGemini)
}

// countGeminiTokens implements the specific logic for Gemini token counting.
func countGeminiTokens(model string, req *ir.UnifiedChatRequest) int64 {
	tok, err := getTokenizer(model)
	if err != nil {
		return 0
	}

	contents, media := buildContentsFromIR(req)

	// Count message content tokens
	var contentTokens int64
	if len(contents) > 0 {
		if result, err := tok.CountTokens(contents, nil); err == nil {
			contentTokens = int64(result.TotalTokens)
		}
	}

	// Count instructions tokens (Responses API system instructions)
	if req.Instructions != "" {
		if cached, ok := InstructionTokenCache.Get(req.Instructions); ok {
			contentTokens += int64(cached)
		} else {
			instructionContent := &genai.Content{
				Role:  "user",
				Parts: []*genai.Part{genai.NewPartFromText(req.Instructions)},
			}
			if result, err := tok.CountTokens([]*genai.Content{instructionContent}, nil); err == nil {
				tokens := int(result.TotalTokens)
				InstructionTokenCache.Set(req.Instructions, tokens)
				contentTokens += int64(tokens)
			}
		}
	}

	// Count tool definition tokens
	toolTokens := countToolTokensFromIR(tok, req.Tools)

	// Count ResponseSchema tokens (structured output JSON schema)
	schemaTokens := countSchemaTokensFromIR(tok, req.ResponseSchema)

	// Count MCPServers tokens (Claude MCP configs, also relevant for translation)
	mcpTokens := countMCPServersTokensFromIR(tok, req.MCPServers)

	// Count Prediction content tokens
	var predictionTokens int64
	if req.Prediction != nil && req.Prediction.Content != "" {
		predictionContent := &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{genai.NewPartFromText(req.Prediction.Content)},
		}
		if result, err := tok.CountTokens([]*genai.Content{predictionContent}, nil); err == nil {
			predictionTokens = int64(result.TotalTokens)
		}
	}

	total := contentTokens + toolTokens + schemaTokens + mcpTokens + predictionTokens + media.total()

	return total
}

// =============================================================================
// Internal Implementation
// =============================================================================

// getTokenizer returns a cached tokenizer for the given model.
// Uses double-checked locking for thread safety.
func getTokenizer(model string) (*tokenizer.LocalTokenizer, error) {
	baseModel := normalizeModel(model)

	// Fast path: check cache with read lock
	tokenizerCacheMu.RLock()
	tok, ok := tokenizerCache[baseModel]
	tokenizerCacheMu.RUnlock()
	if ok {
		return tok, nil
	}

	// Slow path: create tokenizer with write lock
	tokenizerCacheMu.Lock()
	defer tokenizerCacheMu.Unlock()

	// Double-check after acquiring write lock
	if tok, ok := tokenizerCache[baseModel]; ok {
		return tok, nil
	}

	tok, err := tokenizer.NewLocalTokenizer(baseModel)
	if err != nil {
		return nil, err
	}

	tokenizerCache[baseModel] = tok
	return tok, nil
}

// normalizeModel maps model names to tokenizer-compatible base models.
// Supported models by google.golang.org/genai/tokenizer (as of v1.40.0):
//   - gemini-1.0-pro, gemini-1.5-pro, gemini-1.5-flash → gemma2 tokenizer
//   - gemini-2.0-flash, gemini-2.0-flash-lite → gemma3 tokenizer
//   - gemini-2.5-pro, gemini-2.5-flash, gemini-2.5-flash-lite → gemma3 tokenizer
//
// Assumes model is already lowercase.
func normalizeModel(model string) string {
	switch {
	// Gemini 2.5 series - use gemini-2.5-flash (officially supported)
	case strings.Contains(model, "gemini-2.5-flash-lite"):
		return "gemini-2.5-flash-lite"
	case strings.Contains(model, "gemini-2.5-flash"):
		return "gemini-2.5-flash"
	case strings.Contains(model, "gemini-2.5-pro"):
		return "gemini-2.5-pro"
	// Gemini 3 series - fallback to gemini-2.5-flash (same gemma3 tokenizer)
	case strings.Contains(model, "gemini-3"):
		return "gemini-2.5-flash"
	// Gemini 2.0 series
	case strings.Contains(model, "gemini-2.0-flash-lite"):
		return "gemini-2.0-flash-lite"
	case strings.Contains(model, "gemini-2.0"):
		return "gemini-2.0-flash"
	// Gemini 1.5 series
	case strings.Contains(model, "gemini-1.5-pro"):
		return "gemini-1.5-pro"
	case strings.Contains(model, "gemini-1.5"):
		return "gemini-1.5-flash"
	// Gemini 1.0 series
	case strings.Contains(model, "gemini-1.0"),
		strings.Contains(model, "gemini-pro"):
		return "gemini-1.0-pro"
	// Default to gemini-2.5-flash for unknown models (most current)
	default:
		return "gemini-2.5-flash"
	}
}

// isGeminiModel checks if the model name corresponds to a native Gemini model.
// Returns false for:
//   - Claude models (use tiktoken for accurate counting)
//   - OpenAI/GPT models (use tiktoken)
//   - Qwen models (use tiktoken)
//
// This ensures models proxied through Gemini infrastructure but using different
// tokenizers are handled correctly.
// Assumes model is already lowercase.
func isGeminiModel(model string) bool {
	// Non-Gemini models that should use tiktoken
	nonGeminiPatterns := []string{
		"claude",    // Claude models (even gemini-claude-*)
		"gpt",       // OpenAI GPT models
		"qwen",      // Alibaba Qwen models
		"codex",     // OpenAI Codex
		"o1",        // OpenAI o1 models
		"dall-e",    // Image models
		"whisper",   // Audio models
		"embedding", // Embedding models
	}

	for _, pattern := range nonGeminiPatterns {
		if strings.Contains(model, pattern) {
			return false
		}
	}

	// Must contain "gemini" to be considered a Gemini model
	return strings.Contains(model, "gemini")
}

// buildContentsFromIR converts IR messages to genai.Content slice for token counting.
// Returns the contents and media counts for non-text content.
func buildContentsFromIR(req *ir.UnifiedChatRequest) ([]*genai.Content, mediaCounts) {
	if req == nil || len(req.Messages) == 0 {
		return nil, mediaCounts{}
	}

	contents := make([]*genai.Content, 0, len(req.Messages))
	var total mediaCounts

	for i := range req.Messages {
		content, media := messageToContent(&req.Messages[i])
		total.images += media.images
		total.audios += media.audios
		total.videos += media.videos
		total.files += media.files
		if content != nil {
			contents = append(contents, content)
		}
	}

	return contents, total
}

// messageToContent converts a single IR message to genai.Content.
// Returns nil if the message has no countable content.
// Uses pooled slice to reduce allocations.
func messageToContent(msg *ir.Message) (*genai.Content, mediaCounts) {
	if msg == nil {
		return nil, mediaCounts{}
	}

	role := mapRole(msg.Role)

	// Get pooled slice and reset
	partsPtr := partsPool.Get().(*[]*genai.Part)
	parts := (*partsPtr)[:0]
	defer func() {
		// Clear references before returning to pool
		for i := range parts {
			parts[i] = nil
		}
		*partsPtr = parts[:0]
		partsPool.Put(partsPtr)
	}()

	var media mediaCounts

	// Process content parts
	for i := range msg.Content {
		part := &msg.Content[i]
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				parts = append(parts, genai.NewPartFromText(part.Text))
			}

		case ir.ContentTypeReasoning:
			if part.Reasoning != "" {
				parts = append(parts, genai.NewPartFromText(part.Reasoning))
			}
			// ThoughtSignature is binary data, skip tokenization
			// (estimated tokens would be len(part.ThoughtSignature) / 4)

		case ir.ContentTypeImage:
			if part.Image != nil {
				media.images++
			}

		case ir.ContentTypeAudio:
			if part.Audio != nil {
				media.audios++
				// Also count transcript if present
				if part.Audio.Transcript != "" {
					parts = append(parts, genai.NewPartFromText(part.Audio.Transcript))
				}
			}

		case ir.ContentTypeVideo:
			if part.Video != nil {
				media.videos++
			}

		case ir.ContentTypeToolResult:
			if part.ToolResult != nil {
				text := formatFunctionResponse(part.ToolResult.ToolCallID, part.ToolResult.Result)
				parts = append(parts, genai.NewPartFromText(text))
				media.images += len(part.ToolResult.Images)
				// Count files in tool results
				media.files += len(part.ToolResult.Files)
			}

		case ir.ContentTypeFile:
			if part.File != nil {
				if part.File.FileData != "" {
					parts = append(parts, genai.NewPartFromText(part.File.FileData))
				} else if part.File.FileURL != "" || part.File.FileID != "" {
					// File reference without inline data
					media.files++
				}
			}

		case ir.ContentTypeExecutableCode:
			if part.CodeExecution != nil && part.CodeExecution.Code != "" {
				parts = append(parts, genai.NewPartFromText(part.CodeExecution.Code))
			}

		case ir.ContentTypeCodeResult:
			if part.CodeExecution != nil && part.CodeExecution.Output != "" {
				parts = append(parts, genai.NewPartFromText(part.CodeExecution.Output))
			}
		}
	}

	// Process tool calls (assistant calling tools)
	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		text := formatFunctionCall(tc.Name, tc.Args)
		parts = append(parts, genai.NewPartFromText(text))
		// ThoughtSignature in tool calls is binary data, skip tokenization
		// (estimated tokens would be len(tc.ThoughtSignature) / 4)
	}

	if len(parts) == 0 {
		return nil, media
	}

	// Copy parts to a new slice (pool slice will be recycled)
	resultParts := make([]*genai.Part, len(parts))
	copy(resultParts, parts)

	return &genai.Content{Role: role, Parts: resultParts}, media
}

// countToolTokensFromIR counts tokens from tool definitions.
// Uses efficient JSON serialization with pre-allocated buffer.
func countToolTokensFromIR(tok *tokenizer.LocalTokenizer, tools []ir.ToolDefinition) int64 {
	if len(tools) == 0 {
		return 0
	}

	// Serialize tools to JSON for counting
	// Note: json.Marshal is acceptable here as tools are typically small
	// and this runs async. For hot paths, consider a custom serializer.
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		return 0
	}

	toolsJSONStr := string(toolsJSON)
	if cached, ok := ToolTokenCache.Get(toolsJSONStr); ok {
		return int64(cached)
	}

	content := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText(toolsJSONStr)},
	}

	result, err := tok.CountTokens([]*genai.Content{content}, nil)
	if err != nil {
		return 0
	}

	tokens := int(result.TotalTokens)
	ToolTokenCache.Set(toolsJSONStr, tokens)
	return int64(tokens)
}

func countSchemaTokensFromIR(tok *tokenizer.LocalTokenizer, schema map[string]any) int64 {
	if schema == nil {
		return 0
	}
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return 0
	}
	return countGeminiJSONBytes(tok, schemaJSON, ToolTokenCache)
}

func countMCPServersTokensFromIR(tok *tokenizer.LocalTokenizer, servers []ir.MCPServer) int64 {
	if len(servers) == 0 {
		return 0
	}
	serversJSON, err := json.Marshal(servers)
	if err != nil {
		return 0
	}
	return countGeminiJSONBytes(tok, serversJSON, ToolTokenCache)
}

func countGeminiJSONBytes(tok *tokenizer.LocalTokenizer, data []byte, cache *TokenCache) int64 {
	dataStr := string(data)
	if cache != nil {
		if cached, ok := cache.Get(dataStr); ok {
			return int64(cached)
		}
	}
	content := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText(dataStr)},
	}
	result, err := tok.CountTokens([]*genai.Content{content}, nil)
	if err != nil {
		return 0
	}
	tokens := int(result.TotalTokens)
	if cache != nil {
		cache.Set(dataStr, tokens)
	}
	return int64(tokens)
}

// =============================================================================
// Helper Functions
// =============================================================================

// mapRole converts IR role to Gemini role string.
func mapRole(role ir.Role) string {
	switch role {
	case ir.RoleAssistant:
		return "model"
	default:
		return "user"
	}
}

// formatFunctionCall creates a text representation of a function call for token counting.
func formatFunctionCall(name, args string) string {
	return fmt.Sprintf("<functionCall name=%q>%s</functionCall>", name, args)
}

// formatFunctionResponse creates a text representation of a function response for token counting.
func formatFunctionResponse(name, result string) string {
	return fmt.Sprintf("<functionResponse name=%q>%s</functionResponse>", name, result)
}
