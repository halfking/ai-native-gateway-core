package upgrader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client HTTP 客户端，调用主控端 API
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient 创建客户端
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// CheckUpdateResponse 检查更新响应
type CheckUpdateResponse struct {
	HasUpdate bool     `json:"has_update"`
	Release   *Release `json:"release,omitempty"`
}

// Release 发布信息
type Release struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256      string `json:"sha256"`
	Changelog   string `json:"changelog"`
	Mandatory   bool   `json:"mandatory"`
}

// CheckUpdate 检查更新
func (c *Client) CheckUpdate(ctx context.Context, currentVersion, channel string) (*CheckUpdateResponse, error) {
	url := fmt.Sprintf("%s/api/v1/updates/latest?channel=%s&current_version=%s",
		c.baseURL, channel, currentVersion)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}

	var result CheckUpdateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// ReportUpdateRequest 上报更新结果
type ReportUpdateRequest struct {
	InstanceID string `json:"instance_id"`
	Version    string `json:"version"`
	Status     string `json:"status"` // success / failed / rolled_back
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms"`
}

// ReportUpdate 上报更新结果
func (c *Client) ReportUpdate(ctx context.Context, req *ReportUpdateRequest) error {
	url := fmt.Sprintf("%s/api/v1/updates/report", c.baseURL)
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// ReportRollbackRequest 上报回滚
type ReportRollbackRequest struct {
	InstanceID string `json:"instance_id"`
	ToVersion  string `json:"to_version"`
	Reason     string `json:"reason"`
}

// ReportRollback 上报回滚
func (c *Client) ReportRollback(ctx context.Context, req *ReportRollbackRequest) error {
	url := fmt.Sprintf("%s/api/v1/updates/rollback", c.baseURL)
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
