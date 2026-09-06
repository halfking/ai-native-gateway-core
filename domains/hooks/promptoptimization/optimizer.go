package promptoptimization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// PromptItem 待优化的一条 prompt（与 prompt-optimizer-service 的
// POST /api/v1/prompts/optimize 契约一致）。
type PromptItem struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OptimizeRequest 优化请求。
type OptimizeRequest struct {
	TenantID string         `json:"tenant_id,omitempty"`
	Model    string         `json:"model,omitempty"`
	Prompts  []PromptItem   `json:"prompts"`
	Options  map[string]any `json:"options,omitempty"`
}

// OptimizedPrompt 优化后的单条 prompt。
type OptimizedPrompt struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Changed bool   `json:"changed"`
}

// OptimizeResult 优化结果。
type OptimizeResult struct {
	OptimizationID  string            `json:"optimization_id"`
	CacheHit        bool              `json:"cache_hit"`
	OriginalTokens  int               `json:"original_tokens"`
	OptimizedTokens int               `json:"optimized_tokens"`
	LatencyMs       int64             `json:"latency_ms"`
	Prompts         []OptimizedPrompt `json:"prompts"`
}

// counter 简单的原子计数器。
type counter struct {
	v atomic.Int64
}

// Inc 计数加一并返回新值。
func (c *counter) Inc() int64 {
	return c.v.Add(1)
}

// Value 返回当前值。
func (c *counter) Value() int64 {
	return c.v.Load()
}

// Metrics Hook 运行指标（进程内计数，导出到日志/审计）。
type Metrics struct {
	// Optimizations 成功应用优化的次数。
	Optimizations *counter
	// CacheHits 缓存命中次数。
	CacheHits *counter
	// CacheMisses 缓存未命中次数。
	CacheMisses *counter
	// CacheEvictions 缓存淘汰次数。
	CacheEvictions *counter
	// OptimizationErrors 调用优化服务失败的次数。
	OptimizationErrors *counter
	// Fallbacks 失败后回退到原始 prompt 的次数。
	Fallbacks *counter
}

// NewMetrics 创建全部归零的指标。
func NewMetrics() *Metrics {
	return &Metrics{
		Optimizations:      &counter{},
		CacheHits:          &counter{},
		CacheMisses:        &counter{},
		CacheEvictions:     &counter{},
		OptimizationErrors: &counter{},
		Fallbacks:          &counter{},
	}
}

// Snapshot 返回指标快照（用于日志输出）。
func (m *Metrics) Snapshot() map[string]int64 {
	return map[string]int64{
		"optimizations":       m.Optimizations.Value(),
		"cache_hits":          m.CacheHits.Value(),
		"cache_misses":        m.CacheMisses.Value(),
		"cache_evictions":     m.CacheEvictions.Value(),
		"optimization_errors": m.OptimizationErrors.Value(),
		"fallbacks":           m.Fallbacks.Value(),
	}
}

// OptimizerClient prompt-optimizer-service 的 HTTP 客户端。
type OptimizerClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewOptimizerClient 创建客户端。baseURL 末尾的 "/" 会被去掉。
func NewOptimizerClient(baseURL string, timeout time.Duration) *OptimizerClient {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &OptimizerClient{
		baseURL:    strings.TrimSuffix(baseURL, "/"),
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Optimize 调用 POST /api/v1/prompts/optimize。
// 非 2xx 响应返回错误（调用方负责回退到原始 prompt）。
func (cl *OptimizerClient) Optimize(ctx context.Context, req *OptimizeRequest) (*OptimizeResult, error) {
	if req == nil || len(req.Prompts) == 0 {
		return nil, fmt.Errorf("optimize request requires prompts")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal optimize request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		cl.baseURL+"/api/v1/prompts/optimize", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build optimize request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := cl.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call optimizer: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("optimizer returned status %d", httpResp.StatusCode)
	}

	var result OptimizeResult
	if err := json.NewDecoder(httpResp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode optimizer response: %w", err)
	}
	if len(result.Prompts) == 0 {
		return nil, fmt.Errorf("optimizer returned empty prompts")
	}
	return &result, nil
}
