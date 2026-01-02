package resilience

import (
	"github.com/sony/gobreaker"
)

// StreamingCircuitBreaker wraps gobreaker's TwoStepCircuitBreaker for streaming operations.
// Unlike the standard CircuitBreaker which wraps synchronous Execute(), this supports
// two-phase commit pattern where:
//   - Phase 1: Allow() checks if the request can proceed and returns a callback
//   - Phase 2: The callback is called when the stream completes (success/failure)
//
// This eliminates the need for hacky RecordSuccess/RecordFailure workarounds.
type StreamingCircuitBreaker struct {
	cb *gobreaker.TwoStepCircuitBreaker
}

// NewStreamingCircuitBreaker creates a new streaming-friendly circuit breaker.
func NewStreamingCircuitBreaker(cfg BreakerConfig) *StreamingCircuitBreaker {
	settings := gobreaker.Settings{
		Name:        cfg.Name,
		MaxRequests: cfg.MaxRequests,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			if counts.Requests < cfg.MinRequests {
				return false
			}
			if counts.ConsecutiveFailures >= cfg.FailureThreshold {
				return true
			}
			failureRatio := float64(counts.TotalFailures) / float64(counts.Requests)
			return failureRatio >= cfg.FailureRatio
		},
		OnStateChange: cfg.OnStateChange,
		IsSuccessful:  cfg.IsSuccessful,
	}
	return &StreamingCircuitBreaker{
		cb: gobreaker.NewTwoStepCircuitBreaker(settings),
	}
}

// Allow checks if the circuit breaker permits a request.
// Returns a done callback that MUST be called when the operation completes.
// The callback signature is: done(success bool)
//   - Call done(true) when the stream completes successfully
//   - Call done(false) when the stream fails or errors mid-stream
//
// Returns gobreaker.ErrOpenState if circuit is open.
// Returns gobreaker.ErrTooManyRequests if in half-open state with max requests.
func (s *StreamingCircuitBreaker) Allow() (done func(success bool), err error) {
	return s.cb.Allow()
}

// State returns the current state of the circuit breaker.
func (s *StreamingCircuitBreaker) State() gobreaker.State {
	return s.cb.State()
}

// Counts returns internal counters of the circuit breaker.
func (s *StreamingCircuitBreaker) Counts() gobreaker.Counts {
	return s.cb.Counts()
}

// Name returns the name of the circuit breaker.
func (s *StreamingCircuitBreaker) Name() string {
	return s.cb.Name()
}
