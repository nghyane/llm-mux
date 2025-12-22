// Package executor provides common utilities for executor implementations.
package executor

import (
	"fmt"
	"io"
	"net/http"

	log "github.com/sirupsen/logrus"
)

// HTTPErrorResult contains the result of handling an HTTP error response.
// This standardizes error handling across all executors.
type HTTPErrorResult struct {
	Error      error
	StatusCode int
	Body       []byte
}

// HandleHTTPError reads error response body and returns categorized error.
// NOTE: This function does NOT close the response body. The caller is responsible
// for closing the body (typically via defer). This avoids double-close bugs when
// callers already have defer resp.Body.Close() set up.
// Parameters:
//   - resp: HTTP response to handle
//   - executorName: Name of the executor for logging (e.g., "claude executor")
// Returns:
//   - HTTPErrorResult with categorized error, status code, and body
// All executors should use this function instead of manual error handling to ensure:
// - Consistent error categorization
// - Standardized logging
func HandleHTTPError(resp *http.Response, executorName string) HTTPErrorResult {
	body, readErr := io.ReadAll(resp.Body)

	// Handle read errors (rare but possible)
	if readErr != nil {
		return HTTPErrorResult{
			Error:      fmt.Errorf("%s: failed to read error response body: %w", executorName, readErr),
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}

	// Log the error response
	log.Debugf("%s: error status: %d, body: %s", executorName, resp.StatusCode,
		summarizeErrorBody(resp.Header.Get("Content-Type"), body))

	// Create categorized error (consistent across all executors)
	return HTTPErrorResult{
		Error:      newCategorizedError(resp.StatusCode, string(body), nil),
		StatusCode: resp.StatusCode,
		Body:       body,
	}
}
