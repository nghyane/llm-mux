package ir

// Logprobs contains token log probability information.
// This replaces the `any` type for type safety.
type Logprobs struct {
	// Content contains log probabilities for each token in the content
	Content []TokenLogprob `json:"content,omitempty"`
}

// TokenLogprob contains the log probability for a single token.
type TokenLogprob struct {
	Token       string       `json:"token"`
	Logprob     float64      `json:"logprob"`
	Bytes       []int        `json:"bytes,omitempty"`
	TopLogprobs []TopLogprob `json:"top_logprobs,omitempty"`
}

// TopLogprob contains a top alternative token and its log probability.
type TopLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes,omitempty"`
}

// ToMap converts Logprobs to a map for JSON serialization.
// Used when provider needs raw map format.
func (l *Logprobs) ToMap() map[string]any {
	if l == nil || len(l.Content) == 0 {
		return nil
	}

	content := make([]map[string]any, len(l.Content))
	for i, c := range l.Content {
		tokenMap := map[string]any{
			"token":   c.Token,
			"logprob": c.Logprob,
		}
		if len(c.Bytes) > 0 {
			tokenMap["bytes"] = c.Bytes
		}
		if len(c.TopLogprobs) > 0 {
			tops := make([]map[string]any, len(c.TopLogprobs))
			for j, t := range c.TopLogprobs {
				topMap := map[string]any{
					"token":   t.Token,
					"logprob": t.Logprob,
				}
				if len(t.Bytes) > 0 {
					topMap["bytes"] = t.Bytes
				}
				tops[j] = topMap
			}
			tokenMap["top_logprobs"] = tops
		}
		content[i] = tokenMap
	}

	return map[string]any{"content": content}
}

// ParseLogprobs parses logprobs from raw JSON data.
// Returns nil if data is nil or cannot be parsed.
func ParseLogprobs(data any) *Logprobs {
	if data == nil {
		return nil
	}

	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}

	content, ok := m["content"].([]any)
	if !ok {
		return nil
	}

	result := &Logprobs{
		Content: make([]TokenLogprob, 0, len(content)),
	}

	for _, item := range content {
		if tokenMap, ok := item.(map[string]any); ok {
			tl := TokenLogprob{}
			if t, ok := tokenMap["token"].(string); ok {
				tl.Token = t
			}
			if lp, ok := tokenMap["logprob"].(float64); ok {
				tl.Logprob = lp
			}
			// Parse bytes and top_logprobs similarly...
			result.Content = append(result.Content, tl)
		}
	}

	return result
}
