package from_ir

import (
	"strings"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

type KiroProvider struct{}

func (p *KiroProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	tools := extractTools(req.Tools)
	systemPrompt := extractSystemPrompt(req.Messages)
	history, currentMessage := processMessages(req.Messages, tools, req.Model)

	injectSystemPrompt(systemPrompt, &history, currentMessage, req.Model)

	request := map[string]any{
		"conversationState": map[string]any{
			"chatTriggerType": "MANUAL",
			"conversationId":  ir.GenerateUUID(),
			"currentMessage":  currentMessage,
			"history":         history,
		},
	}
	if req.Metadata != nil {
		if arn, ok := req.Metadata["profileArn"].(string); ok && arn != "" {
			request["profileArn"] = arn
		}
	}

	result, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	return []byte(ir.SanitizeText(string(result))), nil
}

func extractTools(irTools []ir.ToolDefinition) []any {
	if len(irTools) == 0 {
		return nil
	}
	tools := make([]any, len(irTools))
	for i, t := range irTools {
		tools[i] = map[string]any{
			"toolSpecification": map[string]any{
				"name": t.Name, "description": t.Description,
				"inputSchema": map[string]any{"json": t.Parameters},
			},
		}
	}
	return tools
}

func extractSystemPrompt(messages []ir.Message) string {
	var parts []string
	for _, msg := range messages {
		if msg.Role == ir.RoleSystem {
			parts = append(parts, ir.CombineTextParts(msg))
		}
	}
	return strings.Join(parts, "\n")
}

func processMessages(messages []ir.Message, tools []any, modelID string) ([]any, map[string]any) {
	var nonSystem []ir.Message
	for _, msg := range messages {
		if msg.Role != ir.RoleSystem {
			nonSystem = append(nonSystem, msg)
		}
	}

	// Merge consecutive same-role messages (assistant/tool only)
	if len(nonSystem) > 1 {
		merged := make([]ir.Message, 0, len(nonSystem))
		for _, msg := range nonSystem {
			if len(merged) > 0 {
				last := &merged[len(merged)-1]
				if last.Role == msg.Role && msg.Role != ir.RoleUser {
					last.Content = append(last.Content, msg.Content...)
					continue
				}
			}
			merged = append(merged, msg)
		}
		nonSystem = merged
	}

	// Ensure alternating roles: User -> Assistant -> User
	var alternated []ir.Message
	for i, msg := range nonSystem {
		if i > 0 {
			prev, curr := nonSystem[i-1].Role, msg.Role
			isUserLike := func(r ir.Role) bool { return r == ir.RoleUser || r == ir.RoleTool }
			if isUserLike(prev) && isUserLike(curr) {
				alternated = append(alternated, ir.Message{Role: ir.RoleAssistant, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "[Continued]"}}})
			} else if prev == ir.RoleAssistant && curr == ir.RoleAssistant {
				alternated = append(alternated, ir.Message{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Continue"}}})
			}
		}
		alternated = append(alternated, msg)
	}
	nonSystem = alternated

	if len(nonSystem) == 0 {
		return nil, nil
	}

	// Last message is currentMessage
	lastMsg := nonSystem[len(nonSystem)-1]
	if lastMsg.Role == ir.RoleUser {
		history := make([]any, 0, len(nonSystem)-1)
		for i := 0; i < len(nonSystem)-1; i++ {
			if m := convertMessage(nonSystem[i], tools, modelID, false); m != nil {
				history = append(history, m)
			}
		}
		return history, convertMessage(lastMsg, tools, modelID, true)
	}

	// Handle trailing tool messages
	trailingStart := len(nonSystem)
	for i := len(nonSystem) - 1; i >= 0; i-- {
		if nonSystem[i].Role == ir.RoleTool {
			trailingStart = i
		} else {
			break
		}
	}

	history := make([]any, 0, trailingStart)
	for i := range trailingStart {
		if m := convertMessage(nonSystem[i], tools, modelID, false); m != nil {
			history = append(history, m)
		}
	}

	var currentMessage map[string]any
	if trailingStart < len(nonSystem) {
		currentMessage = buildMergedToolResultMessage(nonSystem[trailingStart:], tools, modelID)
	} else {
		currentMessage = convertMessage(nonSystem[len(nonSystem)-1], tools, modelID, true)
	}
	return history, currentMessage
}

func convertMessage(msg ir.Message, tools []any, modelID string, isCurrent bool) map[string]any {
	switch msg.Role {
	case ir.RoleUser:
		return buildUserMessage(msg, tools, modelID, isCurrent)
	case ir.RoleAssistant:
		return buildAssistantMessage(msg, isCurrent)
	case ir.RoleTool:
		return buildToolResultMessage(msg, modelID)
	}
	return nil
}

func buildUserMessage(msg ir.Message, tools []any, modelID string, isCurrent bool) map[string]any {
	content := ir.CombineTextParts(msg)
	var toolResults, images []any
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			toolResults = append(toolResults, buildToolResultItem(part.ToolResult))
		} else if part.Type == ir.ContentTypeImage && part.Image != nil {
			images = append(images, buildImageItem(part.Image))
		}
	}

	if isCurrent && content == "" && len(toolResults) == 0 {
		content = "Continue"
	}

	ctx := map[string]any{}
	if len(toolResults) > 0 {
		ctx["toolResults"] = toolResults
	}
	if isCurrent && len(tools) > 0 {
		ctx["tools"] = tools
	}

	userInput := map[string]any{
		"content": content, "modelId": modelID, "origin": "AI_EDITOR", "userInputMessageContext": ctx,
	}
	if len(images) > 0 {
		userInput["images"] = images
	} else if isCurrent {
		userInput["images"] = nil // Explicit nil for current message if empty
	}

	return map[string]any{"userInputMessage": userInput}
}

func buildAssistantMessage(msg ir.Message, _ bool) map[string]any {
	toolUses := make([]any, len(msg.ToolCalls))
	for i, tc := range msg.ToolCalls {
		toolUses[i] = map[string]any{
			"input": ir.ParseToolCallArgs(tc.Args), "name": tc.Name, "toolUseId": tc.ID,
		}
	}
	assistantMsg := map[string]any{"content": ir.CombineTextParts(msg), "toolUses": toolUses}
	return map[string]any{"assistantResponseMessage": assistantMsg}
}

func buildToolResultMessage(msg ir.Message, modelID string) map[string]any {
	var toolResults []any
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			toolResults = append(toolResults, buildToolResultItem(part.ToolResult))
		}
	}
	if len(toolResults) == 0 {
		return nil
	}
	return map[string]any{
		"userInputMessage": map[string]any{
			"content": "Continue", "modelId": modelID, "origin": "AI_EDITOR", "images": []any{},
			"userInputMessageContext": map[string]any{"toolResults": toolResults},
		},
	}
}

func buildMergedToolResultMessage(msgs []ir.Message, tools []any, modelID string) map[string]any {
	var toolResults []any
	var textParts []string
	for _, msg := range msgs {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
				toolResults = append(toolResults, buildToolResultItem(part.ToolResult))
			} else if part.Type == ir.ContentTypeText && part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
	}
	content := "Continue"
	if len(textParts) > 0 {
		content = strings.Join(textParts, "\n")
	}
	ctx := map[string]any{"toolResults": toolResults}
	if len(tools) > 0 {
		ctx["tools"] = tools
	}
	return map[string]any{
		"userInputMessage": map[string]any{
			"content": content, "modelId": modelID, "origin": "AI_EDITOR", "images": nil, "userInputMessageContext": ctx,
		},
	}
}

func buildToolResultItem(tr *ir.ToolResultPart) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"text": ir.SanitizeText(tr.Result)}},
		"status":  "success", "toolUseId": tr.ToolCallID,
	}
}

func buildImageItem(img *ir.ImagePart) map[string]any {
	format := "png"
	if parts := strings.Split(img.MimeType, "/"); len(parts) == 2 {
		format = parts[1]
	}
	return map[string]any{"format": format, "source": map[string]any{"bytes": img.Data}}
}

func injectSystemPrompt(prompt string, history *[]any, currentMessage map[string]any, modelID string) {
	if prompt == "" {
		return
	}
	prepend := func(msg any) bool {
		if m, ok := msg.(map[string]any); ok {
			if userMsg, ok := m["userInputMessage"].(map[string]any); ok {
				if existing, _ := userMsg["content"].(string); existing != "" {
					userMsg["content"] = prompt + "\n\n" + existing
				} else {
					userMsg["content"] = prompt
				}
				return true
			}
		}
		return false
	}

	if len(*history) > 0 && prepend((*history)[0]) {
		return
	}
	if currentMessage != nil && prepend(currentMessage) {
		return
	}

	*history = append([]any{map[string]any{
		"userInputMessage": map[string]any{
			"content": prompt, "modelId": modelID, "origin": "AI_EDITOR",
		},
	}}, *history...)
}
