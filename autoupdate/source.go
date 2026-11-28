package autoupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EnvConfigUpdate 是发布物携带的一条主机配置变更指令。
//
// 语义（升级配置合同）：Key 在主机 env 中已存在 ⇒ 原值保留，除非 Forced=true
// 才覆盖；不存在 ⇒ 追加。安装器在改动前先备份原 env，失败自动还原——
// 「保存主机上原来的配置，只处理新增或强制更新的配置」。
type EnvConfigUpdate struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Forced bool   `json:"forced"`
	Note   string `json:"note,omitempty"`
}

// RemoteRelease 是远端发布源返回的一个候选升级版本。
//
// 字段分两层：
//   - 检索层（Version/BuildSeq/Mandatory/ReleaseNotes）：判断「有没有新版」的依据。
//   - 制品层（BinaryURL/BinarySHA256）：执行安装时真正要下载的东西。
//
// 检索层来自 /api/v1/updates/latest，制品层来自 /api/v1/updates/manifest。
// 两者分属两个端点的原因：latest 用来回答「有没有」，manifest 用来回答「装什么」。
// 只实现检索层而不实现制品层，等于「能发现新版但装不了」——那是半个流程。
type RemoteRelease struct {
	Version      string  `json:"version"`
	BuildSeq     int     `json:"build_seq"`
	Channel      Channel `json:"channel"`
	Mandatory    bool    `json:"mandatory"`
	ManifestURL  string  `json:"manifest_url,omitempty"`
	SHA256       string  `json:"sha256,omitempty"`
	SizeMB       int     `json:"size_mb,omitempty"`
	ReleaseNotes string  `json:"release_notes,omitempty"`

	BinaryURL    string `json:"binary_url,omitempty"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`
	// ConfigUpdates 是该版本要求应用的主机配置变更（新增/强制更新）。
	// 旧服务端不下发 ⇒ nil，安装器跳过配置步骤（行为与旧版一致）。
	ConfigUpdates []EnvConfigUpdate `json:"config_updates,omitempty"`
}

// RemoteSource 是远端版本检索源。
//
// 约定：已是最新时返回 (nil, nil)；只有「真出错」才返回 error。
// 把「没有新版」表达成 error 会让上层把正常状态记成故障。
type RemoteSource interface {
	Latest(ctx context.Context, currentVersion string, currentBuildSeq int, channel Channel) (*RemoteRelease, error)
}

// HTTPRemoteSource 通过 HTTP 访问远端发布源（license-authority 的
// /api/v1/updates/latest + /api/v1/updates/manifest）。
type HTTPRemoteSource struct {
	baseURL      string
	latestPath   string
	manifestPath string
	httpClient   *http.Client
	// tokenFn 每次请求时取令牌（返回空串表示不带 Authorization 头）。
	// 令牌会轮换，所以不能在建结构体时取一次就固化。
	tokenFn func() string
}

// HTTPRemoteSourceOption 用于覆盖默认路径。
type HTTPRemoteSourceOption func(*HTTPRemoteSource)

// WithLatestPath 覆盖「检索最新版本」的路径。
func WithLatestPath(path string) HTTPRemoteSourceOption {
	return func(s *HTTPRemoteSource) { s.latestPath = path }
}

// WithManifestPath 覆盖「取制品清单」的路径。
func WithManifestPath(path string) HTTPRemoteSourceOption {
	return func(s *HTTPRemoteSource) { s.manifestPath = path }
}

// WithTokenFunc 设置动态令牌来源。
func WithTokenFunc(fn func() string) HTTPRemoteSourceOption {
	return func(s *HTTPRemoteSource) { s.tokenFn = fn }
}

// WithHTTPClient 覆盖 HTTP 客户端（测试用）。
func WithHTTPClient(c *http.Client) HTTPRemoteSourceOption {
	return func(s *HTTPRemoteSource) { s.httpClient = c }
}

// NewHTTPRemoteSource 创建远端源客户端。
func NewHTTPRemoteSource(baseURL string, opts ...HTTPRemoteSourceOption) *HTTPRemoteSource {
	s := &HTTPRemoteSource{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		latestPath:   "/api/v1/updates/latest",
		manifestPath: "/api/v1/updates/manifest",
		httpClient:   &http.Client{Timeout: 20 * time.Second},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// latestResponse 是 /api/v1/updates/latest 的响应体。
type latestResponse struct {
	UpToDate     bool    `json:"up_to_date"`
	Version      string  `json:"version"`
	BuildSeq     int     `json:"build_seq"`
	Channel      Channel `json:"channel"`
	Mandatory    bool    `json:"mandatory"`
	ManifestURL  string  `json:"manifest_url"`
	SHA256       string  `json:"sha256"`
	SizeMB       int     `json:"size_mb"`
	ReleaseNotes string  `json:"release_notes"`
}

// manifestResponse 是 /api/v1/updates/manifest 的响应体。
type manifestResponse struct {
	Version      string `json:"version"`
	BuildSeq     int    `json:"build_seq"`
	BinaryURL    string `json:"binary_url"`
	BinarySHA256 string `json:"binary_sha256"`
	Changelog    string `json:"changelog"`
	// ConfigUpdates：主机配置变更指令（新增/强制更新），maintain 发版中心
	// 下发；旧服务端无此字段 ⇒ 解码为 nil，安装按无配置变更处理。
	ConfigUpdates []EnvConfigUpdate `json:"config_updates,omitempty"`
}

// Latest 检索远端是否有比当前更新的版本。
func (s *HTTPRemoteSource) Latest(ctx context.Context, currentVersion string, currentBuildSeq int, channel Channel) (*RemoteRelease, error) {
	if s.baseURL == "" {
		return nil, fmt.Errorf("remote source base URL is not configured")
	}
	if currentVersion == "" {
		currentVersion = "dev"
	}
	if channel == "" {
		channel = ChannelStable
	}

	q := url.Values{}
	q.Set("current_version", currentVersion)
	q.Set("channel", string(channel))
	if currentBuildSeq > 0 {
		// 部分服务端按 build_seq 比较；带上它可以让服务端少做一次版本号反推。
		q.Set("build_seq", fmt.Sprintf("%d", currentBuildSeq))
	}

	endpoint := s.baseURL + s.latestPath + "?" + q.Encode()
	body, err := s.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var resp latestResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode latest release response: %w", err)
	}
	if resp.UpToDate || resp.Version == "" {
		return nil, nil
	}

	rel := &RemoteRelease{
		Version:      resp.Version,
		BuildSeq:     resp.BuildSeq,
		Channel:      resp.Channel,
		Mandatory:    resp.Mandatory,
		ManifestURL:  resp.ManifestURL,
		SHA256:       resp.SHA256,
		SizeMB:       resp.SizeMB,
		ReleaseNotes: resp.ReleaseNotes,
	}
	if rel.Channel == "" {
		rel.Channel = channel
	}

	// 制品信息：优先用 latest 返回的 manifest_url；没有就用约定的相对路径兜底。
	// 取不到制品信息不致命（检索结论仍然成立），但安装阶段会缺 binary_url ⇒
	// 因此这里返回的 release 带一个明确的缺口标记，由调用方决定是否降级。
	manifestURL := resp.ManifestURL
	if manifestURL == "" {
		manifestURL = s.baseURL + s.manifestPath + "?v=" + url.QueryEscape(rel.Version)
	}
	manifestBody, err := s.get(ctx, manifestURL)
	if err != nil {
		// manifest 取不到 ⇒ 保留检索结论，制品层留空。
		return rel, nil
	}
	var mf manifestResponse
	if err := json.Unmarshal(manifestBody, &mf); err == nil {
		rel.BinaryURL = mf.BinaryURL
		rel.BinarySHA256 = mf.BinarySHA256
		rel.ConfigUpdates = mf.ConfigUpdates
		if mf.Changelog != "" && rel.ReleaseNotes == "" {
			rel.ReleaseNotes = mf.Changelog
		}
	}
	return rel, nil
}

// get 发起一次 GET 请求并返回响应体（有界读取）。
func (s *HTTPRemoteSource) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", endpoint, err)
	}
	if s.tokenFn != nil {
		if token := strings.TrimSpace(s.tokenFn()); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", endpoint, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
