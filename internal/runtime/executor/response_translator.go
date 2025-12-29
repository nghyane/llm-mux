// Package executor provides request/response translation between API formats.
//
// Non-streaming response translation architecture:
//   - ResponseTranslator: Unified IR-to-format conversion (mirrors StreamTranslator)
//   - TranslateResponseNonStream: Single entry point for all response translations
package executor

import (
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	"github.com/tidwall/gjson"
)

// =============================================================================
// Response Translator - Unified Non-Streaming Translation
// =============================================================================

// ResponseTranslator handles unified IR-to-format conversion for non-streaming responses.
// This mirrors StreamTranslator architecture for consistency.
type ResponseTranslator struct {
	cfg       *config.Config
	to        string
	model     string
	messageID string
}

// NewResponseTranslator creates a translator for non-streaming responses.
func NewResponseTranslator(cfg *config.Config, to, model string) *ResponseTranslator {
	return &ResponseTranslator{
		cfg:       cfg,
		to:        to,
		model:     model,
		messageID: generateMessageID(to, model),
	}
}

// generateMessageID creates format-appropriate message ID.
func generateMessageID(to, model string) string {
	switch to {
	case "codex", "openai-response":
		return "resp-" + model
	case "claude":
		return "msg-" + model
	default:
		return "chatcmpl-" + model
	}
}

// Translate converts IR messages + usage to target format.
func (t *ResponseTranslator) Translate(messages []ir.Message, usage *ir.Usage, meta *ir.OpenAIMeta) ([]byte, error) {
	switch t.to {
	case "openai", "cline":
		if meta != nil {
			return from_ir.ToOpenAIChatCompletionMeta(messages, usage, t.model, t.messageID, meta)
		}
		return from_ir.ToOpenAIChatCompletion(messages, usage, t.model, t.messageID)
	case "claude":
		return from_ir.ToClaudeResponse(messages, usage, t.model, t.messageID)
	case "ollama":
		return from_ir.ToOllamaChatResponse(messages, usage, t.model)
	case "gemini", "gemini-cli":
		return from_ir.ToGeminiResponse(messages, usage, t.model)
	case "codex", "openai-response":
		return from_ir.ToResponsesAPIResponse(messages, usage, t.model, meta)
	default:
		return nil, nil
	}
}

// =============================================================================
// Source Format Parsers
// =============================================================================

// ParsedResponse contains parsed IR data from source format.
type ParsedResponse struct {
	Messages []ir.Message
	Usage    *ir.Usage
	Meta     *ir.OpenAIMeta
}

// parseOpenAIResponse parses OpenAI/Codex format to IR.
func parseOpenAIResponse(response []byte) (*ParsedResponse, error) {
	messages, usage, err := to_ir.ParseOpenAIResponse(response)
	if err != nil {
		return nil, err
	}
	return &ParsedResponse{Messages: messages, Usage: usage}, nil
}

// parseClaudeResponse parses Claude format to IR.
func parseClaudeResponse(response []byte) (*ParsedResponse, error) {
	messages, usage, err := to_ir.ParseClaudeResponse(response)
	if err != nil {
		return nil, err
	}
	return &ParsedResponse{Messages: messages, Usage: usage}, nil
}

// parseGeminiResponse parses Gemini format to IR with metadata.
func parseGeminiResponse(response []byte) (*ParsedResponse, error) {
	messages, usage, meta, err := to_ir.ParseGeminiResponseMeta(response)
	if err != nil {
		return nil, err
	}
	return &ParsedResponse{Messages: messages, Usage: usage, Meta: meta}, nil
}

// parseGeminiCLIResponse parses Gemini CLI format to IR with metadata.
func parseGeminiCLIResponse(response []byte) (*ParsedResponse, error) {
	messages, usage, meta, err := to_ir.ParseGeminiResponseMetaWithContext(response, nil)
	if err != nil {
		return nil, err
	}
	return &ParsedResponse{Messages: messages, Usage: usage, Meta: meta}, nil
}

// =============================================================================
// Unified Entry Points
// =============================================================================

// TranslateResponseNonStream is the unified entry point for non-streaming response translation.
// It replaces the 5 separate Translate*ResponseNonStream functions.
//
// Parameters:
//   - from: Source format (openai, claude, gemini, gemini-cli, codex)
//   - to: Target format
//   - response: Raw response bytes
//   - model: Model name for response metadata
func TranslateResponseNonStream(cfg *config.Config, from, to sdktranslator.Format, response []byte, model string) ([]byte, error) {
	fromStr := from.String()
	toStr := to.String()

	// Handle passthrough cases
	if passthrough := handlePassthrough(fromStr, toStr, response); passthrough != nil {
		return passthrough, nil
	}

	// Handle Gemini multi-candidate case (special OpenAI output)
	if (fromStr == "gemini" || fromStr == "gemini-cli") && (toStr == "openai" || toStr == "cline") {
		if hasMultipleCandidates(response) {
			return translateGeminiCandidates(response, model)
		}
	}

	// Parse source format to IR
	parsed, err := parseSourceResponse(fromStr, response)
	if err != nil {
		return nil, err
	}

	// Convert IR to target format
	translator := NewResponseTranslator(cfg, toStr, model)

	// Update messageID from meta if available
	if parsed.Meta != nil && parsed.Meta.ResponseID != "" {
		translator.messageID = parsed.Meta.ResponseID
	}

	return translator.Translate(parsed.Messages, parsed.Usage, parsed.Meta)
}

// handlePassthrough returns response bytes if passthrough is needed, nil otherwise.
func handlePassthrough(from, to string, response []byte) []byte {
	switch {
	// Same format passthrough
	case from == to:
		return response

	// Codex/OpenAI-response passthrough
	case (to == "codex" || to == "openai-response") && (from == "codex" || from == "openai-response"):
		return response

	// Gemini format passthrough (with envelope unwrap)
	case (to == "gemini" || to == "gemini-cli") && (from == "gemini" || from == "gemini-cli"):
		return unwrapGeminiEnvelope(response)

	// Claude passthrough
	case to == "claude" && from == "claude":
		return response
	}

	return nil
}

// unwrapGeminiEnvelope unwraps Antigravity envelope if present.
func unwrapGeminiEnvelope(response []byte) []byte {
	if responseWrapper := gjson.GetBytes(response, "response"); responseWrapper.Exists() {
		return []byte(responseWrapper.Raw)
	}
	return response
}

// parseSourceResponse parses response based on source format.
func parseSourceResponse(from string, response []byte) (*ParsedResponse, error) {
	switch from {
	case "openai", "cline":
		return parseOpenAIResponse(response)
	case "codex", "openai-response":
		return parseOpenAIResponse(response)
	case "claude":
		return parseClaudeResponse(response)
	case "gemini":
		return parseGeminiResponse(response)
	case "gemini-cli":
		return parseGeminiCLIResponse(response)
	default:
		return nil, nil
	}
}

// translateGeminiCandidates handles Gemini multi-candidate responses for OpenAI format.
func translateGeminiCandidates(response []byte, model string) ([]byte, error) {
	candidates, usage, meta, err := to_ir.ParseGeminiResponseCandidates(response, nil)
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
