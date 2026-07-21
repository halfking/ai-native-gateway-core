package integration

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// callProviderN sends n HTTP GET requests to the URL and returns the count of
// successful responses (HTTP 200). It is used by tests that do not need
// per-provider stats.
func callProviderN(url string, n int) (success, failure int) {
	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < n; i++ {
		resp, err := client.Get(url)
		if err != nil {
			failure++
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			success++
		} else {
			failure++
		}
	}
	return
}

// TestMockProvider_Healthy tests healthy behavior.
func TestMockProvider_Healthy(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 10*time.Millisecond, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 5)
	stats := mp.Stats()
	assert.Equal(t, int64(5), stats.RequestCount)
	assert.Equal(t, int64(5), stats.SuccessCount)
	assert.Equal(t, int64(0), stats.ErrorCount)

	t.Logf("✓ Healthy provider: %d requests, %d success, %d errors",
		stats.RequestCount, stats.SuccessCount, stats.ErrorCount)
}

// TestMockProvider_5xxError tests 5xx error behavior.
func TestMockProvider_5xxError(t *testing.T) {
	mp := NewMockProvider(BehaviorError, 0, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 5)
	stats := mp.Stats()
	assert.Equal(t, int64(5), stats.RequestCount)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(5), stats.ErrorCount)

	t.Logf("✓ 5xx Error provider: %d requests, %d success, %d errors",
		stats.RequestCount, stats.SuccessCount, stats.ErrorCount)
}

// TestMockProvider_QuotaExhausted tests 429 quota behavior.
func TestMockProvider_QuotaExhausted(t *testing.T) {
	mp := NewMockProvider(BehaviorQuotaExhausted, 0, 0)
	mp.SetQuotaExhausted(true, time.Now().Add(60*time.Second))
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 3)
	stats := mp.Stats()
	assert.Equal(t, int64(3), stats.RequestCount)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(3), stats.ErrorCount)

	t.Logf("✓ Quota Exhausted: %d requests, %d errors", stats.RequestCount, stats.ErrorCount)
}

// TestMockProvider_ServiceDown tests 503 behavior.
func TestMockProvider_ServiceDown(t *testing.T) {
	mp := NewMockProvider(BehaviorServiceDown, 0, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 3)
	stats := mp.Stats()
	assert.Equal(t, int64(3), stats.RequestCount)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(3), stats.ErrorCount)

	t.Logf("✓ Service Down provider: %d requests, %d errors", stats.RequestCount, stats.ErrorCount)
}

// TestMockProvider_DynamicBehaviorChange tests behavior changes at runtime.
func TestMockProvider_DynamicBehaviorChange(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 0, 0)
	defer mp.Close()

	// T0: Healthy
	_, _ = callProviderN(mp.URL(), 3)
	stats := mp.Stats()
	assert.Equal(t, int64(3), stats.RequestCount)
	assert.Equal(t, int64(3), stats.SuccessCount)
	assert.Equal(t, int64(0), stats.ErrorCount)

	// T1: Switch to error
	mp.SetBehavior(BehaviorError)
	_, _ = callProviderN(mp.URL(), 3)
	stats = mp.Stats()
	assert.Equal(t, int64(6), stats.RequestCount) // cumulative
	assert.Equal(t, int64(3), stats.ErrorCount)

	// T2: Back to healthy
	mp.SetBehavior(BehaviorHealthy)
	_, _ = callProviderN(mp.URL(), 3)
	stats = mp.Stats()
	assert.Equal(t, int64(9), stats.RequestCount)
	assert.Equal(t, int64(6), stats.SuccessCount)
	assert.Equal(t, int64(3), stats.ErrorCount)

	t.Logf("✓ Dynamic behavior change: cumulative stats correct")
}

// TestMockProvider_LatencyChange tests dynamic latency changes.
func TestMockProvider_LatencyChange(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 50*time.Millisecond, 0)
	defer mp.Close()

	// Slow: ~50ms per request
	start := time.Now()
	_, _ = callProviderN(mp.URL(), 3)
	slowDuration := time.Since(start)

	// Switch to fast: 5ms per request
	mp.SetLatency(5 * time.Millisecond)
	start = time.Now()
	_, _ = callProviderN(mp.URL(), 3)
	fastDuration := time.Since(start)

	t.Logf("Slow 3 reqs: %v, Fast 3 reqs: %v", slowDuration, fastDuration)
	assert.True(t, slowDuration > fastDuration, "Slow should take longer than fast")
}

// TestMockProvider_ErrorRateRandom tests probabilistic error injection.
func TestMockProvider_ErrorRateRandom(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 0, 30) // 30% error rate
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 100)
	stats := mp.Stats()

	// With 30% error rate and 100 samples, expect ~30 errors (range: 15-45)
	assert.True(t, stats.ErrorCount >= 15 && stats.ErrorCount <= 45,
		"Error count %d should be in range [15, 45]", stats.ErrorCount)
	assert.Equal(t, int64(100), stats.RequestCount)
	assert.Equal(t, int64(100), stats.SuccessCount+stats.ErrorCount)

	t.Logf("✓ Random error rate (30%%): %d errors out of 100 requests", stats.ErrorCount)
}

// TestMockProvider_Reset tests counter reset.
func TestMockProvider_Reset(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 0, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 5)
	stats := mp.Stats()
	assert.Equal(t, int64(5), stats.RequestCount)

	mp.Reset()
	stats = mp.Stats()
	assert.Equal(t, int64(0), stats.RequestCount)
	assert.Equal(t, int64(0), stats.ErrorCount)
	assert.Equal(t, int64(0), stats.SuccessCount)

	t.Logf("✓ Reset clears counters")
}

// TestMockProvider_ConcurrentAccess tests thread safety.
func TestMockProvider_ConcurrentAccess(t *testing.T) {
	mp := NewMockProvider(BehaviorHealthy, 0, 0)
	defer mp.Close()

	const goroutines = 20
	const requestsPerGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			_, _ = callProviderN(mp.URL(), requestsPerGoroutine)
		}()
	}

	wg.Wait()

	stats := mp.Stats()
	expected := int64(goroutines * requestsPerGoroutine)
	assert.Equal(t, expected, stats.RequestCount)
	assert.Equal(t, expected, stats.SuccessCount)
	assert.Equal(t, int64(0), stats.ErrorCount)

	t.Logf("✓ Concurrent access safe: %d requests handled correctly", stats.RequestCount)
}

// TestMockProvider_GatewayTimeout tests 504 behavior.
func TestMockProvider_GatewayTimeout(t *testing.T) {
	mp := NewMockProvider(BehaviorGatewayTimeout, 0, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 3)
	stats := mp.Stats()
	assert.Equal(t, int64(3), stats.RequestCount)
	assert.Equal(t, int64(0), stats.SuccessCount)
	assert.Equal(t, int64(3), stats.ErrorCount)

	t.Logf("✓ Gateway Timeout provider: 3 errors")
}

// TestMockProvider_ContextExceeded tests 400 context exceeded behavior.
func TestMockProvider_ContextExceeded(t *testing.T) {
	mp := NewMockProvider(BehaviorContextExceeded, 0, 0)
	defer mp.Close()

	_, _ = callProviderN(mp.URL(), 3)
	stats := mp.Stats()
	assert.Equal(t, int64(3), stats.RequestCount)
	assert.Equal(t, int64(0), stats.SuccessCount)

	t.Logf("✓ Context Exceeded: 3 errors")
}

// BenchmarkMockProvider_Healthy benchmarks healthy provider throughput.
func BenchmarkMockProvider_Healthy(b *testing.B) {
	mp := NewMockProvider(BehaviorHealthy, 0, 0)
	defer mp.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(mp.URL())
		if err == nil {
			resp.Body.Close()
		}
	}
}
