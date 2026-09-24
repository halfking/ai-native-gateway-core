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

// startMockMaster 启动一个 httptest.Server 同时 mock register 与 activate 接口。
// 返回的 *int32 用于计数；可作为原子自增以验证调用次数。
func startMockMaster(t *testing.T, registerStatus int, activateStatus int) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var registerCalls, activateCalls int32

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instances/register", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&registerCalls, 1)
		if r.Method != http.MethodPost {
			t.Errorf("register: expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		// 至少要能解析为通用 map，证明请求结构对了
		var generic map[string]interface{}
		if err := json.Unmarshal(body, &generic); err != nil {
			t.Errorf("register: invalid JSON body: %v", err)
		}

		switch registerStatus {
		case http.StatusOK:
			resp := map[string]interface{}{
				"instance_token":    "test-instance-token",
				"refresh_token":     "test-refresh-token",
				"server_public_key": "test-server-pub",
				"expires_at":        time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		case http.StatusConflict:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"device_limit_exceeded"}`))
		default:
			w.WriteHeader(registerStatus)
			w.Write([]byte(`{"error":"unknown"}`))
		}
	})
	mux.HandleFunc("/api/v1/license/activate", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&activateCalls, 1)
		switch activateStatus {
		case http.StatusOK:
			resp := map[string]interface{}{
				"success":        true,
				"signed_license": "test-signed-license",
				"expires_at":     time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339),
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(activateStatus)
			w.Write([]byte(`{"success":false,"message":"invalid license key"}`))
		}
	})

	srv := httptest.NewServer(mux)
	return srv, &registerCalls, &activateCalls
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
// 1. 成功路径：注册 + 在线激活
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_OnlineActivation_Success(t *testing.T) {
	srv, regCalls, actCalls := startMockMaster(t, http.StatusOK, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()

	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "TEST-LICENSE-KEY",
		InstallerVer: "1.0.0-test",
		StorageMode:  "standalone",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("RunAutoActivate returned error: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call, got %d", regCalls)
	}
	if atomic.LoadInt32(actCalls) != 1 {
		t.Errorf("expected 1 activate call, got %d", actCalls)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusActivated {
		t.Errorf("expected status=activated, got %s", st.Status)
	}
	if st.InstanceID == "" {
		t.Error("expected non-empty instance_id")
	}
	if st.InstanceToken != "test-instance-token" {
		t.Errorf("expected instance_token=test-instance-token, got %s", st.InstanceToken)
	}
	if st.DeviceCode != "test-instance-token" {
		t.Errorf("expected device_code == instance_token, got %s", st.DeviceCode)
	}
	if st.IPAddress != "10.20.30.40" {
		t.Errorf("expected ip=10.20.30.40, got %s", st.IPAddress)
	}
	if st.Mode != "standalone" {
		t.Errorf("expected mode=standalone, got %s", st.Mode)
	}
	if st.ExpiresAt == "" {
		t.Error("expected expires_at to be set on activated status")
	}
	if st.Error != "" {
		t.Errorf("expected empty error, got %s", st.Error)
	}

	tok := readInstanceTokenFile(t, installDir)
	if tok != "test-instance-token" {
		t.Errorf("expected token file contains test-instance-token, got %s", tok)
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
// 2. 注册 4xx：写 status=failed，error 非空；不返回 error
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_RegisterConflict(t *testing.T) {
	srv, regCalls, actCalls := startMockMaster(t, http.StatusConflict, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "TEST-LICENSE-KEY",
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call, got %d", regCalls)
	}
	if atomic.LoadInt32(actCalls) != 0 {
		t.Errorf("expected 0 activate call after register fail, got %d", actCalls)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusFailed {
		t.Errorf("expected status=failed, got %s", st.Status)
	}
	if !strings.Contains(st.Error, "register") {
		t.Errorf("expected error to mention register, got %s", st.Error)
	}
	if st.InstanceToken != "" {
		t.Errorf("expected empty instance_token on register failure, got %s", st.InstanceToken)
	}
	if _, err := os.Stat(filepath.Join(installDir, "state", "instance.token")); !os.IsNotExist(err) {
		t.Errorf("expected no instance.token file on register failure, stat err=%v", err)
	}
}

// ─────────────────────────────────────────────────────────────
// 3. 网络错误：mock server 立刻关闭，enrollment.Register 应该 fail-open
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_NetworkError(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK, http.StatusOK)
	masterURL := srv.URL
	srv.Close() // 立即关闭模拟网络错误

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    masterURL,
		LicenseKey:   "TEST-LICENSE-KEY",
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
// 4. LicenseKey / TrialEmail 都缺失：写 status=skipped，不调用主控
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_NoLicenseNoTrial_Skipped(t *testing.T) {
	srv, regCalls, actCalls := startMockMaster(t, http.StatusOK, http.StatusOK)
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

	if atomic.LoadInt32(regCalls) != 1 {
		t.Errorf("expected 1 register call (always called before activation), got %d", regCalls)
	}
	if atomic.LoadInt32(actCalls) != 0 {
		t.Errorf("expected 0 activate calls (skipped), got %d", actCalls)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusSkipped {
		t.Errorf("expected status=skipped, got %s", st.Status)
	}
}

// ─────────────────────────────────────────────────────────────
// 5. INSTALL_SKIP_ACTIVATION=1：完全跳过，不调任何主控接口
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_InstallSkipActivation(t *testing.T) {
	srv, regCalls, actCalls := startMockMaster(t, http.StatusOK, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "TEST-LICENSE-KEY",
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register calls when Skip=true, got %d", regCalls)
	}
	if atomic.LoadInt32(actCalls) != 0 {
		t.Errorf("expected 0 activate calls when Skip=true, got %d", actCalls)
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
// 6. InstallDir 不可写：返回 error（不静默吞）
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_InstallDirUnwritable(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK, http.StatusOK)
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
		LicenseKey:   "TEST-LICENSE-KEY",
		InstallerVer: "1.0.0-test",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err == nil {
		t.Fatal("expected error when installDir is unwritable, got nil")
	}
}

// ─────────────────────────────────────────────────────────────
// 7. 试用申请：成功路径 → status=trial
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_TrialSuccess(t *testing.T) {
	var trialCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/instances/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"instance_token":    "trial-instance-token",
			"refresh_token":     "trial-refresh",
			"server_public_key": "trial-server-pub",
			"expires_at":        time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/api/v1/license/trial", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&trialCalls, 1)
		var req TrialRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Email != "user@example.com" || !req.Agree {
			t.Errorf("unexpected trial request: %+v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":     true,
			"license_key": "TRIAL-KEY-XXX",
			"expires_at":  time.Now().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		})
	})
	srv := httptest.NewServer(mux)
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

	if atomic.LoadInt32(&trialCalls) != 1 {
		t.Errorf("expected 1 trial call, got %d", trialCalls)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusTrial {
		t.Errorf("expected status=trial, got %s", st.Status)
	}
	if st.LicenseKey != "TRIAL-KEY-XXX" {
		t.Errorf("expected license_key=TRIAL-KEY-XXX, got %s", st.LicenseKey)
	}
}

// ─────────────────────────────────────────────────────────────
// 8. 激活接口返回 4xx：写 status=failed，error 非空；不返回 error
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_ActivateFailed(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusOK, http.StatusBadRequest)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "BAD-LICENSE-KEY",
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
	if !strings.Contains(st.Error, "activate") {
		t.Errorf("expected error to mention activate, got %s", st.Error)
	}
	// 注册已成功，token 应该已经写出来了
	if st.InstanceToken != "test-instance-token" {
		t.Errorf("expected instance_token to be persisted, got %s", st.InstanceToken)
	}
	tok := readInstanceTokenFile(t, installDir)
	if tok != "test-instance-token" {
		t.Errorf("expected token file to persist after register, got %s", tok)
	}
}

// ─────────────────────────────────────────────────────────────
// 9. 单元：writeActivationState 与 writeInstanceToken 权限正确
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
// 10. 安全契约：跳过激活时，activation.json 中不能出现 license_key 字段
//     （不依赖 omitempty 字符串空值，而是断言 key 在 JSON map 中缺席，
//      防止未来某次重构把 LicenseKey 从结构体删除后写默认值上去。）
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_SkippedHasNoKey(t *testing.T) {
	srv, regCalls, actCalls := startMockMaster(t, http.StatusOK, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "SECRET-LICENSE-KEY-MUST-NOT-LEAK",
		InstallerVer: "1.0.0-test",
		StorageMode:  "standalone",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
		Skip:         true,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if atomic.LoadInt32(regCalls) != 0 {
		t.Errorf("expected 0 register calls when Skip=true, got %d", regCalls)
	}
	if atomic.LoadInt32(actCalls) != 0 {
		t.Errorf("expected 0 activate calls when Skip=true, got %d", actCalls)
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

// ─────────────────────────────────────────────────────────────
// 11. 安全契约：注册失败时，activation.json 中不能出现 license_key 字段
//     即使用户传入了 LicenseKey，错误路径也不应该把密钥落盘。
// ─────────────────────────────────────────────────────────────

func TestRunAutoActivate_FailedHasNoKey(t *testing.T) {
	srv, _, _ := startMockMaster(t, http.StatusConflict, http.StatusOK)
	defer srv.Close()

	installDir := t.TempDir()
	err := RunAutoActivate(context.Background(), AutoActivateOptions{
		InstallDir:   installDir,
		MasterURL:    srv.URL,
		LicenseKey:   "SECRET-LICENSE-KEY-MUST-NOT-LEAK",
		InstallerVer: "1.0.0-test",
		StorageMode:  "standalone",
		DiscoverIPFn: stubDiscoverIP("10.20.30.40"),
	})
	if err != nil {
		t.Fatalf("expected no error (fail-open), got: %v", err)
	}

	st := readStateFile(t, installDir)
	if st.Status != StatusFailed {
		t.Errorf("expected status=failed, got %s", st.Status)
	}

	raw := readRawStateFile(t, installDir)
	if _, present := raw["license_key"]; present {
		t.Errorf("license_key MUST NOT be present in activation.json on failure, raw=%v", raw)
	}
}
