package health

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTCPChecker_Success tests successful TCP connection
func TestTCPChecker_Success(t *testing.T) {
	// Start a test TCP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().String()

	// Accept connections in background
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Test
	checker := NewTCPChecker(1 * time.Second)
	result := checker.Check(context.Background(), addr)

	assert.True(t, result.Success, "TCP check should succeed")
	assert.NoError(t, result.Error)
	assert.Greater(t, result.Latency, time.Duration(0))
	assert.Less(t, result.Latency, 100*time.Millisecond, "Should connect quickly to localhost")
	assert.False(t, result.CheckedAt.IsZero())

	t.Logf("✓ TCP连接成功，延迟: %v", result.Latency)
}

// withDial replaces the package dialTCP for the duration of t and restores it
// on cleanup. Tests use this to avoid depending on the host network (the
// default documentation-IP / invalid-hostname tests are flaky behind proxies).
func withDial(t *testing.T, fn func(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error)) {
	t.Helper()
	old := dialTCP
	dialTCP = fn
	t.Cleanup(func() { dialTCP = old })
}

// TestTCPChecker_Timeout tests TCP connection timeout using a deterministic
// fake dial that blocks until ctx/timeout fires.
func TestTCPChecker_Timeout(t *testing.T) {
	withDial(t, func(ctx context.Context, _, _ string, timeout time.Duration) (net.Conn, error) {
		// Honour the sooner of the dial timeout and the caller's ctx.
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, fmt.Errorf("i/o timeout")
		}
	})
	checker := NewTCPChecker(500 * time.Millisecond)

	start := time.Now()
	result := checker.Check(context.Background(), "203.0.113.1:9999")
	elapsed := time.Since(start)

	assert.False(t, result.Success, "TCP check should fail on timeout")
	require.Error(t, result.Error)
	assert.GreaterOrEqual(t, result.Latency, 400*time.Millisecond, "Should wait close to timeout")
	assert.Less(t, elapsed, 1*time.Second, "Should not exceed timeout significantly")

	t.Logf("✓ TCP超时检测正常，延迟: %v, 错误: %v", result.Latency, result.Error)
}

// TestTCPChecker_ConnectionRefused tests connection refused scenario
func TestTCPChecker_ConnectionRefused(t *testing.T) {
	withDial(t, func(ctx context.Context, _, addr string, _ time.Duration) (net.Conn, error) {
		return nil, fmt.Errorf("dial tcp %s: connect: connection refused", addr)
	})
	checker := NewTCPChecker(1 * time.Second)
	result := checker.Check(context.Background(), "127.0.0.1:9")

	assert.False(t, result.Success, "TCP check should fail on connection refused")
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "connection refused", "Should report connection refused")
	assert.Less(t, result.Latency, 100*time.Millisecond, "Should fail quickly")

	t.Logf("✓ TCP连接拒绝检测正常，延迟: %v", result.Latency)
}

// TestTCPChecker_InvalidAddress tests invalid address / unresolvable host.
func TestTCPChecker_InvalidAddress(t *testing.T) {
	withDial(t, func(ctx context.Context, _, addr string, _ time.Duration) (net.Conn, error) {
		return nil, fmt.Errorf("dial tcp: lookup %s: no such host", strings.Split(addr, ":")[0])
	})
	checker := NewTCPChecker(1 * time.Second)
	result := checker.Check(context.Background(), "invalid-host-that-does-not-exist-12345:443")

	assert.False(t, result.Success, "TCP check should fail on invalid address")
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "no such host")

	t.Logf("✓ 无效地址检测正常，错误: %v", result.Error)
}

// TestTCPChecker_ContextCancellation tests context cancellation
func TestTCPChecker_ContextCancellation(t *testing.T) {
	withDial(t, func(ctx context.Context, _, _ string, _ time.Duration) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	checker := NewTCPChecker(5 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := checker.Check(ctx, "203.0.113.1:9999")

	assert.False(t, result.Success)
	require.Error(t, result.Error)
	// Context cancellation may show as "context deadline exceeded" or "i/o timeout".
	errStr := result.Error.Error()
	hasDeadline := strings.Contains(errStr, "context deadline exceeded") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "operation was canceled")
	assert.True(t, hasDeadline, "expected cancellation/timeout error, got %q", errStr)
	assert.NotEmpty(t, errStr, "Should have error message")

	t.Logf("✓ Context取消检测正常")
}

// TestTCPChecker_WithRetry tests retry logic
func TestTCPChecker_WithRetry(t *testing.T) {
	// Start a server that accepts after a delay
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	checker := NewTCPChecker(1 * time.Second)
	result := checker.CheckWithRetry(context.Background(), addr, 3)

	assert.True(t, result.Success, "Should succeed with retry")
	assert.NoError(t, result.Error)

	t.Logf("✓ 重试逻辑正常")
}

// TestTCPChecker_RetryExhaustion tests retry exhaustion
func TestTCPChecker_RetryExhaustion(t *testing.T) {
	withDial(t, func(ctx context.Context, _, _ string, timeout time.Duration) (net.Conn, error) {
		<-time.After(timeout)
		return nil, fmt.Errorf("i/o timeout")
	})
	checker := NewTCPChecker(100 * time.Millisecond)

	start := time.Now()
	result := checker.CheckWithRetry(context.Background(), "203.0.113.1:9999", 2)
	elapsed := time.Since(start)

	assert.False(t, result.Success, "Should fail after retries")
	require.Error(t, result.Error)

	// Should take at least: 3 attempts × 100ms timeout + 2 delays
	assert.Greater(t, elapsed, 300*time.Millisecond, "Should attempt multiple times")

	t.Logf("✓ 重试耗尽检测正常，总耗时: %v", elapsed)
}

// TestPing_ConvenienceFunction tests the convenience Ping function
func TestPing_ConvenienceFunction(t *testing.T) {
	// Start test server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// Test convenience function
	latency, err := Ping(addr, 1*time.Second)

	assert.NoError(t, err)
	assert.Greater(t, latency, time.Duration(0))
	assert.Less(t, latency, 100*time.Millisecond)

	t.Logf("✓ Ping便捷函数正常，延迟: %v", latency)
}

// TestTCPChecker_DefaultTimeout tests default timeout value
func TestTCPChecker_DefaultTimeout(t *testing.T) {
	withDial(t, func(ctx context.Context, _, _ string, timeout time.Duration) (net.Conn, error) {
		<-time.After(timeout)
		return nil, fmt.Errorf("i/o timeout")
	})
	checker := NewTCPChecker(0) // Should use default 1s

	// Verify it uses a reasonable timeout by checking it doesn't hang forever
	done := make(chan struct{})
	go func() {
		checker.Check(context.Background(), "203.0.113.1:9999")
		close(done)
	}()

	select {
	case <-done:
		t.Log("✓ 默认超时配置正常")
	case <-time.After(2 * time.Second):
		t.Fatal("Default timeout should be ~1s, but check took >2s")
	}
}

// BenchmarkTCPChecker_Success benchmarks successful TCP checks
func BenchmarkTCPChecker_Success(b *testing.B) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer listener.Close()

	addr := listener.Addr().String()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	checker := NewTCPChecker(1 * time.Second)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := checker.Check(ctx, addr)
		if !result.Success {
			b.Fatalf("Check failed: %v", result.Error)
		}
	}
}

// TestTCPChecker_RealWorldScenario tests against common provider endpoints
func TestTCPChecker_RealWorldScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实网络测试")
	}

	testCases := []struct {
		name          string
		addr          string
		timeout       time.Duration
		shouldSucceed bool
	}{
		{
			name:          "Google DNS",
			addr:          "8.8.8.8:53",
			timeout:       5 * time.Second,
			shouldSucceed: true,
		},
		{
			name:          "Cloudflare DNS",
			addr:          "1.1.1.1:53",
			timeout:       5 * time.Second,
			shouldSucceed: true,
		},
		{
			name:          "Invalid port",
			addr:          "127.0.0.1:99999",
			timeout:       1 * time.Second,
			shouldSucceed: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			checker := NewTCPChecker(tc.timeout)
			result := checker.Check(context.Background(), tc.addr)

			if tc.shouldSucceed {
				assert.True(t, result.Success, "Expected success for %s", tc.name)
				t.Logf("✓ %s: 延迟 %v", tc.name, result.Latency)
			} else {
				assert.False(t, result.Success, "Expected failure for %s", tc.name)
				t.Logf("✓ %s: 预期失败，错误 %v", tc.name, result.Error)
			}
		})
	}
}

// Example demonstrates basic usage of TCPChecker
func ExampleTCPChecker_Check() {
	checker := NewTCPChecker(2 * time.Second)
	result := checker.Check(context.Background(), "8.8.8.8:53")

	if result.Success {
		fmt.Printf("TCP连接成功，延迟: %v\n", result.Latency)
	} else {
		fmt.Printf("TCP连接失败: %v\n", result.Error)
	}
}

// Example demonstrates the convenience Ping function
func ExamplePing() {
	latency, err := Ping("8.8.8.8:53", 2*time.Second)
	if err != nil {
		fmt.Printf("Ping失败: %v\n", err)
		return
	}
	fmt.Printf("Ping成功，延迟: %v\n", latency)
}
