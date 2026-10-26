// Package streaming — summarize_asm.go
//
// asm 在线优先委托器（2026-10-27 多模态标准接口轮）。
//
// 复杂总结（/v1/summarize 的长文本路径）在 ai-session-manager（asm）在线时
// 委托给它的智能体端点 POST /api/v1/summarize（grounded 纪要 + 原文金句），
// asm 离线/未挂载该端点/调用失败时回退网关自己的 map-reduce 管线——两条路
// 都跑本地模型（asm 的 LLM 上游按其 per-purpose env 指向本地；网关管线经环
// 回 chat 面路由本地模型），成本曲线不变。
//
// 在线判定：GET {asm}/api/v1/healthz（asm 的无鉴权存活口，恒 200），结果
// 缓存 30s——探测失败不阻断请求，只决定「这次走谁」。委托调用带 plugin-
// context HMAC 五件套头（与 asm 其余 /api/v1 域同一把尺子，secret 与 asm
// 的 AI_SESSION_MANAGER_GATEWAY_CONTEXT_SECRET 同源 = 网关 cfg.SecretKey）。
//
// 失败语义：委托的任何失败（探测红、非 2xx、超时、坏体）都**响亮地降级**——
// 记 WARN 后走网关管线，绝不把失败返回给调用方；调用方只在响应的 engine
// 字段里看到实际是谁干的。
package streaming

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
)

const (
	// asmHealthCacheTTL 在线判定的缓存窗。asm 重启窗口内最多多走一次失败
	// 委托（马上降级），不该让每个请求都付一次探测延迟。
	asmHealthCacheTTL = 30 * time.Second
	// asmProbeTimeout 探测硬上限：探测是「谁来做」的路由判断，不是业务调用。
	asmProbeTimeout = 800 * time.Millisecond
	// asmDelegateTimeout 单次委托总预算。默认 8min：asm 侧 llmchat 客户端
	// 自带超时（默认 5min），网关侧预算必须 ≥ 上游预算否则白等。
	asmDelegateTimeout = 8 * time.Minute
)

type asmDelegate struct {
	baseURL  string
	pluginID string
	tenantID string
	secret   []byte
	hc       *http.Client
	// budget 是单次委托的 ctx 预算（与 hc.Timeout 同源；ctx 更早被父级
	// 掐断时以父级为准）。
	budget time.Duration

	mu        sync.Mutex
	online    bool
	checkedAt time.Time
}

// newASMDelegate 组装委托器。baseURL 已剥尾斜杠；预算可被
// LLM_GATEWAY_ASM_TIMEOUT（Go duration）覆盖。
func newASMDelegate(baseURL, pluginID, tenantID string, secret []byte) *asmDelegate {
	timeout := asmDelegateTimeout
	if v := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ASM_TIMEOUT")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			timeout = d
		}
	}
	return &asmDelegate{
		baseURL:  strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		pluginID: strings.TrimSpace(pluginID),
		tenantID: strings.TrimSpace(tenantID),
		secret:   secret,
		hc:       &http.Client{Timeout: timeout},
		budget:   timeout,
	}
}

// available 报告 asm 当前是否在线（30s 缓存）。探测失败返回 false 且把
// 缓存置为离线——下次请求 30s 内不再探测，直接走网关管线。
func (a *asmDelegate) available(ctx context.Context) bool {
	a.mu.Lock()
	if time.Since(a.checkedAt) < asmHealthCacheTTL {
		online := a.online
		a.mu.Unlock()
		return online
	}
	a.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, asmProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, a.baseURL+"/api/v1/healthz", nil)
	online := false
	if err == nil {
		if resp, rerr := a.hc.Do(req); rerr == nil {
			_ = resp.Body.Close()
			online = resp.StatusCode == http.StatusOK
		}
	}
	a.mu.Lock()
	a.online = online
	a.checkedAt = time.Now()
	a.mu.Unlock()
	return online
}

// markOffline 在委托调用失败后把缓存置为离线，下一请求直接走网关管线。
func (a *asmDelegate) markOffline() {
	a.mu.Lock()
	a.online = false
	a.checkedAt = time.Now()
	a.mu.Unlock()
}

type asmSummarizeResult struct {
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Quotes    []string `json:"quotes"`
	Model     string   `json:"model"`
	ElapsedMS int64    `json:"elapsed_ms"`
}

// summarize 把长文本委托给 asm。任何失败都以 error 返回（调用方降级），
// 绝不返回半成品。
func (a *asmDelegate) summarize(ctx context.Context, text, style, language string) (asmSummarizeResult, error) {
	var out asmSummarizeResult
	payload, err := json.Marshal(map[string]any{
		"text":     text,
		"style":    style,
		"language": language,
	})
	if err != nil {
		return out, fmt.Errorf("marshal asm payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/api/v1/summarize", bytes.NewReader(payload))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	// plugin-context HMAC 五件套：asm 的 /api/v1 域只认这个（与网关插件
	// 代理同一套签名；secret 双侧同源）。
	pluginruntime.SignHeaders(req.Header, a.secret, a.pluginID, a.tenantID)

	resp, err := a.hc.Do(req)
	if err != nil {
		return out, fmt.Errorf("asm delegate call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := readLimitedResponse(resp.Body, 8<<20)
	if err != nil {
		return out, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		a.markOffline()
		if resp.StatusCode == http.StatusNotFound {
			return out, fmt.Errorf("asm /api/v1/summarize not mounted (env pair unset on asm side)")
		}
		return out, fmt.Errorf("asm delegate http %d: %.200s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("asm delegate response not JSON: %w", err)
	}
	if strings.TrimSpace(out.Summary) == "" {
		return out, fmt.Errorf("asm delegate returned empty summary")
	}
	return out, nil
}

// setASMDelegate 是 SummarizeService 的注入口（main.go 装配时调用；
// LLM_GATEWAY_ASM_BASE_URL 为空 = 功能关闭，长文本恒走网关管线）。
func (s *SummarizeService) SetASMDelegate(baseURL, pluginID, tenantID string, secret []byte) {
	if strings.TrimSpace(baseURL) == "" {
		return
	}
	s.asm = newASMDelegate(baseURL, pluginID, tenantID, secret)
}
