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

	"github.com/nghyane/llm-mux/internal/transport"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

var (
	sharedTransport     *http.Transport
	sharedTransportOnce sync.Once
)

func SharedTransport() *http.Transport {
	sharedTransportOnce.Do(func() {
		sharedTransport = newBaseTransport()
		sharedTransport.DialContext = newDialer().DialContext
	})
	return sharedTransport
}

func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   transport.Config.DialTimeout,
		KeepAlive: transport.Config.KeepAlive,
		DualStack: true,
	}
}

func newBaseTransport() *http.Transport {
	t := &http.Transport{
		MaxIdleConns:        transport.Config.MaxIdleConns,
		MaxIdleConnsPerHost: transport.Config.MaxIdleConnsPerHost,
		MaxConnsPerHost:     transport.Config.MaxConnsPerHost,
		IdleConnTimeout:     transport.Config.IdleConnTimeout,

		TLSHandshakeTimeout:   transport.Config.TLSHandshakeTimeout,
		ExpectContinueTimeout: transport.Config.ExpectContinueTimeout,
		ResponseHeaderTimeout: transport.Config.ResponseHeaderTimeout,

		ForceAttemptHTTP2:  true,
		DisableCompression: false,

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

		WriteBufferSize: 64 * 1024,
		ReadBufferSize:  64 * 1024,
	}
	configureHTTP2(t)
	return t
}

func configureHTTP2(t *http.Transport) {
	h2Transport, err := http2.ConfigureTransports(t)
	if err != nil {
		return
	}
	h2Transport.ReadIdleTimeout = transport.Config.H2ReadIdleTimeout
	h2Transport.PingTimeout = transport.Config.H2PingTimeout
	h2Transport.StrictMaxConcurrentStreams = transport.Config.H2StrictMaxConcurrentStreams
	h2Transport.AllowHTTP = transport.Config.H2AllowHTTP
}

func ProxyTransport(proxyURL *url.URL) *http.Transport {
	t := newBaseTransport()
	t.Proxy = http.ProxyURL(proxyURL)
	t.DialContext = newDialer().DialContext
	return t
}

func SOCKS5Transport(dialFunc func(network, addr string) (net.Conn, error)) *http.Transport {
	t := newBaseTransport()
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		return dialFunc(network, addr)
	}
	return t
}

type TransportCache struct {
	mu    sync.RWMutex
	cache map[string]*http.Transport
}

func NewTransportCache() *TransportCache {
	return &TransportCache{
		cache: make(map[string]*http.Transport),
	}
}

func (c *TransportCache) Get(proxyURL string) *http.Transport {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cache[proxyURL]
}

func (c *TransportCache) GetOrCreate(proxyURLStr string) (*http.Transport, error) {
	if proxyURLStr == "" {
		return SharedTransport(), nil
	}

	c.mu.RLock()
	if t := c.cache[proxyURLStr]; t != nil {
		c.mu.RUnlock()
		return t, nil
	}
	c.mu.RUnlock()

	proxyURL, err := url.Parse(proxyURLStr)
	if err != nil {
		return nil, err
	}

	var t *http.Transport
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
		t = SOCKS5Transport(dialer.Dial)
	case "http", "https":
		t = ProxyTransport(proxyURL)
	default:
		return SharedTransport(), nil
	}

	c.mu.Lock()
	c.cache[proxyURLStr] = t
	c.mu.Unlock()

	return t, nil
}

func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	cache := globalTransportCache()
	t, err := cache.GetOrCreate(proxyURL)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: t,
		Timeout:   timeout,
	}, nil
}

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
