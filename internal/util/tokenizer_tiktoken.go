package util

import (
	"github.com/nghyane/llm-mux/internal/json"
	"strings"
	"sync"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tiktoken-go/tokenizer"
)

var (
	tiktokenCache   = make(map[tokenizer.Encoding]tokenizer.Codec)
	tiktokenCacheMu sync.RWMutex
)

var (
	roleTokenCache   = make(map[string]int64)
	roleTokenCacheMu sync.RWMutex
)

const (
	ImageTokenCostTiktoken = 255
	DocTokenCostTiktoken   = 500
	AudioTokenCostTiktoken = 300
	VideoTokenCostTiktoken = 2000
)

const maxPooledBuilderCap = 256 * 1024

var stringBuilderPool = sync.Pool{
	New: func() any {
		sb := &strings.Builder{}
		sb.Grow(1024)
		return sb
	},
}

func acquireBuilder() *strings.Builder {
	return stringBuilderPool.Get().(*strings.Builder)
}

func releaseBuilder(sb *strings.Builder) {
	if sb.Cap() > maxPooledBuilderCap {
		return
	}
	sb.Reset()
	stringBuilderPool.Put(sb)
}

const TokenEstimationThreshold = 100_000

func CountTiktokenTokens(model string, req *ir.UnifiedChatRequest) int64 {
	if req == nil {
		return 0
	}

	encodingName := getTiktokenEncodingName(model)
	enc, err := getTiktokenCodec(encodingName)
	if err != nil {
		return 0
	}

	var totalTokens int64

	const tokensPerMessage int64 = 3

	if req.Instructions != "" {
		if cached, ok := InstructionTokenCache.Get(req.Instructions); ok {
			totalTokens += int64(cached) + tokensPerMessage
		} else {
			tokens := countTokens(enc, req.Instructions)
			InstructionTokenCache.Set(req.Instructions, int(tokens))
			totalTokens += tokens + tokensPerMessage
		}
	}

	sb := acquireBuilder()
	defer releaseBuilder(sb)

	for i := range req.Messages {
		msg := &req.Messages[i]
		totalTokens += tokensPerMessage
		totalTokens += countRoleTokens(enc, string(msg.Role))

		sb.Reset()
		hasContentToCount := false

		// Process content parts
		// We stream parts into the tokenizer buffer, but switch to direct estimation
		// for large chunks to avoid memory spikes and tokenizer overhead.
		for j := range msg.Content {
			part := &msg.Content[j]
			switch part.Type {
			case ir.ContentTypeText:
				if len(part.Text) > TokenEstimationThreshold {
					if sb.Len() > 0 {
						totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
						sb.Reset()
					}
					// Estimate large text directly
					totalTokens += estimateTokens(part.Text)
					hasContentToCount = false
				} else if part.Text != "" {
					sb.WriteString(part.Text)
					hasContentToCount = true
				}

			case ir.ContentTypeReasoning:
				if len(part.Reasoning) > TokenEstimationThreshold {
					if sb.Len() > 0 {
						totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
						sb.Reset()
					}
					totalTokens += estimateTokens(part.Reasoning)
					hasContentToCount = false
				} else if part.Reasoning != "" {
					sb.WriteString(part.Reasoning)
					hasContentToCount = true
				}
				if len(part.ThoughtSignature) > 0 {
					sb.Write(part.ThoughtSignature)
					hasContentToCount = true
				}

			case ir.ContentTypeCodeResult:
				if part.CodeExecution != nil && part.CodeExecution.Output != "" {
					if len(part.CodeExecution.Output) > TokenEstimationThreshold {
						if sb.Len() > 0 {
							totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
							sb.Reset()
						}
						totalTokens += estimateTokens(part.CodeExecution.Output)
						hasContentToCount = false
					} else {
						sb.WriteString(part.CodeExecution.Output)
						hasContentToCount = true
					}
				}

			case ir.ContentTypeExecutableCode:
				if part.CodeExecution != nil && part.CodeExecution.Code != "" {
					if len(part.CodeExecution.Code) > TokenEstimationThreshold {
						if sb.Len() > 0 {
							totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
							sb.Reset()
						}
						totalTokens += estimateTokens(part.CodeExecution.Code)
						hasContentToCount = false
					} else {
						sb.WriteString(part.CodeExecution.Code)
						hasContentToCount = true
					}
				}

			case ir.ContentTypeImage:
				if part.Image != nil {
					totalTokens += ImageTokenCostTiktoken
				}

			case ir.ContentTypeFile:
				if part.File != nil {
					if part.File.FileData != "" {
						if len(part.File.FileData) > TokenEstimationThreshold {
							if sb.Len() > 0 {
								totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
								sb.Reset()
							}
							totalTokens += estimateTokens(part.File.FileData)
							hasContentToCount = false
						} else {
							sb.WriteString(part.File.FileData)
							hasContentToCount = true
						}
					} else if part.File.FileURL != "" || part.File.FileID != "" {
						totalTokens += DocTokenCostTiktoken
					}
				}

			case ir.ContentTypeAudio:
				if part.Audio != nil {
					if part.Audio.Transcript != "" {
						sb.WriteString(part.Audio.Transcript)
						hasContentToCount = true
					}
					totalTokens += AudioTokenCostTiktoken
				}

			case ir.ContentTypeVideo:
				if part.Video != nil {
					totalTokens += VideoTokenCostTiktoken
				}

			case ir.ContentTypeToolResult:
				if part.ToolResult != nil {
					// Tool result formatting involves mixed content, handle carefully
					// Simplified: just estimate if result is huge
					if len(part.ToolResult.Result) > TokenEstimationThreshold {
						if sb.Len() > 0 {
							totalTokens += countTokensWithCache(enc, sb.String(), ContentTokenCache)
							sb.Reset()
						}
						sb.WriteString("\nTool ")
						sb.WriteString(part.ToolResult.ToolCallID)
						sb.WriteString(" result: ")
						// Flush header
						headerStr := sb.String()
						if cached, ok := ContentTokenCache.Get(headerStr); ok {
							totalTokens += int64(cached)
						} else {
							tokens := countTokens(enc, headerStr)
							ContentTokenCache.Set(headerStr, int(tokens))
							totalTokens += tokens
						}
						sb.Reset()

						totalTokens += estimateTokens(part.ToolResult.Result)
						hasContentToCount = false
					} else {
						sb.WriteString("\nTool ")
						sb.WriteString(part.ToolResult.ToolCallID)
						sb.WriteString(" result: ")
						sb.WriteString(part.ToolResult.Result)
						hasContentToCount = true
					}
					totalTokens += int64(len(part.ToolResult.Images) * ImageTokenCostTiktoken)
					totalTokens += int64(len(part.ToolResult.Files) * DocTokenCostTiktoken)
				}

			case ir.ContentTypeRedactedThinking:
				// Estimate tokens for encrypted binary data
				if len(part.RedactedData) > 0 {
					totalTokens += int64(len(part.RedactedData) / 4)
				}
			}
		}

		// Process tool calls
		for j := range msg.ToolCalls {
			tc := &msg.ToolCalls[j]
			sb.WriteString("\nCall tool ")
			sb.WriteString(tc.Name)
			sb.WriteByte('(')
			if len(tc.Args) > TokenEstimationThreshold {
				// Flush prefix
				prefixStr := sb.String()
				if cached, ok := ContentTokenCache.Get(prefixStr); ok {
					totalTokens += int64(cached)
				} else {
					tokens := countTokens(enc, prefixStr)
					ContentTokenCache.Set(prefixStr, int(tokens))
					totalTokens += tokens
				}
				sb.Reset()

				totalTokens += estimateTokens(tc.Args)
				sb.WriteByte(')')
				// Flush suffix
				suffixStr := sb.String()
				if cached, ok := ContentTokenCache.Get(suffixStr); ok {
					totalTokens += int64(cached)
				} else {
					tokens := countTokens(enc, suffixStr)
					ContentTokenCache.Set(suffixStr, int(tokens))
					totalTokens += tokens
				}
				sb.Reset()
				hasContentToCount = false
			} else {
				sb.WriteString(tc.Args)
				sb.WriteByte(')')
				hasContentToCount = true
			}
			// ThoughtSignature in tool calls
			if len(tc.ThoughtSignature) > 0 {
				sb.Write(tc.ThoughtSignature)
				hasContentToCount = true
			}
		}

		// Final flush for this message
		if hasContentToCount && sb.Len() > 0 {
			contentStr := sb.String()
			if cached, ok := ContentTokenCache.Get(contentStr); ok {
				totalTokens += int64(cached)
			} else {
				tokens := countTokens(enc, contentStr)
				ContentTokenCache.Set(contentStr, int(tokens))
				totalTokens += tokens
			}
		}

	}

	totalTokens += countToolDefinitionsTokens(enc, req.Tools)

	if req.ResponseSchema != nil {
		if data, err := json.Marshal(req.ResponseSchema); err == nil {
			totalTokens += countJSONDataTokens(enc, data, ToolTokenCache)
		}
	}

	if len(req.MCPServers) > 0 {
		if data, err := json.Marshal(req.MCPServers); err == nil {
			totalTokens += countJSONDataTokens(enc, data, ToolTokenCache)
		}
	}

	if req.Prediction != nil && req.Prediction.Content != "" {
		if len(req.Prediction.Content) > TokenEstimationThreshold {
			totalTokens += estimateTokens(req.Prediction.Content)
		} else {
			totalTokens += countTokens(enc, req.Prediction.Content)
		}
	}

	totalTokens += 3

	return totalTokens
}

// estimateTokens returns a fast approximation of token count for large strings.
// Uses content-aware divisors for better accuracy:
//   - Base64 data: ~4.0 chars/token (dense encoding)
//   - JSON data: ~4.0 chars/token (structural overhead)
//   - Code: ~4.2 chars/token (keywords, symbols)
//   - Plain text: ~3.5 chars/token (default)
func estimateTokens(s string) int64 {
	divisor := detectContentDivisor(s)
	return int64(float64(len(s)) / divisor)
}

// detectContentDivisor analyzes content to determine the best divisor for estimation.
// Only samples the first 1KB for performance.
func detectContentDivisor(s string) float64 {
	// Sample first 1KB for content detection
	sample := s
	if len(s) > 1024 {
		sample = s[:1024]
	}

	// Check for base64 patterns (data URLs or pure base64)
	if isLikelyBase64(sample) {
		return 4.0
	}

	// Check for JSON patterns
	if isLikelyJSON(sample) {
		return 4.0
	}

	// Check for code patterns
	if isLikelyCode(sample) {
		return 4.2
	}

	// Default for plain text
	return 3.5
}

// isLikelyBase64 checks if content looks like base64 encoded data.
func isLikelyBase64(s string) bool {
	if len(s) < 50 {
		return false
	}
	// Check for data URL prefix
	if strings.HasPrefix(s, "data:") {
		return true
	}
	// Check for base64 character density (A-Za-z0-9+/=)
	base64Chars := 0
	for i := 0; i < len(s) && i < 200; i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
			base64Chars++
		}
	}
	// If >90% are base64 chars, likely base64
	return float64(base64Chars)/float64(min(len(s), 200)) > 0.9
}

// isLikelyJSON checks if content looks like JSON.
func isLikelyJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return false
	}
	// Check for JSON object or array start
	return (s[0] == '{' || s[0] == '[') && strings.ContainsAny(s[:min(len(s), 100)], "\":,")
}

// isLikelyCode checks if content looks like source code.
func isLikelyCode(s string) bool {
	// Check for common code patterns
	codeIndicators := []string{
		"func ", "function ", "def ", "class ", "import ", "package ",
		"const ", "var ", "let ", "if ", "for ", "while ", "return ",
		"public ", "private ", "static ", "void ", "int ", "string ",
		"{\n", "}\n", "();", "[];", "=>", "->", "//", "/*", "#include",
	}
	for _, indicator := range codeIndicators {
		if strings.Contains(s, indicator) {
			return true
		}
	}
	return false
}

func countTokens(enc tokenizer.Codec, s string) int64 {
	// Defense in depth: callers should check threshold before calling,
	// but we check again to prevent memory issues with large strings
	if len(s) > TokenEstimationThreshold {
		return estimateTokens(s)
	}
	ids, _, _ := enc.Encode(s)
	return int64(len(ids))
}

func countTokensWithCache(enc tokenizer.Codec, s string, cache *TokenCache) int64 {
	if cached, ok := cache.Get(s); ok {
		return int64(cached)
	}
	tokens := countTokens(enc, s)
	cache.Set(s, int(tokens))
	return tokens
}

// countRoleTokens counts tokens for a role string with caching.
func countRoleTokens(enc tokenizer.Codec, role string) int64 {
	roleTokenCacheMu.RLock()
	count, ok := roleTokenCache[role]
	roleTokenCacheMu.RUnlock()
	if ok {
		return count
	}

	ids, _, _ := enc.Encode(role)
	count = int64(len(ids))

	roleTokenCacheMu.Lock()
	roleTokenCache[role] = count
	roleTokenCacheMu.Unlock()

	return count
}

func getTiktokenCodec(encoding tokenizer.Encoding) (tokenizer.Codec, error) {
	tiktokenCacheMu.RLock()
	codec, ok := tiktokenCache[encoding]
	tiktokenCacheMu.RUnlock()
	if ok {
		return codec, nil
	}

	tiktokenCacheMu.Lock()
	defer tiktokenCacheMu.Unlock()

	if codec, ok := tiktokenCache[encoding]; ok {
		return codec, nil
	}

	codec, err := tokenizer.Get(encoding)
	if err != nil {
		return nil, err
	}

	tiktokenCache[encoding] = codec
	return codec, nil
}

func getTiktokenEncodingName(model string) tokenizer.Encoding {
	lower := strings.ToLower(model)

	switch {
	case strings.Contains(lower, "gpt-5"),
		strings.Contains(lower, "gpt-4o"),
		strings.Contains(lower, "claude"),
		strings.Contains(lower, "qwen"),
		strings.Contains(lower, "antigravity"):
		return tokenizer.O200kBase

	case strings.Contains(lower, "gpt-4"),
		strings.Contains(lower, "gpt-3.5"),
		strings.Contains(lower, "turbo"):
		return tokenizer.Cl100kBase

	default:
		return tokenizer.O200kBase
	}
}

func countToolDefinitionsTokens(enc tokenizer.Codec, tools []ir.ToolDefinition) int64 {
	if len(tools) == 0 {
		return 0
	}
	data, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return countJSONDataTokens(enc, data, ToolTokenCache)
}

func countJSONDataTokens(enc tokenizer.Codec, data []byte, cache *TokenCache) int64 {
	dataStr := string(data)
	if cache != nil {
		if cached, ok := cache.Get(dataStr); ok {
			return int64(cached)
		}
	}
	var tokens int64
	if len(data) > TokenEstimationThreshold {
		tokens = int64(float64(len(data)) / 4.0)
	} else {
		ids, _, _ := enc.Encode(dataStr)
		tokens = int64(len(ids))
	}
	if cache != nil {
		cache.Set(dataStr, int(tokens))
	}
	return tokens
}
