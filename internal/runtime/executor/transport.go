package executor

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/url"

	"github.com/nghyane/llm-mux/internal/transport"
	"golang.org/x/net/http2"
)

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

func newDialer() *net.Dialer {
	return &net.Dialer{
		Timeout:   transport.Config.DialTimeout,
		KeepAlive: transport.Config.KeepAlive,
		DualStack: true,
	}
}

func baseTransport() *http.Transport {
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

var SharedTransport = baseTransport()

func init() {
	SharedTransport.DialContext = newDialer().DialContext
}

func ProxyTransport(proxyURL *url.URL) *http.Transport {
	t := baseTransport()
	t.Proxy = http.ProxyURL(proxyURL)
	t.DialContext = newDialer().DialContext
	return t
}

func SOCKS5Transport(dialFunc func(network, addr string) (net.Conn, error)) *http.Transport {
	t := baseTransport()
	t.DialContext = func(_ context.Context, network, addr string) (net.Conn, error) {
		return dialFunc(network, addr)
	}
	return t
}

func CloseIdleConnections() {
	SharedTransport.CloseIdleConnections()
}
