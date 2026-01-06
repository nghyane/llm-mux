package executor

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// UsageReporter handles usage metrics reporting for executor requests.
type UsageReporter struct {
	provider    string
	model       string
	authID      string
	authIndex   uint64
	apiKey      string
	source      string
	requestedAt time.Time
	once        sync.Once
}

// For internal compatibility
type usageReporter = UsageReporter

func NewUsageReporter(ctx context.Context, provider, model string, auth *provider.Auth) *UsageReporter {
	apiKey := apiKeyFromContext(ctx)
	reporter := &usageReporter{
		provider:    provider,
		model:       model,
		requestedAt: time.Now(),
		apiKey:      apiKey,
		source:      resolveUsageSource(auth, apiKey),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
	}
	return reporter
}

func (r *usageReporter) publish(ctx context.Context, u *ir.Usage) {
	r.publishWithOutcome(ctx, u, false)
}

// Publish implements stream.UsageReporter interface
func (r *usageReporter) Publish(ctx context.Context, u *ir.Usage) {
	r.publish(ctx, u)
}

func (r *usageReporter) publishFailure(ctx context.Context) {
	r.publishWithOutcome(ctx, nil, true)
}

// PublishFailure implements stream.UsageReporter interface
func (r *usageReporter) PublishFailure(ctx context.Context) {
	r.publishFailure(ctx)
}

func (r *usageReporter) trackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		if !isUserError(*errPtr) {
			r.publishFailure(ctx)
		}
	}
}

// TrackFailure is an exported alias for trackFailure.
func (r *UsageReporter) TrackFailure(ctx context.Context, errPtr *error) {
	r.trackFailure(ctx, errPtr)
}

func isUserError(err error) bool {
	if err == nil {
		return false
	}
	type statusCoder interface {
		StatusCode() int
	}
	if sc, ok := err.(statusCoder); ok {
		return sc.StatusCode() == 400
	}
	type categorizer interface {
		Category() provider.ErrorCategory
	}
	if cat, ok := err.(categorizer); ok {
		return cat.Category() == provider.CategoryUserError
	}
	return false
}

func (r *usageReporter) publishWithOutcome(ctx context.Context, u *ir.Usage, failed bool) {
	if r == nil {
		return
	}
	if u == nil && !failed {
		return
	}
	if u != nil && u.TotalTokens == 0 && u.PromptTokens == 0 && u.CompletionTokens == 0 && !failed {
		return
	}
	r.once.Do(func() {
		usage.PublishRecord(ctx, usage.Record{
			Provider:    r.provider,
			Model:       r.model,
			Source:      r.source,
			APIKey:      r.apiKey,
			AuthID:      r.authID,
			AuthIndex:   r.authIndex,
			RequestedAt: r.requestedAt,
			Failed:      failed,
			Usage:       u,
		})
	})
}

func (r *usageReporter) ensurePublished(ctx context.Context) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		usage.PublishRecord(ctx, usage.Record{
			Provider:    r.provider,
			Model:       r.model,
			Source:      r.source,
			APIKey:      r.apiKey,
			AuthID:      r.authID,
			AuthIndex:   r.authIndex,
			RequestedAt: r.requestedAt,
			Failed:      false,
			Usage:       nil,
		})
	})
}

// EnsurePublished implements stream.UsageReporter interface
func (r *usageReporter) EnsurePublished(ctx context.Context) {
	r.ensurePublished(ctx)
}

func apiKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("apiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

func resolveUsageSource(auth *provider.Auth, ctxAPIKey string) string {
	if auth != nil {
		provider := strings.TrimSpace(auth.Provider)
		if strings.EqualFold(provider, "gemini-cli") {
			if id := strings.TrimSpace(auth.ID); id != "" {
				return id
			}
		}
		if strings.EqualFold(provider, "vertex") {
			if auth.Metadata != nil {
				if projectID, ok := auth.Metadata["project_id"].(string); ok {
					if trimmed := strings.TrimSpace(projectID); trimmed != "" {
						return trimmed
					}
				}
				if project, ok := auth.Metadata["project"].(string); ok {
					if trimmed := strings.TrimSpace(project); trimmed != "" {
						return trimmed
					}
				}
			}
		}
		if _, value := auth.AccountInfo(); value != "" {
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if key := AttrStringValue(auth.Attributes, "api_key"); key != "" {
			return key
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return trimmed
	}
	return ""
}

func extractUsageFromClaudeResponse(data []byte) *ir.Usage {
	_, usage, err := to_ir.ParseClaudeResponse(data)
	if err != nil {
		return nil
	}
	return usage
}

// ExtractUsageFromClaudeResponse is an exported alias for extractUsageFromClaudeResponse.
func ExtractUsageFromClaudeResponse(data []byte) *ir.Usage {
	return extractUsageFromClaudeResponse(data)
}

func extractUsageFromOpenAIResponse(data []byte) *ir.Usage {
	_, usage, err := to_ir.ParseOpenAIResponse(data)
	if err != nil {
		return nil
	}
	return usage
}

// ExtractUsageFromOpenAIResponse is an exported alias for extractUsageFromOpenAIResponse.
func ExtractUsageFromOpenAIResponse(data []byte) *ir.Usage {
	return extractUsageFromOpenAIResponse(data)
}

func extractUsageFromGeminiResponse(data []byte) *ir.Usage {
	_, _, usage, err := to_ir.ParseGeminiResponse(data)
	if err != nil {
		return nil
	}
	return usage
}

// ExtractUsageFromGeminiResponse is an exported alias for extractUsageFromGeminiResponse.
func ExtractUsageFromGeminiResponse(data []byte) *ir.Usage {
	return extractUsageFromGeminiResponse(data)
}

var (
	stopChunkWithoutUsage = make(map[string]time.Time)
	stopChunkMutex        sync.RWMutex
	cleanupOnce           sync.Once
)

func initCleanup() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			stopChunkMutex.Lock()
			for traceID, expiry := range stopChunkWithoutUsage {
				if now.After(expiry) {
					delete(stopChunkWithoutUsage, traceID)
				}
			}
			stopChunkMutex.Unlock()
		}
	}()
}

func rememberStopWithoutUsage(traceID string) {
	cleanupOnce.Do(initCleanup)
	stopChunkMutex.Lock()
	stopChunkWithoutUsage[traceID] = time.Now().Add(10 * time.Minute)
	stopChunkMutex.Unlock()
}

func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	var outputLines [][]byte
	modified := false
	foundData := false

	for _, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			outputLines = append(outputLines, line)
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, []byte("data:"))
		if dataIdx < 0 {
			outputLines = append(outputLines, line)
			continue
		}
		rawJSON := bytes.TrimSpace(line[dataIdx+5:])
		if len(rawJSON) == 0 {
			outputLines = append(outputLines, line)
			continue
		}
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			modified = true
			continue
		}
		if traceID != "" {
			stopChunkMutex.Lock()
			expiry, ok := stopChunkWithoutUsage[traceID]
			if ok && time.Now().Before(expiry) && hasUsageMetadata(rawJSON) {
				delete(stopChunkWithoutUsage, traceID)
				stopChunkMutex.Unlock()
				modified = true
				continue
			}
			stopChunkMutex.Unlock()
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			outputLines = append(outputLines, line)
			continue
		}
		var rebuilt []byte
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, []byte("data:")...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		outputLines = append(outputLines, rebuilt)
		modified = true
	}
	if !modified {
		if !foundData {
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(outputLines, []byte("\n"))
}

func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	parsed := gjson.ParseBytes(jsonBytes)

	finishReason := parsed.Get("candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = parsed.Get("response.candidates.0.finishReason")
	}
	terminalReason := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""

	if terminalReason {
		return rawJSON, false
	}

	hasUsage := parsed.Get("usageMetadata").Exists()
	hasResponseUsage := parsed.Get("response.usageMetadata").Exists()

	if !hasUsage && !hasResponseUsage {
		return rawJSON, false
	}

	cleaned := jsonBytes
	var changed bool

	if hasUsage {
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}

	if hasResponseUsage {
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}

	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	parsed := gjson.ParseBytes(jsonBytes)
	return parsed.Get("usageMetadata").Exists() || parsed.Get("response.usageMetadata").Exists()
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	parsed := gjson.ParseBytes(jsonBytes)

	finishReason := parsed.Get("candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = parsed.Get("response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !parsed.Get("usageMetadata").Exists() && !parsed.Get("response.usageMetadata").Exists()
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}

// JSONPayload is an exported alias for jsonPayload.
func JSONPayload(line []byte) []byte {
	return jsonPayload(line)
}
