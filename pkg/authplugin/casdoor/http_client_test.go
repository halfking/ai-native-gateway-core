package casdoor

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultHTTPClientConfig(t *testing.T) {
	config := DefaultHTTPClientConfig()

	assert.NotNil(t, config)
	assert.Equal(t, 100, config.MaxIdleConns)
	assert.Equal(t, 10, config.MaxIdleConnsPerHost)
	assert.Equal(t, 50, config.MaxConnsPerHost)
	assert.Equal(t, 90*time.Second, config.IdleConnTimeout)
	assert.Equal(t, 10*time.Second, config.DialTimeout)
	assert.Equal(t, 10*time.Second, config.TLSHandshakeTimeout)
	assert.Equal(t, 30*time.Second, config.ResponseHeaderTimeout)
	assert.Equal(t, 30*time.Second, config.RequestTimeout)
	assert.False(t, config.DisableKeepAlives)
	assert.False(t, config.DisableCompression)
}

func TestNewHTTPClient(t *testing.T) {
	t.Run("WithDefaultConfig", func(t *testing.T) {
		client := NewHTTPClient(nil)
		require.NotNil(t, client)
		assert.NotNil(t, client.Transport)
		assert.Equal(t, 30*time.Second, client.Timeout)

		// Check transport settings
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.Equal(t, 100, transport.MaxIdleConns)
		assert.Equal(t, 10, transport.MaxIdleConnsPerHost)
		assert.Equal(t, 50, transport.MaxConnsPerHost)
		assert.Equal(t, 90*time.Second, transport.IdleConnTimeout)
		assert.False(t, transport.DisableKeepAlives)
	})

	t.Run("WithCustomConfig", func(t *testing.T) {
		config := &HTTPClientConfig{
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   20,
			MaxConnsPerHost:       100,
			IdleConnTimeout:       120 * time.Second,
			DialTimeout:           5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			RequestTimeout:        60 * time.Second,
			DisableKeepAlives:     false,
			DisableCompression:    true,
		}

		client := NewHTTPClient(config)
		require.NotNil(t, client)
		assert.Equal(t, 60*time.Second, client.Timeout)

		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.Equal(t, 200, transport.MaxIdleConns)
		assert.Equal(t, 20, transport.MaxIdleConnsPerHost)
		assert.Equal(t, 100, transport.MaxConnsPerHost)
		assert.Equal(t, 120*time.Second, transport.IdleConnTimeout)
		assert.True(t, transport.DisableCompression)
	})

	t.Run("WithKeepAliveDisabled", func(t *testing.T) {
		config := &HTTPClientConfig{
			MaxIdleConns:        10,
			MaxIdleConnsPerHost: 5,
			DisableKeepAlives:   true,
			RequestTimeout:      10 * time.Second,
		}

		client := NewHTTPClient(config)
		require.NotNil(t, client)

		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.True(t, transport.DisableKeepAlives)
	})
}

func TestNewHTTPClientWithDefaults(t *testing.T) {
	client := NewHTTPClientWithDefaults()
	require.NotNil(t, client)
	assert.NotNil(t, client.Transport)
	assert.Equal(t, 30*time.Second, client.Timeout)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Equal(t, 100, transport.MaxIdleConns)
	assert.Equal(t, 10, transport.MaxIdleConnsPerHost)
}

func TestGetHTTPClientStats(t *testing.T) {
	t.Run("WithValidClient", func(t *testing.T) {
		config := &HTTPClientConfig{
			MaxIdleConns:        150,
			MaxIdleConnsPerHost: 15,
			MaxConnsPerHost:     75,
			IdleConnTimeout:     100 * time.Second,
			RequestTimeout:      45 * time.Second,
		}

		client := NewHTTPClient(config)
		stats := GetHTTPClientStats(client)

		require.NotNil(t, stats)
		assert.Equal(t, 150, stats.MaxIdleConns)
		assert.Equal(t, 15, stats.MaxIdleConnsPerHost)
		assert.Equal(t, 75, stats.MaxConnsPerHost)
		assert.Equal(t, 100*time.Second, stats.IdleConnTimeout)
		assert.Equal(t, 45*time.Second, stats.RequestTimeout)
	})

	t.Run("WithNilClient", func(t *testing.T) {
		stats := GetHTTPClientStats(nil)
		assert.Nil(t, stats)
	})

	t.Run("WithClientNoTransport", func(t *testing.T) {
		client := &http.Client{
			Timeout: 30 * time.Second,
		}
		stats := GetHTTPClientStats(client)
		assert.Nil(t, stats)
	})

	t.Run("WithNonStandardTransport", func(t *testing.T) {
		// Create a client with a custom transport
		type customTransport struct {
			http.RoundTripper
		}
		client := &http.Client{
			Transport: &customTransport{},
			Timeout:   30 * time.Second,
		}
		stats := GetHTTPClientStats(client)
		assert.Nil(t, stats)
	})
}

func TestHTTPClientConfiguration(t *testing.T) {
	t.Run("HTTP2Support", func(t *testing.T) {
		client := NewHTTPClientWithDefaults()
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.True(t, transport.ForceAttemptHTTP2)
	})

	t.Run("ExpectContinueTimeout", func(t *testing.T) {
		client := NewHTTPClientWithDefaults()
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.Equal(t, 1*time.Second, transport.ExpectContinueTimeout)
	})

	t.Run("DialerConfiguration", func(t *testing.T) {
		config := &HTTPClientConfig{
			DialTimeout: 15 * time.Second,
		}
		client := NewHTTPClient(config)
		transport, ok := client.Transport.(*http.Transport)
		require.True(t, ok)
		assert.NotNil(t, transport.DialContext)
	})
}

// BenchmarkHTTPClientCreation benchmarks HTTP client creation.
func BenchmarkHTTPClientCreation(b *testing.B) {
	b.Run("WithDefaults", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = NewHTTPClientWithDefaults()
		}
	})

	b.Run("WithCustomConfig", func(b *testing.B) {
		config := &HTTPClientConfig{
			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 20,
			RequestTimeout:      60 * time.Second,
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = NewHTTPClient(config)
		}
	})
}

// Example configurations for different scenarios
func TestHTTPClientScenarios(t *testing.T) {
	t.Run("HighTrafficScenario", func(t *testing.T) {
		// Configuration for high-traffic production environments
		config := &HTTPClientConfig{
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   50,
			MaxConnsPerHost:       100,
			IdleConnTimeout:       90 * time.Second,
			DialTimeout:           10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			RequestTimeout:        30 * time.Second,
		}

		client := NewHTTPClient(config)
		assert.NotNil(t, client)

		stats := GetHTTPClientStats(client)
		assert.Equal(t, 200, stats.MaxIdleConns)
		assert.Equal(t, 50, stats.MaxIdleConnsPerHost)
	})

	t.Run("LowLatencyScenario", func(t *testing.T) {
		// Configuration optimized for low latency
		config := &HTTPClientConfig{
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			MaxConnsPerHost:       50,
			IdleConnTimeout:       60 * time.Second,
			DialTimeout:           5 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			RequestTimeout:        15 * time.Second,
		}

		client := NewHTTPClient(config)
		assert.NotNil(t, client)
		assert.Equal(t, 15*time.Second, client.Timeout)
	})

	t.Run("ConservativeScenario", func(t *testing.T) {
		// Configuration for resource-constrained environments
		config := &HTTPClientConfig{
			MaxIdleConns:          20,
			MaxIdleConnsPerHost:   5,
			MaxConnsPerHost:       10,
			IdleConnTimeout:       30 * time.Second,
			DialTimeout:           10 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			RequestTimeout:        30 * time.Second,
		}

		client := NewHTTPClient(config)
		assert.NotNil(t, client)

		stats := GetHTTPClientStats(client)
		assert.Equal(t, 20, stats.MaxIdleConns)
		assert.Equal(t, 5, stats.MaxIdleConnsPerHost)
	})
}
