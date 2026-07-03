package memoraauto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// KxmemoryClient kxmemory HTTP 客户端
//
// 功能：
//   - 调用 kxmemory 会话接收 API
//   - 支持超时控制
//   - 错误处理
type KxmemoryClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewKxmemoryClient 创建 kxmemory 客户端
func NewKxmemoryClient(baseURL string, timeout time.Duration) *KxmemoryClient {
	return &KxmemoryClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// IngestSession 发送会话数据到 kxmemory
func (c *KxmemoryClient) IngestSession(ctx context.Context, req *SessionIngestRequest) (*SessionIngestResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is nil")
	}

	// 验证必填字段
	if req.SessionKey == "" {
		return nil, fmt.Errorf("session_key is required")
	}
	if req.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}

	// 序列化请求体
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// 创建 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "llm-gateway-go/memoraauto")

	// 发送请求
	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer httpResp.Body.Close()

	// 读取响应体
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// 检查 HTTP 状态码
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("http error %d: %s", httpResp.StatusCode, string(respBody))
	}

	// 解析响应
	var resp SessionIngestResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// 检查业务状态
	if !resp.Success {
		return &resp, fmt.Errorf("kxmemory returned error: %s", resp.Message)
	}

	return &resp, nil
}

// Ping 检查 kxmemory 服务是否可用
func (c *KxmemoryClient) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create ping request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ping failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("kxmemory service unavailable: status %d", resp.StatusCode)
	}

	return nil
}
