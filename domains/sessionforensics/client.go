package sessionforensics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// HTTPClient is the minimal *http.Client interface used by Client. Tests can
// inject a custom client. Default = http.DefaultClient with 30s timeout.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client is a small wrapper around an *http.Client for the admin
// /api/admin/session-export endpoints. The same wrapper is used by:
//
//   - admin web UI buttons (e.g. "export this session")
//   - CLI tools + CI scripts (`sessionforensics download --id=gw_xxx --out=...`)
//   - cross-host migration (push to staging on host B, pull on host C)
type Client struct {
	BaseURL    string     // 例如 "https://llm.itestu.cn"
	HTTPClient HTTPClient // 默认 = &http.Client{Timeout: 30s}
	Bearer     string     // admin JWT; 空字符串 = 不发 Authorization
	Cookie     string     // 可选：llmgw_session=...

	// 可选：覆盖导出格式
	UserAgent string
}

// NewClient returns a Client with default http.Client (30s timeout).
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		UserAgent:  "sessionforensics/1.0",
	}
}

// Download fetches a session pack from the remote admin endpoint.
//
//	id        — gw_session_id
//	tenantID  — tenant_id (空 → "default")
//
// Equivalent to GET /api/admin/session-export?id=...&tenant=...
func (c *Client) Download(ctx context.Context, id, tenantID string) (*SessionPack, error) {
	if id == "" {
		return nil, fmt.Errorf("sessionforensics: missing session_id")
	}
	if tenantID == "" {
		tenantID = "default"
	}
	q := url.Values{}
	q.Set("id", id)
	q.Set("tenant", tenantID)
	endpoint := c.BaseURL + "/api/admin/session-export?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sessionforensics: download %s: %w", id, err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSessionNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("sessionforensics: download %s: status=%d body=%s",
			id, resp.StatusCode, truncate(string(body), 240))
	}
	var pack SessionPack
	if err := json.Unmarshal(body, &pack); err != nil {
		return nil, fmt.Errorf("sessionforensics: decode pack: %w body=%s",
			err, truncate(string(body), 240))
	}
	if pack.SessionMeta.ID == "" {
		pack.SessionMeta.ID = id
	}
	return &pack, nil
}

// Upload pushes a pack to the staging table on the target host via
// POST /api/admin/session-export/import. Returns the pack_id assigned by the
// target host so the caller can later FetchByPackID it.
func (c *Client) Upload(ctx context.Context, pack *SessionPack) (string, error) {
	if pack == nil {
		return "", fmt.Errorf("sessionforensics: nil pack")
	}
	if pack.SessionMeta.ID == "" {
		return "", fmt.Errorf("sessionforensics: pack.session_meta.id is required")
	}
	endpoint := c.BaseURL + "/api/admin/session-export/import?tenant=" +
		url.QueryEscape(orDefault(pack.SessionMeta.TenantID, "default"))

	body, err := json.Marshal(pack)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	c.applyAuth(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("sessionforensics: upload status=%d body=%s",
			resp.StatusCode, truncate(string(rb), 240))
	}
	var got struct {
		PackID    string `json:"pack_id"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rb, &got); err != nil {
		return "", fmt.Errorf("sessionforensics: decode upload resp: %w", err)
	}
	if got.PackID == "" {
		return "", fmt.Errorf("sessionforensics: empty pack_id in upload response")
	}
	return got.PackID, nil
}

// FetchByPackID 用 pack_id 反向拉回 (POST → GET) staging 包；
// 主要用于跨主机迁移场景：目标主机调用这个接口拿 pack。
func (c *Client) FetchByPackID(ctx context.Context, packID, tenantID string) (*SessionPack, error) {
	if packID == "" {
		return nil, fmt.Errorf("sessionforensics: missing pack_id")
	}
	if tenantID == "" {
		tenantID = "default"
	}
	q := url.Values{}
	q.Set("id", packID)
	q.Set("tenant", tenantID)
	endpoint := c.BaseURL + "/api/admin/session-export/pack?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSessionNotFound
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status=%d body=%s", resp.StatusCode, truncate(string(body), 240))
	}
	var pack SessionPack
	if err := json.Unmarshal(body, &pack); err != nil {
		return nil, err
	}
	return &pack, nil
}

func (c *Client) applyAuth(req *http.Request) {
	if c.Bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.Bearer)
	}
	if c.Cookie != "" {
		req.AddCookie(&http.Cookie{Name: "llmgw_session", Value: c.Cookie})
	}
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
