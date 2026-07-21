package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestInferenceChecker_LightInference_Success tests successful light inference
func TestInferenceChecker_LightInference_Success(t *testing.T) {
	// Mock OpenAI API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate quick response
		time.Sleep(500 * time.Millisecond)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "2"}},
			},
			Usage: struct {
				TotalTokens int `json:"total_tokens"`
			}{TotalTokens: 15},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success, "Light inference should succeed")
	assert.NoError(t, result.Error)
	assert.Equal(t, "2", result.ResponseText)
	assert.Equal(t, 15, result.TokenCount)
	assert.Greater(t, result.Latency, 400*time.Millisecond)
	assert.Less(t, result.Latency, 1*time.Second)
	assert.Equal(t, "light", result.CheckType)

	status, shouldTriggerHeavy := EvaluateLatency(result)
	assert.Equal(t, "Active", status)
	assert.False(t, shouldTriggerHeavy)

	t.Logf("✓ L3轻量推理成功，延迟: %v, 响应: %s", result.Latency, result.ResponseText)
}

// TestInferenceChecker_LightInference_SlowButOK tests slow light inference (3-5s)
func TestInferenceChecker_LightInference_SlowButOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response (3 seconds)
		time.Sleep(3 * time.Second)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "2"}},
			},
			Usage: struct {
				TotalTokens int `json:"total_tokens"`
			}{TotalTokens: 15},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success, "Should still succeed despite slowness")
	assert.Greater(t, result.Latency, 2500*time.Millisecond)

	status, shouldTriggerHeavy := EvaluateLatency(result)
	assert.Equal(t, "Active", status, "3s is still acceptable for light check")
	assert.False(t, shouldTriggerHeavy)

	t.Logf("✓ L3慢速但可接受，延迟: %v", result.Latency)
}

// TestInferenceChecker_LightInference_TooSlow tests too slow light inference (>5s)
func TestInferenceChecker_LightInference_TooSlow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate very slow response (6 seconds)
		time.Sleep(6 * time.Second)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "2"}},
			},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success, "Request succeeds but too slow")
	assert.Greater(t, result.Latency, 5*time.Second)

	status, shouldTriggerHeavy := EvaluateLatency(result)
	assert.Equal(t, "Degraded", status)
	assert.True(t, shouldTriggerHeavy, "Should trigger L4 heavy check")

	t.Logf("✓ L3过慢，应触发L4，延迟: %v", result.Latency)
}

// TestInferenceChecker_LightInference_InvalidResponse tests invalid response
func TestInferenceChecker_LightInference_InvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices": []}`)) // Empty choices
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")

	assert.False(t, result.Success, "Should fail on empty response")
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "empty response")

	t.Logf("✓ L3空响应检测正常")
}

// TestInferenceChecker_HeavyLoad_Success tests successful heavy load check
func TestInferenceChecker_HeavyLoad_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过重负载测试")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate heavy processing (5 seconds)
		time.Sleep(5 * time.Second)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "Summary: AI has revolutionized many industries..."}},
			},
			Usage: struct {
				TotalTokens int `json:"total_tokens"`
			}{TotalTokens: 20500},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckHeavy(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success, "Heavy load check should succeed")
	assert.NoError(t, result.Error)
	assert.Contains(t, result.ResponseText, "Summary")
	assert.Greater(t, result.TokenCount, 20000)
	assert.Greater(t, result.Latency, 4*time.Second)
	assert.Less(t, result.Latency, 10*time.Second)
	assert.Equal(t, "heavy", result.CheckType)

	status, _ := EvaluateLatency(result)
	assert.Equal(t, "Active", status, "<10s should be Active")

	t.Logf("✓ L4重负载成功，延迟: %v, tokens: %d", result.Latency, result.TokenCount)
}

// TestInferenceChecker_HeavyLoad_SlowButOK tests slow heavy load (10-20s)
func TestInferenceChecker_HeavyLoad_SlowButOK(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过重负载测试")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate very slow processing (15 seconds)
		time.Sleep(15 * time.Second)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "Summary..."}},
			},
			Usage: struct {
				TotalTokens int `json:"total_tokens"`
			}{TotalTokens: 20500},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(20*time.Second, 30*time.Second)
	result := checker.CheckHeavy(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success)
	assert.Greater(t, result.Latency, 14*time.Second)

	status, _ := EvaluateLatency(result)
	assert.Equal(t, "Degraded", status, "10-20s should be Degraded")

	t.Logf("✓ L4慢速但可接受，延迟: %v", result.Latency)
}

// TestInferenceChecker_HeavyLoad_TooSlow tests too slow heavy load (>20s)
func TestInferenceChecker_HeavyLoad_TooSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过重负载测试")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate extremely slow processing (25 seconds)
		time.Sleep(25 * time.Second)

		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "Summary..."}},
			},
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(30*time.Second, 30*time.Second)
	result := checker.CheckHeavy(context.Background(), server.URL, "test-key", "gpt-4")

	assert.True(t, result.Success, "Request completes but too slow")
	assert.Greater(t, result.Latency, 20*time.Second)

	status, _ := EvaluateLatency(result)
	assert.Equal(t, "Unhealthy", status, ">20s should be Unhealthy")

	t.Logf("✓ L4过慢标记为Unhealthy，延迟: %v", result.Latency)
}

// TestInferenceChecker_HeavyLoad_ContextWindowExceeded tests context window error
func TestInferenceChecker_HeavyLoad_ContextWindowExceeded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"message": "context_length_exceeded"}}`))
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckHeavy(context.Background(), server.URL, "test-key", "gpt-4")

	assert.False(t, result.Success, "Should fail on context window error")
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "HTTP 400")

	t.Logf("✓ L4上下文窗口超限检测正常")
}

// TestInferenceChecker_500Error tests 500 error from provider
func TestInferenceChecker_500Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")

	assert.False(t, result.Success)
	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "HTTP 500")

	t.Logf("✓ 500错误检测正常")
}

// TestInferenceChecker_Timeout tests request timeout
func TestInferenceChecker_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	checker := NewInferenceChecker(1*time.Second, 2*time.Second)

	start := time.Now()
	result := checker.CheckLight(context.Background(), server.URL, "test-key", "gpt-4")
	elapsed := time.Since(start)

	assert.False(t, result.Success)
	assert.Error(t, result.Error)
	assert.Less(t, elapsed, 3*time.Second, "Should timeout around 2s")

	t.Logf("✓ 推理超时检测正常，实际耗时: %v", elapsed)
}

// TestInferenceChecker_HeavyPromptGeneration tests heavy prompt is large enough
func TestInferenceChecker_HeavyPromptGeneration(t *testing.T) {
	prompt := generateHeavyPrompt()

	// Rough estimate: ~4 chars per token, so 20K tokens ≈ 80K chars
	assert.Greater(t, len(prompt), 60000, "Heavy prompt should be large (~80K chars for 20K tokens)")
	assert.Contains(t, prompt, "artificial intelligence")
	assert.Contains(t, prompt, "Summary:")

	// Count repetitions (should be ~200)
	count := strings.Count(prompt, "The development of artificial intelligence")
	assert.GreaterOrEqual(t, count, 190, "Should repeat paragraph ~200 times")

	t.Logf("✓ 重负载prompt生成正常，长度: %d chars, 重复: %d次", len(prompt), count)
}

// BenchmarkInferenceChecker_Light benchmarks light inference check
func BenchmarkInferenceChecker_Light(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := OpenAIResponse{
			Choices: []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			}{
				{Message: struct {
					Content string `json:"content"`
				}{Content: "2"}},
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	checker := NewInferenceChecker(10*time.Second, 30*time.Second)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := checker.CheckLight(ctx, server.URL, "test-key", "gpt-4")
		if !result.Success {
			b.Fatalf("Check failed: %v", result.Error)
		}
	}
}
