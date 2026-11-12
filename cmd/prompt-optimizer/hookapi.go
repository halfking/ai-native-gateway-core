package main

// hookapi.go 实现**网关内置 Hook** 的兼容端点。
//
// 背景：本仓 domains/hooks/promptoptimization/ 已实现完整的提示词优化 Hook，
// 但它调用的是另一个服务的契约 ——
//
//	POST {PROMPT_OPTIMIZER_URL}/api/v1/prompts/optimize
//	请求 {"tenant_id","model","prompts":[{"role","content"}],"options"}
//	响应 {"optimization_id","cache_hit","original_tokens","optimized_tokens",
//	      "latency_ms","prompts":[{"role","content","changed"}]}
//
// 本服务原先只暴露 /api/v1/optimize（单条 prompt、字段完全不同），
// 因此 Hook 一旦接线就会一直 404 回退 —— "实现了但永远不生效"。
// 本文件把网关 Hook 需要的契约补上，让接线后真的能跑通。
//
// 契约细节（对齐 domains/hooks/promptoptimization/optimizer.go）：
//   - 返回的 prompts 数量必须与请求一致，否则 Hook 判定 count mismatch 并回退；
//   - Hook 只写回 changed=true 的 content；
//   - 非 2xx 一律回退（Hook 侧有 fallback 计数），所以错误必须如实返回状态码。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	optimizer "github.com/kaixuan/llm-gateway-go/internal/prompt-optimizer"
)

// HookOptimizeRequest 是网关 Hook 的请求体。
type HookOptimizeRequest struct {
	TenantID string         `json:"tenant_id,omitempty"`
	Model    string         `json:"model,omitempty"`
	Prompts  []HookPromptIn `json:"prompts"`
	Options  map[string]any `json:"options,omitempty"`
}

// HookPromptIn 是待优化的一条提示词。
type HookPromptIn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// HookOptimizeResult 是网关 Hook 的响应体。
type HookOptimizeResult struct {
	OptimizationID  string          `json:"optimization_id"`
	CacheHit        bool            `json:"cache_hit"`
	OriginalTokens  int             `json:"original_tokens"`
	OptimizedTokens int             `json:"optimized_tokens"`
	LatencyMs       int64           `json:"latency_ms"`
	Prompts         []HookPromptOut `json:"prompts"`

	// 以下是诊断字段，网关 Hook 会忽略（json 解码忽略未知字段）。
	// 保留它们是为了让"这次优化到底做了什么"可观测。
	HarnessElements   []string `json:"harness_elements,omitempty"`
	AppliedStrategies []string `json:"applied_strategies,omitempty"`
	Source            string   `json:"source,omitempty"`
}

// HookPromptOut 是优化后的一条提示词。
type HookPromptOut struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Changed bool   `json:"changed"`
}

// handleHookOptimize 处理 POST /api/v1/prompts/optimize。
func (s *service) handleHookOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, errorResponse{Error: "method not allowed"})
		return
	}

	var req HookOptimizeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.Prompts) == 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "prompts must not be empty"})
		return
	}
	for i, p := range req.Prompts {
		if strings.TrimSpace(p.Content) == "" {
			writeJSON(w, http.StatusBadRequest, errorResponse{
				Error: fmt.Sprintf("prompts[%d].content must not be empty", i),
			})
			return
		}
	}

	start := time.Now()

	// 缓存键：模型 + 全部提示词内容（顺序敏感）。
	key := hookCacheKey(req)
	if cached, ok := s.hookCache.Get(key); ok {
		cached.CacheHit = true
		cached.LatencyMs = time.Since(start).Milliseconds()
		writeJSON(w, http.StatusOK, cached)
		return
	}

	out := make([]HookPromptOut, 0, len(req.Prompts))
	originalTokens := 0
	optimizedTokens := 0
	var harnessElements []string
	var appliedStrategies []string

	for _, p := range req.Prompts {
		res, err := s.opt.Optimize(r.Context(), p.Content, optimizer.OptimizeOptions{
			TargetModel:   req.Model,
			UseMemora:     hookOptionBool(req.Options, "use_memora", true),
			InjectHarness: hookOptionBool(req.Options, "inject_harness", true),
		})
		if err != nil {
			// 优化失败 ⇒ 如实报错，让 Hook 走 fallback（原始 prompt 原样通过）。
			writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
			return
		}
		originalTokens += estimateTokens(p.Content)
		optimizedTokens += estimateTokens(res.Optimized)
		harnessElements = appendUnique(harnessElements, res.HarnessElements...)
		appliedStrategies = appendUnique(appliedStrategies, res.AppliedStrategies...)
		out = append(out, HookPromptOut{
			Role:    p.Role,
			Content: res.Optimized,
			Changed: res.Optimized != p.Content,
		})
	}

	result := HookOptimizeResult{
		OptimizationID:    newOptimizationID(),
		CacheHit:          false,
		OriginalTokens:    originalTokens,
		OptimizedTokens:   optimizedTokens,
		LatencyMs:         time.Since(start).Milliseconds(),
		Prompts:           out,
		HarnessElements:   harnessElements,
		AppliedStrategies: appliedStrategies,
		Source:            "prompt-optimizer",
	}

	s.hookCache.Put(key, result)

	// 历史闭环：把本次优化写回 Memora（best-effort，不影响响应）。
	go s.recordOptimization(r.Context(), req.TenantID, req.Model,
		joinPromptContents(req.Prompts), joinOptimizedContents(out), appliedStrategies)

	writeJSON(w, http.StatusOK, result)
}

// joinPromptContents 把多条提示词内容拼成一段文本（用于历史记录）。
func joinPromptContents(prompts []HookPromptIn) string {
	parts := make([]string, 0, len(prompts))
	for _, p := range prompts {
		parts = append(parts, p.Content)
	}
	return strings.Join(parts, "\n")
}

// joinOptimizedContents 把优化后的内容拼成一段文本。
func joinOptimizedContents(prompts []HookPromptOut) string {
	parts := make([]string, 0, len(prompts))
	for _, p := range prompts {
		parts = append(parts, p.Content)
	}
	return strings.Join(parts, "\n")
}

// hookOptionBool 读 options 里的布尔项。
func hookOptionBool(options map[string]any, key string, def bool) bool {
	if options == nil {
		return def
	}
	v, ok := options[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

// hookCacheKey 计算缓存键。
func hookCacheKey(req HookOptimizeRequest) string {
	h := sha256.New()
	h.Write([]byte(req.Model))
	h.Write([]byte{0})
	for _, p := range req.Prompts {
		h.Write([]byte(p.Role))
		h.Write([]byte{0})
		h.Write([]byte(p.Content))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newOptimizationID 生成优化 ID（opt_ + 8 位 hex）。
func newOptimizationID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机数不可用不应该让优化失败：用时间戳兜底（仍是唯一值量级）。
		return fmt.Sprintf("opt_%d", time.Now().UnixNano())
	}
	return "opt_" + hex.EncodeToString(b[:])
}

// estimateTokens 估算 token 数。
//
// ⚠️ 这是**估算**，不是真实分词结果：本仓不引入分词器依赖（vendor 模式）。
// 口径：CJK 表意文字按 1 token/字，其余按 4 字符/token。
// 该数字只用于网关 Hook 的审计元数据（原始/优化后体积对比），
// 不参与任何质量判定。
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk := 0
	other := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) ||
			unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			cjk++
			continue
		}
		other++
	}
	tokens := cjk + (other+3)/4
	if tokens == 0 {
		return 1
	}
	return tokens
}

// appendUnique 追加去重。
func appendUnique(dst []string, values ...string) []string {
	for _, v := range values {
		found := false
		for _, existing := range dst {
			if existing == v {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, v)
		}
	}
	return dst
}

// hookCache 是优化结果缓存。
//
// 网关 Hook 自己也有缓存（SHA256），但 Hook 缓存的是"要不要调优化服务"，
// 服务端再缓存一层可以把"不同请求、相同内容"的重复调用也吃掉。
type hookCache struct {
	mu      sync.Mutex
	entries map[string]hookCacheEntry
	ttl     time.Duration
}

type hookCacheEntry struct {
	result  HookOptimizeResult
	expired time.Time
}

// newHookCache 构造缓存。ttl <= 0 时禁用。
func newHookCache(ttl time.Duration) *hookCache {
	return &hookCache{entries: map[string]hookCacheEntry{}, ttl: ttl}
}

// Get 取缓存。
func (c *hookCache) Get(key string) (HookOptimizeResult, bool) {
	if c == nil || c.ttl <= 0 {
		return HookOptimizeResult{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return HookOptimizeResult{}, false
	}
	if time.Now().After(e.expired) {
		delete(c.entries, key)
		return HookOptimizeResult{}, false
	}
	return e.result, true
}

// Put 写缓存。
func (c *hookCache) Put(key string, result HookOptimizeResult) {
	if c == nil || c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = hookCacheEntry{result: result, expired: time.Now().Add(c.ttl)}
}
