package freediscovery

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

// maxScanBodyBytes 上游响应体积上限 (防御恶意/异常端点).
const maxScanBodyBytes = 8 << 20 // 8 MiB

// ProviderScanner 扫描上游提供商的模型列表.
type ProviderScanner interface {
	// ScanModels 拉取并解析模型列表, 过滤出免费条目.
	ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error)
}

// HTTPScanner 基于 OpenAI 兼容 GET {base_url}{models_endpoint} 的通用扫描器.
// Groq / OpenRouter / SiliconFlow / 智谱 等 openai-completions 提供商共用,
// 差异通过 freeOf / poolKeyOf / quotaEstimator 钩子注入.
type HTTPScanner struct {
	client *http.Client

	// freeOf 判断上游模型条目是否属于免费层 (nil = openAICompatibleFreeOf 默认规则)
	freeOf func(m modelEntry) bool
	// poolKeyOf 推断跨模型共享配额池标识 (nil = 无共享池)
	poolKeyOf func(m modelEntry) string
	// quotaEstimator 估算免费配额 (nil = 不估算, 0)
	quotaEstimator func(m modelEntry) (monthly, daily int64)
}

// NewHTTPScanner 构造通用扫描器 (httpClient nil = 默认 30s 超时).
func NewHTTPScanner(httpClient *http.Client) *HTTPScanner {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPScanner{client: httpClient}
}

// modelEntry 上游 /models 响应的单个条目 (OpenAI 兼容形态 + 常见扩展字段).
type modelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`                  // OpenRouter 展示名
	DisplayName string `json:"display_name"`          // Groq 展示名
	ContextLen  int    `json:"context_length"`        // OpenRouter
	ContextWin  int    `json:"context_window"`        // Groq
	MaxOut      int    `json:"max_output_tokens"`     // Groq
	MaxComp     int    `json:"max_completion_tokens"` // OpenRouter
	// OpenRouter 定价 (字符串化的美元/百万token); "0" = 免费
	PricingPrompt     string `json:"pricing_prompt"`
	PricingCompletion string `json:"pricing_completion"`
	// 原始字段兜底
	raw map[string]any
}

func (e modelEntry) displayName() string {
	switch {
	case e.DisplayName != "":
		return e.DisplayName
	case e.Name != "":
		return e.Name
	default:
		return e.ID
	}
}

func (e modelEntry) contextWindow() int {
	if e.ContextWin > 0 {
		return e.ContextWin
	}
	return e.ContextLen
}

func (e modelEntry) maxTokens() int {
	if e.MaxOut > 0 {
		return e.MaxOut
	}
	return e.MaxComp
}

// IsFree 归一化后的免费判定输入: OpenRouter ":free" 后缀或零定价.
func (e modelEntry) isZeroPriced() bool {
	return isZeroPrice(e.PricingPrompt) && isZeroPrice(e.PricingCompletion)
}

func isZeroPrice(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "0" || s == "0.0" || s == "0.00"
}

// ScanModels 拉取 {base_url}{models_endpoint} 并解析.
func (s *HTTPScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	endpoint := tpl.ModelsEndpoint
	if endpoint == "" {
		endpoint = "/models"
	}
	listURL, err := joinURL(tpl.BaseURL, endpoint)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: invalid models endpoint: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// keyless 提供商允许空 Authorization 缺省
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: scan %s: %w", tpl.ProviderCode, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("freediscovery: read scan response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freediscovery: scan %s: upstream status %d: %.200s",
			tpl.ProviderCode, resp.StatusCode, string(body))
	}

	entries, err := parseModelsResponse(body)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: parse %s response: %w", tpl.ProviderCode, err)
	}

	freeOf := s.freeOf
	if freeOf == nil {
		freeOf = openAICompatibleFreeOf
	}

	models := make([]DiscoveredModel, 0, len(entries))
	for _, e := range entries {
		if !freeOf(e) {
			continue
		}
		dm := DiscoveredModel{
			ProviderCode:  tpl.ProviderCode,
			ModelID:       e.ID,
			DisplayName:   e.displayName(),
			ContextWindow: e.contextWindow(),
			MaxTokens:     e.maxTokens(),
			FreeType:      inferFreeType(e),
			RawMetadata:   e.raw,
		}
		if s.poolKeyOf != nil {
			dm.PoolKey = s.poolKeyOf(e)
		}
		if s.quotaEstimator != nil {
			dm.MonthlyTokens, dm.DailyTokens = s.quotaEstimator(e)
		}
		models = append(models, dm)
	}
	return models, nil
}

// parseModelsResponse 兼容两种响应形态:
//   - OpenAI/Groq:  {"data": [...]}
//   - OpenRouter:   {"data": [...]} (同形)
//   - 裸数组:       [...]
func parseModelsResponse(body []byte) ([]modelEntry, error) {
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Data != nil {
		return decodeEntries(envelope.Data)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err == nil {
		return decodeEntries(arr)
	}
	return nil, fmt.Errorf("response is neither {data:[...]} nor [...]")
}

func decodeEntries(raw []json.RawMessage) ([]modelEntry, error) {
	out := make([]modelEntry, 0, len(raw))
	for _, r := range raw {
		var e modelEntry
		if err := json.Unmarshal(r, &e); err != nil {
			continue // 单条畸形数据跳过, 不整体失败
		}
		if e.ID == "" {
			continue
		}
		var meta map[string]any
		_ = json.Unmarshal(r, &meta)
		e.raw = meta
		out = append(out, e)
	}
	return out, nil
}

// openAICompatibleFreeOf 默认免费判定规则:
//   - 模型 ID 带 ":free" 后缀 (OpenRouter)
//   - 或上游定价全为零 (OpenRouter pricing 字段)
func openAICompatibleFreeOf(m modelEntry) bool {
	return strings.HasSuffix(m.ID, ":free") || (m.PricingPrompt != "" && m.isZeroPriced())
}

// inferFreeType 从模型特征推断免费类型 (写入 discovery_results.free_type).
func inferFreeType(m modelEntry) string {
	id := strings.ToLower(m.ID)
	switch {
	case strings.Contains(id, "free"):
		return string(FreeTypeRecurringUncapped) // OpenRouter :free 无总量上限, 仅 RPM/RPD
	case strings.Contains(id, "trial"):
		return string(FreeTypeOneTimeInitial)
	default:
		return string(FreeTypeRecurringUncapped)
	}
}

// joinURL 拼接 base 与 endpoint, 处理斜杠边界.
func joinURL(base, endpoint string) (string, error) {
	if _, err := url.Parse(base); err != nil {
		return "", fmt.Errorf("invalid base_url %q: %w", base, err)
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return base + endpoint, nil
}
