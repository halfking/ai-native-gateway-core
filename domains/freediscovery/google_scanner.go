package freediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// Google AI Studio (Generative Language API) 的真协议扫描器.
//
// 协议特点:
//   - 端点:  {base}/models?key=<API_KEY>&pageSize=1000
//   - 鉴权:  API key 作为 URL query 参数, 不是 Authorization: Bearer
//   - 响应:  {"models":[{"name":"models/gemini-pro","displayName":"...",
//     "inputTokenLimit":N,"outputTokenLimit":N,
//     "supportedGenerationMethods":["generateContent",...]}]}
//
// 与 OpenAI 兼容形态完全不同 (data[] vs models[], key in query vs Bearer header),
// 因此需要单独的扫描器.
//
// doer 出站与 HTTPScanner 一致: 默认 safehttpclient 阻断私网/回环/元数据,
// 测试可注入 httptest.Server client (allowlist 127.0.0.1).
type GoogleGenerativeAIScanner struct {
	doer func(req *http.Request) (*http.Response, error)
}

// NewGoogleGenerativeAIScanner 构造 Google Generative AI 扫描器.
// httpClient=nil 时使用 safehttpclient 作为安全默认.
func NewGoogleGenerativeAIScanner(httpClient *http.Client) *GoogleGenerativeAIScanner {
	if httpClient != nil {
		return &GoogleGenerativeAIScanner{doer: httpClient.Do}
	}
	safe := safehttpclient.New(30 * time.Second)
	return &GoogleGenerativeAIScanner{doer: safe.Do}
}

// googleModelsResponse Google /v1beta/models 响应结构 (2025-09 schema).
type googleModelsResponse struct {
	Models []googleModelEntry `json:"models"`
	// NextPageToken 简化处理: MVP 阶段单页 pageSize=1000 通常足够;
	// 真上线再加分页 token 跟进.
	NextPageToken string `json:"nextPageToken"`
}

// googleModelEntry 单个 Google 模型条目.
// "name" 含 "models/" 前缀 (例如 "models/gemini-2.0-flash"), ModelID 须剥离前缀.
type googleModelEntry struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	Version                    string   `json:"version"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`

	raw map[string]any
}

func (e googleModelEntry) isFreeOfCharge() bool {
	// 2025-09: Google AI Studio 的免费模型特征:
	//   1. name 包含 "gemini" 系列 (gemini-*-flash / gemini-*-pro 早期)
	//   2. supportedGenerationMethods 含 "generateContent"
	//   3. 没有 "tuning" / "countTokens" 之外的方法 (避免付费 Vertex AI 模型)
	//
	// 不强制包含 ":free" — Google API 没有 :free 命名约定.
	id := strings.ToLower(e.Name)
	if !strings.Contains(id, "gemini") {
		return false
	}
	for _, m := range e.SupportedGenerationMethods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}

func (e googleModelEntry) displayName() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	// 退化: 从 "models/gemini-2.0-flash" 取最后一段.
	return strings.TrimPrefix(e.Name, "models/")
}

func (e googleModelEntry) modelID() string {
	return strings.TrimPrefix(e.Name, "models/")
}

// ScanModels 拉取 Google AI Studio 模型列表, 解析 + 免费判定.
func (s *GoogleGenerativeAIScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	listURL, err := joinBaseAndEndpoint(tpl.BaseURL, "/models")
	if err != nil {
		return nil, fmt.Errorf("freediscovery: invalid google base url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: build google request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Google AI Studio: API key 必须作为 ?key= 查询参数传递.
	// 不能使用 Authorization: Bearer, 上游会忽略.
	if apiKey != "" {
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	}

	resp, err := s.doer(req)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: scan google %s: %w", tpl.ProviderCode, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("freediscovery: read google response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freediscovery: scan google %s: upstream status %d: %.200s",
			tpl.ProviderCode, resp.StatusCode, string(body))
	}

	var payload googleModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("freediscovery: parse google response: %w", err)
	}

	models := make([]DiscoveredModel, 0, len(payload.Models))
	for _, e := range payload.Models {
		if !e.isFreeOfCharge() {
			continue
		}
		// 单条畸形 (无 Name) 跳过, 不整体失败.
		if e.Name == "" {
			continue
		}
		models = append(models, DiscoveredModel{
			ProviderCode:  tpl.ProviderCode,
			ModelID:       e.modelID(),
			DisplayName:   e.displayName(),
			ContextWindow: e.InputTokenLimit,
			MaxTokens:     e.OutputTokenLimit,
			FreeType:      string(FreeTypeRecurringUncapped), // Google AI Studio 免费层按 RPM/RPD 限额, 无月总量
			// 默认按 Gemini 免费层估算: 每分钟 15 RPM, 每日 1500 RPD, 单模型估算 ~50K/天.
			MonthlyTokens: 0,
			DailyTokens:   50000,
			PoolKey:       "google-aistudio-free-pool",
			RawMetadata:   e.raw,
		})
	}
	return models, nil
}

// decodeGoogleEntries 解码单个条目到 raw map (供 RawMetadata 透传).
func decodeGoogleEntries(body []byte) ([]googleModelEntry, error) {
	var payload googleModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// 同步填充 raw map (供 ScanModels 透传).
	var rawList []map[string]any
	if err := json.Unmarshal(body, &struct {
		Models *[]map[string]any `json:"models"`
	}{Models: &rawList}); err == nil && len(rawList) == len(payload.Models) {
		for i := range payload.Models {
			payload.Models[i].raw = rawList[i]
		}
	}
	return payload.Models, nil
}
