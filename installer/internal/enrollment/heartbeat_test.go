package enrollment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

	// 验证 X-Signature header（需要 HMAC-SHA256 签名，密钥 = SHA256(instance_token)）
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

// TestReadInstanceToken_Fallback 验证 ~/.kx-gateway/instance.token 缺失时，
// 应当 fallback 到 ${INSTALL_DIR}/state/instance.token
func TestReadInstanceToken_Fallback(t *testing.T) {
	// HOME 指向没有 .kx-gateway 的目录（primary 必然失败）
	homeDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", homeDir)
	defer os.Setenv("HOME", oldHome)

	// INSTALL_DIR 指向有 state/instance.token 的目录
	installDir := t.TempDir()
	stateDir := installDir + "/state"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(stateDir+"/instance.token", []byte("fallback-token-xyz"), 0600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	oldInstallDir := os.Getenv("INSTALL_DIR")
	os.Setenv("INSTALL_DIR", installDir)
	defer os.Setenv("INSTALL_DIR", oldInstallDir)

	got, err := readInstanceToken()
	if err != nil {
		t.Fatalf("readInstanceToken failed: %v", err)
	}
	if got != "fallback-token-xyz" {
		t.Errorf("expected fallback-token-xyz, got %s", got)
	}
}

// TestReadInstanceToken_PrimaryWins 验证当 primary 路径存在时优先使用
func TestReadInstanceToken_PrimaryWins(t *testing.T) {
	// HOME 下写 token-a
	homeDir := t.TempDir()
	if err := os.MkdirAll(homeDir+"/.kx-gateway", 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(homeDir+"/.kx-gateway/instance.token", []byte("primary-token"), 0600); err != nil {
		t.Fatalf("write primary: %v", err)
	}
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", homeDir)
	defer os.Setenv("HOME", oldHome)

	// INSTALL_DIR 下写 token-b
	installDir := t.TempDir()
	stateDir := installDir + "/state"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(stateDir+"/instance.token", []byte("fallback-token"), 0600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	oldInstallDir := os.Getenv("INSTALL_DIR")
	os.Setenv("INSTALL_DIR", installDir)
	defer os.Setenv("INSTALL_DIR", oldInstallDir)

	got, err := readInstanceToken()
	if err != nil {
		t.Fatalf("readInstanceToken failed: %v", err)
	}
	if got != "primary-token" {
		t.Errorf("expected primary wins, got %s", got)
	}
}

// TestReadInstanceToken_NotFound 验证两路径都失败时返回 error
func TestReadInstanceToken_NotFound(t *testing.T) {
	homeDir := t.TempDir()
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", homeDir)
	defer os.Setenv("HOME", oldHome)

	installDir := t.TempDir()
	oldInstallDir := os.Getenv("INSTALL_DIR")
	os.Setenv("INSTALL_DIR", installDir)
	defer os.Setenv("INSTALL_DIR", oldInstallDir)

	_, err := readInstanceToken()
	if err == nil {
		t.Fatal("expected error when both paths missing, got nil")
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

// TestSignHeartbeatBody 验证 HMAC-SHA256 签名:
//   - 派生密钥 = SHA256(instance_token)
//   - message = timestamp + "." + nonce + "." + body
//   - 相同输入必产生相同输出(确定性)
//   - 任一输入分量变化都会改变签名(证明派生真实使用了所有分量)
//   - 空 token 拒绝
func TestSignHeartbeatBody(t *testing.T) {
	token := "super-secret-instance-token"
	timestamp := "1700000000"
	nonce := "abc123def456"
	body := []byte(`{"instance_id":"i-001"}`)

	sig1, err := signHeartbeatBody(token, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("signHeartbeatBody failed: %v", err)
	}
	sig2, err := signHeartbeatBody(token, timestamp, nonce, body)
	if err != nil {
		t.Fatalf("signHeartbeatBody second call failed: %v", err)
	}
	if sig1 != sig2 {
		t.Errorf("signHeartbeatBody not deterministic: got %s, then %s", sig1, sig2)
	}
	if len(sig1) != 64 {
		t.Errorf("expected 64-char hex signature, got %d chars: %s", len(sig1), sig1)
	}

	sigOtherToken, err := signHeartbeatBody("other-token", timestamp, nonce, body)
	if err != nil {
		t.Fatalf("signHeartbeatBody with other token failed: %v", err)
	}
	if sigOtherToken == sig1 {
		t.Error("expected signature to change when token changes (key derivation broken?)")
	}

	sigOtherTS, err := signHeartbeatBody(token, "1700000001", nonce, body)
	if err != nil {
		t.Fatalf("signHeartbeatBody with other timestamp failed: %v", err)
	}
	if sigOtherTS == sig1 {
		t.Error("expected signature to change when timestamp changes")
	}

	sigOtherNonce, err := signHeartbeatBody(token, timestamp, "different-nonce", body)
	if err != nil {
		t.Fatalf("signHeartbeatBody with other nonce failed: %v", err)
	}
	if sigOtherNonce == sig1 {
		t.Error("expected signature to change when nonce changes (replay protection broken)")
	}

	sigOtherBody, err := signHeartbeatBody(token, timestamp, nonce, []byte(`{"instance_id":"i-002"}`))
	if err != nil {
		t.Fatalf("signHeartbeatBody with other body failed: %v", err)
	}
	if sigOtherBody == sig1 {
		t.Error("expected signature to change when body changes")
	}

	keyHash := sha256.Sum256([]byte(token))
	mac := hmac.New(sha256.New, keyHash[:])
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if sig1 != expected {
		t.Errorf("signature mismatch with reference HMAC: got %s, want %s", sig1, expected)
	}

	if _, err := signHeartbeatBody("", timestamp, nonce, body); err == nil {
		t.Error("expected error for empty token, got nil")
	}
}

// TestRandomNonce 验证 randomNonce:
//   - 输出长度 = 2n hex 字符
//   - 多次调用无碰撞(1000 个 16-byte nonce 碰撞概率 < 2^-80)
//   - 输出是合法 hex
func TestRandomNonce(t *testing.T) {
	const n = 16
	seen := make(map[string]struct{}, 1000)
	const samples = 1000

	for i := 0; i < samples; i++ {
		nonce, err := randomNonce(n)
		if err != nil {
			t.Fatalf("randomNonce failed at iter %d: %v", i, err)
		}
		if len(nonce) != 2*n {
			t.Errorf("iter %d: expected %d chars, got %d (%s)", i, 2*n, len(nonce), nonce)
		}
		if _, err := hex.DecodeString(nonce); err != nil {
			t.Errorf("iter %d: nonce %q is not valid hex: %v", i, nonce, err)
		}
		if _, dup := seen[nonce]; dup {
			t.Errorf("nonce collision at iter %d: %s", i, nonce)
		}
		seen[nonce] = struct{}{}
	}

	if len(seen) != samples {
		t.Errorf("expected %d unique nonces, got %d", samples, len(seen))
	}
}

// TestReadInstanceToken_EmptyFile 验证 primary 文件存在但内容为空(仅空白)
// 时，应当回退到 install-state 路径。
func TestReadInstanceToken_EmptyFile(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(homeDir+"/.kx-gateway", 0755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(homeDir+"/.kx-gateway/instance.token", []byte("   \n\t  "), 0600); err != nil {
		t.Fatalf("write primary (empty/whitespace): %v", err)
	}
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", homeDir)
	defer os.Setenv("HOME", oldHome)

	installDir := t.TempDir()
	stateDir := installDir + "/state"
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatalf("mkdir state: %v", err)
	}
	if err := os.WriteFile(stateDir+"/instance.token", []byte("fallback-after-empty"), 0600); err != nil {
		t.Fatalf("write fallback: %v", err)
	}
	oldInstallDir := os.Getenv("INSTALL_DIR")
	os.Setenv("INSTALL_DIR", installDir)
	defer os.Setenv("INSTALL_DIR", oldInstallDir)

	got, err := readInstanceToken()
	if err != nil {
		t.Fatalf("readInstanceToken should have fallen back after empty primary, got error: %v", err)
	}
	if got != "fallback-after-empty" {
		t.Errorf("expected fallback-after-empty, got %s", got)
	}
}
