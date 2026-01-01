package resilience

import (
	"context"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
	"github.com/sony/gobreaker"
)

type RetryConfig struct {
	MaxRetries  int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	JitterDelay time.Duration
	ShouldRetry func(resp *http.Response, err error) bool
}

var DefaultRetryConfig = RetryConfig{
	MaxRetries:  3,
	BaseDelay:   500 * time.Millisecond,
	MaxDelay:    30 * time.Second,
	JitterDelay: 250 * time.Millisecond,
	ShouldRetry: func(resp *http.Response, err error) bool {
		if err != nil {
			return true
		}
		if resp == nil {
			return false
		}
		return resp.StatusCode == 429 || resp.StatusCode >= 500
	},
}

type BreakerConfig struct {
	Name             string
	MaxRequests      uint32
	Interval         time.Duration
	Timeout          time.Duration
	FailureThreshold uint32
	FailureRatio     float64
	MinRequests      uint32
	OnStateChange    func(name string, from, to gobreaker.State)
	IsSuccessful     func(err error) bool
}

// DefaultIsSuccessful is a callback to determine if an error should count as
// a circuit breaker failure. User errors should NOT trip the breaker.
// Set this from provider package during init to avoid import cycles.
var DefaultIsSuccessful func(err error) bool

func DefaultBreakerConfig(name string) BreakerConfig {
	isSuccessful := DefaultIsSuccessful
	if isSuccessful == nil {
		// Fallback: only nil errors are successful
		isSuccessful = func(err error) bool { return err == nil }
	}
	return BreakerConfig{
		Name:             name,
		MaxRequests:      3,
		Interval:         10 * time.Second,
		Timeout:          30 * time.Second,
		FailureThreshold: 5,
		FailureRatio:     0.5,
		MinRequests:      10,
		IsSuccessful:     isSuccessful,
	}
}

type CircuitBreaker struct {
	cb *gobreaker.CircuitBreaker
}

func NewCircuitBreaker(cfg BreakerConfig) *CircuitBreaker {
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
	return &CircuitBreaker{cb: gobreaker.NewCircuitBreaker(settings)}
}

func (c *CircuitBreaker) Execute(fn func() (any, error)) (any, error) {
	return c.cb.Execute(fn)
}

func (c *CircuitBreaker) State() gobreaker.State {
	return c.cb.State()
}

func (c *CircuitBreaker) Counts() gobreaker.Counts {
	return c.cb.Counts()
}

func (c *CircuitBreaker) Name() string {
	return c.cb.Name()
}

func NewRetryPolicy[R any](cfg RetryConfig) retrypolicy.RetryPolicy[R] {
	builder := retrypolicy.NewBuilder[R]().
		WithMaxRetries(cfg.MaxRetries).
		WithBackoff(cfg.BaseDelay, cfg.MaxDelay)
	if cfg.JitterDelay > 0 {
		builder = builder.WithJitter(cfg.JitterDelay)
	}
	return builder.Build()
}

type Executor[R any] struct {
	executor failsafe.Executor[R]
	breaker  *CircuitBreaker
}

func NewExecutor[R any](retryConfig RetryConfig, breakerConfig *BreakerConfig) *Executor[R] {
	rp := NewRetryPolicy[R](retryConfig)

	var breaker *CircuitBreaker
	if breakerConfig != nil {
		breaker = NewCircuitBreaker(*breakerConfig)
	}

	return &Executor[R]{
		executor: failsafe.With(rp),
		breaker:  breaker,
	}
}

func (e *Executor[R]) Execute(ctx context.Context, fn func() (R, error)) (R, error) {
	if e.breaker != nil {
		result, err := e.breaker.Execute(func() (any, error) {
			return e.executor.WithContext(ctx).Get(fn)
		})
		if err != nil {
			var zero R
			return zero, err
		}
		return result.(R), nil
	}
	return e.executor.WithContext(ctx).Get(fn)
}

func (e *Executor[R]) CircuitBreaker() *CircuitBreaker {
	return e.breaker
}

// CalculateBackoff computes exponential backoff with full jitter.
// Full jitter (industry standard) returns a random value between 0 and the
// calculated exponential delay, providing better load distribution than
// additive jitter.
//
// Formula: random(0, min(maxDelay, baseDelay * 2^attempt))
//
// The jitterDelay parameter is kept for API compatibility but is ignored
// when using full jitter. Set to 0 for explicit full jitter behavior.
func CalculateBackoff(attempt int, baseDelay, maxDelay, jitterDelay time.Duration) time.Duration {
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay)))
}

// CalculateBackoffNoJitter computes exponential backoff without jitter.
// Use this when deterministic delays are required (e.g., for testing).
func CalculateBackoffNoJitter(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	delay := baseDelay * time.Duration(1<<attempt)
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func WaitWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RetryBudget implements a token bucket to prevent retry storms.
// It limits the number of concurrent retries to avoid overwhelming
// upstream services when multiple requests fail simultaneously.
type RetryBudget struct {
	capacity    atomic.Int64
	maxCapacity int64
}

// NewRetryBudget creates a retry budget with the specified capacity.
// Typical values: 10-100 depending on expected concurrency.
func NewRetryBudget(maxCapacity int64) *RetryBudget {
	if maxCapacity <= 0 {
		maxCapacity = 50 // sensible default
	}
	rb := &RetryBudget{maxCapacity: maxCapacity}
	rb.capacity.Store(maxCapacity)
	return rb
}

// TryAcquire attempts to acquire a retry token.
// Returns true if a token was acquired, false if budget exhausted.
func (rb *RetryBudget) TryAcquire() bool {
	for {
		current := rb.capacity.Load()
		if current <= 0 {
			return false
		}
		if rb.capacity.CompareAndSwap(current, current-1) {
			return true
		}
	}
}

// Release returns a retry token to the budget.
// Should be called after a retry attempt completes (success or failure).
func (rb *RetryBudget) Release() {
	for {
		current := rb.capacity.Load()
		if current >= rb.maxCapacity {
			return
		}
		if rb.capacity.CompareAndSwap(current, current+1) {
			return
		}
	}
}

// Available returns the current number of available retry tokens.
func (rb *RetryBudget) Available() int64 {
	return rb.capacity.Load()
}

// MaxCapacity returns the maximum budget capacity.
func (rb *RetryBudget) MaxCapacity() int64 {
	return rb.maxCapacity
}
