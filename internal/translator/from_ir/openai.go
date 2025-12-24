// Package from_ir converts unified request format to provider-specific formats.
package from_ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// OpenAIRequestFormat specifies which OpenAI API format to generate.
type OpenAIRequestFormat int

const (
	FormatChatCompletions OpenAIRequestFormat = iota // /v1/chat/completions - uses "messages"
	FormatResponsesAPI                               // /v1/responses - uses "input"
)

// ToOpenAIRequest converts unified request to OpenAI Chat Completions API JSON (default format).
func ToOpenAIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	return ToOpenAIRequestFmt(req, FormatChatCompletions)
}

// ToOpenAIRequestFmt converts unified request to specified OpenAI API format.
// Use FormatChatCompletions for traditional /v1/chat/completions endpoint.
// Use FormatResponsesAPI for new /v1/responses endpoint (Codex CLI, etc.).
func ToOpenAIRequestFmt(req *ir.UnifiedChatRequest, format OpenAIRequestFormat) ([]byte, error) {
	if format == FormatResponsesAPI {
		return convertToResponsesAPIRequest(req)
	}
	return convertToChatCompletionsRequest(req)
}

// convertToChatCompletionsRequest builds JSON for /v1/chat/completions endpoint.
// This is the traditional OpenAI format used by most clients (Cursor, Cline, etc.).
func convertToChatCompletionsRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{
		"model":    req.Model,
		"messages": []any{},
	}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		m["stop"] = req.StopSequences
	}
	if req.Thinking != nil && req.Thinking.IncludeThoughts {
		budget := 0
		if req.Thinking.ThinkingBudget != nil {
			budget = int(*req.Thinking.ThinkingBudget)
		}
		m["reasoning_effort"] = ir.BudgetToEffort(budget, "auto")
	}

	var messages []any
	for _, msg := range req.Messages {
		if msgObj := convertMessageToOpenAI(msg); msgObj != nil {
			messages = append(messages, msgObj)
		}
	}
	m["messages"] = messages

	// Build tools array (function tools + built-in tools from metadata)
	var tools []any
	for _, t := range req.Tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": params,
			},
		})
	}

	// Add built-in tools from Metadata
	if req.Metadata != nil {
		// web_search tool
		if gsConfig, ok := req.Metadata[ir.MetaGoogleSearch]; ok {
			webSearchTool := map[string]any{"type": "web_search_preview"}
			if cfg, ok := gsConfig.(map[string]any); ok {
				if scs, ok := cfg["search_context_size"]; ok {
					webSearchTool["search_context_size"] = scs
				}
				if ul, ok := cfg["user_location"]; ok {
					webSearchTool["user_location"] = ul
				}
			}
			tools = append(tools, webSearchTool)
		}

		// code_interpreter tool
		if ciConfig, ok := req.Metadata[ir.MetaCodeExecution]; ok {
			codeInterpreterTool := map[string]any{"type": "code_interpreter"}
			if cfg, ok := ciConfig.(map[string]any); ok {
				if container, ok := cfg["container"]; ok {
					codeInterpreterTool["container"] = container
				}
			}
			tools = append(tools, codeInterpreterTool)
		}

		// file_search tool
		if fsConfig, ok := req.Metadata[ir.MetaFileSearch]; ok {
			fileSearchTool := map[string]any{"type": "file_search"}
			if cfg, ok := fsConfig.(map[string]any); ok {
				if vs, ok := cfg["vector_store"]; ok {
					fileSearchTool["vector_store"] = vs
				}
				if mnr, ok := cfg["max_num_results"]; ok {
					fileSearchTool["max_num_results"] = mnr
				}
				if ro, ok := cfg["ranking_options"]; ok {
					fileSearchTool["ranking_options"] = ro
				}
			}
			tools = append(tools, fileSearchTool)
		}
	}

	if len(tools) > 0 {
		m["tools"] = tools
	}

	if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.ResponseModality) > 0 {
		m["modalities"] = req.ResponseModality
	}

	// Add audio config for OpenAI audio models (gpt-4o-audio-preview)
	if req.AudioConfig != nil {
		audioConfig := map[string]any{}
		if req.AudioConfig.Voice != "" {
			audioConfig["voice"] = req.AudioConfig.Voice
		}
		if req.AudioConfig.Format != "" {
			audioConfig["format"] = req.AudioConfig.Format
		}
		if len(audioConfig) > 0 {
			m["audio"] = audioConfig
		}
	}

	// Restore OpenAI-specific fields from Metadata (passthrough)
	if req.Metadata != nil {
		if v, ok := req.Metadata[ir.MetaOpenAILogprobs]; ok {
			m["logprobs"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAITopLogprobs]; ok {
			m["top_logprobs"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAILogitBias]; ok {
			m["logit_bias"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAISeed]; ok {
			m["seed"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAIUser]; ok {
			m["user"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAIFrequencyPenalty]; ok {
			m["frequency_penalty"] = v
		}
		if v, ok := req.Metadata[ir.MetaOpenAIPresencePenalty]; ok {
			m["presence_penalty"] = v
		}
	}

	// Add service_tier if present (OpenAI-specific)
	if req.ServiceTier != "" {
		m["service_tier"] = string(req.ServiceTier)
	}

	return json.Marshal(m)
}

// convertToResponsesAPIRequest builds JSON for /v1/responses endpoint.
// This is the new OpenAI format used by Codex CLI and newer clients.
// Key differences: uses "input" instead of "messages", "max_output_tokens" instead of "max_tokens".
func convertToResponsesAPIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{"model": req.Model}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_output_tokens"] = *req.MaxTokens
	}
	if req.Instructions != "" {
		m["instructions"] = req.Instructions
	}

	// Add audio config for OpenAI audio models in Responses API
	if req.AudioConfig != nil {
		audioConfig := map[string]any{}
		if req.AudioConfig.Voice != "" {
			audioConfig["voice"] = req.AudioConfig.Voice
		}
		if req.AudioConfig.Format != "" {
			audioConfig["format"] = req.AudioConfig.Format
		}
		if len(audioConfig) > 0 {
			m["audio"] = audioConfig
		}
	}

	// Add modalities for Responses API
	if len(req.ResponseModality) > 0 {
		m["modalities"] = req.ResponseModality
	}

	var input []any
	for _, msg := range req.Messages {
		if msg.Role == ir.RoleSystem && req.Instructions != "" {
			continue
		}
		if item := convertMessageToResponsesInput(msg); item != nil {
			input = append(input, item)
		}
	}
	if len(input) > 0 {
		m["input"] = input
	}

	if req.Thinking != nil && (req.Thinking.IncludeThoughts || req.Thinking.Effort != "" || req.Thinking.Summary != "") {
		reasoning := map[string]any{}
		if req.Thinking.Effort != "" {
			reasoning["effort"] = req.Thinking.Effort
		} else if req.Thinking.IncludeThoughts {
			budget := 0
			if req.Thinking.ThinkingBudget != nil {
				budget = int(*req.Thinking.ThinkingBudget)
			}
			reasoning["effort"] = ir.BudgetToEffort(budget, "low")
		}
		if req.Thinking.Summary != "" {
			reasoning["summary"] = req.Thinking.Summary
		}
		if len(reasoning) > 0 {
			m["reasoning"] = reasoning
		}
	}

	if len(req.Tools) > 0 {
		tools := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = map[string]any{
				"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters,
			}
		}
		m["tools"] = tools
	}

	// Add built-in tools from Metadata for Responses API
	if req.Metadata != nil {
		var builtInTools []any

		// web_search tool
		if gsConfig, ok := req.Metadata[ir.MetaGoogleSearch]; ok {
			webSearchTool := map[string]any{"type": "web_search_preview"}
			if cfg, ok := gsConfig.(map[string]any); ok {
				if scs, ok := cfg["search_context_size"]; ok {
					webSearchTool["search_context_size"] = scs
				}
				if ul, ok := cfg["user_location"]; ok {
					webSearchTool["user_location"] = ul
				}
			}
			builtInTools = append(builtInTools, webSearchTool)
		}

		// code_interpreter tool
		if ciConfig, ok := req.Metadata[ir.MetaCodeExecution]; ok {
			codeInterpreterTool := map[string]any{"type": "code_interpreter"}
			if cfg, ok := ciConfig.(map[string]any); ok {
				if container, ok := cfg["container"]; ok {
					codeInterpreterTool["container"] = container
				}
			}
			builtInTools = append(builtInTools, codeInterpreterTool)
		}

		// file_search tool
		if fsConfig, ok := req.Metadata[ir.MetaFileSearch]; ok {
			fileSearchTool := map[string]any{"type": "file_search"}
			if cfg, ok := fsConfig.(map[string]any); ok {
				if vs, ok := cfg["vector_store"]; ok {
					fileSearchTool["vector_store"] = vs
				}
				if mnr, ok := cfg["max_num_results"]; ok {
					fileSearchTool["max_num_results"] = mnr
				}
				if ro, ok := cfg["ranking_options"]; ok {
					fileSearchTool["ranking_options"] = ro
				}
			}
			builtInTools = append(builtInTools, fileSearchTool)
		}

		if len(builtInTools) > 0 {
			existingTools, _ := m["tools"].([]any)
			m["tools"] = append(existingTools, builtInTools...)
		}
	}

	if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.PreviousResponseID != "" {
		m["previous_response_id"] = req.PreviousResponseID
	}
	if req.PromptID != "" {
		prompt := map[string]any{"id": req.PromptID}
		if req.PromptVersion != "" {
			prompt["version"] = req.PromptVersion
		}
		if len(req.PromptVariables) > 0 {
			prompt["variables"] = req.PromptVariables
		}
		m["prompt"] = prompt
	}
	if req.PromptCacheKey != "" {
		m["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.Store != nil {
		m["store"] = *req.Store
	}

	return json.Marshal(m)
}

func convertMessageToResponsesInput(msg ir.Message) any {
	switch msg.Role {
	case ir.RoleSystem:
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]any{
				"type": "message", "role": "system",
				"content": []any{map[string]any{"type": "input_text", "text": text}},
			}
		}
	case ir.RoleUser:
		return buildResponsesUserMessage(msg)
	case ir.RoleAssistant:
		if len(msg.ToolCalls) > 0 {
			tc := msg.ToolCalls[0]
			return map[string]any{
				"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Args,
			}
		}
		if text := ir.CombineTextParts(msg); text != "" {
			return map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text}},
			}
		}
	case ir.RoleTool:
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
				output := part.ToolResult.Result
				result := map[string]any{
					"type": "function_call_output", "call_id": part.ToolResult.ToolCallID, "output": output,
				}
				// Responses API supports is_error field
				if part.ToolResult.IsError {
					result["is_error"] = true
				}
				return result
			}
		}
	}
	return nil
}

func buildResponsesUserMessage(msg ir.Message) any {
	var content []any
	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				content = append(content, map[string]any{"type": "input_text", "text": part.Text})
			}
		case ir.ContentTypeImage:
			if part.Image != nil {
				if part.Image.URL != "" {
					content = append(content, map[string]any{"type": "input_image", "image_url": part.Image.URL})
				} else if part.Image.Data != "" {
					content = append(content, map[string]any{
						"type": "input_image", "image_url": fmt.Sprintf("data:%s;base64,%s", part.Image.MimeType, part.Image.Data),
					})
				}
			}
		case ir.ContentTypeFile:
			if part.File != nil {
				fileItem := map[string]any{"type": "input_file"}
				if part.File.FileID != "" {
					fileItem["file_id"] = part.File.FileID
				}
				if part.File.FileURL != "" {
					fileItem["file_url"] = part.File.FileURL
				}
				if part.File.Filename != "" {
					fileItem["filename"] = part.File.Filename
				}
				if part.File.FileData != "" {
					fileItem["file_data"] = part.File.FileData
				}
				content = append(content, fileItem)
			}
		case ir.ContentTypeAudio:
			// Audio input for Responses API
			if part.Audio != nil && part.Audio.Data != "" {
				inputAudio := map[string]any{
					"data": part.Audio.Data,
				}
				if part.Audio.Format != "" {
					inputAudio["format"] = part.Audio.Format
				}
				content = append(content, map[string]any{
					"type":        "input_audio",
					"input_audio": inputAudio,
				})
			}
		}
	}
	if len(content) == 0 {
		return nil
	}
	return map[string]any{"type": "message", "role": "user", "content": content}
}

// ToOpenAIChatCompletion converts messages to OpenAI chat completion response.
func ToOpenAIChatCompletion(messages []ir.Message, usage *ir.Usage, model, messageID string) ([]byte, error) {
	return ToOpenAIChatCompletionMeta(messages, usage, model, messageID, nil)
}

// ToOpenAIChatCompletionCandidates converts multiple candidates to OpenAI chat completion with multiple choices.
func ToOpenAIChatCompletionCandidates(candidates []ir.CandidateResult, usage *ir.Usage, model, messageID string, meta *ir.OpenAIMeta) ([]byte, error) {
	responseID, created := messageID, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	response := map[string]any{
		"id": responseID, "object": "chat.completion", "created": created, "model": model, "choices": []any{},
	}

	var choices []any
	for _, candidate := range candidates {
		if len(candidate.Messages) == 0 {
			continue
		}

		builder := ir.NewResponseBuilder(candidate.Messages, usage, model)
		msg := builder.GetLastMessage()
		if msg == nil {
			continue
		}

		msgContent := map[string]any{"role": string(msg.Role)}
		text := builder.GetTextContent()
		tcs := builder.BuildOpenAIToolCalls()
		if text != "" {
			msgContent["content"] = text
		} else if tcs != nil {
			// OpenAI spec: content must be null (not omitted) when tool_calls present
			msgContent["content"] = nil
		}
		if reasoning := builder.GetReasoningContent(); reasoning != "" {
			ir.AddReasoningToMessage(msgContent, reasoning, "")
		}
		if tcs != nil {
			msgContent["tool_calls"] = tcs
		}

		choiceObj := map[string]any{
			"index": candidate.Index, "finish_reason": ir.MapFinishReasonToOpenAI(candidate.FinishReason), "message": msgContent,
		}
		if candidate.Logprobs != nil {
			choiceObj["logprobs"] = candidate.Logprobs
		}
		choices = append(choices, choiceObj)
	}

	response["choices"] = choices

	if usage != nil {
		usageMap := map[string]any{
			"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens,
		}
		// Add prompt_tokens_details if available
		if usage.PromptTokensDetails != nil {
			promptDetails := map[string]any{}
			if usage.PromptTokensDetails.CachedTokens > 0 {
				promptDetails["cached_tokens"] = usage.PromptTokensDetails.CachedTokens
			}
			if usage.PromptTokensDetails.AudioTokens > 0 {
				promptDetails["audio_tokens"] = usage.PromptTokensDetails.AudioTokens
			}
			if len(promptDetails) > 0 {
				usageMap["prompt_tokens_details"] = promptDetails
			}
		}
		// Add completion_tokens_details
		var thoughtsTokens int32
		if meta != nil && meta.ThoughtsTokenCount > 0 {
			thoughtsTokens = meta.ThoughtsTokenCount
		} else if usage.ThoughtsTokenCount > 0 {
			thoughtsTokens = usage.ThoughtsTokenCount
		}
		completionDetails := map[string]any{}
		if thoughtsTokens > 0 {
			completionDetails["reasoning_tokens"] = thoughtsTokens
		}
		if usage.CompletionTokensDetails != nil {
			if usage.CompletionTokensDetails.AudioTokens > 0 {
				completionDetails["audio_tokens"] = usage.CompletionTokensDetails.AudioTokens
			}
			if usage.CompletionTokensDetails.AcceptedPredictionTokens > 0 {
				completionDetails["accepted_prediction_tokens"] = usage.CompletionTokensDetails.AcceptedPredictionTokens
			}
			if usage.CompletionTokensDetails.RejectedPredictionTokens > 0 {
				completionDetails["rejected_prediction_tokens"] = usage.CompletionTokensDetails.RejectedPredictionTokens
			}
		}
		if len(completionDetails) > 0 {
			usageMap["completion_tokens_details"] = completionDetails
		}
		response["usage"] = usageMap
	}

	// Add grounding metadata from meta or first candidate with grounding
	if meta != nil && meta.GroundingMetadata != nil {
		response["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	} else {
		for _, candidate := range candidates {
			if candidate.GroundingMetadata != nil {
				response["grounding_metadata"] = buildOpenAIGroundingMetadata(candidate.GroundingMetadata)
				break
			}
		}
	}

	return json.Marshal(response)
}

func ToOpenAIChatCompletionMeta(messages []ir.Message, usage *ir.Usage, model, messageID string, meta *ir.OpenAIMeta) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)
	responseID, created := messageID, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	response := map[string]any{
		"id": responseID, "object": "chat.completion", "created": created, "model": model, "choices": []any{},
	}

	if msg := builder.GetLastMessage(); msg != nil {
		msgContent := map[string]any{"role": string(msg.Role)}
		text := builder.GetTextContent()
		tcs := builder.BuildOpenAIToolCalls()
		if text != "" {
			msgContent["content"] = text
		} else if tcs != nil {
			// OpenAI spec: content must be null (not omitted) when tool_calls present
			msgContent["content"] = nil
		}
		if reasoning := builder.GetReasoningContent(); reasoning != "" {
			ir.AddReasoningToMessage(msgContent, reasoning, "")
		}
		if tcs != nil {
			msgContent["tool_calls"] = tcs
		}

		// Add audio output if present (gpt-4o-audio-preview)
		if audioPart := findAudioContent(msg); audioPart != nil {
			audioObj := map[string]any{}
			if audioPart.ID != "" {
				audioObj["id"] = audioPart.ID
			}
			if audioPart.Data != "" {
				audioObj["data"] = audioPart.Data
			}
			if audioPart.Transcript != "" {
				audioObj["transcript"] = audioPart.Transcript
			}
			if audioPart.ExpiresAt > 0 {
				audioObj["expires_at"] = audioPart.ExpiresAt
			}
			if len(audioObj) > 0 {
				msgContent["audio"] = audioObj
			}
		}

		choiceObj := map[string]any{
			"index": 0, "finish_reason": builder.DetermineFinishReason(), "message": msgContent,
		}
		if meta != nil && meta.NativeFinishReason != "" {
			choiceObj["native_finish_reason"] = meta.NativeFinishReason
		}
		if meta != nil && meta.Logprobs != nil {
			choiceObj["logprobs"] = meta.Logprobs
		}
		response["choices"] = []any{choiceObj}
	}

	if usageMap := builder.BuildUsageMap(); usageMap != nil {
		// Merge reasoning tokens into completion_tokens_details if not already present
		var thoughtsTokens int32
		if meta != nil && meta.ThoughtsTokenCount > 0 {
			thoughtsTokens = meta.ThoughtsTokenCount
		} else if usage != nil && usage.ThoughtsTokenCount > 0 {
			thoughtsTokens = usage.ThoughtsTokenCount
		}
		if thoughtsTokens > 0 {
			if completionDetails, ok := usageMap["completion_tokens_details"].(map[string]any); ok {
				if _, hasReasoningTokens := completionDetails["reasoning_tokens"]; !hasReasoningTokens {
					completionDetails["reasoning_tokens"] = thoughtsTokens
				}
			} else if thoughtsTokens > 0 {
				usageMap["completion_tokens_details"] = map[string]any{"reasoning_tokens": thoughtsTokens}
			}
		}
		response["usage"] = usageMap
	}

	// Add grounding metadata if present (for Google Search grounding)
	if meta != nil && meta.GroundingMetadata != nil {
		response["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	}

	return json.Marshal(response)
}

// ToOpenAIChunk converts event to OpenAI SSE streaming chunk.
// ToOpenAIChunk converts event to OpenAI SSE streaming chunk.
func ToOpenAIChunk(event ir.UnifiedEvent, model, messageID string, chunkIndex int) ([]byte, error) {
	return ToOpenAIChunkMeta(event, model, messageID, chunkIndex, nil)
}

// openaiTextChunk is optimized struct for the most common case: text token streaming.
// Using a fixed struct allows faster JSON encoding than map[string]any.
type openaiTextChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
}

// ToOpenAIChunkMeta converts event to OpenAI SSE streaming chunk with metadata.
// Optimized for the hot path (text tokens) using typed struct instead of map.
func ToOpenAIChunkMeta(event ir.UnifiedEvent, model, messageID string, chunkIndex int, meta *ir.OpenAIMeta) ([]byte, error) {
	responseID, created := messageID, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	// Fast path: simple text token (most common case ~90% of chunks)
	if event.Type == ir.EventTypeToken && event.Content != "" && event.Refusal == "" &&
		event.Logprobs == nil && event.SystemFingerprint == "" {
		chunk := openaiTextChunk{
			ID:      responseID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: make([]struct {
				Index int `json:"index"`
				Delta struct {
					Role    string `json:"role,omitempty"`
					Content string `json:"content,omitempty"`
				} `json:"delta"`
			}, 1),
		}
		chunk.Choices[0].Delta.Role = "assistant"
		chunk.Choices[0].Delta.Content = event.Content
		jsonBytes, err := json.Marshal(chunk)
		if err != nil {
			return nil, err
		}
		return ir.BuildSSEChunk(jsonBytes), nil
	}

	// Slow path: complex events (tool calls, finish, etc.)
	chunk := map[string]any{
		"id": responseID, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{},
	}
	if event.SystemFingerprint != "" {
		chunk["system_fingerprint"] = event.SystemFingerprint
	}

	choice := map[string]any{"index": 0, "delta": map[string]any{}}

	switch event.Type {
	case ir.EventTypeToken:
		delta := map[string]any{"role": "assistant"}
		if event.Content != "" {
			delta["content"] = event.Content
		}
		if event.Refusal != "" {
			delta["refusal"] = event.Refusal
		}
		choice["delta"] = delta
	case ir.EventTypeReasoning:
		choice["delta"] = ir.BuildReasoningDelta(event.Reasoning, string(event.ThoughtSignature))
	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			tcMap := map[string]any{
				"index": chunkIndex, "id": event.ToolCall.ID, "type": "function",
				"function": map[string]any{"name": event.ToolCall.Name, "arguments": event.ToolCall.Args},
			}
			// Inject thought_signature for Gemini 3 compatibility
			// Use event.ThoughtSignature (from event metadata) or fallback to ToolCall.ThoughtSignature
			ts := event.ThoughtSignature
			if len(ts) == 0 {
				ts = event.ToolCall.ThoughtSignature
			}
			if len(ts) > 0 {
				tcMap["extra_content"] = map[string]any{
					"google": map[string]any{
						"thought_signature": string(ts),
					},
				}
			}

			choice["delta"] = map[string]any{
				"role": "assistant",
				"tool_calls": []any{
					tcMap,
				},
			}
		}
	case ir.EventTypeImage:
		if event.Image != nil {
			choice["delta"] = map[string]any{
				"role": "assistant",
				"images": []any{
					map[string]any{
						"type": "image_url",
						"image_url": map[string]string{
							"url": fmt.Sprintf("data:%s;base64,%s", event.Image.MimeType, event.Image.Data),
						},
					},
				},
			}
		}
	case ir.EventTypeAudio:
		// Audio streaming output for gpt-4o-audio-preview
		if event.Audio != nil {
			audioObj := map[string]any{}
			if event.Audio.ID != "" {
				audioObj["id"] = event.Audio.ID
			}
			if event.Audio.Data != "" {
				audioObj["data"] = event.Audio.Data
			}
			if event.Audio.Transcript != "" {
				audioObj["transcript"] = event.Audio.Transcript
			}
			if event.Audio.ExpiresAt > 0 {
				audioObj["expires_at"] = event.Audio.ExpiresAt
			}
			choice["delta"] = map[string]any{
				"role":  "assistant",
				"audio": audioObj,
			}
		}
	case ir.EventTypeFinish:
		choice["finish_reason"] = ir.MapFinishReasonToOpenAI(event.FinishReason)
		if meta != nil && meta.NativeFinishReason != "" {
			choice["native_finish_reason"] = meta.NativeFinishReason
		}
		if event.Logprobs != nil {
			choice["logprobs"] = event.Logprobs
		}
		if event.ContentFilter != nil {
			choice["content_filter_results"] = event.ContentFilter
		}

		if event.Usage != nil {
			usageMap := map[string]any{
				"prompt_tokens": event.Usage.PromptTokens, "completion_tokens": event.Usage.CompletionTokens, "total_tokens": event.Usage.TotalTokens,
			}

			promptDetails := map[string]any{}
			// Use structured PromptTokensDetails if available
			if event.Usage.PromptTokensDetails != nil {
				if event.Usage.PromptTokensDetails.CachedTokens > 0 {
					promptDetails["cached_tokens"] = event.Usage.PromptTokensDetails.CachedTokens
				}
				if event.Usage.PromptTokensDetails.AudioTokens > 0 {
					promptDetails["audio_tokens"] = event.Usage.PromptTokensDetails.AudioTokens
				}
			} else {
				// Fall back to flat fields for backward compatibility
				if event.Usage.CachedTokens > 0 {
					promptDetails["cached_tokens"] = event.Usage.CachedTokens
				}
				if event.Usage.AudioTokens > 0 {
					promptDetails["audio_tokens"] = event.Usage.AudioTokens
				}
			}
			if len(promptDetails) > 0 {
				usageMap["prompt_tokens_details"] = promptDetails
			}

			completionDetails := map[string]any{}
			// Use structured CompletionTokensDetails if available
			if event.Usage.CompletionTokensDetails != nil {
				if event.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
					completionDetails["reasoning_tokens"] = event.Usage.CompletionTokensDetails.ReasoningTokens
				}
				if event.Usage.CompletionTokensDetails.AudioTokens > 0 {
					completionDetails["audio_tokens"] = event.Usage.CompletionTokensDetails.AudioTokens
				}
				if event.Usage.CompletionTokensDetails.AcceptedPredictionTokens > 0 {
					completionDetails["accepted_prediction_tokens"] = event.Usage.CompletionTokensDetails.AcceptedPredictionTokens
				}
				if event.Usage.CompletionTokensDetails.RejectedPredictionTokens > 0 {
					completionDetails["rejected_prediction_tokens"] = event.Usage.CompletionTokensDetails.RejectedPredictionTokens
				}
			}

			// Also handle reasoning tokens from meta and flat fields
			var thoughtsTokens int32
			if meta != nil && meta.ThoughtsTokenCount > 0 {
				thoughtsTokens = meta.ThoughtsTokenCount
			} else if event.Usage.ThoughtsTokenCount > 0 {
				thoughtsTokens = event.Usage.ThoughtsTokenCount
			}
			if thoughtsTokens > 0 && completionDetails["reasoning_tokens"] == nil {
				completionDetails["reasoning_tokens"] = thoughtsTokens
			}

			// Backward compatibility: add flat fields if they exist but aren't in details
			if event.Usage.AcceptedPredictionTokens > 0 && completionDetails["accepted_prediction_tokens"] == nil {
				completionDetails["accepted_prediction_tokens"] = event.Usage.AcceptedPredictionTokens
			}
			if event.Usage.RejectedPredictionTokens > 0 && completionDetails["rejected_prediction_tokens"] == nil {
				completionDetails["rejected_prediction_tokens"] = event.Usage.RejectedPredictionTokens
			}

			if len(completionDetails) > 0 {
				usageMap["completion_tokens_details"] = completionDetails
			}

			chunk["usage"] = usageMap
		}
		// Add grounding metadata in final streaming chunk
		if event.GroundingMetadata != nil {
			chunk["grounding_metadata"] = buildOpenAIGroundingMetadata(event.GroundingMetadata)
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)
	default:
		return nil, nil
	}

	// Add logprobs to non-finish events if present
	if event.Logprobs != nil && event.Type != ir.EventTypeFinish {
		choice["logprobs"] = event.Logprobs
	}

	chunk["choices"] = []any{choice}
	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	return ir.BuildSSEChunk(jsonBytes), nil
}

func convertMessageToOpenAI(msg ir.Message) map[string]any {
	var result map[string]any
	switch msg.Role {
	case ir.RoleSystem:
		if text := ir.CombineTextParts(msg); text != "" {
			result = map[string]any{"role": "system", "content": text}
		}
	case ir.RoleUser:
		result = buildOpenAIUserMessage(msg)
	case ir.RoleAssistant:
		result = buildOpenAIAssistantMessage(msg)
	case ir.RoleTool:
		result = buildOpenAIToolMessage(msg)
	}

	// Add cache_control if present
	if result != nil && msg.CacheControl != nil {
		cacheCtrl := map[string]any{"type": msg.CacheControl.Type}
		if msg.CacheControl.TTL != nil {
			cacheCtrl["ttl"] = *msg.CacheControl.TTL
		}
		result["cache_control"] = cacheCtrl
	}

	return result
}

func buildOpenAIUserMessage(msg ir.Message) map[string]any {
	// Pre-allocate with capacity
	parts := make([]any, 0, len(msg.Content))
	for i := range msg.Content {
		part := &msg.Content[i]
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": part.Text})
			}
		case ir.ContentTypeImage:
			if part.Image != nil {
				parts = append(parts, map[string]any{
					"type":      "image_url",
					"image_url": map[string]string{"url": fmt.Sprintf("data:%s;base64,%s", part.Image.MimeType, part.Image.Data)},
				})
			}
		case ir.ContentTypeAudio:
			// OpenAI audio input for gpt-4o-audio-preview models
			if part.Audio != nil && part.Audio.Data != "" {
				inputAudio := map[string]any{
					"data": part.Audio.Data,
				}
				if part.Audio.Format != "" {
					inputAudio["format"] = part.Audio.Format
				}
				parts = append(parts, map[string]any{
					"type":        "input_audio",
					"input_audio": inputAudio,
				})
			}
		}
	}
	if len(parts) == 0 {
		return nil
	}
	if len(parts) == 1 {
		if tp, ok := parts[0].(map[string]any); ok && tp["type"] == "text" {
			return map[string]any{"role": "user", "content": tp["text"]}
		}
	}
	return map[string]any{"role": "user", "content": parts}
}

func buildOpenAIAssistantMessage(msg ir.Message) map[string]any {
	result := map[string]any{"role": "assistant"}
	text, reasoning := ir.CombineTextAndReasoning(msg)
	if text != "" {
		result["content"] = text
	}
	if reasoning != "" {
		ir.AddReasoningToMessage(result, reasoning, ir.GetFirstReasoningSignature(msg))
	}
	if len(msg.ToolCalls) > 0 {
		tcs := make([]any, len(msg.ToolCalls))
		for i := range msg.ToolCalls {
			tc := &msg.ToolCalls[i]
			tcMap := map[string]any{
				"id": tc.ID, "type": "function",
				"function": map[string]any{"name": tc.Name, "arguments": tc.Args},
			}
			// Inject thought_signature for Gemini 3 compatibility
			if len(tc.ThoughtSignature) > 0 {
				tcMap["extra_content"] = map[string]any{
					"google": map[string]any{
						"thought_signature": string(tc.ThoughtSignature),
					},
				}
			}
			tcs[i] = tcMap
		}
		result["tool_calls"] = tcs
	}
	// Include refusal message if model declined to respond
	if msg.Refusal != "" {
		result["refusal"] = msg.Refusal
	}
	return result
}

func buildOpenAIToolMessage(msg ir.Message) map[string]any {
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			// OpenAI Chat Completions API: tool messages don't have is_error field
			// Error information should be embedded in content by the caller
			return map[string]any{
				"role": "tool", "tool_call_id": part.ToolResult.ToolCallID, "content": part.ToolResult.Result,
			}
		}
	}
	return nil
}

// ToResponsesAPIResponse converts messages to Responses API non-streaming response.
func ToResponsesAPIResponse(messages []ir.Message, usage *ir.Usage, model string, meta *ir.OpenAIMeta) ([]byte, error) {
	responseID, created := fmt.Sprintf("resp_%d", time.Now().UnixNano()), time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			responseID = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			created = meta.CreateTime
		}
	}

	response := map[string]any{
		"id": responseID, "object": "response", "created_at": created, "status": "completed", "model": model,
	}

	var output []any
	var outputText string
	builder := ir.NewResponseBuilder(messages, usage, model)

	for _, msg := range messages {
		if msg.Role != ir.RoleAssistant {
			continue
		}
		text, reasoning := ir.CombineTextAndReasoning(msg)
		if reasoning != "" {
			output = append(output, map[string]any{
				"id": fmt.Sprintf("rs_%s", responseID), "type": "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": reasoning}},
			})
		}
		if text != "" {
			outputText = text
			output = append(output, map[string]any{
				"id": fmt.Sprintf("msg_%s", responseID), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
			})
		}
		for _, tc := range msg.ToolCalls {
			output = append(output, map[string]any{
				"id": fmt.Sprintf("fc_%s", tc.ID), "type": "function_call", "status": "completed",
				"call_id": tc.ID, "name": tc.Name, "arguments": tc.Args,
			})
		}
	}

	if len(output) > 0 {
		response["output"] = output
	}
	if outputText != "" {
		response["output_text"] = outputText
	}

	if usageMap := builder.BuildUsageMap(); usageMap != nil {
		responsesUsage := map[string]any{
			"input_tokens": usageMap["prompt_tokens"], "output_tokens": usageMap["completion_tokens"], "total_tokens": usageMap["total_tokens"],
		}
		// Check PromptTokensDetails first, then fall back to flat CachedTokens
		var cachedTokens int64
		if usage != nil && usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			cachedTokens = usage.PromptTokensDetails.CachedTokens
		} else if usage != nil && usage.CachedTokens > 0 {
			cachedTokens = usage.CachedTokens
		}
		if cachedTokens > 0 {
			responsesUsage["input_tokens_details"] = map[string]any{"cached_tokens": cachedTokens}
		}
		// Build output_tokens_details with all available fields
		outputDetails := map[string]any{}
		var thoughtsTokens int64
		if usage != nil && usage.CompletionTokensDetails != nil && usage.CompletionTokensDetails.ReasoningTokens > 0 {
			thoughtsTokens = usage.CompletionTokensDetails.ReasoningTokens
		} else if meta != nil && meta.ThoughtsTokenCount > 0 {
			thoughtsTokens = int64(meta.ThoughtsTokenCount)
		} else if usage != nil && usage.ThoughtsTokenCount > 0 {
			thoughtsTokens = int64(usage.ThoughtsTokenCount)
		}
		if thoughtsTokens > 0 {
			outputDetails["reasoning_tokens"] = thoughtsTokens
		}
		if usage != nil && usage.CompletionTokensDetails != nil {
			if usage.CompletionTokensDetails.AudioTokens > 0 {
				outputDetails["audio_tokens"] = usage.CompletionTokensDetails.AudioTokens
			}
		}
		if len(outputDetails) > 0 {
			responsesUsage["output_tokens_details"] = outputDetails
		}
		response["usage"] = responsesUsage
	}

	// Add grounding metadata if present
	if meta != nil && meta.GroundingMetadata != nil {
		response["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	}

	return json.Marshal(response)
}

// ResponsesStreamState holds state for Responses API streaming conversion.
// Responses API streaming uses semantic events (response.output_text.delta, etc.)
// and requires tracking state across multiple events to build proper "done" events.
type ResponsesStreamState struct {
	Seq             int                      // Sequence number for events (required by Responses API)
	ResponseID      string                   // Response ID (generated once, reused for all events)
	Created         int64                    // Creation timestamp
	Started         bool                     // Whether initial events (response.created, response.in_progress) were sent
	ReasoningID     string                   // ID for reasoning output item (if any)
	MsgID           string                   // ID for message output item (if any)
	TextBuffer      strings.Builder          // Accumulated text content (needed for "done" event)
	ReasoningBuffer strings.Builder          // Accumulated reasoning content
	FuncCallIDs     map[int]string           // Tool call IDs by index
	FuncNames       map[int]string           // Tool call names by index
	FuncArgsBuffer  map[int]*strings.Builder // Accumulated tool call arguments by index
}

// NewResponsesStreamState creates a new streaming state for Responses API.
func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{
		FuncCallIDs:    make(map[int]string),
		FuncNames:      make(map[int]string),
		FuncArgsBuffer: make(map[int]*strings.Builder),
	}
}

// formatResponsesSSE formats an SSE event efficiently for Responses API.
func formatResponsesSSE(eventType string, jsonData []byte) string {
	// Pre-calculate size: "event: " + type + "\ndata: " + json + "\n\n"
	size := 7 + len(eventType) + 7 + len(jsonData) + 2
	var b strings.Builder
	b.Grow(size)
	b.WriteString("event: ")
	b.WriteString(eventType)
	b.WriteString("\ndata: ")
	b.Write(jsonData)
	b.WriteString("\n\n")
	return b.String()
}

// ToResponsesAPIChunk converts event to Responses API SSE streaming chunks.
// Returns multiple SSE strings because Responses API requires semantic events
// (e.g., first token requires output_item.added + content_part.added + delta events).
func ToResponsesAPIChunk(event ir.UnifiedEvent, model string, state *ResponsesStreamState) ([]string, error) {
	if state.ResponseID == "" {
		state.ResponseID = fmt.Sprintf("resp_%d", time.Now().UnixNano())
		state.Created = time.Now().Unix()
	}

	nextSeq := func() int { state.Seq++; return state.Seq }
	// Pre-allocate: most common case is 1 event, max is ~5 for first token or finish
	out := make([]string, 0, 4)

	if !state.Started {
		for _, t := range []string{"response.created", "response.in_progress"} {
			b, _ := json.Marshal(map[string]any{
				"type": t, "sequence_number": nextSeq(),
				"response": map[string]any{
					"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "in_progress",
				},
			})
			out = append(out, formatResponsesSSE(t, b))
		}
		state.Started = true
	}

	switch event.Type {
	case ir.EventTypeToken:
		if state.MsgID == "" {
			state.MsgID = fmt.Sprintf("msg_%s", state.ResponseID)
			b1, _ := json.Marshal(map[string]any{
				"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": 0,
				"item": map[string]any{"id": state.MsgID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}},
			})
			out = append(out, formatResponsesSSE("response.output_item.added", b1))
			b2, _ := json.Marshal(map[string]any{
				"type": "response.content_part.added", "sequence_number": nextSeq(), "item_id": state.MsgID,
				"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""},
			})
			out = append(out, formatResponsesSSE("response.content_part.added", b2))
		}
		state.TextBuffer.WriteString(event.Content)
		b, _ := json.Marshal(map[string]any{
			"type": "response.output_text.delta", "sequence_number": nextSeq(), "item_id": state.MsgID,
			"output_index": 0, "content_index": 0, "delta": event.Content,
		})
		out = append(out, formatResponsesSSE("response.output_text.delta", b))

	case ir.EventTypeReasoning, ir.EventTypeReasoningSummary:
		text := event.Reasoning
		if event.Type == ir.EventTypeReasoningSummary {
			text = event.ReasoningSummary
		}
		if state.ReasoningID == "" {
			state.ReasoningID = fmt.Sprintf("rs_%s", state.ResponseID)
			b, _ := json.Marshal(map[string]any{
				"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": 0,
				"item": map[string]any{"id": state.ReasoningID, "type": "reasoning", "status": "in_progress", "summary": []any{}},
			})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		state.ReasoningBuffer.WriteString(text)
		b, _ := json.Marshal(map[string]any{
			"type": "response.reasoning_summary_text.delta", "sequence_number": nextSeq(), "item_id": state.ReasoningID,
			"output_index": 0, "content_index": 0, "delta": text,
		})
		out = append(out, formatResponsesSSE("response.reasoning_summary_text.delta", b))

	case ir.EventTypeToolCall:
		idx := event.ToolCallIndex
		if _, exists := state.FuncCallIDs[idx]; !exists {
			state.FuncCallIDs[idx] = fmt.Sprintf("fc_%s", event.ToolCall.ID)
			state.FuncNames[idx] = event.ToolCall.Name
			b, _ := json.Marshal(map[string]any{
				"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": idx,
				"item": map[string]any{
					"id": state.FuncCallIDs[idx], "type": "function_call", "status": "in_progress",
					"call_id": event.ToolCall.ID, "name": event.ToolCall.Name, "arguments": "",
				},
			})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		// For complete tool call, we might not get deltas, so we can just emit done if needed,
		// but usually we get deltas or the full args. If we get full args here:
		if event.ToolCall.Args != "" {
			b, _ := json.Marshal(map[string]any{
				"type": "response.function_call_arguments.delta", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
				"output_index": idx, "delta": event.ToolCall.Args,
			})
			out = append(out, formatResponsesSSE("response.function_call_arguments.delta", b))
		}
		b, _ := json.Marshal(map[string]any{
			"type": "response.output_item.done", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
			"output_index": idx, "item": map[string]any{
				"id": state.FuncCallIDs[idx], "type": "function_call", "status": "completed",
				"call_id": event.ToolCall.ID, "name": event.ToolCall.Name, "arguments": event.ToolCall.Args,
			},
		})
		out = append(out, formatResponsesSSE("response.output_item.done", b))

	case ir.EventTypeToolCallDelta:
		idx := event.ToolCallIndex
		if _, exists := state.FuncCallIDs[idx]; !exists {
			state.FuncCallIDs[idx] = fmt.Sprintf("fc_%s", event.ToolCall.ID)
			b, _ := json.Marshal(map[string]any{
				"type": "response.output_item.added", "sequence_number": nextSeq(), "output_index": idx,
				"item": map[string]any{
					"id": state.FuncCallIDs[idx], "type": "function_call", "status": "in_progress",
					"call_id": event.ToolCall.ID, "name": "", "arguments": "",
				},
			})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		if state.FuncArgsBuffer[idx] == nil {
			state.FuncArgsBuffer[idx] = &strings.Builder{}
		}
		state.FuncArgsBuffer[idx].WriteString(event.ToolCall.Args)
		b, _ := json.Marshal(map[string]any{
			"type": "response.function_call_arguments.delta", "sequence_number": nextSeq(), "item_id": state.FuncCallIDs[idx],
			"output_index": idx, "delta": event.ToolCall.Args,
		})
		out = append(out, formatResponsesSSE("response.function_call_arguments.delta", b))

	case ir.EventTypeFinish:
		textContent := state.TextBuffer.String()
		reasoningContent := state.ReasoningBuffer.String()
		if state.MsgID != "" {
			b1, _ := json.Marshal(map[string]any{
				"type": "response.content_part.done", "sequence_number": nextSeq(), "item_id": state.MsgID,
				"output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": textContent},
			})
			out = append(out, formatResponsesSSE("response.content_part.done", b1))
			b2, _ := json.Marshal(map[string]any{
				"type": "response.output_item.done", "sequence_number": nextSeq(), "output_index": 0,
				"item": map[string]any{
					"id": state.MsgID, "type": "message", "status": "completed", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": textContent}},
				},
			})
			out = append(out, formatResponsesSSE("response.output_item.done", b2))
		}
		if state.ReasoningID != "" {
			b, _ := json.Marshal(map[string]any{
				"type": "response.output_item.done", "sequence_number": nextSeq(), "output_index": 0,
				"item": map[string]any{
					"id": state.ReasoningID, "type": "reasoning", "status": "completed",
					"summary": []any{map[string]any{"type": "summary_text", "text": reasoningContent}},
				},
			})
			out = append(out, formatResponsesSSE("response.output_item.done", b))
		}

		usageMap := map[string]any{}
		if event.Usage != nil {
			usageMap = map[string]any{
				"input_tokens": event.Usage.PromptTokens, "output_tokens": event.Usage.CompletionTokens, "total_tokens": event.Usage.TotalTokens,
			}
			// Check PromptTokensDetails first, then fall back to flat CachedTokens
			var cachedTokens int64
			if event.Usage.PromptTokensDetails != nil && event.Usage.PromptTokensDetails.CachedTokens > 0 {
				cachedTokens = event.Usage.PromptTokensDetails.CachedTokens
			} else if event.Usage.CachedTokens > 0 {
				cachedTokens = event.Usage.CachedTokens
			}
			if cachedTokens > 0 {
				usageMap["input_tokens_details"] = map[string]any{"cached_tokens": cachedTokens}
			}
			// Check CompletionTokensDetails first, then fall back to ThoughtsTokenCount
			var reasoningTokens int64
			if event.Usage.CompletionTokensDetails != nil && event.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
				reasoningTokens = event.Usage.CompletionTokensDetails.ReasoningTokens
			} else if event.Usage.ThoughtsTokenCount > 0 {
				reasoningTokens = int64(event.Usage.ThoughtsTokenCount)
			}
			if reasoningTokens > 0 {
				usageMap["output_tokens_details"] = map[string]any{"reasoning_tokens": reasoningTokens}
			}
		}

		b, _ := json.Marshal(map[string]any{
			"type": "response.done", "sequence_number": nextSeq(),
			"response": map[string]any{
				"id": state.ResponseID, "object": "response", "created_at": state.Created, "status": "completed",
				"usage": usageMap,
			},
		})
		out = append(out, formatResponsesSSE("response.done", b))
	}

	return out, nil
}

// buildOpenAIGroundingMetadata converts GroundingMetadata to OpenAI-compatible format.
// This is an extension for Google Search grounding results in OpenAI format responses.
func buildOpenAIGroundingMetadata(gm *ir.GroundingMetadata) map[string]any {
	if gm == nil {
		return nil
	}

	result := map[string]any{}

	if len(gm.WebSearchQueries) > 0 {
		result["web_search_queries"] = gm.WebSearchQueries
	}

	if gm.SearchEntryPoint != nil && gm.SearchEntryPoint.RenderedContent != "" {
		result["search_entry_point"] = map[string]any{
			"rendered_content": gm.SearchEntryPoint.RenderedContent,
		}
	}

	if len(gm.GroundingChunks) > 0 {
		var sources []map[string]any
		for _, chunk := range gm.GroundingChunks {
			if chunk.Web != nil {
				source := map[string]any{
					"type":  "web",
					"uri":   chunk.Web.URI,
					"title": chunk.Web.Title,
				}
				if chunk.Web.Domain != "" {
					source["domain"] = chunk.Web.Domain
				}
				sources = append(sources, source)
			}
		}
		if len(sources) > 0 {
			result["sources"] = sources
		}
	}

	if len(gm.GroundingSupports) > 0 {
		var citations []map[string]any
		for _, s := range gm.GroundingSupports {
			citation := map[string]any{}
			if s.Segment != nil {
				citation["text"] = s.Segment.Text
				if s.Segment.StartIndex > 0 {
					citation["start_index"] = s.Segment.StartIndex
				}
				if s.Segment.EndIndex > 0 {
					citation["end_index"] = s.Segment.EndIndex
				}
			}
			if len(s.GroundingChunkIndices) > 0 {
				citation["source_indices"] = s.GroundingChunkIndices
			}
			citations = append(citations, citation)
		}
		if len(citations) > 0 {
			result["citations"] = citations
		}
	}

	return result
}

// findAudioContent finds the first audio content part in a message.
func findAudioContent(msg *ir.Message) *ir.AudioPart {
	if msg == nil {
		return nil
	}
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeAudio && part.Audio != nil {
			return part.Audio
		}
	}
	return nil
}
