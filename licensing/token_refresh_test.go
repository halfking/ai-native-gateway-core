package licensing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAutoRefreshToken_Success(t *testing.T) {
	// Mock 主控端服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/instances/refresh" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// 返回成功响应
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"instance_token":"new-instance-token-123"}`))
	}))
	defer server.Close()

	// 创建临时目录
	tmpDir := t.TempDir()
	refreshTokenPath := filepath.Join(tmpDir, "refresh.token")
	instanceTokenPath := filepath.Join(tmpDir, "instance.token")

	// 写入 refresh_token
	if err := os.WriteFile(refreshTokenPath, []byte("valid-refresh-token"), 0600); err != nil {
		t.Fatalf("failed to write refresh_token: %v", err)
	}

	// 执行刷新
	ctx := context.Background()
	err := AutoRefreshToken(ctx, server.URL, refreshTokenPath, instanceTokenPath)
	if err != nil {
		t.Fatalf("AutoRefreshToken failed: %v", err)
	}

	// 验证 instance_token 已写入
	content, err := os.ReadFile(instanceTokenPath)
	if err != nil {
		t.Fatalf("failed to read instance_token: %v", err)
	}

	if string(content) != "new-instance-token-123" {
		t.Errorf("expected 'new-instance-token-123', got %s", string(content))
	}
}

func TestAutoRefreshToken_RefreshTokenNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	refreshTokenPath := filepath.Join(tmpDir, "nonexistent.token")
	instanceTokenPath := filepath.Join(tmpDir, "instance.token")

	ctx := context.Background()
	err := AutoRefreshToken(ctx, "http://localhost", refreshTokenPath, instanceTokenPath)

	if err == nil {
		t.Fatal("expected error for missing refresh_token, got nil")
	}

	if !os.IsNotExist(err) && err.Error() != "refresh_token file not found: "+refreshTokenPath {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAutoRefreshToken_ExpiredToken(t *testing.T) {
	// Mock 主控端返回 401
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"success":false,"message":"refresh_token expired"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshTokenPath := filepath.Join(tmpDir, "refresh.token")
	instanceTokenPath := filepath.Join(tmpDir, "instance.token")

	if err := os.WriteFile(refreshTokenPath, []byte("expired-token"), 0600); err != nil {
		t.Fatalf("failed to write refresh_token: %v", err)
	}

	ctx := context.Background()
	err := AutoRefreshToken(ctx, server.URL, refreshTokenPath, instanceTokenPath)

	if err == nil {
		t.Fatal("expected error for expired token, got nil")
	}

	// 不应重试（直接返回过期错误）
	if err.Error() != "refresh_token expired, re-registration required: status 401" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAutoRefreshToken_Retry(t *testing.T) {
	attempts := 0

	// Mock 主控端：前 2 次失败，第 3 次成功
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"success":false,"message":"temporary error"}`))
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"success":true,"instance_token":"retry-success-token"}`))
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshTokenPath := filepath.Join(tmpDir, "refresh.token")
	instanceTokenPath := filepath.Join(tmpDir, "instance.token")

	if err := os.WriteFile(refreshTokenPath, []byte("retry-token"), 0600); err != nil {
		t.Fatalf("failed to write refresh_token: %v", err)
	}

	ctx := context.Background()
	err := AutoRefreshToken(ctx, server.URL, refreshTokenPath, instanceTokenPath)

	if err != nil {
		t.Fatalf("AutoRefreshToken failed after retries: %v", err)
	}

	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}

	// 验证写入的 token
	content, err := os.ReadFile(instanceTokenPath)
	if err != nil {
		t.Fatalf("failed to read instance_token: %v", err)
	}

	if string(content) != "retry-success-token" {
		t.Errorf("expected 'retry-success-token', got %s", string(content))
	}
}

func TestAutoRefreshToken_ContextCancelled(t *testing.T) {
	// Mock 主控端：第一次失败，触发重试
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	refreshTokenPath := filepath.Join(tmpDir, "refresh.token")
	instanceTokenPath := filepath.Join(tmpDir, "instance.token")

	if err := os.WriteFile(refreshTokenPath, []byte("cancel-token"), 0600); err != nil {
		t.Fatalf("failed to write refresh_token: %v", err)
	}

	// 在首次失败后取消上下文
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := AutoRefreshToken(ctx, server.URL, refreshTokenPath, instanceTokenPath)

	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}

	if err.Error() != "token refresh cancelled: context canceled" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartTokenRefreshDaemon_ImmediateCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	// 守护进程应立即返回
	done := make(chan struct{})
	go func() {
		StartTokenRefreshDaemon(ctx, "http://localhost", "/tmp/refresh.token", "/tmp/instance.token", 1*time.Hour)
		close(done)
	}()

	select {
	case <-done:
		// 成功：守护进程已退出
	case <-time.After(1 * time.Second):
		t.Fatal("daemon did not stop after context cancellation")
	}
}

func TestWriteInstanceToken_AtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "subdir", "instance.token")

	// 写入 token（自动创建目录）
	err := writeInstanceToken(tokenPath, "atomic-token-123")
	if err != nil {
		t.Fatalf("writeInstanceToken failed: %v", err)
	}

	// 验证文件存在且内容正确
	content, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}

	if string(content) != "atomic-token-123" {
		t.Errorf("expected 'atomic-token-123', got %s", string(content))
	}

	// 验证临时文件已清理
	tmpPath := tokenPath + ".tmp"
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("temp file should not exist: %s", tmpPath)
	}
}
