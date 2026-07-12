package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHeartbeatSingleMode 测试单次心跳模式
func TestHeartbeatSingleMode(t *testing.T) {
	// 创建 mock 主控端
	heartbeatCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		if !strings.Contains(r.URL.Path, "/heartbeat") {
			t.Errorf("expected /heartbeat path, got %s", r.URL.Path)
		}

		// 验证 Authorization header
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			t.Errorf("expected Bearer token, got %s", auth)
		}

		// 验证 payload
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body failed: %v", err)
		}

		var payload HeartbeatPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal payload failed: %v", err)
		}

		// 验证指标
		if payload.GoVersion == "" {
			t.Error("expected non-empty GoVersion")
		}
		if payload.CPUCores <= 0 {
			t.Error("expected positive CPUCores")
		}

		heartbeatCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// 准备测试环境
	tmpDir := t.TempDir()
	kxDir := filepath.Join(tmpDir, ".kx-gateway")
	os.MkdirAll(kxDir, 0755)

	instanceID := "test-instance-123"
	instanceToken := "test-token-456"

	os.WriteFile(filepath.Join(kxDir, "instance.id"), []byte(instanceID), 0644)
	os.WriteFile(filepath.Join(kxDir, "instance.token"), []byte(instanceToken), 0600)

	// 临时修改 HOME（让 readInstanceToken/readInstanceID 读取测试目录）
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// 创建 HeartbeatSender
	sender := NewHeartbeatSender(server.URL, instanceID, instanceToken, "1.0.0")

	// 发送一次心跳
	ctx := context.Background()
	if err := sender.SendHeartbeat(ctx); err != nil {
		t.Fatalf("SendHeartbeat failed: %v", err)
	}

	// 验证心跳计数
	if heartbeatCount != 1 {
		t.Errorf("expected 1 heartbeat, got %d", heartbeatCount)
	}
}

// TestHeartbeatDaemonMode 测试 daemon 模式
func TestHeartbeatDaemonMode(t *testing.T) {
	// 创建 mock 主控端
	heartbeatCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		heartbeatCount++
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// 准备测试环境
	tmpDir := t.TempDir()
	kxDir := filepath.Join(tmpDir, ".kx-gateway")
	os.MkdirAll(kxDir, 0755)

	instanceID := "test-instance-123"
	instanceToken := "test-token-456"

	os.WriteFile(filepath.Join(kxDir, "instance.id"), []byte(instanceID), 0644)
	os.WriteFile(filepath.Join(kxDir, "instance.token"), []byte(instanceToken), 0600)

	// 临时修改 HOME
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// 创建 HeartbeatSender
	sender := NewHeartbeatSender(server.URL, instanceID, instanceToken, "1.0.0")

	// 启动 daemon（2秒间隔，运行 5 秒后取消）
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go sender.StartDaemon(ctx, 2*time.Second)

	// 等待 daemon 运行
	<-ctx.Done()

	// 验证心跳计数（5秒内 2秒间隔，预期 2-3 次）
	// 第一次立即发送，然后每 2 秒一次：0s, 2s, 4s = 3 次
	if heartbeatCount < 2 || heartbeatCount > 4 {
		t.Errorf("expected 2-4 heartbeats in 5 seconds (interval 2s), got %d", heartbeatCount)
	}

	t.Logf("daemon mode sent %d heartbeats in 5 seconds", heartbeatCount)
}

// TestHeartbeatPayloadValidation 测试心跳载荷验证
func TestHeartbeatPayloadValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload HeartbeatPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		// 验证必要字段
		if payload.GoVersion == "" {
			t.Error("GoVersion is empty")
		}
		if payload.CPUCores <= 0 {
			t.Error("CPUCores is not positive")
		}
		if payload.NumGoroutine <= 0 {
			t.Error("NumGoroutine is not positive")
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHeartbeatSender(server.URL, "test-id", "test-token", "1.0.0")
	ctx := context.Background()

	if err := sender.SendHeartbeat(ctx); err != nil {
		t.Fatalf("SendHeartbeat failed: %v", err)
	}
}

// TestHeartbeatAPIError 测试 API 错误处理
func TestHeartbeatAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"internal server error"}`))
	}))
	defer server.Close()

	sender := NewHeartbeatSender(server.URL, "test-id", "test-token", "1.0.0")
	ctx := context.Background()

	err := sender.SendHeartbeat(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "API error") {
		t.Errorf("expected API error message, got: %v", err)
	}
}

// TestReadInstanceFiles 测试读取实例文件
func TestReadInstanceFiles(t *testing.T) {
	tmpDir := t.TempDir()
	kxDir := filepath.Join(tmpDir, ".kx-gateway")
	os.MkdirAll(kxDir, 0755)

	// 写入测试文件
	instanceID := "test-instance-123"
	instanceToken := "test-token-456"

	os.WriteFile(filepath.Join(kxDir, "instance.id"), []byte(instanceID+"\n"), 0644)
	os.WriteFile(filepath.Join(kxDir, "instance.token"), []byte(instanceToken+"\n"), 0600)

	// 临时修改 HOME
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// 读取并验证
	id, err := readInstanceID()
	if err != nil {
		t.Fatalf("readInstanceID failed: %v", err)
	}
	if id != instanceID {
		t.Errorf("expected %s, got %s", instanceID, id)
	}

	token, err := readInstanceToken()
	if err != nil {
		t.Fatalf("readInstanceToken failed: %v", err)
	}
	if token != instanceToken {
		t.Errorf("expected %s, got %s", instanceToken, token)
	}
}

// TestReadInstanceFilesNotExist 测试文件不存在的情况
func TestReadInstanceFilesNotExist(t *testing.T) {
	tmpDir := t.TempDir()

	// 临时修改 HOME
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// 读取不存在的文件
	_, err := readInstanceID()
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	_, err = readInstanceToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
