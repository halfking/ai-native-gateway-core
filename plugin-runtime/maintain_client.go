package pluginruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// CatalogEntry 镜像 maintain 插件目录响应的一个子集（Gateway installer 所需字段）。
// 字段名与 maintain/store repository.go 保持一致。
type CatalogEntry struct {
	PluginID       string           `json:"plugin_id"`
	DisplayName    string           `json:"display_name"`
	LatestVersion  string           `json:"latest_version"`
	LatestBuildSeq int              `json:"latest_build_seq"`
	DefaultChannel string           `json:"default_channel"`
	Releases       []CatalogRelease `json:"releases"`
}

// CatalogRelease 对应目录中某个 release 的一项。
type CatalogRelease struct {
	PluginVersion     string            `json:"plugin_version"`
	BuildSeq          int               `json:"build_seq"`
	Channel           string            `json:"channel"`
	GatewayMinVersion string            `json:"gateway_min_version"`
	GatewayMaxVersion string            `json:"gateway_max_version"`
	APIContract       string            `json:"api_contract"`
	Artifacts         []CatalogArtifact `json:"artifacts"`
}

// CatalogArtifact 对应 release 下某个 platform/arch 的二进制。
type CatalogArtifact struct {
	Platform     string `json:"platform"`
	Arch         string `json:"arch"`
	ArtifactName string `json:"artifact_name"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// PluginTicket 镜像 maintain store.PluginDownloadTicket。
type PluginTicket struct {
	RequestID string    `json:"request_id"`
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	FileName  string    `json:"file_name"`
	SHA256    string    `json:"sha256"`
}

// MaintainCatalogClient 调用 maintain 的 catalog/ticket/download API。
type MaintainCatalogClient struct {
	baseURL  string
	platform string
	arch     string
	gwVer    string
	http     *http.Client
}

// NewMaintainCatalogClient 构造一个指向 maintain baseURL 的客户端。
// platform/arch 会在 ticket 请求和 release 匹配时使用；gatewayVersion 用于
// 版本兼容性过滤。baseURL 末尾的 '/' 会被去掉。
func NewMaintainCatalogClient(baseURL, platform, arch, gatewayVersion string) *MaintainCatalogClient {
	return &MaintainCatalogClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		platform: platform,
		arch:     arch,
		gwVer:    gatewayVersion,
		http:     &http.Client{Timeout: 5 * time.Minute},
	}
}

// Catalog 拉取并解析 maintain 的插件目录。
func (c *MaintainCatalogClient) Catalog(ctx context.Context) ([]CatalogEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/maintain-api/plugins/catalog", nil)
	if err != nil {
		return nil, fmt.Errorf("catalog request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog: status %d", resp.StatusCode)
	}
	var out struct {
		Plugins []CatalogEntry `json:"plugins"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("catalog decode: %w", err)
	}
	return out.Plugins, nil
}

// FindRelease 在已拉取的目录中按 pluginID/version 查找 release，并依据
// 本客户端的 platform/arch 与 gateway 版本兼容性做过滤，返回 release 与
// 匹配到的 artifact。
func (c *MaintainCatalogClient) FindRelease(entries []CatalogEntry, pluginID, version string) (CatalogRelease, CatalogArtifact, error) {
	for _, e := range entries {
		if e.PluginID != pluginID {
			continue
		}
		for _, rel := range e.Releases {
			if rel.PluginVersion != version {
				continue
			}
			if !versionCompatible(c.gwVer, rel.GatewayMinVersion, rel.GatewayMaxVersion) {
				continue
			}
			for _, a := range rel.Artifacts {
				if a.Platform == c.platform && a.Arch == c.arch {
					return rel, a, nil
				}
			}
		}
	}
	return CatalogRelease{}, CatalogArtifact{}, fmt.Errorf(
		"plugin %s version %s not found for %s/%s",
		pluginID, version, c.platform, c.arch,
	)
}

// Ticket 向 maintain 申请 pluginID/version/buildSeq 的下载票据。请求体携带本
// 客户端的 platform/arch。
func (c *MaintainCatalogClient) Ticket(ctx context.Context, pluginID, version string, buildSeq int) (PluginTicket, error) {
	body := map[string]any{
		"version":   version,
		"build_seq": buildSeq,
		"platform":  c.platform,
		"arch":      c.arch,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return PluginTicket{}, fmt.Errorf("ticket encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/maintain-api/plugins/"+pluginID+"/ticket", bytes.NewReader(bodyBytes))
	if err != nil {
		return PluginTicket{}, fmt.Errorf("ticket request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return PluginTicket{}, fmt.Errorf("ticket: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PluginTicket{}, fmt.Errorf("ticket: status %d", resp.StatusCode)
	}
	var ticket PluginTicket
	if err := json.NewDecoder(resp.Body).Decode(&ticket); err != nil {
		return PluginTicket{}, fmt.Errorf("ticket decode: %w", err)
	}
	return ticket, nil
}

// DownloadArtifact 流式下载 ticket.URL 指向的 artifact 到 destDir/ticket.FileName，
// 校验 SHA256 == ticket.SHA256，返回本地路径。destDir 必须已存在。
func (c *MaintainCatalogClient) DownloadArtifact(ctx context.Context, ticket PluginTicket, destDir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ticket.URL, nil)
	if err != nil {
		return "", fmt.Errorf("download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: status %d", resp.StatusCode)
	}
	destPath := destDir + "/" + ticket.FileName
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("download create: %w", err)
	}
	defer out.Close()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), resp.Body); err != nil {
		return "", fmt.Errorf("download copy: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != ticket.SHA256 {
		return "", fmt.Errorf("checksum mismatch: expected %s, got %s", ticket.SHA256, got)
	}
	return destPath, nil
}

// versionCompatible 用字符串比较做简单的版本门控。空边界表示无约束。
// 注意：这是字符串字典序比较，不是真正的 SemVer。P12 足够（asm 0.x、
// gw 2.4.x 区间）。P13+ 应换成真正的 SemVer 解析器。
func versionCompatible(current, minV, maxV string) bool {
	if minV != "" && current < minV {
		return false
	}
	if maxV != "" && current > maxV {
		return false
	}
	return true
}
