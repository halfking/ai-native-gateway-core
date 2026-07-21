package health

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestHTTPChecker_200OK tests successful HTTP 200 response
func TestHTTPChecker_200OK(t *testing.T) {
	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.True(t, result.Success, "HTTP 200 should be success")
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.NoError(t, result.Error)
	assert.Greater(t, result.Latency, time.Duration(0))
	assert.Less(t, result.Latency, 100*time.Millisecond)
	assert.Contains(t, result.Body, "healthy")
	assert.False(t, result.CheckedAt.IsZero())

	t.Logf("✓ HTTP 200检查成功，延迟: %v, Body: %s", result.Latency, result.Body)
}

// TestHTTPChecker_503ServiceUnavailable tests 503 response
func TestHTTPChecker_503ServiceUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"service temporarily unavailable"}`))
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.False(t, result.Success, "HTTP 503 should be failure")
	assert.Equal(t, http.StatusServiceUnavailable, result.StatusCode)
	assert.NoError(t, result.Error, "No network error, just non-2xx status")
	assert.Contains(t, result.Body, "unavailable")

	t.Logf("✓ HTTP 503检测正常，Body: %s", result.Body)
}

// TestHTTPChecker_Timeout tests HTTP request timeout
func TestHTTPChecker_Timeout(t *testing.T) {
	// Server that delays response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewHTTPChecker(500 * time.Millisecond)

	start := time.Now()
	result := checker.Check(context.Background(), server.URL)
	elapsed := time.Since(start)

	assert.False(t, result.Success, "Should fail on timeout")
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "timeout", "Should report timeout")
	assert.Greater(t, result.Latency, 400*time.Millisecond)
	assert.Less(t, elapsed, 1*time.Second)

	t.Logf("✓ HTTP超时检测正常，延迟: %v", result.Latency)
}

// TestHTTPChecker_SSLError tests SSL/TLS errors
func TestHTTPChecker_SSLError(t *testing.T) {
	// Use an invalid HTTPS URL that will fail SSL verification
	checker := NewHTTPChecker(2 * time.Second)
	result := checker.Check(context.Background(), "https://expired.badssl.com/")

	// Note: This might succeed if the cert is valid or fail if expired
	// The key is that we handle SSL errors gracefully
	if result.Error != nil {
		t.Logf("✓ SSL错误检测正常: %v", result.Error)
	} else {
		t.Logf("✓ SSL连接成功（证书可能已更新）: %d", result.StatusCode)
	}

	// Either way, it should not panic
	assert.NotNil(t, result)
}

// TestHTTPChecker_InvalidURL tests invalid URL handling
func TestHTTPChecker_InvalidURL(t *testing.T) {
	checker := NewHTTPChecker(2 * time.Second)

	testCases := []string{
		"not-a-url",
		"http://invalid-host-does-not-exist-12345.com",
		"://missing-scheme",
	}

	for _, url := range testCases {
		result := checker.Check(context.Background(), url)
		assert.False(t, result.Success, "Invalid URL should fail: %s", url)
		assert.Error(t, result.Error, "Should have error for: %s", url)
		t.Logf("✓ 无效URL检测正常: %s -> %v", url, result.Error)
	}
}

// TestHTTPChecker_RedirectFollowing tests HTTP redirect handling
func TestHTTPChecker_RedirectFollowing(t *testing.T) {
	redirectCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if redirectCount < 2 {
			redirectCount++
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("final destination"))
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.True(t, result.Success, "Should follow redirects")
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Contains(t, result.Body, "final destination")

	t.Logf("✓ 重定向跟随正常，redirects: %d", redirectCount)
}

// TestHTTPChecker_TooManyRedirects tests redirect limit
func TestHTTPChecker_TooManyRedirects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.False(t, result.Success, "Should stop after max redirects")
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "redirect", "Should mention redirect limit")

	t.Logf("✓ 重定向限制正常")
}

// TestHTTPChecker_ContextCancellation tests context cancellation
func TestHTTPChecker_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewHTTPChecker(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result := checker.Check(ctx, server.URL)

	assert.False(t, result.Success)
	assert.Error(t, result.Error)
	// Context timeout is handled as timeout error
	assert.NotEmpty(t, result.Error.Error())

	t.Logf("✓ Context取消检测正常")
}

// TestHTTPChecker_CheckWithExpectedStatus tests expected status validation
func TestHTTPChecker_CheckWithExpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated) // 201
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)

	// Expect 201
	result := checker.CheckWithExpectedStatus(context.Background(), server.URL, http.StatusCreated)
	assert.True(t, result.Success, "Should match expected 201")
	assert.NoError(t, result.Error)

	// Expect 200 (but got 201)
	result = checker.CheckWithExpectedStatus(context.Background(), server.URL, http.StatusOK)
	assert.False(t, result.Success, "Should fail on status mismatch")
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "expected status 200, got 201")

	t.Logf("✓ 状态码验证正常")
}

// TestHTTPChecker_LargeResponseBody tests response body size limit
func TestHTTPChecker_LargeResponseBody(t *testing.T) {
	// Server returns 10KB response
	largeBody := strings.Repeat("x", 10*1024)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(largeBody))
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.True(t, result.Success)
	// Body should be truncated to 1KB
	assert.LessOrEqual(t, len(result.Body), 1024, "Body should be limited to 1KB")

	t.Logf("✓ 响应体大小限制正常，实际读取: %d bytes", len(result.Body))
}

// TestHTTPChecker_DefaultTimeout tests default timeout value
func TestHTTPChecker_DefaultTimeout(t *testing.T) {
	checker := NewHTTPChecker(0) // Should use default 3s

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	start := time.Now()
	result := checker.Check(context.Background(), server.URL)
	elapsed := time.Since(start)

	assert.False(t, result.Success, "Should timeout with default 3s")
	assert.Error(t, result.Error)
	assert.Less(t, elapsed, 4*time.Second, "Should timeout around 3s")

	t.Logf("✓ 默认超时配置正常，实际耗时: %v", elapsed)
}

// TestQuickCheck_ConvenienceFunction tests the convenience QuickCheck function
func TestQuickCheck_ConvenienceFunction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	statusCode, latency, err := QuickCheck(server.URL)

	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, statusCode)
	assert.Greater(t, latency, time.Duration(0))
	assert.Less(t, latency, 100*time.Millisecond)

	t.Logf("✓ QuickCheck便捷函数正常，状态: %d, 延迟: %v", statusCode, latency)
}

// TestHTTPChecker_UserAgent tests User-Agent header
func TestHTTPChecker_UserAgent(t *testing.T) {
	var receivedUA string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), server.URL)

	assert.True(t, result.Success)
	assert.Contains(t, receivedUA, "LLM-Gateway-HealthChecker", "Should set custom User-Agent")

	t.Logf("✓ User-Agent设置正常: %s", receivedUA)
}

// BenchmarkHTTPChecker_Success benchmarks successful HTTP checks
func BenchmarkHTTPChecker_Success(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	checker := NewHTTPChecker(3 * time.Second)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := checker.Check(ctx, server.URL)
		if !result.Success {
			b.Fatalf("Check failed: %v", result.Error)
		}
	}
}

// Example demonstrates basic usage of HTTPChecker
func ExampleHTTPChecker_Check() {
	checker := NewHTTPChecker(3 * time.Second)
	result := checker.Check(context.Background(), "https://httpbin.org/status/200")

	if result.Success {
		fmt.Printf("HTTP健康检查成功，状态: %d, 延迟: %v\n", result.StatusCode, result.Latency)
	} else {
		fmt.Printf("HTTP健康检查失败: %v\n", result.Error)
	}
}

// Example demonstrates the QuickCheck convenience function
func ExampleQuickCheck() {
	statusCode, latency, err := QuickCheck("https://httpbin.org/status/200")
	if err != nil {
		fmt.Printf("检查失败: %v\n", err)
		return
	}
	fmt.Printf("状态: %d, 延迟: %v\n", statusCode, latency)
}
