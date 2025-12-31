package provider

// Error describes an authentication related failure in a provider agnostic format.
type Error struct {
	Code        string        `json:"code,omitempty"`
	Message     string        `json:"message"`
	Retryable   bool          `json:"retryable"`
	HTTPStatus  int           `json:"http_status,omitempty"`
	ErrCategory ErrorCategory `json:"category,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

// StatusCode implements optional status accessor for manager decision making.
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.HTTPStatus
}

// Category returns the error category for retry/fallback decisions.
// This enables categoryFromError to extract category without fallback to status code parsing.
func (e *Error) Category() ErrorCategory {
	if e == nil {
		return CategoryUnknown
	}
	return e.ErrCategory
}
