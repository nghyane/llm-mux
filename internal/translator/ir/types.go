package ir

const (
	MetaGoogleSearch          = "google_search"
	MetaGoogleSearchRetrieval = "google_search_retrieval"
	MetaCodeExecution         = "code_execution"
	MetaURLContext            = "url_context"
	MetaGroundingMetadata     = "grounding_metadata"

	MetaOpenAILogprobs         = "openai:logprobs"
	MetaOpenAITopLogprobs      = "openai:top_logprobs"
	MetaOpenAILogitBias        = "openai:logit_bias"
	MetaOpenAISeed             = "openai:seed"
	MetaOpenAIUser             = "openai:user"
	MetaOpenAIFrequencyPenalty = "openai:frequency_penalty"
	MetaOpenAIPresencePenalty  = "openai:presence_penalty"

	MetaGeminiCachedContent = "gemini:cachedContent"
	MetaGeminiLabels        = "gemini:labels"

	MetaClaudeMetadata = "claude:metadata"
)

type EventType string

const (
	EventTypeToken            EventType = "token"
	EventTypeReasoning        EventType = "reasoning"
	EventTypeReasoningSummary EventType = "reasoning_summary"
	EventTypeToolCall         EventType = "tool_call"
	EventTypeToolCallDelta    EventType = "tool_call_delta"
	EventTypeImage            EventType = "image"
	EventTypeCodeExecution    EventType = "code_execution"
	EventTypeError            EventType = "error"
	EventTypeFinish           EventType = "finish"
)

type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"
	FinishReasonLength        FinishReason = "length"
	FinishReasonToolCalls     FinishReason = "tool_calls"
	FinishReasonContentFilter FinishReason = "content_filter"
	FinishReasonError         FinishReason = "error"
	FinishReasonUnknown       FinishReason = "unknown"
)

type UnifiedEvent struct {
	Type              EventType
	Content           string
	Reasoning         string
	ReasoningSummary  string
	ThoughtSignature  string
	ToolCall          *ToolCall
	ToolCallIndex     int
	Image             *ImagePart
	CodeExecution     *CodeExecutionPart
	GroundingMetadata *GroundingMetadata
	Error             error
	Usage             *Usage
	FinishReason      FinishReason // Why generation stopped (for EventTypeFinish)
	Refusal           string       // Refusal message (if model refuses to answer)
	Logprobs          any          // Log probabilities (if requested)
	ContentFilter     any          // Content filter results
	SystemFingerprint string       // System fingerprint
}

type Usage struct {
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
	ThoughtsTokenCount       int // Reasoning/thinking token count (for completion_tokens_details)
	CachedTokens             int // Cached input tokens (Responses API prompt caching)
	AudioTokens              int // Audio input tokens
	AcceptedPredictionTokens int // Accepted prediction tokens
	RejectedPredictionTokens int // Rejected prediction tokens
}

// OpenAIMeta contains metadata from upstream response for passthrough.
// Used to preserve original response fields like responseId, createTime, finishReason.
// This is the unified metadata type used across all providers.
type OpenAIMeta struct {
	ResponseID         string
	CreateTime         int64
	NativeFinishReason string
	ThoughtsTokenCount int
	Logprobs           any
}

// ResponseMeta is an alias for OpenAIMeta for backward compatibility.
// Deprecated: Use OpenAIMeta directly instead.
type ResponseMeta = OpenAIMeta

// CandidateResult holds the result of a single candidate/choice from the model.
// Used when candidateCount/n > 1 to return multiple alternatives.
type CandidateResult struct {
	Index        int          // Candidate index (0-based)
	Messages     []Message    // Messages from this candidate
	FinishReason FinishReason // Why this candidate stopped
	Logprobs     any          // Log probabilities for this candidate (OpenAI format)
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID               string
	Name             string
	Args             string
	PartialArgs      string
	ThoughtSignature string
}

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleTool      Role = "tool"
)

// ContentType defines the type of content part.
type ContentType string

const (
	ContentTypeText           ContentType = "text"
	ContentTypeReasoning      ContentType = "reasoning"
	ContentTypeImage          ContentType = "image"
	ContentTypeFile           ContentType = "file"
	ContentTypeToolResult     ContentType = "tool_result"
	ContentTypeExecutableCode ContentType = "executable_code"
	ContentTypeCodeResult     ContentType = "code_result"
)

// ContentPart represents a discrete part of a message (e.g., a block of text, an image).
type ContentPart struct {
	Type             ContentType
	Text             string
	Reasoning        string
	ThoughtSignature string
	Image            *ImagePart
	File             *FilePart
	ToolResult       *ToolResultPart
	CodeExecution    *CodeExecutionPart
}

type ImagePart struct {
	MimeType string
	Data     string
	URL      string
}

// FilePart represents a file input (PDF, etc.) for Responses API.
type FilePart struct {
	FileID   string
	FileURL  string
	Filename string
	FileData string
}

type ToolResultPart struct {
	ToolCallID string
	Result     string
	Images     []*ImagePart
	Files      []*FilePart
}

// CodeExecutionPart represents Gemini code execution content.
type CodeExecutionPart struct {
	Language string
	Code     string
	Outcome  string
	Output   string
}

// GroundingMetadata contains search grounding information from Gemini.
type GroundingMetadata struct {
	SearchEntryPoint *SearchEntryPoint `json:"searchEntryPoint,omitempty"`
	GroundingChunks  []GroundingChunk  `json:"groundingChunks,omitempty"`
	WebSearchQueries []string          `json:"webSearchQueries,omitempty"`
}

// SearchEntryPoint contains the rendered search entry point HTML.
type SearchEntryPoint struct {
	RenderedContent string `json:"renderedContent,omitempty"`
}

// GroundingChunk represents a single grounding source.
type GroundingChunk struct {
	Web *WebGrounding `json:"web,omitempty"`
}

// WebGrounding contains web source information.
type WebGrounding struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

type Message struct {
	Role      Role
	Content   []ContentPart
	ToolCalls []ToolCall
}

// ToolDefinition represents a tool capability exposed to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// UnifiedChatRequest represents the unified chat request structure.
type UnifiedChatRequest struct {
	Model            string
	Messages         []Message
	Tools            []ToolDefinition
	Temperature      *float64
	TopP             *float64
	TopK             *int
	MaxTokens        *int
	StopSequences    []string
	FrequencyPenalty *float64
	PresencePenalty  *float64
	Logprobs         *bool
	TopLogprobs      *int
	CandidateCount   *int
	Thinking         *ThinkingConfig
	SafetySettings   []SafetySetting // Safety/content filtering settings
	ImageConfig      *ImageConfig    // Image generation configuration
	ResponseModality []string        // Response modalities (e.g., ["TEXT", "IMAGE"])
	Metadata         map[string]any  // Additional provider-specific metadata

	// Responses API specific fields
	Instructions       string // System instructions (Responses API)
	PreviousResponseID string
	PromptID           string         // Prompt template ID (Responses API)
	PromptVersion      string         // Prompt template version (Responses API)
	PromptVariables    map[string]any // Variables for prompt template (Responses API)
	PromptCacheKey     string         // Cache key for prompt caching (Responses API)
	Store              *bool          // Whether to store the response (Responses API)
	ParallelToolCalls  *bool          // Whether to allow parallel tool calls (Responses API)
	ToolChoice         string         // Tool choice mode (Responses API)
	ResponseSchema     map[string]any
	FunctionCalling    *FunctionCallingConfig // Function calling configuration
}

// FunctionCallingConfig controls function calling behavior.
type FunctionCallingConfig struct {
	Mode                        string   // "AUTO", "ANY", "NONE"
	AllowedFunctionNames        []string // Whitelist of functions
	StreamFunctionCallArguments bool     // Enable streaming of arguments (Gemini 3+)
}

// ThinkingConfig controls the reasoning capabilities of the model.
type ThinkingConfig struct {
	IncludeThoughts bool
	Budget          int
	Summary         string // Reasoning summary mode: "auto", "concise", "detailed" (Responses API)
	Effort          string // Reasoning effort: "none", "low", "medium", "high" (Responses API)
}

// SafetySetting represents content safety filtering configuration.
type SafetySetting struct {
	Category  string
	Threshold string
}

// ImageConfig controls image generation parameters.
type ImageConfig struct {
	AspectRatio string
	ImageSize   string
}
