// Package transport provides shared HTTP transport configuration for the llm-mux API gateway.
// This package exists to break circular imports between executor and resilience packages.
package transport

import "time"

// Config holds optimized HTTP transport settings for API gateway workloads.
// These values are tuned for high-concurrency LLM API streaming.
//
// This is the single source of truth for transport configuration.
// Both executor and resilience packages import from here.
var Config = struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ExpectContinueTimeout time.Duration
	ResponseHeaderTimeout time.Duration
	DialTimeout           time.Duration
	KeepAlive             time.Duration
	// HTTP/2 specific settings
	H2ReadIdleTimeout            time.Duration
	H2PingTimeout                time.Duration
	H2StrictMaxConcurrentStreams bool
	H2AllowHTTP                  bool
}{
	// Connection pool settings - optimized for high concurrency
	MaxIdleConns:        1000, // Total idle connections across all hosts
	MaxIdleConnsPerHost: 100,  // Idle connections per host (default is 2)
	MaxConnsPerHost:     0,    // 0 = no limit, let HTTP/2 multiplex

	// Timeout settings
	IdleConnTimeout:       90 * time.Second,  // How long idle connections stay in pool
	TLSHandshakeTimeout:   10 * time.Second,  // TLS handshake timeout
	ExpectContinueTimeout: 1 * time.Second,   // 100-continue timeout
	ResponseHeaderTimeout: 600 * time.Second, // 10 minutes for large context processing
	DialTimeout:           30 * time.Second,  // TCP dial timeout
	KeepAlive:             30 * time.Second,  // TCP keep-alive interval

	// HTTP/2 settings for streaming stability
	H2ReadIdleTimeout:            30 * time.Second, // Ping if no data received
	H2PingTimeout:                15 * time.Second, // Wait for ping response
	H2StrictMaxConcurrentStreams: false,            // Don't limit concurrent streams strictly
	H2AllowHTTP:                  false,            // Require HTTPS for HTTP/2
}
