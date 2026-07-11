package memoraauto

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
)

// TestMemoraAutoHook_Name 测试 Hook 名称
func TestMemoraAutoHook_Name(t *testing.T) {
	hook := NewMemoraAutoHook(nil, nil)
	if hook.Name() != "memora.auto" {
		t.Errorf("Expected name 'memora.auto', got '%s'", hook.Name())
	}
}

// TestMemoraAutoHook_Priority 测试优先级
func TestMemoraAutoHook_Priority(t *testing.T) {
	hook := NewMemoraAutoHook(nil, nil)
	if hook.Priority() != 200 {
		t.Errorf("Expected priority 200, got %d", hook.Priority())
	}
}

// TestMemoraAutoHook_Enabled_Disabled 测试禁用状态
func TestMemoraAutoHook_Enabled_Disabled(t *testing.T) {
	config := DefaultConfig()
	config.Enabled = false

	hook := NewMemoraAutoHook(config, nil)
	ctx := context.Background()
	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
	}

	if hook.Enabled(ctx, env) {
		t.Error("Hook should be disabled")
	}
}

// TestMemoraAutoHook_Enabled_NoSession 测试没有会话信息
func TestMemoraAutoHook_Enabled_NoSession(t *testing.T) {
	hook := NewMemoraAutoHook(nil, nil)
	ctx := context.Background()
	env := &domain.PipelineRequest{
		SessionID: "", // 空会话
		TenantID:  "tenant1",
	}

	if hook.Enabled(ctx, env) {
		t.Error("Hook should be disabled without session_id")
	}
}

// TestMemoraAutoHook_Enabled_Success 测试启用成功
func TestMemoraAutoHook_Enabled_Success(t *testing.T) {
	hook := NewMemoraAutoHook(nil, nil)
	ctx := context.Background()
	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
	}

	if !hook.Enabled(ctx, env) {
		t.Error("Hook should be enabled")
	}
}

// TestMemoraAutoHook_Execute_Track 测试会话跟踪
func TestMemoraAutoHook_Execute_Track(t *testing.T) {
	hook := NewMemoraAutoHook(nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
		Metadata:  map[string]any{"task_id": "task1"},
	}

	err := hook.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 验证会话已被跟踪
	stats, err := hook.GetIdleDetector().GetStats(ctx, "session1")
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.RequestCount != 1 {
		t.Errorf("Expected request_count 1, got %d", stats.RequestCount)
	}
}

// TestMemoraAutoHook_Execute_NotIdle 测试未空闲
func TestMemoraAutoHook_Execute_NotIdle(t *testing.T) {
	config := DefaultConfig()
	config.IdleThreshold = 1 * time.Hour
	config.MinRequestCount = 3

	hook := NewMemoraAutoHook(config, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
		Metadata:  map[string]any{"task_id": "task1"},
	}

	// 执行2次（少于3次）
	hook.Execute(ctx, env)
	err := hook.Execute(ctx, env)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 验证未触发沉淀（因为请求数不足）
	stats, _ := hook.GetIdleDetector().GetStats(ctx, "session1")
	if stats.RequestCount >= 3 {
		t.Error("Request count should be less than 3")
	}
}

// TestMemoraAutoHook_Execute_Idle 测试空闲触发
func TestMemoraAutoHook_Execute_Idle(t *testing.T) {
	// 创建测试服务器
	ingestCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ingestCalled = true
		resp := SessionIngestResponse{
			Success: true,
			Message: "OK",
			JobID:   "job123",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// 配置短的空闲阈值
	config := DefaultConfig()
	config.KxmemoryURL = server.URL
	config.IdleThreshold = 50 * time.Millisecond
	config.MinRequestCount = 3

	hook := NewMemoraAutoHook(config, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
		Metadata:  map[string]any{"task_id": "task1"},
	}

	// 执行3次
	for i := 0; i < 3; i++ {
		hook.Execute(ctx, env)
	}

	// 等待超过空闲阈值
	time.Sleep(100 * time.Millisecond)

	// 手动修改 LastActive 以模拟空闲状态（用于测试）
	detector := hook.GetIdleDetector()
	detector.SetLastActiveForTest("session1", time.Now().Add(-2*time.Hour))

	// 再执行一次，应该触发沉淀
	hook.Execute(ctx, env)

	// 等待异步调用完成
	time.Sleep(300 * time.Millisecond)

	if !ingestCalled {
		t.Error("Expected ingest to be called")
	}
}

// TestMemoraAutoHook_OnError 测试错误处理
func TestMemoraAutoHook_OnError(t *testing.T) {
	hook := NewMemoraAutoHook(nil, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	env := &domain.PipelineRequest{
		SessionID: "session1",
		TenantID:  "tenant1",
		Metadata:  make(map[string]any),
	}

	testErr := &tempError{msg: "test error"}
	err := hook.OnError(ctx, env, testErr)

	// OnError 应该吞掉错误
	if err != nil {
		t.Errorf("OnError should return nil, got %v", err)
	}

	// 验证错误已记录到 metadata
	if env.Metadata["memora_auto_error"] == nil {
		t.Error("Error should be recorded in metadata")
	}
}

// TestMemoraAutoHook_DefaultConfig 测试默认配置
func TestMemoraAutoHook_DefaultConfig(t *testing.T) {
	hook := NewMemoraAutoHook(nil, nil)

	if hook.config.IdleThreshold != 1*time.Hour {
		t.Errorf("Expected idle_threshold 1h, got %v", hook.config.IdleThreshold)
	}
	if hook.config.MinRequestCount != 3 {
		t.Errorf("Expected min_request_count 3, got %d", hook.config.MinRequestCount)
	}
	if hook.config.MaxRetries != 3 {
		t.Errorf("Expected max_retries 3, got %d", hook.config.MaxRetries)
	}
}

// TestSessionStats_IsIdle 测试 SessionStats.IsIdle
func TestSessionStats_IsIdle(t *testing.T) {
	tests := []struct {
		name         string
		requestCount int
		lastActive   time.Time
		expected     bool
	}{
		{
			name:         "Not enough requests",
			requestCount: 2,
			lastActive:   time.Now().Add(-2 * time.Hour),
			expected:     false,
		},
		{
			name:         "Not enough time",
			requestCount: 3,
			lastActive:   time.Now().Add(-30 * time.Minute),
			expected:     false,
		},
		{
			name:         "Idle - enough requests and time",
			requestCount: 3,
			lastActive:   time.Now().Add(-2 * time.Hour),
			expected:     true,
		},
		{
			name:         "Idle - many requests",
			requestCount: 10,
			lastActive:   time.Now().Add(-2 * time.Hour),
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := &SessionStats{
				RequestCount: tt.requestCount,
				LastActive:   tt.lastActive,
			}

			if stats.IsIdle() != tt.expected {
				t.Errorf("IsIdle() = %v, want %v", stats.IsIdle(), tt.expected)
			}
		})
	}
}

// TestConfig_DefaultConfig 测试默认配置值
func TestConfig_DefaultConfig(t *testing.T) {
	config := DefaultConfig()

	if !config.Enabled {
		t.Error("Default config should be enabled")
	}
	if config.KxmemoryURL != "http://localhost:8000/api/sessions/ingest" {
		t.Errorf("Unexpected default KxmemoryURL: %s", config.KxmemoryURL)
	}
	if config.Timeout != 10*time.Second {
		t.Errorf("Expected timeout 10s, got %v", config.Timeout)
	}
	if config.IdleThreshold != 1*time.Hour {
		t.Errorf("Expected idle_threshold 1h, got %v", config.IdleThreshold)
	}
	if config.MinRequestCount != 3 {
		t.Errorf("Expected min_request_count 3, got %d", config.MinRequestCount)
	}
	if config.MaxRetries != 3 {
		t.Errorf("Expected max_retries 3, got %d", config.MaxRetries)
	}
	if config.RetryBackoff != 1*time.Second {
		t.Errorf("Expected retry_backoff 1s, got %v", config.RetryBackoff)
	}
}

// TestMemoraAutoHook_Integration 集成测试
func TestMemoraAutoHook_Integration(t *testing.T) {
	// 创建模拟 kxmemory 服务器
	requestsReceived := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestsReceived++

		// 解析请求
		var req SessionIngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// 验证请求字段
		if req.SessionKey == "" || req.TenantID == "" {
			t.Error("Missing required fields in request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// 返回成功响应
		resp := SessionIngestResponse{
			Success: true,
			Message: "Session ingested successfully",
			JobID:   "job-" + req.SessionKey,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// 配置 Hook
	config := DefaultConfig()
	config.KxmemoryURL = server.URL
	config.IdleThreshold = 100 * time.Millisecond
	config.MinRequestCount = 3
	config.Timeout = 2 * time.Second

	hook := NewMemoraAutoHook(config, slog.New(slog.NewTextHandler(os.Stdout, nil)))
	ctx := context.Background()

	// 模拟会话请求
	env := &domain.PipelineRequest{
		SessionID: "integration-session",
		TenantID:  "tenant-456",
		Metadata:  map[string]any{"task_id": "task-123"},
	}

	// 执行3次请求
	for i := 0; i < 3; i++ {
		if err := hook.Execute(ctx, env); err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
	}

	// 等待超过空闲阈值
	time.Sleep(150 * time.Millisecond)

	// 手动修改 LastActive 以模拟空闲状态
	detector := hook.GetIdleDetector()
	detector.SetLastActiveForTest("integration-session", time.Now().Add(-2*time.Hour))

	// 再执行一次，触发沉淀
	if err := hook.Execute(ctx, env); err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 等待异步调用完成
	time.Sleep(500 * time.Millisecond)

	// 验证请求已发送
	if requestsReceived == 0 {
		t.Error("Expected at least one request to kxmemory")
	}

	// 验证会话已被标记为已处理
	_, err := hook.GetIdleDetector().GetStats(ctx, "integration-session")
	if err == nil {
		t.Error("Session should be marked as processed and removed")
	}
}
