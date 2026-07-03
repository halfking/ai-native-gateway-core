package memoraauto

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIdleDetector_Track 测试会话跟踪
func TestIdleDetector_Track(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	err := detector.Track(ctx, "session1", "task1", "tenant1")
	if err != nil {
		t.Fatalf("Track failed: %v", err)
	}

	stats, err := detector.GetStats(ctx, "session1")
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.SessionKey != "session1" {
		t.Errorf("Expected session_key 'session1', got '%s'", stats.SessionKey)
	}
	if stats.RequestCount != 1 {
		t.Errorf("Expected request_count 1, got %d", stats.RequestCount)
	}
}

// TestIdleDetector_Track_Multiple 测试多次跟踪
func TestIdleDetector_Track_Multiple(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	// 跟踪3次
	for i := 0; i < 3; i++ {
		err := detector.Track(ctx, "session1", "task1", "tenant1")
		if err != nil {
			t.Fatalf("Track failed: %v", err)
		}
	}

	stats, err := detector.GetStats(ctx, "session1")
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.RequestCount != 3 {
		t.Errorf("Expected request_count 3, got %d", stats.RequestCount)
	}
}

// TestIdleDetector_CheckIdle_NotEnoughRequests 测试请求数不足
func TestIdleDetector_CheckIdle_NotEnoughRequests(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	// 只跟踪2次
	detector.Track(ctx, "session1", "task1", "tenant1")
	detector.Track(ctx, "session1", "task1", "tenant1")

	isIdle, stats, err := detector.CheckIdle(ctx, "session1")
	if err != nil {
		t.Fatalf("CheckIdle failed: %v", err)
	}

	if isIdle {
		t.Error("Session should not be idle (only 2 requests)")
	}
	if stats.RequestCount != 2 {
		t.Errorf("Expected request_count 2, got %d", stats.RequestCount)
	}
}

// TestIdleDetector_CheckIdle_NotEnoughTime 测试时间不足
func TestIdleDetector_CheckIdle_NotEnoughTime(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	// 跟踪3次
	for i := 0; i < 3; i++ {
		detector.Track(ctx, "session1", "task1", "tenant1")
	}

	// 立即检查（时间不足）
	isIdle, _, err := detector.CheckIdle(ctx, "session1")
	if err != nil {
		t.Fatalf("CheckIdle failed: %v", err)
	}

	if isIdle {
		t.Error("Session should not be idle (not enough time passed)")
	}
}

// TestIdleDetector_CheckIdle_Success 测试空闲检测成功
func TestIdleDetector_CheckIdle_Success(t *testing.T) {
	// 使用较短的空闲阈值便于测试
	detector := NewIdleDetector(100*time.Millisecond, 3)
	ctx := context.Background()

	// 跟踪3次
	for i := 0; i < 3; i++ {
		detector.Track(ctx, "session1", "task1", "tenant1")
	}

	// 等待超过空闲阈值
	time.Sleep(150 * time.Millisecond)

	isIdle, stats, err := detector.CheckIdle(ctx, "session1")
	if err != nil {
		t.Fatalf("CheckIdle failed: %v", err)
	}

	if !isIdle {
		t.Error("Session should be idle")
	}
	if stats.RequestCount != 3 {
		t.Errorf("Expected request_count 3, got %d", stats.RequestCount)
	}
}

// TestIdleDetector_MarkProcessed 测试标记已处理
func TestIdleDetector_MarkProcessed(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	detector.Track(ctx, "session1", "task1", "tenant1")

	err := detector.MarkProcessed(ctx, "session1")
	if err != nil {
		t.Fatalf("MarkProcessed failed: %v", err)
	}

	// 检查会话已被移除
	_, err = detector.GetStats(ctx, "session1")
	if err == nil {
		t.Error("Session should be removed after MarkProcessed")
	}
}

// TestIdleDetector_CleanupOldSessions 测试清理旧会话
func TestIdleDetector_CleanupOldSessions(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	// 创建多个会话
	detector.Track(ctx, "session1", "task1", "tenant1")
	detector.Track(ctx, "session2", "task2", "tenant1")

	// 等待一小段时间
	time.Sleep(50 * time.Millisecond)

	// 清理超过 100ms 的会话
	removed := detector.CleanupOldSessions(ctx, 1*time.Millisecond)
	if removed != 2 {
		t.Errorf("Expected to remove 2 sessions, removed %d", removed)
	}

	if detector.Size() != 0 {
		t.Errorf("Expected size 0, got %d", detector.Size())
	}
}

// TestIdleDetector_EmptySessionKey 测试空会话键
func TestIdleDetector_EmptySessionKey(t *testing.T) {
	detector := NewIdleDetector(1*time.Hour, 3)
	ctx := context.Background()

	err := detector.Track(ctx, "", "task1", "tenant1")
	if err == nil {
		t.Error("Track should fail with empty session_key")
	}

	_, _, err = detector.CheckIdle(ctx, "")
	if err == nil {
		t.Error("CheckIdle should fail with empty session_key")
	}
}

// TestKxmemoryClient_IngestSession 测试会话接收
func TestKxmemoryClient_IngestSession(t *testing.T) {
	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求方法
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}

		// 验证 Content-Type
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}

		// 返回成功响应
		resp := SessionIngestResponse{
			Success: true,
			Message: "OK",
			JobID:   "job123",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// 创建客户端
	client := NewKxmemoryClient(server.URL, 5*time.Second)
	ctx := context.Background()

	// 发送请求
	req := &SessionIngestRequest{
		SessionKey: "session1",
		TaskID:     "task1",
		TenantID:   "tenant1",
	}

	resp, err := client.IngestSession(ctx, req)
	if err != nil {
		t.Fatalf("IngestSession failed: %v", err)
	}

	if !resp.Success {
		t.Error("Expected success=true")
	}
	if resp.JobID != "job123" {
		t.Errorf("Expected job_id 'job123', got '%s'", resp.JobID)
	}
}

// TestKxmemoryClient_IngestSession_Error 测试错误响应
func TestKxmemoryClient_IngestSession_Error(t *testing.T) {
	// 创建返回错误的测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	client := NewKxmemoryClient(server.URL, 5*time.Second)
	ctx := context.Background()

	req := &SessionIngestRequest{
		SessionKey: "session1",
		TaskID:     "task1",
		TenantID:   "tenant1",
	}

	_, err := client.IngestSession(ctx, req)
	if err == nil {
		t.Error("Expected error for 500 response")
	}
}

// TestKxmemoryClient_IngestSession_Timeout 测试超时
func TestKxmemoryClient_IngestSession_Timeout(t *testing.T) {
	// 创建延迟响应的测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// 使用短超时时间
	client := NewKxmemoryClient(server.URL, 50*time.Millisecond)
	ctx := context.Background()

	req := &SessionIngestRequest{
		SessionKey: "session1",
		TaskID:     "task1",
		TenantID:   "tenant1",
	}

	_, err := client.IngestSession(ctx, req)
	if err == nil {
		t.Error("Expected timeout error")
	}
}

// TestKxmemoryClient_IngestSession_MissingFields 测试缺少必填字段
func TestKxmemoryClient_IngestSession_MissingFields(t *testing.T) {
	client := NewKxmemoryClient("http://localhost:8000", 5*time.Second)
	ctx := context.Background()

	// 缺少 session_key
	req1 := &SessionIngestRequest{
		TaskID:   "task1",
		TenantID: "tenant1",
	}
	_, err := client.IngestSession(ctx, req1)
	if err == nil {
		t.Error("Expected error for missing session_key")
	}

	// 缺少 tenant_id
	req2 := &SessionIngestRequest{
		SessionKey: "session1",
		TaskID:     "task1",
	}
	_, err = client.IngestSession(ctx, req2)
	if err == nil {
		t.Error("Expected error for missing tenant_id")
	}
}

// TestKxmemoryClient_Ping 测试Ping功能
func TestKxmemoryClient_Ping(t *testing.T) {
	// 创建正常响应的服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewKxmemoryClient(server.URL, 5*time.Second)
	ctx := context.Background()

	err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping should succeed: %v", err)
	}
}

// TestKxmemoryClient_Ping_ServerError 测试Ping服务器错误
func TestKxmemoryClient_Ping_ServerError(t *testing.T) {
	// 创建返回500错误的服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewKxmemoryClient(server.URL, 5*time.Second)
	ctx := context.Background()

	err := client.Ping(ctx)
	if err == nil {
		t.Error("Ping should fail with 500 error")
	}
}

// TestKxmemoryClient_IngestSession_NilRequest 测试nil请求
func TestKxmemoryClient_IngestSession_NilRequest(t *testing.T) {
	client := NewKxmemoryClient("http://localhost:8000", 5*time.Second)
	ctx := context.Background()

	_, err := client.IngestSession(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil request")
	}
}

// TestRetryManager_Execute_Success 测试重试成功
func TestRetryManager_Execute_Success(t *testing.T) {
	manager := NewRetryManager(3, 10*time.Millisecond)
	ctx := context.Background()

	attempts := 0
	err := manager.Execute(ctx, func(ctx context.Context, attempt int) error {
		attempts++
		return nil // 第一次就成功
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", attempts)
	}
}

// TestRetryManager_Execute_RetrySuccess 测试重试后成功
func TestRetryManager_Execute_RetrySuccess(t *testing.T) {
	manager := NewRetryManager(3, 10*time.Millisecond)
	ctx := context.Background()

	attempts := 0
	err := manager.Execute(ctx, func(ctx context.Context, attempt int) error {
		attempts++
		if attempt < 2 {
			return &tempError{msg: "temporary error"}
		}
		return nil // 第3次成功
	})

	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetryManager_Execute_AllFailed 测试所有重试都失败
func TestRetryManager_Execute_AllFailed(t *testing.T) {
	manager := NewRetryManager(2, 10*time.Millisecond)
	ctx := context.Background()

	attempts := 0
	err := manager.Execute(ctx, func(ctx context.Context, attempt int) error {
		attempts++
		return &tempError{msg: "always fail"}
	})

	if err == nil {
		t.Error("Expected error after all retries failed")
	}
	// maxRetries=2 意味着总共尝试 3 次（0, 1, 2）
	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

// TestRetryManager_Execute_ContextCanceled 测试上下文取消
func TestRetryManager_Execute_ContextCanceled(t *testing.T) {
	manager := NewRetryManager(5, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	// 在第一次尝试后取消
	attempts := 0
	err := manager.Execute(ctx, func(ctx context.Context, attempt int) error {
		attempts++
		if attempt == 0 {
			cancel() // 取消上下文
		}
		return &tempError{msg: "error"}
	})

	if err == nil {
		t.Error("Expected error due to context cancellation")
	}
	// 应该只尝试1次或2次（取决于取消时机）
	if attempts > 2 {
		t.Errorf("Expected at most 2 attempts, got %d", attempts)
	}
}

// TestRetryManager_calculateBackoff 测试退避时间计算
func TestRetryManager_calculateBackoff(t *testing.T) {
	manager := NewRetryManager(3, 1*time.Second)

	tests := []struct {
		attempt  int
		expected time.Duration
	}{
		{0, 1 * time.Second},     // 1 * 2^0 = 1
		{1, 2 * time.Second},     // 1 * 2^1 = 2
		{2, 4 * time.Second},     // 1 * 2^2 = 4
		{3, 8 * time.Second},     // 1 * 2^3 = 8
		{4, 16 * time.Second},    // 1 * 2^4 = 16
		{5, 30 * time.Second},    // 1 * 2^5 = 32, 但限制在 maxBackoff=30
	}

	for _, tt := range tests {
		backoff := manager.calculateBackoff(tt.attempt)
		if backoff != tt.expected {
			t.Errorf("calculateBackoff(%d) = %v, want %v", tt.attempt, backoff, tt.expected)
		}
	}
}

// TestRetryManager_ExecuteWithStats 测试带统计的重试
func TestRetryManager_ExecuteWithStats(t *testing.T) {
	manager := NewRetryManager(3, 10*time.Millisecond)
	ctx := context.Background()

	// 测试成功场景
	stats := manager.ExecuteWithStats(ctx, func(ctx context.Context, attempt int) error {
		return nil
	})

	if !stats.Success {
		t.Error("Expected success=true")
	}
	if stats.LastError != nil {
		t.Errorf("Expected no error, got %v", stats.LastError)
	}
	if stats.TotalAttempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", stats.TotalAttempts)
	}

	// 测试失败场景
	stats = manager.ExecuteWithStats(ctx, func(ctx context.Context, attempt int) error {
		return &tempError{msg: "always fail"}
	})

	if stats.Success {
		t.Error("Expected success=false")
	}
	if stats.LastError == nil {
		t.Error("Expected error")
	}
	if stats.TotalAttempts != 4 { // maxRetries=3 means 4 attempts (0,1,2,3)
		t.Errorf("Expected 4 attempts, got %d", stats.TotalAttempts)
	}
	if stats.TotalDuration == 0 {
		t.Error("Expected non-zero duration")
	}
}

// tempError 临时错误（用于测试）
type tempError struct {
	msg string
}

func (e *tempError) Error() string {
	return e.msg
}
