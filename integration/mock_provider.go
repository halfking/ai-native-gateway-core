package integration

import (
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"
)

// MockProvider simulates a real LLM provider with configurable behavior.
//
// Behavior can be changed at runtime via SetBehavior, enabling complex
// failure-injection scenarios (slow, error, quota_exhausted, healthy).
type MockProvider struct {
	server *httptest.Server

	// Mutable behavior
	behavior atomic.Value // Behavior string

	// Configurable parameters
	latency   atomic.Int64 // nanoseconds
	errorRate atomic.Int64 // 0-100 (percentage)

	// Counters
	requestCount atomic.Int64
	errorCount   atomic.Int64
	successCount atomic.Int64

	// Quota state
	quotaExhausted atomic.Bool
	quotaResetAt   atomic.Int64 // unix nanos

	mu sync.RWMutex
}

// Behavior constants for mock providers.
const (
	BehaviorHealthy         = "healthy"
	BehaviorSlow            = "slow"
	BehaviorError           = "error"            // 500 Internal Server Error
	BehaviorQuotaExhausted  = "quota_exhausted"  // 429 Too Many Requests
	BehaviorServiceDown     = "service_down"     // 503 Service Unavailable
	BehaviorGatewayTimeout  = "gateway_timeout"  // 504 Gateway Timeout
	BehaviorContextExceeded = "context_exceeded" // 400 Bad Request (context)
)

// NewMockProvider creates a mock LLM provider with the given behavior.
func NewMockProvider(behavior string, latency time.Duration, errorRate int) *MockProvider {
	mp := &MockProvider{}
	mp.server = httptest.NewServer(http.HandlerFunc(mp.handle))
	mp.behavior.Store(behavior)
	mp.latency.Store(latency.Nanoseconds())
	mp.errorRate.Store(int64(errorRate))
	return mp
}

// URL returns the base URL of the mock provider.
func (mp *MockProvider) URL() string {
	return mp.server.URL
}

// Close shuts down the mock provider.
func (mp *MockProvider) Close() {
	mp.server.Close()
}

// SetBehavior changes the runtime behavior.
func (mp *MockProvider) SetBehavior(behavior string) {
	mp.behavior.Store(behavior)
}

// SetLatency changes the simulated response latency.
func (mp *MockProvider) SetLatency(latency time.Duration) {
	mp.latency.Store(latency.Nanoseconds())
}

// SetErrorRate changes the simulated error rate (0-100).
func (mp *MockProvider) SetErrorRate(rate int) {
	if rate < 0 {
		rate = 0
	}
	if rate > 100 {
		rate = 100
	}
	mp.errorRate.Store(int64(rate))
}

// SetQuotaExhausted toggles quota exhaustion state.
func (mp *MockProvider) SetQuotaExhausted(exhausted bool, resetAt time.Time) {
	mp.quotaExhausted.Store(exhausted)
	mp.quotaResetAt.Store(resetAt.UnixNano())
}

// Stats returns request statistics.
type ProviderStats struct {
	RequestCount int64
	ErrorCount   int64
	SuccessCount int64
	Behavior     string
}

// Stats returns the current request statistics.
func (mp *MockProvider) Stats() ProviderStats {
	return ProviderStats{
		RequestCount: mp.requestCount.Load(),
		ErrorCount:   mp.errorCount.Load(),
		SuccessCount: mp.successCount.Load(),
		Behavior:     mp.behavior.Load().(string),
	}
}

// Reset clears all counters.
func (mp *MockProvider) Reset() {
	mp.requestCount.Store(0)
	mp.errorCount.Store(0)
	mp.successCount.Store(0)
}

// handle is the HTTP request handler.
func (mp *MockProvider) handle(w http.ResponseWriter, r *http.Request) {
	mp.requestCount.Add(1)

	behavior := mp.behavior.Load().(string)
	latencyNs := mp.latency.Load()
	errorRate := mp.errorRate.Load()

	// Simulate latency
	if latencyNs > 0 {
		select {
		case <-time.After(time.Duration(latencyNs)):
		case <-r.Context().Done():
			return
		}
	}

	// Apply error rate (random)
	if errorRate > 0 {
		if rand.Intn(100) < int(errorRate) {
			mp.errorCount.Add(1)
			http.Error(w, `{"error":{"message":"random error"}}`, http.StatusInternalServerError)
			return
		}
	}

	// Apply specific behavior
	switch behavior {
	case BehaviorError:
		mp.errorCount.Add(1)
		http.Error(w, `{"error":{"message":"internal server error"}}`, http.StatusInternalServerError)

	case BehaviorQuotaExhausted:
		mp.errorCount.Add(1)
		resetAt := time.Unix(0, mp.quotaResetAt.Load())
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(time.Until(resetAt).Seconds())))
		http.Error(w, `{"error":{"message":"quota exhausted","type":"insufficient_quota"}}`, http.StatusTooManyRequests)

	case BehaviorServiceDown:
		mp.errorCount.Add(1)
		http.Error(w, `{"error":{"message":"service temporarily unavailable"}}`, http.StatusServiceUnavailable)

	case BehaviorGatewayTimeout:
		mp.errorCount.Add(1)
		http.Error(w, `{"error":{"message":"gateway timeout"}}`, http.StatusGatewayTimeout)

	case BehaviorContextExceeded:
		mp.errorCount.Add(1)
		http.Error(w, `{"error":{"message":"context_length_exceeded"}}`, http.StatusBadRequest)

	case BehaviorSlow:
		// Slow but successful
		fallthrough

	case BehaviorHealthy:
		fallthrough

	default:
		mp.successCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{
			"id": "chatcmpl-mock-%d",
			"object": "chat.completion",
			"created": %d,
			"model": "gpt-4",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Mock response"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`, mp.requestCount.Load(), time.Now().Unix())
	}
}
