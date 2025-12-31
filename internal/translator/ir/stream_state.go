package ir

// StreamState defines the interface for streaming state management.
// Implementations track block indices, content accumulation, and state transitions
// during SSE streaming for different providers.
type StreamState interface {
	// MarkBlockStarted signals the start of a new content block.
	// blockType is the type of block (e.g., "text", "tool_use", "thinking")
	// index is the block index within the message
	MarkBlockStarted(blockType string, index int)

	// MarkBlockFinished signals the end of the current block.
	// Returns the index of the finished block.
	MarkBlockFinished() int

	// CurrentBlockType returns the type of the current active block.
	CurrentBlockType() string

	// CurrentBlockIndex returns the index of the current active block.
	CurrentBlockIndex() int

	// HasContent returns true if any content has been emitted.
	HasContent() bool

	// Reset clears all state for reuse.
	Reset()
}

// BaseStreamState provides a default implementation of StreamState.
// Providers can embed this or implement their own.
type BaseStreamState struct {
	blockType    string
	blockIndex   int
	blockStarted bool
	hasContent   bool
}

// MarkBlockStarted implements StreamState.
func (s *BaseStreamState) MarkBlockStarted(blockType string, index int) {
	s.blockType = blockType
	s.blockIndex = index
	s.blockStarted = true
}

// MarkBlockFinished implements StreamState.
func (s *BaseStreamState) MarkBlockFinished() int {
	idx := s.blockIndex
	s.blockStarted = false
	s.hasContent = true
	return idx
}

// CurrentBlockType implements StreamState.
func (s *BaseStreamState) CurrentBlockType() string {
	return s.blockType
}

// CurrentBlockIndex implements StreamState.
func (s *BaseStreamState) CurrentBlockIndex() int {
	return s.blockIndex
}

// HasContent implements StreamState.
func (s *BaseStreamState) HasContent() bool {
	return s.hasContent
}

// Reset implements StreamState.
func (s *BaseStreamState) Reset() {
	s.blockType = ""
	s.blockIndex = 0
	s.blockStarted = false
	s.hasContent = false
}

// IsBlockActive returns true if a block is currently being processed.
func (s *BaseStreamState) IsBlockActive() bool {
	return s.blockStarted
}

// StreamStatePool provides pooling for StreamState instances.
var streamStatePool = make(chan *BaseStreamState, 32)

// GetStreamState returns a StreamState from the pool or creates a new one.
func GetStreamState() *BaseStreamState {
	select {
	case s := <-streamStatePool:
		s.Reset()
		return s
	default:
		return &BaseStreamState{}
	}
}

// PutStreamState returns a StreamState to the pool.
func PutStreamState(s *BaseStreamState) {
	if s == nil {
		return
	}
	select {
	case streamStatePool <- s:
	default:
	}
}
