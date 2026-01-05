package ir

// GeminiStreamParserState tracks state for parsing Gemini SSE stream.
// Buffers the last thinking event to allow attaching orphan signatures
// that arrive in subsequent chunks (Gemini Vertex sends signature after thinking text).
//
// Usage:
//
//	state := NewGeminiStreamParserState()
//	for chunk := range stream {
//	    events, err := ParseGeminiChunkWithState(chunk, state)
//	    // process events...
//	}
//	// At stream end, call Finalize() to get any buffered event
//	if finalEvent := state.Finalize(); finalEvent != nil {
//	    // process final event
//	}
type GeminiStreamParserState struct {
	// PendingThinkingEvent buffers the last thinking event.
	// We hold it until we know if the next chunk has a signature for it.
	PendingThinkingEvent *UnifiedEvent
}

// NewGeminiStreamParserState creates a new state for parsing Gemini streams.
func NewGeminiStreamParserState() *GeminiStreamParserState {
	return &GeminiStreamParserState{}
}

// BufferThinkingEvent stores a thinking event for later emission.
// Returns any previously buffered event that should be emitted now.
func (s *GeminiStreamParserState) BufferThinkingEvent(event *UnifiedEvent) *UnifiedEvent {
	prev := s.PendingThinkingEvent
	s.PendingThinkingEvent = event
	return prev
}

// AttachSignature attaches a signature to the buffered thinking event.
// Returns the completed event (with signature) and clears the buffer.
// Returns nil if no event is buffered.
func (s *GeminiStreamParserState) AttachSignature(sig []byte) *UnifiedEvent {
	if s.PendingThinkingEvent == nil {
		return nil
	}
	event := s.PendingThinkingEvent
	event.ThoughtSignature = sig
	s.PendingThinkingEvent = nil
	return event
}

// HasPendingEvent returns true if there's a buffered thinking event.
func (s *GeminiStreamParserState) HasPendingEvent() bool {
	return s.PendingThinkingEvent != nil
}

// FlushPending returns and clears any buffered thinking event.
// Call this before emitting non-thinking events.
func (s *GeminiStreamParserState) FlushPending() *UnifiedEvent {
	event := s.PendingThinkingEvent
	s.PendingThinkingEvent = nil
	return event
}

// Finalize returns any buffered thinking event at stream end.
// Call this at stream end to ensure no events are lost.
func (s *GeminiStreamParserState) Finalize() *UnifiedEvent {
	return s.FlushPending()
}
