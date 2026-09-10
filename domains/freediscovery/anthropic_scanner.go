package freediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// Anthropic Messages API 的真协议扫描器.
//
// 协议特点:
//   - 端点:  {base}/v1/models?limit=1000 (base 默认 https://api.anthropic.com)
//   - 鉴权:  x-api-key 请求头 + anthropic-version: 2023-06-01
//   - 响应:  {"data":[{"id":"claude-...","type":"model","display_name":"..."}],
//     "has_more":bool,"first_id":...,"last_id":...}
//
// 与 OpenAI 兼容形态的差异: data[] 内是 display_name (camelCase) 而非
// display_name/context_window 数值字段; 鉴权是 x-api-key 而非 Bearer,
// 因此不能复用 HTTPScanner, 需要独立实现.
//
// doer 出站与 HTTPScanner 一致: 默认 safehttpclient 阻断私网/回环/元数据,
// 测试可注入 httptest.Server client (allowlist 127.0.0.1).
type AnthropicScanner struct {
	doer func(req *http.Request) (*http.Response, error)
}

// NewAnthropicScanner 构造 Anthropic 扫描器. httpClient=nil 时使用
// safehttpclient 作为安全默认.
func NewAnthropicScanner(httpClient *http.Client) *AnthropicScanner {
	if httpClient != nil {
		return &AnthropicScanner{doer: httpClient.Do}
	}
	safe := safehttpclient.New(30 * time.Second)
	return &AnthropicScanner{doer: safe.Do}
}

// anthropicModelsResponse Anthropic /v1/models 响应结构.
type anthropicModelsResponse struct {
	Data    []anthropicModelEntry `json:"data"`
	HasMore bool                  `json:"has_more"`
	LastID  string                `json:"last_id"`
}

// anthropicModelEntry 单个 Anthropic 模型条目.
type anthropicModelEntry struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`

	raw map[string]any
}

func (e anthropicModelEntry) displayName() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.ID
}

// anthropicModelsPerPage Anthropic 单页上限 (API 最大值, 减少分页往返).
const anthropicModelsPerPage = 1000

// ScanModels 拉取 Anthropic 模型列表 (自动翻页, has_more 跟进),
// 解析后全部返回为待审查候选 — Anthropic API 无公开免费层,
// 免费判定交给 ToSChecker (preset verdict=caution → 默认不自动导入).
func (s *AnthropicScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("freediscovery: scan anthropic %s: api key required (x-api-key)", tpl.ProviderCode)
	}

	base := tpl.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}

	models := make([]DiscoveredModel, 0, 64)
	afterID := ""
	for page := 0; page < 10; page++ { // 硬上限 10 页 = 10000 模型, 防御异常上游
		pageURL, err := joinBaseAndEndpoint(base, "/v1/models")
		if err != nil {
			return nil, fmt.Errorf("freediscovery: invalid anthropic base url: %w", err)
		}
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", anthropicModelsPerPage))
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		listURL := pageURL + "?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("freediscovery: build anthropic request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := s.doer(req)
		if err != nil {
			return nil, fmt.Errorf("freediscovery: scan anthropic %s: %w", tpl.ProviderCode, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("freediscovery: read anthropic response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("freediscovery: scan anthropic %s: upstream status %d: %.200s",
				tpl.ProviderCode, resp.StatusCode, string(body))
		}

		var payload anthropicModelsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("freediscovery: parse anthropic response: %w", err)
		}

		var rawList []map[string]any
		_ = json.Unmarshal(body, &struct {
			Data *[]map[string]any `json:"data"`
		}{Data: &rawList})

		for i, e := range payload.Data {
			if e.ID == "" {
				continue // 单条畸形跳过, 不整体失败.
			}
			var raw map[string]any
			if i < len(rawList) {
				raw = rawList[i]
			}
			models = append(models, DiscoveredModel{
				ProviderCode: tpl.ProviderCode,
				ModelID:      e.ID,
				DisplayName:  e.displayName(),
				FreeType:     string(FreeTypeRecurringUncapped), // 待 ToS 人工审查确认
				PoolKey:      "anthropic-api-pool",
				RawMetadata:  raw,
			})
		}

		if !payload.HasMore || payload.LastID == "" || payload.LastID == afterID {
			break
		}
		afterID = payload.LastID
	}
	return models, nil
}
