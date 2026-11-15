package casdoor

import (
	"net"
	"net/http"
	"time"
)

// HTTPClientConfig holds HTTP client configuration for Casdoor API calls.
type HTTPClientConfig struct {
	// Maximum number of idle connections across all hosts
	MaxIdleConns int

	// Maximum number of idle connections per host
	MaxIdleConnsPerHost int

	// Maximum number of connections per host
	MaxConnsPerHost int

	// How long an idle connection remains idle before being closed
	IdleConnTimeout time.Duration

	// Timeout for establishing a new connection
	DialTimeout time.Duration

	// Timeout for TLS handshake
	TLSHandshakeTimeout time.Duration

	// Timeout for waiting for a response header
	ResponseHeaderTimeout time.Duration

	// Overall request timeout
	RequestTimeout time.Duration

	// Whether to disable HTTP keep-alive
	DisableKeepAlives bool

	// Whether to disable compression
	DisableCompression bool
}

// DefaultHTTPClientConfig returns the default HTTP client configuration
// optimized for Casdoor API calls.
func DefaultHTTPClientConfig() *HTTPClientConfig {
	return &HTTPClientConfig{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		DialTimeout:           10 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		RequestTimeout:        30 * time.Second,
		DisableKeepAlives:     false,
		DisableCompression:    false,
	}
}

// NewHTTPClient creates a new HTTP client with connection pooling
// optimized for Casdoor API calls.
func NewHTTPClient(config *HTTPClientConfig) *http.Client {
	if config == nil {
		config = DefaultHTTPClientConfig()
	}

	transport := &http.Transport{
		// Connection pooling settings
		MaxIdleConns:        config.MaxIdleConns,
		MaxIdleConnsPerHost: config.MaxIdleConnsPerHost,
		MaxConnsPerHost:     config.MaxConnsPerHost,
		IdleConnTimeout:     config.IdleConnTimeout,
		DisableKeepAlives:   config.DisableKeepAlives,
		DisableCompression:  config.DisableCompression,

		// Dial settings
		DialContext: (&net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,

		// TLS settings
		TLSHandshakeTimeout: config.TLSHandshakeTimeout,

		// Response settings
		ResponseHeaderTimeout: config.ResponseHeaderTimeout,

		// Force HTTP/2 (if available)
		ForceAttemptHTTP2: true,

		// Expect continue timeout for large requests
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   config.RequestTimeout,
	}
}

// NewHTTPClientWithDefaults creates an HTTP client with default configuration.
func NewHTTPClientWithDefaults() *http.Client {
	return NewHTTPClient(DefaultHTTPClientConfig())
}

// HTTPClientStats returns statistics about the HTTP transport.
type HTTPClientStats struct {
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int
	IdleConnTimeout     time.Duration
	RequestTimeout      time.Duration
}

// GetHTTPClientStats extracts statistics from an HTTP client.
func GetHTTPClientStats(client *http.Client) *HTTPClientStats {
	if client == nil || client.Transport == nil {
		return nil
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil
	}

	return &HTTPClientStats{
		MaxIdleConns:        transport.MaxIdleConns,
		MaxIdleConnsPerHost: transport.MaxIdleConnsPerHost,
		MaxConnsPerHost:     transport.MaxConnsPerHost,
		IdleConnTimeout:     transport.IdleConnTimeout,
		RequestTimeout:      client.Timeout,
	}
}
