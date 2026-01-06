package usage

import "time"

// AggregatedStats represents summary statistics for a time period.
type AggregatedStats struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
}

// DailyStats represents aggregated metrics for a single day.
type DailyStats struct {
	Day      string `json:"day"` // Format: "2006-01-02"
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// HourlyStats represents aggregated metrics for an hour of the day.
type HourlyStats struct {
	Hour     int   `json:"hour"` // 0-23
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// ProviderStats represents aggregated metrics per provider.
type ProviderStats struct {
	Provider        string   `json:"provider"`
	Requests        int64    `json:"requests"`
	SuccessCount    int64    `json:"success_count"`
	FailureCount    int64    `json:"failure_count"`
	InputTokens     int64    `json:"input_tokens"`
	OutputTokens    int64    `json:"output_tokens"`
	ReasoningTokens int64    `json:"reasoning_tokens"`
	TotalTokens     int64    `json:"total_tokens"`
	AccountCount    int64    `json:"account_count"`
	Models          []string `json:"models"`
}

// AuthStats represents aggregated metrics per auth account.
type AuthStats struct {
	Provider        string `json:"provider"`
	AuthID          string `json:"auth_id"`
	Requests        int64  `json:"requests"`
	SuccessCount    int64  `json:"success_count"`
	FailureCount    int64  `json:"failure_count"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
}

// ModelStats represents aggregated metrics per model.
type ModelStats struct {
	Model           string `json:"model"`
	Provider        string `json:"provider"`
	Requests        int64  `json:"requests"`
	SuccessCount    int64  `json:"success_count"`
	FailureCount    int64  `json:"failure_count"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	ReasoningTokens int64  `json:"reasoning_tokens"`
	TotalTokens     int64  `json:"total_tokens"`
}

// DetailRecord represents a single recent request for detailed views.
type DetailRecord struct {
	APIKey      string     `json:"api_key"`
	Model       string     `json:"model"`
	Provider    string     `json:"provider"`
	RequestedAt time.Time  `json:"requested_at"`
	Source      string     `json:"source"`
	AuthIndex   uint64     `json:"auth_index"`
	Failed      bool       `json:"failed"`
	Tokens      TokenStats `json:"tokens"`
}

// UsageSnapshot combines counters with database query results
// for the GET /usage API response.
type UsageSnapshot struct {
	// From atomic counters (instant)
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	// From database queries
	RequestsByDay  map[string]int64 `json:"requests_by_day,omitempty"`
	RequestsByHour map[string]int64 `json:"requests_by_hour,omitempty"`
	TokensByDay    map[string]int64 `json:"tokens_by_day,omitempty"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour,omitempty"`

	// API breakdown (built dynamically from database queries)
	APIs map[string]interface{} `json:"apis,omitempty"`
}
