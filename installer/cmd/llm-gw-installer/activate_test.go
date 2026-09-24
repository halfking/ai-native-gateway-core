package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRegisterMockServer 构造一个 mock 主控端 /api/v1/instances/register，
// 返回固定 instance_token / refresh_token（与主控端契约对齐）。
func newRegisterMockServer(t *testing.T, instanceToken string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instances/register", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("register: expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"instance_token":    instanceToken,
			"refresh_token":     "reg-refresh-token",
			"server_public_key": "reg-server-pub",
			"expires_at":        time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		})
	})
	return httptest.NewServer(mux)
}

// TestHandleOnlineActivation_PersistsInstanceToken 回归钉桩（P2 修复）：
// activate --mode key 注册成功后必须把 instance_token 写到
// ~/.kx-gateway/instance.token（0600），否则 heartbeat 子命令永远读不到
// 凭据（"请先执行 activate"死循环）。
func TestHandleOnlineActivation_PersistsInstanceToken(t *testing.T) {
	srv := newRegisterMockServer(t, "reg-instance-token")
	defer srv.Close()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := handleOnlineActivation(srv.URL, "LIC-4f7c1d2e8a9b0c3d5e6f7a8b9c0d1e2f"); err != nil {
		t.Fatalf("handleOnlineActivation: %v", err)
	}

	tokenPath := filepath.Join(tmpHome, ".kx-gateway", "instance.token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatalf("expected instance.token persisted after activate: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "reg-instance-token" {
		t.Errorf("expected reg-instance-token, got %q", got)
	}
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat instance.token: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected instance.token perm 0600, got %o", perm)
	}
}

// TestHandleOnlineActivation_MissingInstanceTokenFails：注册成功但响应缺
// instance_token 时必须报错（凭据不完整 = 激活对 heartbeat 无效），且不落
// 半截 token 文件。
func TestHandleOnlineActivation_MissingInstanceTokenFails(t *testing.T) {
	srv := newRegisterMockServer(t, "")
	defer srv.Close()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	err := handleOnlineActivation(srv.URL, "LIC-4f7c1d2e8a9b0c3d5e6f7a8b9c0d1e2f")
	if err == nil {
		t.Fatal("expected error when register response lacks instance_token, got nil")
	}
	if !strings.Contains(err.Error(), "instance_token") {
		t.Errorf("expected error to mention instance_token, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(tmpHome, ".kx-gateway", "instance.token")); !os.IsNotExist(statErr) {
		t.Errorf("expected no instance.token file on incomplete response, stat err=%v", statErr)
	}
}

// TestHandleOnlineActivation_EmptyLicenseKeyRejected：入参校验仍在最前面。
func TestHandleOnlineActivation_EmptyLicenseKeyRejected(t *testing.T) {
	if err := handleOnlineActivation("https://master.example.com", ""); err == nil {
		t.Fatal("expected error for empty license key, got nil")
	}
}
