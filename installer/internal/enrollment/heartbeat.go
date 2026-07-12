package enrollment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
// 携带 Authorization: Bearer <instance_token> + Ed25519 签名头
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

	// TODO: 添加 Ed25519 签名头（依赖 Agent-D1 签名逻辑）
	// 当前简化实现，仅携带 Authorization
	httpReq.Header.Set("X-Signature", "placeholder-signature")

	// 发送请求
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

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
