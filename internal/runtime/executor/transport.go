package executor

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/http2"
)

// TransportConfig holds optimized HTTP transport settings for API gateway workloads.
// These values are tuned for high-concurrency LLM API streaming.
//
// NOTE: This config is duplicated in internal/resilience/transport.go. We cannot consolidate
// due to circular import (executor imports resilience for retry). Keep values in sync.
var TransportConfig = struct {
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

// configureHTTP2 enables HTTP/2 with settings optimized for LLM streaming.
// HTTP/2 provides:
// - Multiplexing: multiple streams over a single TCP connection
// - Header compression: reduces overhead for repeated headers
// - Flow control: prevents fast sender from overwhelming slow receiver
// - Connection keep-alive via PING frames
func configureHTTP2(transport *http.Transport) {
	h2Transport, err := http2.ConfigureTransports(transport)
	if err != nil {
		return
	}
	// ReadIdleTimeout: If no data received within this time, send a PING
	// This detects dead connections faster than TCP keep-alive alone
	h2Transport.ReadIdleTimeout = TransportConfig.H2ReadIdleTimeout

	// PingTimeout: How long to wait for PING response before closing connection
	h2Transport.PingTimeout = TransportConfig.H2PingTimeout

	// StrictMaxConcurrentStreams: When false, allows more concurrent streams
	// per connection which improves throughput for many parallel requests
	h2Transport.StrictMaxConcurrentStreams = TransportConfig.H2StrictMaxConcurrentStreams

	// AllowHTTP: Whether to allow HTTP/2 over plain HTTP (h2c)
	h2Transport.AllowHTTP = TransportConfig.H2AllowHTTP
}

// newDialer creates a net.Dialer with optimized settings for LLM API connections.
func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   TransportConfig.DialTimeout,
		KeepAlive: TransportConfig.KeepAlive,
		// DualStack enables both IPv4 and IPv6 (Go 1.12+)
		DualStack: true,
	}
}

// baseTransport creates an http.Transport with all optimizations applied.
func baseTransport() *http.Transport {
	t := &http.Transport{
		// Connection pooling
		MaxIdleConns:        TransportConfig.MaxIdleConns,
		MaxIdleConnsPerHost: TransportConfig.MaxIdleConnsPerHost,
		MaxConnsPerHost:     TransportConfig.MaxConnsPerHost,
		IdleConnTimeout:     TransportConfig.IdleConnTimeout,

		// Timeouts
		TLSHandshakeTimeout:   TransportConfig.TLSHandshakeTimeout,
		ExpectContinueTimeout: TransportConfig.ExpectContinueTimeout,
		ResponseHeaderTimeout: TransportConfig.ResponseHeaderTimeout,

		// HTTP/2
		ForceAttemptHTTP2: true,

		// Compression - keep enabled for smaller payloads
		DisableCompression: false,

		// TLS configuration
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// Use modern cipher suites for better performance
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			},
		},

		// Write buffer - larger buffer for streaming
		WriteBufferSize: 64 * 1024, // 64KB write buffer
		ReadBufferSize:  64 * 1024, // 64KB read buffer
	}
	configureHTTP2(t)
	return t
}

// SharedTransport is the singleton transport for non-proxy requests.
// It is configured with HTTP/2 support and optimized connection pooling.
var SharedTransport = baseTransport()

func init() {
	SharedTransport.DialContext = newDialer().DialContext
}

// ProxyTransport creates a transport configured with an HTTP/HTTPS proxy.
func ProxyTransport(proxyURL *url.URL) *http.Transport {
	t := baseTransport()
	t.Proxy = http.ProxyURL(proxyURL)
	t.DialContext = newDialer().DialContext
	return t
}

// SOCKS5Transport creates a transport configured with a SOCKS5 proxy.
func SOCKS5Transport(dialFunc func(network, addr string) (net.Conn, error)) *http.Transport {
	t := baseTransport()
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		return dialFunc(network, addr)
	}
	return t
}

// CloseIdleConnections closes idle connections on the shared transport.
// Call this periodically to clean up stale connections.
func CloseIdleConnections() {
	SharedTransport.CloseIdleConnections()
}
