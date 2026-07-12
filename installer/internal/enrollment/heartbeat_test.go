package enrollment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestSendHeartbeat_Success 测试心跳发送成功
func TestSendHeartbeat_Success(t *testing.T) {
	// 准备测试环境：创建临时 token 文件
	tmpDir := t.TempDir()
	tokenPath := tmpDir + "/.kx-gateway"
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	tokenFile := tokenPath + "/instance.token"
	if err := os.WriteFile(tokenFile, []byte("test-token-123"), 0644); err != nil {
		t.Fatalf("Failed to write token file: %v", err)
	}

	// 临时替换 HOME 环境变量
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Mock server
	called := false
	var receivedPayload HeartbeatPayload
	var receivedAuthHeader string
	var receivedSigHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true

		// 验证请求方法和路径
		if r.Method != http.MethodPost {
			t.Errorf("Expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/api/v1/instances/heartbeat" {
			t.Errorf("Expected /api/v1/instances/heartbeat, got %s", r.URL.Path)
		}

		// 验证请求头
		receivedAuthHeader = r.Header.Get("Authorization")
		receivedSigHeader = r.Header.Get("X-Signature")

		// 读取 body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("Failed to read body: %v", err)
		}

		if err := json.Unmarshal(body, &receivedPayload); err != nil {
			t.Fatalf("Failed to unmarshal payload: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// 创建客户端
	client := NewClient(server.URL)

	// 构造 payload
	payload := HeartbeatPayload{
		InstanceID:         "test-instance-001",
		Version:            "v1.13.0",
		UptimeSecs:         3600,
		CurrentConcurrency: 10,
		Last5MinTPS:        5.5,
		Last5MinP99MS:      120,
		LicenseKeyHash:     "abc123",
	}

	// 发送心跳
	err := client.SendHeartbeat(context.Background(), payload)
	if err != nil {
		t.Fatalf("SendHeartbeat failed: %v", err)
	}

	// 验证服务器被调用
	if !called {
		t.Fatal("Server was not called")
	}

	// 验证 payload
	if receivedPayload.InstanceID != payload.InstanceID {
		t.Errorf("Expected instance_id %s, got %s", payload.InstanceID, receivedPayload.InstanceID)
	}
	if receivedPayload.Version != payload.Version {
		t.Errorf("Expected version %s, got %s", payload.Version, receivedPayload.Version)
	}
	if receivedPayload.UptimeSecs != payload.UptimeSecs {
		t.Errorf("Expected uptime_secs %d, got %d", payload.UptimeSecs, receivedPayload.UptimeSecs)
	}

	// 验证 Authorization header（需要 instance_token）
	if receivedAuthHeader != "Bearer test-token-123" {
		t.Errorf("Expected 'Bearer test-token-123', got %s", receivedAuthHeader)
	}

	// 验证 X-Signature header（需要 Ed25519 签名）
	if receivedSigHeader == "" {
		t.Error("Expected X-Signature header, got empty")
	}
}

// TestSendHeartbeat_ServerError 测试服务端错误
func TestSendHeartbeat_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal_error"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL)
	payload := HeartbeatPayload{
		InstanceID: "test-instance-001",
		Version:    "v1.13.0",
	}

	err := client.SendHeartbeat(context.Background(), payload)
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

// TestSendHeartbeat_ContextCanceled 测试 context 取消
func TestSendHeartbeat_ContextCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	payload := HeartbeatPayload{
		InstanceID: "test-instance-001",
		Version:    "v1.13.0",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	err := client.SendHeartbeat(ctx, payload)
	if err == nil {
		t.Fatal("Expected context canceled error, got nil")
	}
}

// TestHeartbeatPayload_JSONMarshaling 测试 JSON 序列化
func TestHeartbeatPayload_JSONMarshaling(t *testing.T) {
	payload := HeartbeatPayload{
		InstanceID:         "test-001",
		Version:            "v1.13.0",
		UptimeSecs:         7200,
		CurrentConcurrency: 15,
		Last5MinTPS:        12.5,
		Last5MinP99MS:      250,
		LicenseKeyHash:     "hash123",
	}

	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded HeartbeatPayload
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.InstanceID != payload.InstanceID {
		t.Errorf("InstanceID mismatch: got %s, want %s", decoded.InstanceID, payload.InstanceID)
	}
	if decoded.Last5MinTPS != payload.Last5MinTPS {
		t.Errorf("Last5MinTPS mismatch: got %f, want %f", decoded.Last5MinTPS, payload.Last5MinTPS)
	}
}
