package activation

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mock 常量：与主控端真实契约对齐（cmd/license-authority）。
// register 的新设备分支把 license_key_hash 当完整 license key 查库，
// 查不到返回 404；试用 license key 由 /api/v1/license/trial 签发。
const (
	mockValidLicenseKey = "LIC-4f7c1d2e8a9b0c3d5e6f7a8b9c0d1e2f"
	mockTrialLicenseKey = "TRIAL-KEY-XXX"
	mockInstanceToken   = "test-instance-token"
)

// startMockMaster 启动一个 httptest.Server mock 主控端接口。
// 行为对齐真实主控：register 的 license_key_hash ∈ {mockValidLicenseKey,
// mockTrialLicenseKey} 时返回 200（register 即激活），否则返回
// registerStatus 指定的错误码（默认 404）。
// 可变参 trialStatus 非负时额外挂载 /api/v1/license/trial（trial 与
// register 共用同一个 MasterURL，与真实 RunAutoActivate 拓扑一致）。
// 返回的 *int32 用于计数；map 记录最近一次 register 请求体关键字段。
func startMockMaster(t *testing.T, registerStatus int, trialStatus ...int) (*httptest.Server, *int32, map[string]string) {
	t.Helper()
	var registerCalls int32
	var lastBody = map[string]string{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instances/register", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&registerCalls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("register: expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var generic map[string]interface{}
		if err := json.Unmarshal(body, &generic); err != nil {
			t.Errorf("register: invalid JSON body: %v", err)
		}
		lastBody["license_key_hash"], _ = generic["license_key_hash"].(string)
		lastBody["public_key"], _ = generic["public_key"].(string)
		lastBody["hardware_hash"], _ = generic["hardware_hash"].(string)

		keyOK := lastBody["license_key_hash"] == mockValidLicenseKey ||
			lastBody["license_key_hash"] == mockTrialLicenseKey
		if registerStatus == http.StatusOK && !keyOK {
			// 真实主控语义：新设备 + 未知 license key → 404
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"license not found"}`))
			return
		}
		switch {
		case registerStatus != http.StatusOK:
			w.WriteHeader(registerStatus)
			w.Write([]byte(`{"error":"license not found"}`))
		default:
			resp := map[string]interface{}{
				"instance_token":    mockInstanceToken,
				"refresh_token":     "test-refresh-token",
				"server_public_key": "test-server-pub",
				"expires_at":        time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		}
	})

	if len(trialStatus) > 0 && trialStatus[0] >= 0 {
		trialSt := trialStatus[0]
		mux.HandleFunc("/api/v1/license/trial", func(w http.ResponseWriter, r *http.Request) {
			var req TrialRequest
			json.NewDecoder(r.Body).Decode(&req)
			if req.Email != "user@example.com" || !req.Agree {
				t.Errorf("unexpected trial request: %+v", req)
			}
			if trialSt != http.StatusOK {
				w.WriteHeader(trialSt)
				w.Write([]byte(`{"success":false,"message":"trial unavailable"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success":     true,
				"license_key": mockTrialLicenseKey,
				"expires_at":  time.Now().Add(15 * 24 * time.Hour).Format(time.RFC3339),
			})
		})
	}

	srv := httptest.NewServer(mux)
	return srv, &registerCalls, lastBody
}

// stubDiscoverIP 返回固定 IP + nil，绕开真实 UDP 探测。
func stubDiscoverIP(ip string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		return ip, nil
	}
}

func readStateFile(t *testing.T, installDir string) *ActivationState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(installDir, "state", "activation.json"))
	if err != nil {
		t.Fatalf("read activation.json: %v", err)
	}
	var st ActivationState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	return &st
}

func readInstanceTokenFile(t *testing.T, installDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(installDir, "state", "instance.token"))
	if err != nil {
		t.Fatalf("read instance.token: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// readRawStateFile 把 activation.json 解析为通用 map，用于断言某个 JSON 字段
// 是否真的缺席（omitempty 写出来是字段不存在，而不是字段为空字符串）。
func readRawStateFile(t *testing.T, installDir string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(installDir, "state", "activation.json"))
	if err != nil {
		t.Fatalf("read activation.json: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw state: %v", err)
	}
	return raw
}

// ─────────────────────────────────────────────────────────────
// 1. 成功路径：license key 随 register 提交 → 同调用激活
//    回归钉桩（P0）：license_key_hash 必须携带完整 license key，
//    而不是 hardware hash —— 否则真实主控 404。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_WithLicenseKey_Success(t *testing.T) {
	srv, regCalls, lastBody := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()

	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		StorageMode:  "full",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("RunAutoActivate returned error: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call, got %d", atomic.LoadInt32(regCalls))
	}
	if got := lastBody["license_key_hash"]; got != mockValidLicenseKey {
		t.Errorf("register must carry full license key in license_key_hash, got %q", got)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusActivated {
		t.Errorf("expected status=activated, got %s", st.Status)
	}
	if st.InstanceID == "" {
		t.Error("expected non-empty instance_id")
	}
	if st.DeviceCode == "" || !strings.HasPrefix(st.DeviceCode, "GW-") {
		t.Errorf("expected derived GW- device code, got %q", st.DeviceCode)
	}
	if st.IPAddress != "10.20.30.40" {
		t.Errorf("expected ip=10.20.30.40, got %s", st.IPAddress)
	}
	if st.Mode != "full" {
		t.Errorf("expected mode=full, got %s", st.Mode)
	}
	if st.Error != "" {
		t.Errorf("expected empty error, got %s", st.Error)
	}

	tok := readInstanceTokenFile(t, installDir)
	if tok != mockInstanceToken {
		t.Errorf("expected token file contains %s, got %s", mockInstanceToken, tok)
	}

	// 验证权限 0600
	info, err := os.Stat(filepath.Join(installDir, "state", "activation.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected activation.json perm 0600, got %o", perm)
	}
}

// ─────────────────────────────────────────────────────────────
// 2. 安全契约（P1 回归钉桩）：instance_token JWT 不得出现在
//    activation.json（会经 launcher /status 接口外泄），只能落
//    state/instance.token；device_code 必须是派生短码而非 token。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_TokenNeverInActivationJSON(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	raw := readRawStateFile(t, installDir)
	if _, present := raw["instance_token"]; present {
		t.Errorf("instance_token MUST NOT be present in activation.json, raw=%v", raw)
	}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.Contains(s, mockInstanceToken) {
			t.Errorf("field %q leaks instance token value: %v", k, raw)
		}
	}

	st := readStateFile(t, installDir)
	if st.DeviceCode == mockInstanceToken {
		t.Error("device_code must be a derived short code, not the instance token")
	}
	if !strings.Contains(st.DeviceCode, "GW-") || len(st.DeviceCode) > 16 {
		t.Errorf("device_code should be short derived code, got %q", st.DeviceCode)
	}
}

// ─────────────────────────────────────────────────────────────
// 3. 注册 4xx：写 status=failed，error 非空；不返回 error
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_RegisterConflict(t *testing.T) {
	srv, regCalls, _ := startMockMaster(t, http.StatusConflict)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call, got %d", atomic.LoadInt32(regCalls))
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusFailed {
		t.Errorf("expected status=failed, got %s", st.Status)
	}
	if !strings.Contains(st.Error, "register") {
		t.Errorf("expected error to mention register, got %s", st.Error)
	}
	if _, err := os.Stat(filepath.Join(installDir, "state", "instance.token")); !os.IsNotExist(err) {
		t.Errorf("expected no instance.token file on register failure, stat err=%v", err)
	}

	raw := readRawStateFile(t, installDir)
	if _, present := raw["license_key"]; present {
		t.Errorf("license_key MUST NOT be present in activation.json on failure, raw=%v", raw)
	}
}

// ─────────────────────────────────────────────────────────────
// 4. 网络错误：mock server 立刻关闭，Register 应该 fail-open
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_NetworkError(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK)
	masterURL := srv.URL
	srv.Close() // 立即关闭模拟网络错误

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    masterURL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusFailed {
		t.Errorf("expected status=failed, got %s", st.Status)
	}
	if st.Error == "" {
		t.Error("expected non-empty error on network failure")
	}
}

// ─────────────────────────────────────────────────────────────
// 5. LicenseKey / TrialEmail 都缺失：写 status=skipped，
//    不发起任何网络调用（真实主控对无 key 新设备必 404，发了也是噪音）。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_NoLicenseNoTrial_Skipped(t *testing.T) {
	srv, regCalls, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register call, got %d", atomic.LoadInt32(regCalls))
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusSkipped {
		t.Errorf("expected status=skipped, got %s", st.Status)
	}
}

// ─────────────────────────────────────────────────────────────
// 6. INSTALL_SKIP_ACTIVATION=1：完全跳过，不调任何主控接口
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_InstallSkipActivation(t *testing.T) {
	srv, regCalls, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register call, got %d", atomic.LoadInt32(regCalls))
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusSkipped {
		t.Errorf("expected status=skipped, got %s", st.Status)
	}
	if _, err := os.Stat(filepath.Join(installDir, "state", "instance.token")); !os.IsNotExist(err) {
		t.Errorf("expected no instance.token file when Skip=true, stat err=%v", err)
	}
}

// ─────────────────────────────────────────────────────────────
// 7. InstallDir 不可写：返回 error（不静默吞）
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_InstallDirUnwritable(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	// t.TempDir() 整体 chmod 0000 → 在非 root 下写文件会失败
	installDir := t.TempDir()
	if err := os.Chmod(installDir, 0000); err != nil {
		t.Skipf("can't chmod 0000 (likely running as root): %v", err)
	}
	defer os.Chmod(installDir, 0755)

	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err == nil {
		t.Fatal("expected error when installDir is unwritable, got nil")
	}
}

// ─────────────────────────────────────────────────────────────
// 8. 试用路径：RequestTrial 换 key 后随 register 提交 → status=trial。
//    回归钉桩（P0 顺序）：trial 必须先于 register，且 register 携带试用 key。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_TrialSuccess(t *testing.T) {
	srv, regCalls, lastBody := startMockMaster(t, http.StatusOK, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		TrialEmail:   "user@example.com",
		AgreeTerms:   true,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call, got %d", atomic.LoadInt32(regCalls))
	}
	if got := lastBody["license_key_hash"]; got != mockTrialLicenseKey {
		t.Errorf("register must carry trial license key, got %q", got)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusTrial {
		t.Errorf("expected status=trial, got %s", st.Status)
	}
	if st.LicenseKey != mockTrialLicenseKey {
		t.Errorf("expected license_key=%s, got %s", mockTrialLicenseKey, st.LicenseKey)
	}
	if st.ExpiresAt == "" {
		t.Error("expected expires_at from trial response")
	}
	tok := readInstanceTokenFile(t, installDir)
	if tok != mockInstanceToken {
		t.Errorf("expected token file persisted on trial path, got %s", tok)
	}
}

// ─────────────────────────────────────────────────────────────
// 9. 试用申请 4xx：写 status=failed，error 提及 trial；不调 register
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_TrialFailed(t *testing.T) {
	srv, regCalls, _ := startMockMaster(t, http.StatusOK, http.StatusTooManyRequests)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		TrialEmail:   "user@example.com",
		AgreeTerms:   true,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register call, got %d", atomic.LoadInt32(regCalls))
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusFailed {
		t.Errorf("expected status=failed, got %s", st.Status)
	}
	if !strings.Contains(st.Error, "trial") {
		t.Errorf("expected error to mention trial, got %s", st.Error)
	}
}

// ─────────────────────────────────────────────────────────────
// 10. 优先级：LicenseKey 与 TrialEmail 同时给出 → license 直通，
//     不调 trial（P2 补口：优先级契约）。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_LicenseKeyWinsOverTrial(t *testing.T) {
	// trial 端点故意返回 500：若实现误先调 trial，状态会变 failed，
	// 用成功结果反证 trial 未被调用。
	srv, regCalls, lastBody := startMockMaster(t, http.StatusOK, http.StatusInternalServerError)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		TrialEmail:   "user@example.com",
		AgreeTerms:   true,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusActivated {
		t.Errorf("expected status=activated (license path wins), got %s", st.Status)
	}
	if got := lastBody["license_key_hash"]; got != mockValidLicenseKey {
		t.Errorf("register must carry the explicit license key, got %q", got)
	}
	if st.LicenseKey != "" && st.LicenseKey != mockValidLicenseKey {
		t.Errorf("unexpected license_key in state: %s", st.LicenseKey)
	}
	_ = regCalls
}

// ─────────────────────────────────────────────────────────────
// 11. token 写入失败：激活状态保留 + error 非空（P2 补口：
//     注册成功但写 token 失败的分支不允许静默）。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_TokenWriteFailure_RecordsError(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	// 把 instance.token 预先占位成目录，迫使 writeInstanceToken 失败
	tokenPath := filepath.Join(installDir, "state", "instance.token")
	if err := os.MkdirAll(tokenPath, 0755); err != nil {
		t.Fatalf("mkdir placeholder: %v", err)
	}

	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusActivated {
		t.Errorf("expected status=activated despite token write failure, got %s", st.Status)
	}
	if st.Error == "" || !strings.Contains(st.Error, "write instance token") {
		t.Errorf("expected error mentioning token write failure, got %q", st.Error)
	}
}

// ─────────────────────────────────────────────────────────────
// 12. ed25519 keypair：公钥随 register 提交且私钥落盘 0600；
//     重复调用公钥稳定（P2：不再提交 placeholder 公钥）。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_RealPublicKey(t *testing.T) {
	srv, _, lastBody := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   mockValidLicenseKey,
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if strings.Contains(lastBody["public_key"], "placeholder") {
		t.Errorf("placeholder public key must not be submitted, got %q", lastBody["public_key"])
	}
	if len(lastBody["public_key"]) < 40 {
		t.Errorf("expected base64 ed25519 public key (32 bytes), got %q", lastBody["public_key"])
	}

	keyPath := filepath.Join(installDir, "state", "instance.ed25519")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("expected persisted private key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected instance.ed25519 perm 0600, got %o", perm)
	}
}

// ─────────────────────────────────────────────────────────────
// 13. 单元：writeActivationState 与 writeInstanceToken 权限正确；
//     state/ 目录本身 0700。
// ─────────────────────────────────────────────────────────────

func TestWriteActivationState_Permissions(t *testing.T) {
	dir := t.TempDir()
	st := &ActivationState{
		InstanceID:  "abc",
		Status:      StatusActivated,
		ActivatedAt: time.Now().Format(time.RFC3339),
	}
	if err := writeActivationState(dir, st); err != nil {
		t.Fatalf("writeActivationState: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "state", "activation.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("expected 0600, got %o", perm)
	}
	dirInfo, err := os.Stat(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Errorf("expected state dir 0700, got %o", perm)
	}

	if err := writeInstanceToken(dir, "tok"); err != nil {
		t.Fatalf("writeInstanceToken: %v", err)
	}
	info2, err := os.Stat(filepath.Join(dir, "state", "instance.token"))
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if perm := info2.Mode().Perm(); perm != 0600 {
		t.Errorf("expected token 0600, got %o", perm)
	}
}

// ─────────────────────────────────────────────────────────────
// 14. 安全契约：跳过激活时，activation.json 中不能出现 license_key 字段
//     （不依赖 omitempty 字符串空值，而是断言 key 在 JSON map 中缺席，
//      防止未来某次重构把 LicenseKey 从结构体删除后写默认值上去。）
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_SkippedHasNoKey(t *testing.T) {
	srv, regCalls, _ := startMockMaster(t, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "SECRET-LICENSE-KEY-MUST-NOT-LEAK",
		InstallerVer: "1.0.0-test",
		StorageMode:  "full",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register call, got %d", atomic.LoadInt32(regCalls))
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusSkipped {
		t.Errorf("expected status=skipped, got %s", st.Status)
	}

	raw := readRawStateFile(t, installDir)
	if _, present := raw["license_key"]; present {
		t.Errorf("license_key MUST NOT be present in activation.json when skipped, raw=%v", raw)
	}
}
