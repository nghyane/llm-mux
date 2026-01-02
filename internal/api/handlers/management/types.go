package management

import "time"

// UsageStatsResponse represents the structured usage statistics response.
type UsageStatsResponse struct {
	Counters UsageCounters            `json:"counters"`
	ByDay    []UsageDayStats          `json:"by_day,omitempty"`
	ByHour   []UsageHourStats         `json:"by_hour,omitempty"`
	ByAPI    map[string]UsageAPIStats `json:"by_api,omitempty"`
}

// UsageCounters holds the atomic counters.
type UsageCounters struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`
}

// UsageDayStats represents aggregated daily stats.
type UsageDayStats struct {
	Day      string `json:"day"` // YYYY-MM-DD
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}

// UsageHourStats represents aggregated hourly stats.
type UsageHourStats struct {
	Hour     int   `json:"hour"` // 0-23
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// UsageAPIStats represents aggregated per-API stats.
type UsageAPIStats struct {
	TotalRequests int64                      `json:"total_requests"`
	TotalTokens   int64                      `json:"total_tokens"`
	Models        map[string]UsageModelStats `json:"models,omitempty"`
}

// UsageModelStats represents aggregated per-model stats.
type UsageModelStats struct {
	TotalRequests int64 `json:"total_requests"`
	TotalTokens   int64 `json:"total_tokens"`
}

// ConfigUpdateResponse represents the response after updating config.
type ConfigUpdateResponse struct {
	Status  string   `json:"status"`
	Changed []string `json:"changed,omitempty"`
	Value   any      `json:"value,omitempty"`
}

// LogEntryResponse represents a single log line in API response.
type LogEntryResponse struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}
