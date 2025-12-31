package ir

// ContentFilterResult contains content safety filtering results.
// This replaces the `any` type for type safety.
type ContentFilterResult struct {
	// Ratings contains safety ratings for different categories
	Ratings []*SafetyRating `json:"ratings,omitempty"`

	// BlockedBy indicates which category blocked generation
	BlockedBy string `json:"blocked_by,omitempty"`

	// BlockReason provides the reason for blocking
	BlockReason string `json:"block_reason,omitempty"`

	// Filtered indicates if any content was filtered
	Filtered bool `json:"filtered,omitempty"`
}

// ContentFilterCategory constants for safety categories
const (
	FilterCategoryHateSpeech = "hate"
	FilterCategorySelfHarm   = "self-harm"
	FilterCategorySexual     = "sexual"
	FilterCategoryViolence   = "violence"
	FilterCategoryHarassment = "harassment"
	FilterCategoryDangerous  = "dangerous"
)

// ToMap converts ContentFilterResult to a map for JSON serialization.
func (c *ContentFilterResult) ToMap() map[string]any {
	if c == nil {
		return nil
	}

	result := map[string]any{}

	if len(c.Ratings) > 0 {
		ratings := make([]map[string]any, len(c.Ratings))
		for i, r := range c.Ratings {
			ratings[i] = map[string]any{
				"category":    r.Category,
				"probability": r.Probability,
				"blocked":     r.Blocked,
			}
			if r.Severity != "" {
				ratings[i]["severity"] = r.Severity
			}
		}
		result["ratings"] = ratings
	}

	if c.BlockedBy != "" {
		result["blocked_by"] = c.BlockedBy
	}
	if c.BlockReason != "" {
		result["block_reason"] = c.BlockReason
	}
	if c.Filtered {
		result["filtered"] = c.Filtered
	}

	return result
}

// ParseContentFilter parses content filter from raw JSON data.
func ParseContentFilter(data any) *ContentFilterResult {
	if data == nil {
		return nil
	}

	m, ok := data.(map[string]any)
	if !ok {
		return nil
	}

	result := &ContentFilterResult{}

	if blocked, ok := m["blocked_by"].(string); ok {
		result.BlockedBy = blocked
	}
	if reason, ok := m["block_reason"].(string); ok {
		result.BlockReason = reason
	}
	if filtered, ok := m["filtered"].(bool); ok {
		result.Filtered = filtered
	}

	// Parse ratings if present
	if ratings, ok := m["ratings"].([]any); ok {
		result.Ratings = make([]*SafetyRating, 0, len(ratings))
		for _, r := range ratings {
			if rm, ok := r.(map[string]any); ok {
				sr := &SafetyRating{}
				if cat, ok := rm["category"].(string); ok {
					sr.Category = cat
				}
				if prob, ok := rm["probability"].(string); ok {
					sr.Probability = prob
				}
				if blocked, ok := rm["blocked"].(bool); ok {
					sr.Blocked = blocked
				}
				result.Ratings = append(result.Ratings, sr)
			}
		}
	}

	return result
}

// IsBlocked returns true if content was blocked by safety filters.
func (c *ContentFilterResult) IsBlocked() bool {
	if c == nil {
		return false
	}
	if c.BlockedBy != "" || c.Filtered {
		return true
	}
	for _, r := range c.Ratings {
		if r != nil && r.Blocked {
			return true
		}
	}
	return false
}
