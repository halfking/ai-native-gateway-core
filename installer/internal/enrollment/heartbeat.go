package enrollment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// HeartbeatPayload 心跳上报结构体（参考设计稿 §2.3）
type HeartbeatPayload struct {
	InstanceID         string  `json:"instance_id"`
	Version            string  `json:"version"`
	UptimeSecs         int64   `json:"uptime_secs"`
	CurrentConcurrency int     `json:"current_concurrency"`
	Last5MinTPS        float64 `json:"last_5min_tps"`
	Last5MinP99MS      int     `json:"last_5min_p99_ms"`
	LicenseKeyHash     string  `json:"license_key_hash"`
}

// SendHeartbeat 发送心跳到主控端
// 携带 Authorization: Bearer <instance_token> + X-Timestamp + X-Nonce + X-Signature
//
// 安全策略：
//   - Authorization: JWT instance_token（主控端强校验）
//   - X-Timestamp + X-Nonce：防重放（每次请求唯一 nonce）
//   - X-Signature：HMAC-SHA256(timestamp + nonce + body) with key = SHA256(instance_token)
//
// 注意：当前服务端 middleware 尚未对所有心跳启用 X-Signature 校验（jwt 已足够），
// 本实现为"未来启用服务端强校验"做准备——已发出合规格式的 headers，
// 服务端随时可上线 sigverify 中间件而不需修改客户端。
func (c *Client) SendHeartbeat(ctx context.Context, payload HeartbeatPayload) error {
	// 验证必填字段
	if payload.InstanceID == "" {
		return fmt.Errorf("instance_id is required")
	}
	if payload.Version == "" {
		return fmt.Errorf("version is required")
	}

	// 构造请求
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	url := c.BaseURL + "/api/v1/instances/heartbeat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "llm-gw-installer/1.0")

	// 读取 instance_token
	token, err := readInstanceToken()
	if err != nil {
		return fmt.Errorf("read instance token: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)

	// 生成时间戳 + 随机 nonce（防重放）
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := randomNonce(16)
	if err != nil {
		return fmt.Errorf("generate nonce: %w", err)
	}
	httpReq.Header.Set("X-Timestamp", timestamp)
	httpReq.Header.Set("X-Nonce", nonce)

	// HMAC-SHA256(timestamp.nonce.body)，密钥 = SHA256(instance_token)
	// 派生规则见 signHeartbeatBody。将来服务端启用 sigverify 时可直接使用。
	sig, err := signHeartbeatBody(token, timestamp, nonce, body)
	if err != nil {
		return fmt.Errorf("sign heartbeat: %w", err)
	}
	httpReq.Header.Set("X-Signature", sig)

	// 发送请求
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	// 处理错误状态码
	if resp.StatusCode >= 400 {
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// signHeartbeatBody 使用 instance_token 派生密钥对 (timestamp, nonce, body) 做 HMAC-SHA256。
// 返回 hex 编码的签名。派生公式：key = SHA256(instance_token)，
// message = timestamp + "." + nonce + "." + body。
func signHeartbeatBody(instanceToken, timestamp, nonce string, body []byte) (string, error) {
	if instanceToken == "" {
		return "", fmt.Errorf("instance_token is empty")
	}
	keyHash := sha256.Sum256([]byte(instanceToken))
	mac := hmac.New(sha256.New, keyHash[:])
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write([]byte(nonce))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// randomNonce 返回 hex 编码的 n 字节随机 nonce。
func randomNonce(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// readInstanceToken 从 ~/.kx-gateway/instance.token 读取 token
func readInstanceToken() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}

	tokenPath := filepath.Join(homeDir, ".kx-gateway", "instance.token")
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}

	token := string(bytes.TrimSpace(data))
	if token == "" {
		return "", fmt.Errorf("token is empty")
	}

	return token, nil
}
