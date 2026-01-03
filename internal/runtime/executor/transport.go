package executor

import (
	"context"
	"crypto/tls"
	"golang.org/x/net/http2"
	"net"
	"net/http"
	"net/url"
	"time"
)

// TransportConfig holds optimized HTTP transport settings for API gateway workloads.
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
}{
	MaxIdleConns:          1000,
	MaxIdleConnsPerHost:   100,
	MaxConnsPerHost:       200,
	IdleConnTimeout:       90 * time.Second,
	TLSHandshakeTimeout:   10 * time.Second,
	ExpectContinueTimeout: 1 * time.Second,
	ResponseHeaderTimeout: 300 * time.Second,
	DialTimeout:           30 * time.Second,
	KeepAlive:             30 * time.Second,
}

func configureHTTP2(transport *http.Transport) {
	h2Transport, err := http2.ConfigureTransports(transport)
	if err != nil {
		return
	}
	h2Transport.ReadIdleTimeout = 30 * time.Second
	h2Transport.PingTimeout = 15 * time.Second
	h2Transport.StrictMaxConcurrentStreams = true
}

func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   TransportConfig.DialTimeout,
		KeepAlive: TransportConfig.KeepAlive,
	}
}

func baseTransport() *http.Transport {
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

var SharedTransport = baseTransport()

func init() {
	SharedTransport.DialContext = newDialer().DialContext
}

func ProxyTransport(proxyURL *url.URL) *http.Transport {
	t := baseTransport()
	t.Proxy = http.ProxyURL(proxyURL)
	return t
}

func SOCKS5Transport(dialFunc func(network, addr string) (net.Conn, error)) *http.Transport {
	t := baseTransport()
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		return dialFunc(network, addr)
	}
	return t
}
