package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMetricsCollector(t *testing.T) {
	collector := NewMetricsCollector()
	assert.NotNil(t, collector)
	assert.NotNil(t, collector.errors)
	assert.False(t, collector.startTime.IsZero())
}

func TestMetricsCollector_Authentication(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("RecordSuccessfulAuth", func(t *testing.T) {
		collector.RecordAuthAttempt(true, 100*time.Millisecond)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(1), metrics.AuthAttempts)
		assert.Equal(t, int64(1), metrics.AuthSuccesses)
		assert.Equal(t, int64(0), metrics.AuthFailures)
		assert.Equal(t, 100.0, metrics.AuthSuccessRate)
	})

	t.Run("RecordFailedAuth", func(t *testing.T) {
		collector.Reset()
		collector.RecordAuthAttempt(false, 50*time.Millisecond)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(1), metrics.AuthAttempts)
		assert.Equal(t, int64(0), metrics.AuthSuccesses)
		assert.Equal(t, int64(1), metrics.AuthFailures)
		assert.Equal(t, 0.0, metrics.AuthSuccessRate)
	})

	t.Run("AverageLatency", func(t *testing.T) {
		collector.Reset()
		collector.RecordAuthAttempt(true, 100*time.Millisecond)
		collector.RecordAuthAttempt(true, 200*time.Millisecond)

		metrics := collector.GetMetrics()
		assert.Equal(t, 150.0, metrics.AuthAvgLatencyMs)
	})
}

func TestMetricsCollector_Tokens(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("RecordTokenValidation", func(t *testing.T) {
		collector.RecordTokenValidation(true)
		collector.RecordTokenValidation(false)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(2), metrics.TokenValidations)
		assert.Equal(t, int64(1), metrics.TokenValidSuccess)
		assert.Equal(t, int64(1), metrics.TokenValidFailure)
		assert.Equal(t, 50.0, metrics.TokenValidRate)
	})

	t.Run("RecordTokenRefresh", func(t *testing.T) {
		collector.Reset()
		collector.RecordTokenRefresh()
		collector.RecordTokenRefresh()

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(2), metrics.TokenRefreshes)
	})
}

func TestMetricsCollector_Users(t *testing.T) {
	collector := NewMetricsCollector()

	collector.RecordUserCreation()
	collector.RecordUserUpdate()
	collector.RecordUserDeletion()
	collector.RecordUserLookup()

	metrics := collector.GetMetrics()
	assert.Equal(t, int64(1), metrics.UserCreations)
	assert.Equal(t, int64(1), metrics.UserUpdates)
	assert.Equal(t, int64(1), metrics.UserDeletions)
	assert.Equal(t, int64(1), metrics.UserLookups)
}

func TestMetricsCollector_MFA(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("RecordMFAOperations", func(t *testing.T) {
		collector.RecordMFAEnabled()
		collector.RecordMFAVerification(true)
		collector.RecordMFAVerification(false)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(1), metrics.MFAEnabled)
		assert.Equal(t, int64(2), metrics.MFAVerifications)
		assert.Equal(t, int64(1), metrics.MFASuccesses)
		assert.Equal(t, int64(1), metrics.MFAFailures)
		assert.Equal(t, 50.0, metrics.MFASuccessRate)
	})
}

func TestMetricsCollector_Sessions(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("SessionLifecycle", func(t *testing.T) {
		collector.RecordSessionCreation()
		collector.RecordSessionCreation()
		collector.RecordSessionRefresh()

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(2), metrics.SessionCreations)
		assert.Equal(t, int64(1), metrics.SessionRefreshes)
		assert.Equal(t, int64(2), metrics.ActiveSessions)

		collector.RecordSessionDeletion()
		metrics = collector.GetMetrics()
		assert.Equal(t, int64(1), metrics.SessionDeletions)
		assert.Equal(t, int64(1), metrics.ActiveSessions)
	})
}

func TestMetricsCollector_RateLimit(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("RecordRateLimitChecks", func(t *testing.T) {
		collector.RecordRateLimitCheck(false)
		collector.RecordRateLimitCheck(true)

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(2), metrics.RateLimitChecks)
		assert.Equal(t, int64(1), metrics.RateLimitBlocked)
		assert.Equal(t, 50.0, metrics.RateLimitBlockRate)
	})
}

func TestMetricsCollector_Errors(t *testing.T) {
	collector := NewMetricsCollector()

	t.Run("RecordErrors", func(t *testing.T) {
		collector.RecordError("invalid_credentials")
		collector.RecordError("invalid_credentials")
		collector.RecordError("token_expired")

		metrics := collector.GetMetrics()
		assert.Equal(t, int64(2), metrics.Errors["invalid_credentials"])
		assert.Equal(t, int64(1), metrics.Errors["token_expired"])
	})
}

func TestMetricsCollector_Reset(t *testing.T) {
	collector := NewMetricsCollector()

	// Add some metrics
	collector.RecordAuthAttempt(true, 100*time.Millisecond)
	collector.RecordTokenValidation(true)
	collector.RecordError("test_error")

	// Reset
	collector.Reset()

	// Check all metrics are zero
	metrics := collector.GetMetrics()
	assert.Equal(t, int64(0), metrics.AuthAttempts)
	assert.Equal(t, int64(0), metrics.TokenValidations)
	assert.Empty(t, metrics.Errors)
}

func TestMetricsCollector_Uptime(t *testing.T) {
	collector := NewMetricsCollector()

	time.Sleep(100 * time.Millisecond)

	metrics := collector.GetMetrics()
	assert.True(t, metrics.Uptime >= 100*time.Millisecond)
}

func TestHealthChecker(t *testing.T) {
	t.Run("AllHealthy", func(t *testing.T) {
		checker := NewHealthChecker("http://casdoor:8000", "db", "redis")
		ctx := context.Background()

		status := checker.Check(ctx)
		assert.NotNil(t, status)
		assert.NotEmpty(t, status.Components)
		assert.False(t, status.Timestamp.IsZero())
	})

	t.Run("NoDatabase", func(t *testing.T) {
		checker := NewHealthChecker("http://casdoor:8000", nil, nil)
		ctx := context.Background()

		status := checker.Check(ctx)
		assert.False(t, status.Healthy)
		assert.Equal(t, "unhealthy", status.Status)
	})

	t.Run("NoCasdoor", func(t *testing.T) {
		checker := NewHealthChecker("", "db", nil)
		ctx := context.Background()

		status := checker.Check(ctx)
		assert.NotNil(t, status)
		assert.Equal(t, "not_configured", status.Components["casdoor"])
	})
}

func TestLogger(t *testing.T) {
	t.Run("NewLogger", func(t *testing.T) {
		logger := NewLogger(100)
		assert.NotNil(t, logger)
		assert.Equal(t, 100, logger.maxSize)
	})

	t.Run("LogEntry", func(t *testing.T) {
		logger := NewLogger(10)

		entry := LogEntry{
			Level:    "info",
			Message:  "Test message",
			UserID:   123,
			Username: "testuser",
			Action:   "login",
			Status:   "success",
		}

		logger.Log(entry)

		logs := logger.GetRecentLogs(1)
		require.Len(t, logs, 1)
		assert.Equal(t, "info", logs[0].Level)
		assert.Equal(t, "Test message", logs[0].Message)
		assert.False(t, logs[0].Timestamp.IsZero())
	})

	t.Run("MaxSize", func(t *testing.T) {
		logger := NewLogger(5)

		// Add 10 entries
		for i := 0; i < 10; i++ {
			logger.Log(LogEntry{
				Level:   "info",
				Message: "Test",
			})
		}

		// Should only keep last 5
		logs := logger.GetRecentLogs(100)
		assert.Len(t, logs, 5)
	})

	t.Run("GetRecentLogs", func(t *testing.T) {
		logger := NewLogger(100)

		// Add 5 entries
		for i := 0; i < 5; i++ {
			logger.Log(LogEntry{
				Level:   "info",
				Message: "Test",
			})
		}

		logs := logger.GetRecentLogs(3)
		assert.Len(t, logs, 3)
	})

	t.Run("Clear", func(t *testing.T) {
		logger := NewLogger(100)

		logger.Log(LogEntry{Level: "info", Message: "Test"})
		logger.Clear()

		logs := logger.GetRecentLogs(100)
		assert.Empty(t, logs)
	})
}

func TestLogLevels(t *testing.T) {
	logger := NewLogger(100)

	levels := []string{"debug", "info", "warn", "error"}
	for _, level := range levels {
		logger.Log(LogEntry{
			Level:   level,
			Message: "Test " + level,
		})
	}

	logs := logger.GetRecentLogs(10)
	assert.Len(t, logs, 4)
}

// BenchmarkMetricsCollector_RecordAuth benchmarks auth recording.
func BenchmarkMetricsCollector_RecordAuth(b *testing.B) {
	collector := NewMetricsCollector()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collector.RecordAuthAttempt(true, 100*time.Millisecond)
	}
}

// BenchmarkMetricsCollector_GetMetrics benchmarks metrics retrieval.
func BenchmarkMetricsCollector_GetMetrics(b *testing.B) {
	collector := NewMetricsCollector()

	// Add some data
	for i := 0; i < 100; i++ {
		collector.RecordAuthAttempt(true, 100*time.Millisecond)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		collector.GetMetrics()
	}
}

// TestConcurrentMetrics tests concurrent metric recording.
func TestConcurrentMetrics(t *testing.T) {
	collector := NewMetricsCollector()

	// Run concurrent updates
	concurrency := 10
	iterations := 100
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func() {
			for j := 0; j < iterations; j++ {
				collector.RecordAuthAttempt(true, 100*time.Millisecond)
				collector.RecordTokenValidation(true)
				collector.RecordError("test_error")
			}
			done <- true
		}()
	}

	// Wait for completion
	for i := 0; i < concurrency; i++ {
		<-done
	}

	metrics := collector.GetMetrics()
	expected := int64(concurrency * iterations)
	assert.Equal(t, expected, metrics.AuthAttempts)
	assert.Equal(t, expected, metrics.TokenValidations)
}
