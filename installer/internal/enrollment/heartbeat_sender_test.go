package enrollment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestHeartbeatSender_StartStop 测试启动和停止
func TestHeartbeatSender_StartStop(t *testing.T) {
	// 准备 mock HTTP server（成功响应）
	callCount := 0
	server := testHTTPServerForHeartbeat(t, &callCount)
	defer server.Close()

	client := NewClient(server.URL)

	// payload 函数
	payloadFunc := func() HeartbeatPayload {
		return HeartbeatPayload{
			InstanceID: "test-001",
			Version:    "v1.0.0",
		}
	}

	sender := NewHeartbeatSender(client, 100*time.Millisecond, payloadFunc)

	// 启动
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 准备测试环境：创建临时 token 文件
	setupTestToken(t)

	sender.Start(ctx)

	// 等待几次心跳
	time.Sleep(250 * time.Millisecond)

	// 停止
	sender.Stop()

	// 验证至少调用了 1 次
	if callCount < 1 {
		t.Errorf("Expected at least 1 call, got %d", callCount)
	}
}

// testHTTPServerForHeartbeat 创建测试用的 HTTP server
func testHTTPServerForHeartbeat(t *testing.T, callCount *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*callCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
}

// setupTestToken 设置测试环境的 token 文件
func setupTestToken(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := tmpDir + "/.kx-gateway"
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	tokenFile := tokenPath + "/instance.token"
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0644); err != nil {
		t.Fatalf("Failed to write token file: %v", err)
	}

	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	t.Cleanup(func() { os.Setenv("HOME", oldHome) })
}

// TestHeartbeatSender_ContextCancel 测试 context 取消
func TestHeartbeatSender_ContextCancel(t *testing.T) {
	callCount := 0
	server := testHTTPServerForHeartbeat(t, &callCount)
	defer server.Close()

	client := NewClient(server.URL)

	payloadFunc := func() HeartbeatPayload {
		return HeartbeatPayload{
			InstanceID: "test-001",
			Version:    "v1.0.0",
		}
	}

	sender := NewHeartbeatSender(client, 1*time.Second, payloadFunc)

	ctx, cancel := context.WithCancel(context.Background())

	setupTestToken(t)

	sender.Start(ctx)

	// 等待一小段时间
	time.Sleep(100 * time.Millisecond)

	// 取消 context
	cancel()

	// 等待 goroutine 退出
	time.Sleep(100 * time.Millisecond)

	// 不应该 panic
}

// TestHeartbeatSender_MultipleStart 测试重复启动
func TestHeartbeatSender_MultipleStart(t *testing.T) {
	callCount := 0
	server := testHTTPServerForHeartbeat(t, &callCount)
	defer server.Close()

	client := NewClient(server.URL)

	payloadFunc := func() HeartbeatPayload {
		return HeartbeatPayload{
			InstanceID: "test-001",
			Version:    "v1.0.0",
		}
	}

	sender := NewHeartbeatSender(client, 1*time.Second, payloadFunc)

	ctx := context.Background()

	setupTestToken(t)

	sender.Start(ctx)

	// 再次启动应该被忽略（不 panic）
	sender.Start(ctx)

	sender.Stop()
}

// TestHeartbeatSender_MultipleStop 测试重复停止
func TestHeartbeatSender_MultipleStop(t *testing.T) {
	callCount := 0
	server := testHTTPServerForHeartbeat(t, &callCount)
	defer server.Close()

	client := NewClient(server.URL)

	payloadFunc := func() HeartbeatPayload {
		return HeartbeatPayload{
			InstanceID: "test-001",
			Version:    "v1.0.0",
		}
	}

	sender := NewHeartbeatSender(client, 1*time.Second, payloadFunc)

	ctx := context.Background()

	setupTestToken(t)

	sender.Start(ctx)
	sender.Stop()

	// 再次停止应该安全（不 panic）
	sender.Stop()
}
