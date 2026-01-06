// Package resilience provides unified HTTP transport, retry, and circuit breaker
// functionality for the llm-mux API gateway.
package resilience

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// TransportConfig holds optimized HTTP transport settings for API gateway workloads.
// These values are tuned for high-concurrency LLM API streaming.
//
// NOTE: This is a duplicate of executor.TransportConfig. We cannot import executor here
// due to circular import (executor imports resilience for retry). Keep values in sync with
// internal/runtime/executor/transport.go.
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

// sharedTransport is the singleton transport for non-proxy requests.
var (
	sharedTransport     *http.Transport
	sharedTransportOnce sync.Once
)

// SharedTransport returns the singleton optimized transport for non-proxy requests.
// It is configured with HTTP/2 support and connection pooling.
func SharedTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		sharedTransport = newBaseTransport()
		sharedTransport.DialContext = newDialer().DialContext
	})
	return sharedTransport
}

// newDialer creates a net.Dialer with optimized timeout and keep-alive settings.
func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   TransportConfig.DialTimeout,
		KeepAlive: TransportConfig.KeepAlive,
		DualStack: true, // Enable both IPv4 and IPv6
	}
}

// newBaseTransport creates a new http.Transport with optimized settings.
// It does NOT set DialContext - caller should set it based on use case.
func newBaseTransport() *http.Transport {
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

		// Compression
		DisableCompression: false,

		// TLS configuration
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			},
		},

		// Buffer sizes for streaming
		WriteBufferSize: 64 * 1024,
		ReadBufferSize:  64 * 1024,
	}
	configureHTTP2(t)
	return t
}

// configureHTTP2 enables HTTP/2 with settings optimized for LLM streaming.
func configureHTTP2(transport *http.Transport) {
	h2Transport, err := http2.ConfigureTransports(transport)
	if err != nil {
		return
	}
	h2Transport.ReadIdleTimeout = TransportConfig.H2ReadIdleTimeout
	h2Transport.PingTimeout = TransportConfig.H2PingTimeout
	h2Transport.StrictMaxConcurrentStreams = TransportConfig.H2StrictMaxConcurrentStreams
	h2Transport.AllowHTTP = TransportConfig.H2AllowHTTP
}

// ProxyTransport creates a new transport configured with an HTTP/HTTPS proxy.
func ProxyTransport(proxyURL *url.URL) *http.Transport {
	t := newBaseTransport()
	t.Proxy = http.ProxyURL(proxyURL)
	t.DialContext = newDialer().DialContext
	return t
}

// SOCKS5Transport creates a new transport configured with a SOCKS5 proxy.
func SOCKS5Transport(dialFunc func(network, addr string) (net.Conn, error)) *http.Transport {
	t := newBaseTransport()
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		return dialFunc(network, addr)
	}
	return t
}

// TransportCache provides thread-safe caching of transports by proxy URL.
// This prevents creating duplicate transports for the same proxy.
type TransportCache struct {
	mu    sync.RWMutex
	cache map[string]*http.Transport
}

// NewTransportCache creates a new transport cache.
func NewTransportCache() *TransportCache {
	return &TransportCache{
		cache: make(map[string]*http.Transport),
	}
}

// Get returns a cached transport for the proxy URL, or nil if not cached.
func (c *TransportCache) Get(proxyURL string) *http.Transport {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache[proxyURL]
}

// GetOrCreate returns a cached transport or creates and caches a new one.
func (c *TransportCache) GetOrCreate(proxyURLStr string) (*http.Transport, error) {
	if proxyURLStr == "" {
		return SharedTransport(), nil
	}

	// Check cache first
	c.mu.RLock()
	if t := c.cache[proxyURLStr]; t != nil {
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()

	// Parse and create transport
	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, err
	}

	var transport *http.Transport
	switch proxyURL.Scheme {
	case "socks5":
		var proxyAuth *proxy.Auth
		if proxyURL.User != nil {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
		if err != nil {
			return nil, err
		}
		transport = SOCKS5Transport(dialer.Dial)
	case "http", "https":
		transport = ProxyTransport(proxyURL)
	default:
		// No proxy, use shared transport
		return SharedTransport(), nil
	}

	// Cache the transport
	c.mu.Lock()
	c.cache[proxyURLStr] = transport
	c.mu.Unlock()

	return transport, nil
}

// NewHTTPClient creates an http.Client with the appropriate transport.
// If proxyURL is empty, uses the shared transport.
func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	cache := globalTransportCache()
	transport, err := cache.GetOrCreate(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

// globalCache is a singleton transport cache.
var (
	globalCache     *TransportCache
	globalCacheOnce sync.Once
)

func globalTransportCache() *TransportCache {
	globalCacheOnce.Do(func() {
		globalCache = NewTransportCache()
	})
	return globalCache
}
