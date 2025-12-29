package ir

const (
	MetaGoogleSearch          = "google_search"
	MetaGoogleSearchRetrieval = "google_search_retrieval"
	MetaCodeExecution         = "code_execution"
	MetaURLContext            = "url_context"
	MetaFileSearch            = "file_search"
	MetaGroundingMetadata     = "grounding_metadata"

	// Claude built-in tools (stored for passthrough)
	MetaClaudeComputer   = "claude:computer"
	MetaClaudeBash       = "claude:bash"
	MetaClaudeTextEditor = "claude:text_editor"
	MetaClaudeMCP        = "claude:mcp" // MCP (Model Context Protocol) servers

	// MCP tool metadata keys
	MetaMCPServers = "mcp_servers" // MCP server configurations in request

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

	// Internal flags (prefixed with _ to indicate internal use)
	MetaForceDisableThinking = "_force_disable_thinking" // Set by translator_wrapper for non-streaming Claude via Antigravity
)

type EventType string

const (
	EventTypeStreamMeta       EventType = "stream_meta"
	EventTypeToken            EventType = "token"
	EventTypeReasoning        EventType = "reasoning"
	EventTypeReasoningSummary EventType = "reasoning_summary"
	EventTypeToolCall         EventType = "tool_call"
	EventTypeToolCallDelta    EventType = "tool_call_delta"
	EventTypeImage            EventType = "image"
	EventTypeAudio            EventType = "audio"
	EventTypeCodeExecution    EventType = "code_execution"
	EventTypeError            EventType = "error"
	EventTypeFinish           EventType = "finish"
)

type FinishReason string

// Unified/normalized finish reasons (used in IR)
// These are the canonical reasons used internally. Provider-specific reasons
// are mapped to/from these in the mapping functions (MapXxxFinishReason, MapFinishReasonToXxx).
// Consolidation notes:
// - FinishReasonMaxTokens: Used for both OpenAI "length" and Claude/Gemini "max_tokens"
// - FinishReasonStop: Used for both normal completion and Claude "end_turn"
// - FinishReasonStopSequence: Specifically for stop sequence triggers (Claude distinction)
const (
	FinishReasonStop          FinishReason = "stop"           // Normal completion (OpenAI "stop", Claude "end_turn", Gemini "STOP")
	FinishReasonMaxTokens     FinishReason = "max_tokens"     // Token limit reached (OpenAI "length", Claude/Gemini "max_tokens")
	FinishReasonToolCalls     FinishReason = "tool_calls"     // Model wants to call tools (OpenAI "tool_calls", Claude "tool_use")
	FinishReasonContentFilter FinishReason = "content_filter" // Content was filtered (OpenAI "content_filter", Gemini "SAFETY")
	FinishReasonStopSequence  FinishReason = "stop_sequence"  // Stop sequence matched (Claude-specific, maps to "stop" for OpenAI)
	FinishReasonError         FinishReason = "error"          // Error occurred
	FinishReasonUnknown       FinishReason = "unknown"        // Unknown/fallback
	// Add new Gemini 2025 values:
	FinishReasonBlocklist         FinishReason = "blocklist"          // Content matched blocklist
	FinishReasonProhibitedContent FinishReason = "prohibited_content" // Prohibited content detected
	FinishReasonSPII              FinishReason = "spii"               // Sensitive PII detected
	FinishReasonImageSafety       FinishReason = "image_safety"       // Image safety issue
	FinishReasonRecitation        FinishReason = "recitation"         // Recitation/copyright issue
)

// ThinkingLevel represents the level of thinking tokens for thinking models.
// Matches Google Gen AI SDK ThinkingLevel type.
type ThinkingLevel string

const (
	ThinkingLevelUnspecified ThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	ThinkingLevelMinimal     ThinkingLevel = "MINIMAL" // Gemini 3 Flash only
	ThinkingLevelLow         ThinkingLevel = "LOW"
	ThinkingLevelMedium      ThinkingLevel = "MEDIUM" // Gemini 3 Flash only
	ThinkingLevelHigh        ThinkingLevel = "HIGH"
	ThinkingLevelOff         ThinkingLevel = "OFF" // Add this - explicitly disable thinking
)

// ReasoningEffort represents the effort level for reasoning/thinking modes.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// ServiceTier represents the service tier for API requests.
type ServiceTier string

const (
	ServiceTierAuto     ServiceTier = "auto"
	ServiceTierDefault  ServiceTier = "default"
	ServiceTierFlex     ServiceTier = "flex"
	ServiceTierScale    ServiceTier = "scale"
	ServiceTierPriority ServiceTier = "priority"
)

// Language represents the programming language for code execution.
// Matches Google Gen AI SDK Language type.
type Language string

const (
	LanguageUnspecified Language = "LANGUAGE_UNSPECIFIED"
	LanguagePython      Language = "PYTHON"
)

// Outcome represents the outcome of code execution.
// Matches Google Gen AI SDK Outcome type.
type Outcome string

const (
	OutcomeUnspecified      Outcome = "OUTCOME_UNSPECIFIED"
	OutcomeOK               Outcome = "OUTCOME_OK"
	OutcomeFailed           Outcome = "OUTCOME_FAILED"
	OutcomeDeadlineExceeded Outcome = "OUTCOME_DEADLINE_EXCEEDED"
)

type StreamMeta struct {
	MessageID            string
	Model                string
	EstimatedInputTokens int64
}

type UnifiedEvent struct {
	Type              EventType
	Content           string
	Reasoning         string
	ReasoningSummary  string
	ThoughtSignature  []byte
	ToolCall          *ToolCall
	ToolCallIndex     int
	Image             *ImagePart
	Audio             *AudioPart
	CodeExecution     *CodeExecutionPart
	GroundingMetadata *GroundingMetadata
	StreamMeta        *StreamMeta
	Error             error
	Usage             *Usage
	FinishReason      FinishReason
	Refusal           string
	Logprobs          any
	ContentFilter     any
	SystemFingerprint string
	RedactedData      string
}

type Usage struct {
	PromptTokens             int64
	CompletionTokens         int64
	TotalTokens              int64
	ThoughtsTokenCount       int32
	CachedTokens             int64
	AudioTokens              int64
	AcceptedPredictionTokens int64
	RejectedPredictionTokens int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ToolUsePromptTokens      int64 // Gemini: tokens used for tool/function call context
	PromptTokensDetails      *PromptTokensDetails
	CompletionTokensDetails  *CompletionTokensDetails
}

type PromptTokensDetails struct {
	CachedTokens int64
	AudioTokens  int64
}

type CompletionTokensDetails struct {
	ReasoningTokens          int64
	AudioTokens              int64
	AcceptedPredictionTokens int64
	RejectedPredictionTokens int64
}

// OpenAIMeta contains metadata from upstream response for passthrough.
// Used to preserve original response fields like responseId, createTime, finishReason.
// This is the unified metadata type used across all providers.
type OpenAIMeta struct {
	ResponseID         string
	CreateTime         int64
	NativeFinishReason string
	ThoughtsTokenCount int32 // Matches SDK int32
	Logprobs           any
	GroundingMetadata  *GroundingMetadata // Google Search grounding metadata
	PromptFeedback     *PromptFeedback    // Prompt-level safety feedback
	ServiceTier        string             // OpenAI service tier used for the request
}

// SafetyRating represents content safety evaluation
type SafetyRating struct {
	Category    string // HarmCategory enum value
	Probability string // NEGLIGIBLE, LOW, MEDIUM, HIGH
	Blocked     bool   // Whether this category blocked generation
	Severity    string // HarmSeverity (Vertex AI only)
}

// PromptFeedback contains prompt-level safety feedback
type PromptFeedback struct {
	BlockReason   string          // BlockReason enum
	SafetyRatings []*SafetyRating // Ratings that caused block
}

// CandidateResult holds the result of a single candidate/choice from the model.
// Used when candidateCount/n > 1 to return multiple alternatives.
type CandidateResult struct {
	Index             int                // Candidate index (0-based)
	Messages          []Message          // Messages from this candidate
	FinishReason      FinishReason       // Why this candidate stopped
	Logprobs          any                // Log probabilities for this candidate (OpenAI format)
	GroundingMetadata *GroundingMetadata // Google Search grounding metadata for this candidate
	SafetyRatings     []*SafetyRating    // Safety evaluation results
}

// ToolCall represents a request from the model to execute a tool.
type ToolCall struct {
	ID               string
	Name             string
	Args             string
	PartialArgs      string
	ThoughtSignature []byte // Opaque signature for thought reuse (matches SDK []byte)
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
	ContentTypeText             ContentType = "text"
	ContentTypeReasoning        ContentType = "reasoning"
	ContentTypeImage            ContentType = "image"
	ContentTypeFile             ContentType = "file"
	ContentTypeAudio            ContentType = "audio" // Audio content (OpenAI audio preview, Gemini Live)
	ContentTypeVideo            ContentType = "video" // Video content (Gemini multimodal)
	ContentTypeToolResult       ContentType = "tool_result"
	ContentTypeExecutableCode   ContentType = "executable_code"
	ContentTypeCodeResult       ContentType = "code_result"
	ContentTypeRedactedThinking ContentType = "redacted_thinking"
)

// ContentPart represents a discrete part of a message (e.g., a block of text, an image).
type ContentPart struct {
	Type             ContentType
	Text             string
	Reasoning        string
	ThoughtSignature []byte // Opaque signature for thought reuse (matches SDK []byte)
	Image            *ImagePart
	File             *FilePart
	Audio            *AudioPart // Audio content (OpenAI/Gemini)
	Video            *VideoPart // Video content (Gemini)
	ToolResult       *ToolResultPart
	CodeExecution    *CodeExecutionPart
	RedactedData     string          // Encrypted data for redacted_thinking (must round-trip exactly)
	Citations        []*TextCitation // Citations for text content (Claude)
}

type ImagePart struct {
	MimeType string
	Data     string // Base64-encoded image data
	URL      string // URL to image
	FileID   string // File ID for Claude Files API
	Detail   string // Vision quality control: auto, low, high
}

// FilePart represents a file input (PDF, etc.) for Responses API.
type FilePart struct {
	FileID   string
	FileURL  string
	Filename string
	FileData string
	MimeType string // MIME type (e.g., "application/pdf", "text/plain")
}

// AudioPart represents audio content for OpenAI audio preview and Gemini Live API.
type AudioPart struct {
	Data       string // Base64-encoded audio data
	FileURI    string // Uploaded file URI reference (Gemini Files API)
	Format     string // Audio format: "wav", "mp3", "pcm", "opus", "flac", "aac"
	MimeType   string // MIME type (e.g., "audio/wav", "audio/mpeg", "audio/pcm")
	Transcript string // Optional transcription of the audio
	ID         string // Audio ID for response audio (OpenAI)
	ExpiresAt  int64  // Expiration timestamp for response audio (OpenAI)
}

// VideoPart represents video content for Gemini multimodal API.
type VideoPart struct {
	Data     string  // Base64-encoded video data
	FileURI  string  // Uploaded file URI reference (Gemini Files API)
	MimeType string  // MIME type (e.g., "video/mp4", "video/webm")
	Duration float64 // Optional duration in seconds
}

type ToolResultPart struct {
	ToolCallID string
	Result     string
	IsError    bool // Tool execution failed (Claude is_error field)
	Images     []*ImagePart
	Files      []*FilePart
}

// CodeExecutionPart represents Gemini code execution content.
// Language and Outcome use SDK enum types.
type CodeExecutionPart struct {
	Language Language // Programming language (matches SDK Language enum)
	Code     string
	Outcome  Outcome // Execution outcome (matches SDK Outcome enum)
	Output   string
}

// GroundingMetadata contains search grounding information from Gemini.
// Matches Google Gen AI SDK GroundingMetadata structure.
type GroundingMetadata struct {
	SearchEntryPoint  *SearchEntryPoint   `json:"searchEntryPoint,omitempty"`
	GroundingChunks   []*GroundingChunk   `json:"groundingChunks,omitempty"`   // Pointer slice per SDK
	GroundingSupports []*GroundingSupport `json:"groundingSupports,omitempty"` // Pointer slice per SDK
	WebSearchQueries  []string            `json:"webSearchQueries,omitempty"`
	RetrievalQueries  []string            `json:"retrievalQueries,omitempty"`  // SDK field
	RetrievalMetadata *RetrievalMetadata  `json:"retrievalMetadata,omitempty"` // SDK field
	CitationMetadata  *CitationMetadata   `json:"citationMetadata,omitempty"`  // Citation information
}

// SearchEntryPoint contains the rendered search entry point.
// Matches Google Gen AI SDK SearchEntryPoint structure.
type SearchEntryPoint struct {
	RenderedContent string `json:"renderedContent,omitempty"`
	SDKBlob         []byte `json:"sdkBlob,omitempty"` // Binary blob per SDK
}

// RetrievalMetadata contains metadata about retrieval operations.
type RetrievalMetadata struct {
	GoogleSearchDynamicRetrievalScore float64 `json:"googleSearchDynamicRetrievalScore,omitempty"`
}

// GroundingChunk represents a single grounding source.
// Matches Google Gen AI SDK GroundingChunk structure.
type GroundingChunk struct {
	Web              *WebGrounding              `json:"web,omitempty"`
	RetrievedContext *RetrievedContextGrounding `json:"retrievedContext,omitempty"` // SDK field
}

// WebGrounding contains web source information.
type WebGrounding struct {
	URI    string `json:"uri,omitempty"`
	Title  string `json:"title,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// RetrievedContextGrounding contains retrieval context information.
type RetrievedContextGrounding struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

// GroundingSupport links response segments to grounding sources.
// Matches Google Gen AI SDK GroundingSupport structure.
type GroundingSupport struct {
	Segment               *GroundingSegment `json:"segment,omitempty"`
	GroundingChunkIndices []int32           `json:"groundingChunkIndices,omitempty"` // int32 per SDK
	ConfidenceScores      []float32         `json:"confidenceScores,omitempty"`      // SDK field
}

// GroundingSegment identifies a portion of the response text.
// Matches Google Gen AI SDK Segment structure.
type GroundingSegment struct {
	StartIndex int32  `json:"startIndex,omitempty"` // int32 per SDK
	EndIndex   int32  `json:"endIndex,omitempty"`   // int32 per SDK
	PartIndex  int32  `json:"partIndex,omitempty"`  // SDK field
	Text       string `json:"text,omitempty"`
}

// TextCitation represents a document citation in text content.
// Supports all Claude citation types: char_location, page_location,
// content_block_location, web_search_result_location, search_result_location
type TextCitation struct {
	Type           string // Citation type: "char_location", "page_location", "content_block_location", "web_search_result_location", "search_result_location"
	DocumentIndex  int    // Index into documents array (char_location, page_location, content_block_location)
	StartCharIndex int    // Start character position (char_location)
	EndCharIndex   int    // End character position (char_location)
	URL            string // For URL citations
	Title          string // Document title

	// Extended fields for full Claude citation support
	FileID          string // Document file ID (char_location, page_location, content_block_location)
	CitedText       string // The actual cited text (all types)
	DocumentTitle   string // Title of the cited document (char_location, page_location, content_block_location)
	StartPageNumber int    // Start page (page_location)
	EndPageNumber   int    // End page (page_location)
	StartBlockIndex int    // Start block index (content_block_location, search_result_location)
	EndBlockIndex   int    // End block index (content_block_location, search_result_location)

	// Web/Search citation fields
	EncryptedIndex    string // Encrypted index (web_search_result_location)
	SearchResultIndex int    // Search result index (search_result_location)
	Source            string // Source identifier (search_result_location)
}

// CitationMetadata contains citation information for generated content.
// Matches Google Gen AI SDK CitationMetadata structure.
type CitationMetadata struct {
	Citations []*Citation `json:"citations,omitempty"`
}

// Citation represents a single citation source.
type Citation struct {
	StartIndex      int32  `json:"startIndex,omitempty"`
	EndIndex        int32  `json:"endIndex,omitempty"`
	URI             string `json:"uri,omitempty"`
	Title           string `json:"title,omitempty"`
	License         string `json:"license,omitempty"`
	PublicationDate string `json:"publicationDate,omitempty"` // ISO date string
}

// URLContextMetadata contains URL context information.
// Matches Google Gen AI SDK URLContextMetadata structure.
type URLContextMetadata struct {
	URLMetadata []*URLMetadata `json:"urlMetadata,omitempty"`
}

// URLMetadata contains metadata about a URL.
type URLMetadata struct {
	RetrievedURL string `json:"retrievedUrl,omitempty"`
	URLCategory  string `json:"urlCategory,omitempty"`
}

// CacheControl specifies caching behavior for request content.
type CacheControl struct {
	Type string
	TTL  *int64
}

type Message struct {
	Role         Role
	Content      []ContentPart
	ToolCalls    []ToolCall
	CacheControl *CacheControl
	Refusal      string
}

// ToolDefinition represents a tool capability exposed to the model.
type ToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// UnifiedChatRequest represents the unified chat request structure.
type PredictionConfig struct {
	Type    string // "content"
	Content string // Predicted content for speculative decoding
}

type StreamOptionsConfig struct {
	IncludeUsage bool // Include usage in final streaming chunk
}

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
	AudioConfig      *AudioConfig    // Audio input/output configuration (OpenAI)
	MCPServers       []MCPServer     // MCP server configurations (Claude)
	ResponseModality []string        // Response modalities (e.g., ["TEXT", "IMAGE", "AUDIO"])
	Metadata         map[string]any  // Additional provider-specific metadata
	ServiceTier      ServiceTier

	// Responses API specific fields
	Instructions         string // System instructions (Responses API)
	PreviousResponseID   string
	PromptID             string         // Prompt template ID (Responses API)
	PromptVersion        string         // Prompt template version (Responses API)
	PromptVariables      map[string]any // Variables for prompt template (Responses API)
	PromptCacheKey       string         // Cache key for prompt caching (Responses API)
	Store                *bool          // Whether to store the response (Responses API)
	ParallelToolCalls    *bool          // Whether to allow parallel tool calls (Responses API)
	ToolChoice           string         // Tool choice mode: "auto", "none", "required", "any"
	ToolChoiceFunction   string         // Specific function name when tool_choice is object format
	AllowedTools         []string       // GPT-5+: Subset of tools the model can use (allowed_tools)
	ResponseSchema       map[string]any
	ResponseSchemaName   string
	ResponseSchemaStrict bool                   `json:"response_schema_strict,omitempty"`
	FunctionCalling      *FunctionCallingConfig // Function calling configuration

	// OpenAI high priority features
	Prediction    *PredictionConfig    // Predicted output for speculative decoding
	StreamOptions *StreamOptionsConfig // Stream configuration options
}

// FunctionCallingConfig controls function calling behavior.
type FunctionCallingConfig struct {
	Mode                        string   // "AUTO", "ANY", "NONE"
	AllowedFunctionNames        []string // Whitelist of functions
	StreamFunctionCallArguments bool     // Enable streaming of arguments (Gemini 3+)
}

// ThinkingConfig controls the reasoning capabilities of the model.
// Matches Google Gen AI SDK ThinkingConfig structure.
type ThinkingConfig struct {
	IncludeThoughts bool            // Whether to include thoughts in response
	ThinkingBudget  *int32          // Budget in tokens (pointer per SDK)
	ThinkingLevel   ThinkingLevel   // Level of thinking (SDK enum)
	Summary         string          // Reasoning summary mode: "auto", "concise", "detailed"
	Effort          ReasoningEffort // Reasoning effort level
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

// AudioConfig controls audio input/output parameters (OpenAI audio preview).
type AudioConfig struct {
	Voice  string // Output voice: "alloy", "echo", "fable", "onyx", "nova", "shimmer"
	Format string // Output format: "wav", "mp3", "opus", "aac", "flac", "pcm"
}

// MCPServer represents an MCP (Model Context Protocol) server configuration.
type MCPServer struct {
	Type               string         // Server type (e.g., "url")
	URL                string         // MCP server URL (SSE endpoint)
	Name               string         // Unique server identifier
	AuthorizationToken string         // Optional auth token
	ToolConfiguration  map[string]any // Tool access configuration
	Metadata           map[string]any // Additional metadata
}
