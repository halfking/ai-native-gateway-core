package integration

import (
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/adapter/unified"
	"github.com/kaixuan/llm-gateway-go/circuit"
	"github.com/kaixuan/llm-gateway-go/security/sensitive"
)

// TestCircuitBreakerWithAdapter 测试熔断器与适配器集成
func TestCircuitBreakerWithAdapter(t *testing.T) {
	// 1. 创建熔断器（更宽松的阈值，确保能完成10个请求）
	breaker := circuit.NewBreaker(circuit.Config{
		ErrorThreshold:  0.8, // 80% 错误率才触发
		MinRequests:     10,  // 至少10个请求
		WindowSize:      10 * time.Second,
		OpenTimeout:     1 * time.Second,
		HalfOpenMaxTest: 2,
	})

	// 2. 注册适配器
	adapter := &mockAdapter{name: "test-provider"}
	unified.Register(adapter)

	// 3. 模拟请求流程
	successCount := 0
	failCount := 0

	for i := 0; i < 10; i++ {
		// 检查熔断器状态
		if breaker.State() == circuit.StateOpen {
			t.Logf("Request %d: Circuit breaker is OPEN, rejecting", i)
			continue
		}

		// 通过适配器处理请求
		retrievedAdapter, err := unified.GetAdapter("test-provider")
		if err != nil {
			t.Fatalf("Get adapter failed: %v", err)
		}

		// 模拟转换：前8个成功，后2个失败
		success := i < 8

		// 记录到熔断器
		breaker.Record(success)

		if success {
			successCount++
			t.Logf("Request %d: SUCCESS (adapter=%s)", i, retrievedAdapter.Name())
		} else {
			failCount++
			t.Logf("Request %d: FAILED", i)
		}
	}

	// 验证熔断器状态
	metrics := breaker.Metrics()
	t.Logf("Final metrics: Total=%d, Successes=%d, Failures=%d, ErrorRate=%.2f, State=%s",
		metrics.TotalRequests, metrics.TotalSuccesses, metrics.TotalFailures, metrics.ErrorRate, breaker.State())

	if metrics.TotalRequests != 10 {
		t.Errorf("Expected 10 total requests, got %d", metrics.TotalRequests)
	}

	if successCount != 8 {
		t.Errorf("Expected 8 successful requests, got %d", successCount)
	}
}

// TestAdapterWithContentSafety 测试适配器与内容安全集成
func TestAdapterWithContentSafety(t *testing.T) {
	// 1. 创建安全引擎（使用 P1 级别确保 warn 动作）
	engine := sensitive.NewSensitiveWordEngine()
	config := &sensitive.SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]sensitive.CategoryConf{
			"political": { // P1 级别
				Name:  "政治敏感",
				Words: []string{"违规"},
			},
		},
	}
	if err := engine.Build(config); err != nil {
		t.Fatalf("Build engine failed: %v", err)
	}

	// 2. 创建适配器
	adapter := &mockAdapter{name: "content-test"}
	unified.Register(adapter)

	// 3. 测试场景
	testCases := []struct {
		name       string
		content    string
		expectSafe bool
		minScore   float64
		maxScore   float64
	}{
		{
			name:       "clean content",
			content:    "这是正常内容",
			expectSafe: true,
			minScore:   0.0,
			maxScore:   0.0,
		},
		{
			name:       "sensitive content",
			content:    "这包含违规内容",
			expectSafe: false,
			minScore:   0.3, // P1 级别应该 >= 0.3
			maxScore:   0.6,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// 通过适配器获取内容
			retrievedAdapter, err := unified.GetAdapter("content-test")
			if err != nil {
				t.Fatalf("Get adapter failed: %v", err)
			}

			// 安全检查
			result := engine.EvaluateSafety(tc.content)

			// 验证
			isSafe := result.Action == sensitive.ActionAllow
			if isSafe != tc.expectSafe {
				t.Errorf("Expected safe=%v, got %v (action=%s, score=%.2f)",
					tc.expectSafe, isSafe, result.Action, result.Score)
			}

			if result.Score < tc.minScore || result.Score > tc.maxScore {
				t.Errorf("Score %.2f not in expected range [%.2f, %.2f]",
					result.Score, tc.minScore, tc.maxScore)
			}

			t.Logf("Adapter=%s, Content=%s, Action=%s, Score=%.2f",
				retrievedAdapter.Name(), tc.content, result.Action, result.Score)
		})
	}
}

// TestEndToEndFlow 端到端流程测试
func TestEndToEndFlow(t *testing.T) {
	// 1. 初始化所有组件
	breaker := circuit.NewBreaker(circuit.Config{
		ErrorThreshold:  0.8,
		MinRequests:     5,
		WindowSize:      10 * time.Second,
		OpenTimeout:     1 * time.Second,
		HalfOpenMaxTest: 2,
	})

	adapter := &mockAdapter{name: "e2e-test"}
	unified.Register(adapter)

	engine := sensitive.NewSensitiveWordEngine()
	config := &sensitive.SensitiveWordConfig{
		Version: "1.0",
		Categories: map[string]sensitive.CategoryConf{
			"terrorism": { // P0 级别，确保 block
				Name:  "恐怖主义",
				Words: []string{"炸弹", "恐怖"},
			},
		},
	}
	if err := engine.Build(config); err != nil {
		t.Fatalf("Build engine failed: %v", err)
	}

	// 2. 模拟请求处理流程
	requests := []struct {
		content       string
		expectBlocked bool
	}{
		{"正常请求", false},
		{"包含炸弹恐怖词", true}, // P0 应该被 block
		{"正常请求", false},
	}

	successCount := 0
	blockedCount := 0

	for i, req := range requests {
		t.Logf("\n=== Request %d: %s ===", i+1, req.content)

		// Step 1: 检查熔断器
		if breaker.State() == circuit.StateOpen {
			t.Logf("  ❌ Blocked by circuit breaker")
			blockedCount++
			continue
		}

		// Step 2: 内容安全检查
		safetyResult := engine.EvaluateSafety(req.content)
		if safetyResult.Action == sensitive.ActionBlock {
			t.Logf("  ❌ Blocked by content safety (score=%.2f)", safetyResult.Score)
			breaker.Record(false)
			blockedCount++
			continue
		}

		// Step 3: 通过适配器处理
		retrievedAdapter, err := unified.GetAdapter("e2e-test")
		if err != nil {
			t.Fatalf("Get adapter failed: %v", err)
		}

		// 模拟处理成功
		breaker.Record(true)
		successCount++
		t.Logf("  ✅ Success (adapter=%s)", retrievedAdapter.Name())
	}

	// 3. 验证结果
	t.Logf("\n=== Summary ===")
	t.Logf("Total: %d, Success: %d, Blocked: %d", len(requests), successCount, blockedCount)

	if successCount != 2 {
		t.Errorf("Expected 2 successful requests, got %d", successCount)
	}

	if blockedCount != 1 {
		t.Errorf("Expected 1 blocked request, got %d", blockedCount)
	}

	// 验证熔断器指标
	metrics := breaker.Metrics()
	t.Logf("Circuit Breaker: Total=%d, Successes=%d, Failures=%d, ErrorRate=%.2f",
		metrics.TotalRequests, metrics.TotalSuccesses, metrics.TotalFailures, metrics.ErrorRate)
}

// mockAdapter 实现 unified.Adapter 接口
type mockAdapter struct {
	name string
}

func (m *mockAdapter) Name() string {
	return m.name
}

func (m *mockAdapter) ToProviderRequest(req *unified.UnifiedRequest) (interface{}, error) {
	return req, nil
}

func (m *mockAdapter) FromProviderResponse(resp interface{}) (*unified.UnifiedResponse, error) {
	return &unified.UnifiedResponse{}, nil
}

func (m *mockAdapter) SupportedModels() []string {
	return []string{"test-model"}
}

func (m *mockAdapter) ValidateRequest(req *unified.UnifiedRequest) error {
	return nil
}
