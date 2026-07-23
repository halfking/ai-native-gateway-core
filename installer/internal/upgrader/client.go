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

// DistributionVersionCheck mirrors the spec §2.1 wire payload from
// GET /maintain-api/distribution/version-check on the new ai-native-maintain
// service.
type DistributionVersionCheck struct {
	UpdateAvailable  bool                  `json:"update_available"`
	LatestVersion    string                `json:"latest_version"`
	CurrentVersion   string                `json:"current_version"`
	Channel         string                `json:"channel"`
	Mandatory       bool                  `json:"mandatory"`
	MinRequired     string                `json:"min_required,omitempty"`
	TargetRelease   *DistributionRelease  `json:"target_release,omitempty"`
	TargetArtifacts []DistributionArtifact `json:"target_artifacts,omitempty"`
}

type DistributionRelease struct {
	Version     string `json:"version"`
	BuildSeq    int    `json:"build_seq"`
	Channel     string `json:"channel"`
	ReleaseDate string `json:"release_date,omitempty"`
	NotesURL    string `json:"notes_url,omitempty"`
}

type DistributionArtifact struct {
	Platform   string `json:"platform"`
	Arch       string `json:"arch"`
	Edition    string `json:"edition"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	StorageURI string `json:"storage_uri"`
}

// CheckUpdateDistribution calls the new
// GET /maintain-api/distribution/version-check endpoint and maps its
// result back into the CheckUpdateResponse shape so existing callers
// don't need to change.
func (c *Client) CheckUpdateDistribution(ctx context.Context, currentVersion, channel, platform, arch string) (*CheckUpdateResponse, error) {
	if platform == "" {
		platform = "linux"
	}
	if arch == "" {
		arch = "amd64"
	}
	url := fmt.Sprintf("%s/maintain-api/distribution/version-check?channel=%s&current=%s&platform=%s&arch=%s",
		c.baseURL, channel, currentVersion, platform, arch)
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
	var v DistributionVersionCheck
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	out := &CheckUpdateResponse{HasUpdate: v.UpdateAvailable}
	if v.UpdateAvailable && len(v.TargetArtifacts) > 0 {
		a := v.TargetArtifacts[0]
		out.Release = &Release{
			Version:     v.LatestVersion,
			DownloadURL: a.StorageURI,
			SHA256:      a.SHA256,
			Mandatory:   v.Mandatory,
		}
	}
	return out, nil
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
