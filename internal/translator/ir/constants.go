package ir

// Magic string constants used throughout the translator.
// These centralize commonly used string literals to improve maintainability
// and reduce the risk of typos in provider-specific mappings.

// Finish Reason Constants
// Provider-specific finish reasons that map to IR FinishReason types
const (
	// OpenAI finish reasons
	OpenAIFinishReasonStop          = "stop"
	OpenAIFinishReasonLength        = "length"
	OpenAIFinishReasonToolCalls     = "tool_calls"
	OpenAIFinishReasonContentFilter = "content_filter"

	// Claude finish reasons
	ClaudeFinishReasonEndTurn      = "end_turn"
	ClaudeFinishReasonStopSequence = "stop_sequence"
	ClaudeFinishReasonToolUse      = "tool_use"

	// Gemini finish reasons
	GeminiFinishReasonSTOP               = "STOP"
	GeminiFinishReasonMAX_TOKENS         = "MAX_TOKENS"
	GeminiFinishReasonSAFETY             = "SAFETY"
	GeminiFinishReasonRECITATION         = "RECITATION"
	GeminiFinishReasonOTHER              = "OTHER"
	GeminiFinishReasonBLOCKLIST          = "BLOCKLIST"
	GeminiFinishReasonPROHIBITED_CONTENT = "PROHIBITED_CONTENT"
	GeminiFinishReasonSPII               = "SPII"
	GeminiFinishReasonIMAGE_SAFETY       = "IMAGE_SAFETY"
)

// Safety Category Constants
// Gemini/Vertex AI safety categories
const (
	SafetyCategoryHarassment       = "HARM_CATEGORY_HARASSMENT"
	SafetyCategoryHateSpeech       = "HARM_CATEGORY_HATE_SPEECH"
	SafetyCategorySexuallyExplicit = "HARM_CATEGORY_SEXUALLY_EXPLICIT"
	SafetyCategoryDangerousContent = "HARM_CATEGORY_DANGEROUS_CONTENT"
	SafetyCategoryCivicIntegrity   = "HARM_CATEGORY_CIVIC_INTEGRITY"
)

// Safety Threshold Constants
// Gemini/Vertex AI safety thresholds
const (
	SafetyThresholdOff                 = "OFF"
	SafetyThresholdBlockNone           = "BLOCK_NONE"
	SafetyThresholdBlockLowAndAbove    = "BLOCK_LOW_AND_ABOVE"
	SafetyThresholdBlockMediumAndAbove = "BLOCK_MEDIUM_AND_ABOVE"
	SafetyThresholdBlockOnlyHigh       = "BLOCK_ONLY_HIGH"
)

// Safety Probability Constants
// Gemini safety rating probabilities
const (
	SafetyProbabilityNegligible = "NEGLIGIBLE"
	SafetyProbabilityLow        = "LOW"
	SafetyProbabilityMedium     = "MEDIUM"
	SafetyProbabilityHigh       = "HIGH"
)

// Tool Choice Constants
// Tool choice modes supported across providers
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceAny      = "any"
	ToolChoiceFunction = "function"
)

// Tool/Function Type Constants
// Types used in tool definitions and calls
const (
	ToolTypeFunction     = "function"
	ToolTypeFunctionCall = "function_call"
	ToolTypeToolUse      = "tool_use"
)

// Content Source Types
// Types for content sources (images, files, etc.)
const (
	ContentSourceTypeBase64 = "base64"
	ContentSourceTypeURL    = "url"
	ContentSourceTypeFile   = "file"
)

// SSE Event Types
// Server-sent event types used by providers
const (
	SSETypeMessageStart      = "message_start"
	SSETypeContentBlockStart = "content_block_start"
	SSETypeContentBlockDelta = "content_block_delta"
	SSETypeContentBlockStop  = "content_block_stop"
	SSETypeMessageDelta      = "message_delta"
	SSETypeMessageStop       = "message_stop"
	SSETypeError             = "error"
)

// Delta Types
// Delta types for streaming content
const (
	DeltaTypeTextDelta        = "text_delta"
	DeltaTypeThinkingDelta    = "thinking_delta"
	DeltaTypeSignatureDelta   = "signature_delta"
	DeltaTypeInputJSONDelta   = "input_json_delta"
	DeltaTypeRedactedThinking = "redacted_thinking_delta"
)

// Tool Status Constants
// Status values for tool call execution
const (
	ToolStatusCompleted  = "completed"
	ToolStatusInProgress = "in_progress"
)

// Response Modality Constants
// Response modalities for multimodal models
const (
	ResponseModalityText  = "TEXT"
	ResponseModalityImage = "IMAGE"
	ResponseModalityAudio = "AUDIO"
)
