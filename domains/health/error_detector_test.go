package health

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestErrorDetector_Single500_TriggerL3Check tests single 500 error triggers L3 check
func TestErrorDetector_Single500_TriggerL3Check(t *testing.T) {
	detector := NewErrorDetector(3)

	event := ErrorEvent{
		CredentialID: "cred-1",
		StatusCode:   500,
		Error:        fmt.Errorf("internal server error"),
		Timestamp:    time.Now(),
	}

	result := detector.OnError(event)

	assert.Equal(t, 1, result.ConsecutiveFails, "Should have 1 consecutive failure")
	assert.True(t, result.ShouldTriggerL3, "500 should trigger L3 check")
	assert.False(t, result.ShouldMarkUnhealthy, "Single failure shouldn't mark Unhealthy")
	assert.Equal(t, "Degraded", result.RecommendedStatus)

	t.Logf("✓ 单次500错误触发L3检查")
}

// TestErrorDetector_Single503_TriggerQuotaRecovery tests 503 triggers quota logic
func TestErrorDetector_Single503_TriggerQuotaRecovery(t *testing.T) {
	detector := NewErrorDetector(3)

	event := ErrorEvent{
		CredentialID: "cred-1",
		StatusCode:   503,
		Error:        fmt.Errorf("service unavailable"),
		Timestamp:    time.Now(),
	}

	result := detector.OnError(event)

	assert.Equal(t, 1, result.ConsecutiveFails)
	assert.True(t, result.ShouldTriggerQuota, "503 should trigger quota recovery")
	assert.False(t, result.ShouldMarkUnhealthy)
	assert.Equal(t, "Degraded", result.RecommendedStatus)

	t.Logf("✓ 单次503错误触发Quota恢复逻辑")
}

// TestErrorDetector_Single504_TriggerLatencyCheck tests 504 classification
func TestErrorDetector_Single504_TriggerLatencyCheck(t *testing.T) {
	detector := NewErrorDetector(3)

	event := ErrorEvent{
		CredentialID: "cred-1",
		StatusCode:   504,
		Error:        fmt.Errorf("gateway timeout"),
		Timestamp:    time.Now(),
	}

	result := detector.OnError(event)

	assert.Equal(t, 1, result.ConsecutiveFails)
	assert.Equal(t, "Degraded", result.RecommendedStatus)

	t.Logf("✓ 单次504错误标记为Degraded")
}

// TestErrorDetector_Consecutive5xx_MarkUnhealthy tests 3 consecutive failures
func TestErrorDetector_Consecutive5xx_MarkUnhealthy(t *testing.T) {
	detector := NewErrorDetector(3)
	credID := "cred-1"

	// First error
	result1 := detector.OnError(ErrorEvent{
		CredentialID: credID,
		StatusCode:   500,
		Timestamp:    time.Now(),
	})
	assert.Equal(t, 1, result1.ConsecutiveFails)
	assert.Equal(t, "Degraded", result1.RecommendedStatus)
	assert.False(t, result1.ShouldMarkUnhealthy)

	// Second error
	result2 := detector.OnError(ErrorEvent{
		CredentialID: credID,
		StatusCode:   500,
		Timestamp:    time.Now(),
	})
	assert.Equal(t, 2, result2.ConsecutiveFails)
	assert.Equal(t, "Degraded", result2.RecommendedStatus)
	assert.False(t, result2.ShouldMarkUnhealthy)

	// Third error - should mark Unhealthy
	result3 := detector.OnError(ErrorEvent{
		CredentialID: credID,
		StatusCode:   500,
		Timestamp:    time.Now(),
	})
	assert.Equal(t, 3, result3.ConsecutiveFails)
	assert.Equal(t, "Unhealthy", result3.RecommendedStatus)
	assert.True(t, result3.ShouldMarkUnhealthy, "3 consecutive failures should mark Unhealthy")

	t.Logf("✓ 连续3次失败标记为Unhealthy")
}

// TestErrorDetector_SuccessReducesFailCount tests success reduces failure count
func TestErrorDetector_SuccessReducesFailCount(t *testing.T) {
	detector := NewErrorDetector(3)
	credID := "cred-1"

	// Two failures
	detector.OnError(ErrorEvent{CredentialID: credID, StatusCode: 500, Timestamp: time.Now()})
	detector.OnError(ErrorEvent{CredentialID: credID, StatusCode: 500, Timestamp: time.Now()})

	assert.Equal(t, 2, detector.GetConsecutiveFails(credID))

	// Success
	detector.OnSuccess(credID)
	assert.Equal(t, 1, detector.GetConsecutiveFails(credID), "Success should reduce fail count")

	// Another success
	detector.OnSuccess(credID)
	assert.Equal(t, 0, detector.GetConsecutiveFails(credID))

	t.Logf("✓ 成功请求递减失败计数")
}

// TestErrorDetector_ErrorsPerMinute tests sliding window error counter
func TestErrorDetector_ErrorsPerMinute(t *testing.T) {
	detector := NewErrorDetector(3)
	credID := "cred-1"

	// Generate 10 errors
	for i := 0; i < 10; i++ {
		detector.OnError(ErrorEvent{
			CredentialID: credID,
			StatusCode:   500,
			Timestamp:    time.Now(),
		})
		time.Sleep(10 * time.Millisecond) // Small delay
	}

	errorsPerMin := detector.GetErrorsPerMinute(credID)
	assert.Equal(t, 10, errorsPerMin, "Should count 10 errors in last minute")

	t.Logf("✓ 每分钟错误计数正常: %d", errorsPerMin)
}

// TestErrorDetector_ResetFailures tests complete reset
func TestErrorDetector_ResetFailures(t *testing.T) {
	detector := NewErrorDetector(3)
	credID := "cred-1"

	// Generate failures
	for i := 0; i < 5; i++ {
		detector.OnError(ErrorEvent{CredentialID: credID, StatusCode: 500, Timestamp: time.Now()})
	}

	assert.Equal(t, 5, detector.GetConsecutiveFails(credID))

	// Reset
	detector.ResetFailures(credID)

	assert.Equal(t, 0, detector.GetConsecutiveFails(credID), "Should reset to 0")

	t.Logf("✓ 失败计数完全重置")
}

// TestErrorDetector_TriggerFastCheck tests L1→L2→L3 fast check sequence
func TestErrorDetector_TriggerFastCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过快速检测集成测试")
	}

	detector := NewErrorDetector(3)

	// Test against Google DNS (should succeed)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Note: This is a TCP/HTTP test, inference will fail without valid API
	// We test the sequence logic, not the actual inference
	healthy, latency, err := detector.TriggerFastCheck(
		ctx,
		"8.8.8.8:53",                    // TCP address
		"http://httpbin.org/status/200", // HTTP health URL
		"http://httpbin.org/status/200", // Mock API URL (will fail at inference)
		"test-key",
		"gpt-4",
	)

	// TCP and HTTP should pass, inference will fail
	assert.False(t, healthy, "Should fail at inference step (no real API)")
	assert.Greater(t, latency, time.Duration(0))
	assert.Error(t, err)

	t.Logf("✓ 快速检测序列执行正常，延迟: %v", latency)
}

// TestErrorCounter_SlidingWindow tests sliding window behavior
func TestErrorCounter_SlidingWindow(t *testing.T) {
	counter := &ErrorCounter{}

	// Add 5 errors in first second
	for i := 0; i < 5; i++ {
		counter.Increment()
	}

	assert.Equal(t, 5, counter.Last1Min())

	// Advance 2 seconds
	time.Sleep(2 * time.Second)

	// Add 3 more errors
	for i := 0; i < 3; i++ {
		counter.Increment()
	}

	total := counter.Last1Min()
	assert.Equal(t, 8, total, "Should count all errors in window")

	t.Logf("✓ 滑动窗口计数正常: %d", total)
}

// TestErrorDetector_MultipleCredentials tests handling multiple credentials
func TestErrorDetector_MultipleCredentials(t *testing.T) {
	detector := NewErrorDetector(3)

	// Credential A: 2 failures
	detector.OnError(ErrorEvent{CredentialID: "cred-a", StatusCode: 500, Timestamp: time.Now()})
	detector.OnError(ErrorEvent{CredentialID: "cred-a", StatusCode: 500, Timestamp: time.Now()})

	// Credential B: 3 failures
	detector.OnError(ErrorEvent{CredentialID: "cred-b", StatusCode: 500, Timestamp: time.Now()})
	detector.OnError(ErrorEvent{CredentialID: "cred-b", StatusCode: 500, Timestamp: time.Now()})
	result := detector.OnError(ErrorEvent{CredentialID: "cred-b", StatusCode: 500, Timestamp: time.Now()})

	// Verify independent tracking
	assert.Equal(t, 2, detector.GetConsecutiveFails("cred-a"), "Cred A should have 2 failures")
	assert.Equal(t, 3, detector.GetConsecutiveFails("cred-b"), "Cred B should have 3 failures")
	assert.True(t, result.ShouldMarkUnhealthy, "Cred B should be Unhealthy")

	t.Logf("✓ 多凭据独立跟踪正常")
}

// BenchmarkErrorDetector_OnError benchmarks error event processing
func BenchmarkErrorDetector_OnError(b *testing.B) {
	detector := NewErrorDetector(3)
	event := ErrorEvent{
		CredentialID: "cred-1",
		StatusCode:   500,
		Error:        fmt.Errorf("error"),
		Timestamp:    time.Now(),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		detector.OnError(event)
	}
}

// BenchmarkErrorCounter_Increment benchmarks error counter increment
func BenchmarkErrorCounter_Increment(b *testing.B) {
	counter := &ErrorCounter{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		counter.Increment()
	}
}
