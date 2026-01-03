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
// These values are tuned for high-concurrency LLM API proxying.
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
}{
	MaxIdleConns:          1000,
	MaxIdleConnsPerHost:   100, // Default is 2, too low for API gateways
	MaxConnsPerHost:       200,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 300 * time.Second,
	DialTimeout:           30 * time.Second,
	KeepAlive:             30 * time.Second,
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
	}
}

// newBaseTransport creates a new http.Transport with optimized settings.
// It does NOT set DialContext - caller should set it based on use case.
func newBaseTransport() *http.Transport {
	t := &http.Transport{
		MaxIdleConns:          TransportConfig.MaxIdleConns,
		MaxIdleConnsPerHost:   TransportConfig.MaxIdleConnsPerHost,
		MaxConnsPerHost:       TransportConfig.MaxConnsPerHost,
		IdleConnTimeout:       TransportConfig.IdleConnTimeout,
		TLSHandshakeTimeout:   TransportConfig.TLSHandshakeTimeout,
		ExpectContinueTimeout: TransportConfig.ExpectContinueTimeout,
		ResponseHeaderTimeout: TransportConfig.ResponseHeaderTimeout,
		ForceAttemptHTTP2:     true,
		DisableCompression:    false,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	configureHTTP2(t)
	return t
}

// configureHTTP2 enables HTTP/2 with optimized settings.
func configureHTTP2(transport *http.Transport) {
	h2Transport, err := http2.ConfigureTransports(transport)
	if err != nil {
		return
	}
	h2Transport.ReadIdleTimeout = 30 * time.Second
	h2Transport.PingTimeout = 15 * time.Second
	h2Transport.StrictMaxConcurrentStreams = true
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
