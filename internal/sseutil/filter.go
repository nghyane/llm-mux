// Package sseutil provides shared SSE (Server-Sent Events) processing utilities.
// This package is designed to be imported by both executor and stream packages
// without creating circular dependencies.
package sseutil

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Global state for tracking stop chunks without usage (for deduplication)
var (
	stopChunkCache = &stopChunkTracker{
		entries: make(map[string]time.Time),
	}
)

// stopChunkTracker manages stop chunk deduplication with automatic cleanup
type stopChunkTracker struct {
	mu          sync.RWMutex
	entries     map[string]time.Time
	cleanupOnce sync.Once
}

func (t *stopChunkTracker) startCleanup() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			t.cleanup()
		}
	}()
}

func (t *stopChunkTracker) cleanup() {
	now := time.Now()
	t.mu.Lock()
	for traceID, expiry := range t.entries {
		if now.After(expiry) {
			delete(t.entries, traceID)
		}
	}
	t.mu.Unlock()
}

func (t *stopChunkTracker) remember(traceID string) {
	t.cleanupOnce.Do(t.startCleanup)
	t.mu.Lock()
	t.entries[traceID] = time.Now().Add(10 * time.Minute)
	t.mu.Unlock()
}

func (t *stopChunkTracker) checkAndRemove(traceID string, hasUsage bool) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	expiry, ok := t.entries[traceID]
	if ok && time.Now().Before(expiry) && hasUsage {
		delete(t.entries, traceID)
		return true
	}
	return false
}

// Pre-allocated byte slices for zero-copy comparisons
var (
	doneMarker  = []byte("[DONE]")
	dataPrefix  = []byte("data:")
	eventPrefix = []byte("event:")
)

// JSONPayload extracts JSON payload from SSE line.
// Returns nil if line is empty, [DONE], event:, or not valid JSON start.
func JSONPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, doneMarker) {
		return nil
	}
	if bytes.HasPrefix(trimmed, eventPrefix) {
		return nil
	}
	if bytes.HasPrefix(trimmed, dataPrefix) {
		trimmed = bytes.TrimSpace(trimmed[len(dataPrefix):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}

// FilterSSEUsageMetadata filters usage metadata from SSE payload.
// This removes intermediate usageMetadata chunks that don't have finishReason,
// keeping only the final chunk with both usage and finish reason.
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
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, dataPrefix) {
			outputLines = append(outputLines, line)
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, dataPrefix)
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
			stopChunkCache.remember(traceID)
			modified = true
			continue
		}
		if traceID != "" && stopChunkCache.checkAndRemove(traceID, hasUsageMetadata(rawJSON)) {
			modified = true
			continue
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			outputLines = append(outputLines, line)
			continue
		}

		// Rebuild line with cleaned JSON
		rebuilt := make([]byte, 0, len(line))
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, dataPrefix...)
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

// UnwrapEnvelope returns the inner content for gemini-cli wrapped responses.
// For gemini-cli format: {"response": {...}} -> returns the inner object bytes
// For gemini format: {...} -> returns original bytes
func UnwrapEnvelope(rawJSON []byte) []byte {
	if len(rawJSON) == 0 {
		return rawJSON
	}
	parsed := gjson.ParseBytes(rawJSON)
	if response := parsed.Get("response"); response.Exists() && response.IsObject() {
		return []byte(response.Raw)
	}
	return rawJSON
}

// WrapEnvelope wraps a Gemini JSON payload in a request envelope for gemini-cli.
// Input: {"contents": [...], "generationConfig": {...}}
// Output: {"request": {"contents": [...], "generationConfig": {...}}}
// Returns empty JSON object {} for empty or invalid payloads.
func WrapEnvelope(payload []byte) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || !gjson.ValidBytes(trimmed) {
		return []byte("{}")
	}
	wrapped, err := sjson.SetRawBytes([]byte("{}"), "request", trimmed)
	if err != nil {
		return []byte("{}")
	}
	return wrapped
}

// unwrapEnvelopeResult returns gjson Result for internal use
func unwrapEnvelopeResult(parsed gjson.Result) (content gjson.Result, pathPrefix string) {
	if response := parsed.Get("response"); response.Exists() && response.IsObject() {
		return response, "response."
	}
	return parsed, ""
}

// StripUsageMetadataFromJSON removes usageMetadata from JSON if not terminal (no finishReason).
// Handles both gemini ({candidates:...}) and gemini-cli ({response:{candidates:...}}) formats.
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	parsed := gjson.ParseBytes(jsonBytes)
	content, prefix := unwrapEnvelopeResult(parsed)

	// Check for terminal finish reason
	finishReason := content.Get("candidates.0.finishReason")
	if finishReason.Exists() && strings.TrimSpace(finishReason.String()) != "" {
		return rawJSON, false
	}

	// Check for usageMetadata
	if !content.Get("usageMetadata").Exists() {
		return rawJSON, false
	}

	// Remove usageMetadata using the correct path
	cleaned, _ := sjson.DeleteBytes(jsonBytes, prefix+"usageMetadata")
	return cleaned, true
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	parsed := gjson.ParseBytes(jsonBytes)
	content, _ := unwrapEnvelopeResult(parsed)
	return content.Get("usageMetadata").Exists()
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	parsed := gjson.ParseBytes(jsonBytes)
	content, _ := unwrapEnvelopeResult(parsed)

	finishReason := content.Get("candidates.0.finishReason")
	if !finishReason.Exists() || strings.TrimSpace(finishReason.String()) == "" {
		return false
	}
	return !content.Get("usageMetadata").Exists()
}

func ExtractPromptTokenCount(line []byte) int64 {
	payload := JSONPayload(line)
	if len(payload) == 0 {
		return 0
	}
	parsed := gjson.ParseBytes(payload)
	content, _ := unwrapEnvelopeResult(parsed)
	if v := content.Get("usageMetadata.promptTokenCount"); v.Exists() {
		return v.Int()
	}
	return 0
}

func ExtractCacheTokenCount(line []byte) int64 {
	payload := JSONPayload(line)
	if len(payload) == 0 {
		return 0
	}
	parsed := gjson.ParseBytes(payload)
	content, _ := unwrapEnvelopeResult(parsed)
	if v := content.Get("usageMetadata.cachedContentTokenCount"); v.Exists() {
		return v.Int()
	}
	return 0
}
