// Package util provides utility functions for the llm-mux project.
package util

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"google.golang.org/genai"
	"google.golang.org/genai/tokenizer"
)

// ImageTokenCost is the fixed token cost per image in Gemini models.
// This is an approximation based on Gemini's standard image processing.
const ImageTokenCost = 258

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

	// Dispatch to appropriate tokenizer
	if isGeminiModel(model) {
		return countGeminiTokens(model, req)
	}

	// For Claude, OpenAI, Qwen, etc.
	return CountTiktokenTokens(model, req)
}

// countGeminiTokens implements the specific logic for Gemini token counting.
func countGeminiTokens(model string, req *ir.UnifiedChatRequest) int64 {
	tok, err := getTokenizer(model)
	if err != nil {
		return 0
	}

	contents, imageCount := buildContentsFromIR(req)

	// Count message content tokens
	var contentTokens int64
	if len(contents) > 0 {
		if result, err := tok.CountTokens(contents, nil); err == nil {
			contentTokens = int64(result.TotalTokens)
		}
	}

	// Count instructions tokens (Responses API system instructions)
	if req.Instructions != "" {
		instructionContent := &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{genai.NewPartFromText(req.Instructions)},
		}
		if result, err := tok.CountTokens([]*genai.Content{instructionContent}, nil); err == nil {
			contentTokens += int64(result.TotalTokens)
		}
	}

	// Count tool definition tokens
	toolTokens := countToolTokensFromIR(tok, req.Tools)

	total := contentTokens + toolTokens + int64(imageCount*ImageTokenCost)

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
func normalizeModel(model string) string {
	lower := strings.ToLower(model)
	switch {
	// Gemini 2.5 series - use gemini-2.5-flash (officially supported)
	case strings.Contains(lower, "gemini-2.5-flash-lite"):
		return "gemini-2.5-flash-lite"
	case strings.Contains(lower, "gemini-2.5-flash"):
		return "gemini-2.5-flash"
	case strings.Contains(lower, "gemini-2.5-pro"):
		return "gemini-2.5-pro"
	// Gemini 3 series - fallback to gemini-2.5-flash (same gemma3 tokenizer)
	case strings.Contains(lower, "gemini-3"):
		return "gemini-2.5-flash"
	// Gemini 2.0 series
	case strings.Contains(lower, "gemini-2.0-flash-lite"):
		return "gemini-2.0-flash-lite"
	case strings.Contains(lower, "gemini-2.0"):
		return "gemini-2.0-flash"
	// Gemini 1.5 series
	case strings.Contains(lower, "gemini-1.5-pro"):
		return "gemini-1.5-pro"
	case strings.Contains(lower, "gemini-1.5"):
		return "gemini-1.5-flash"
	// Gemini 1.0 series
	case strings.Contains(lower, "gemini-1.0"),
		strings.Contains(lower, "gemini-pro"):
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
// This ensures models proxied through Gemini infrastructure but using different
// tokenizers are handled correctly.
func isGeminiModel(model string) bool {
	lower := strings.ToLower(model)

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
		if strings.Contains(lower, pattern) {
			return false
		}
	}

	// Must contain "gemini" to be considered a Gemini model
	return strings.Contains(lower, "gemini")
}

// buildContentsFromIR converts IR messages to genai.Content slice for token counting.
// Returns the contents and total image count.
func buildContentsFromIR(req *ir.UnifiedChatRequest) ([]*genai.Content, int) {
	if req == nil || len(req.Messages) == 0 {
		return nil, 0
	}

	contents := make([]*genai.Content, 0, len(req.Messages))
	totalImages := 0

	for i := range req.Messages {
		content, imageCount := messageToContent(&req.Messages[i])
		totalImages += imageCount
		if content != nil {
			contents = append(contents, content)
		}
	}

	return contents, totalImages
}

// messageToContent converts a single IR message to genai.Content.
// Returns nil if the message has no countable content.
// Uses pooled slice to reduce allocations.
func messageToContent(msg *ir.Message) (*genai.Content, int) {
	if msg == nil {
		return nil, 0
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

	imageCount := 0

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

		case ir.ContentTypeImage:
			if part.Image != nil {
				imageCount++
			}

		case ir.ContentTypeToolResult:
			if part.ToolResult != nil {
				text := formatFunctionResponse(part.ToolResult.ToolCallID, part.ToolResult.Result)
				parts = append(parts, genai.NewPartFromText(text))
				imageCount += len(part.ToolResult.Images)
			}

		case ir.ContentTypeFile:
			if part.File != nil && part.File.FileData != "" {
				parts = append(parts, genai.NewPartFromText(part.File.FileData))
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
	}

	if len(parts) == 0 {
		return nil, imageCount
	}

	// Copy parts to a new slice (pool slice will be recycled)
	resultParts := make([]*genai.Part, len(parts))
	copy(resultParts, parts)

	return &genai.Content{Role: role, Parts: resultParts}, imageCount
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

	content := &genai.Content{
		Role:  "user",
		Parts: []*genai.Part{genai.NewPartFromText(string(toolsJSON))},
	}

	result, err := tok.CountTokens([]*genai.Content{content}, nil)
	if err != nil {
		return 0
	}

	return int64(result.TotalTokens)
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
