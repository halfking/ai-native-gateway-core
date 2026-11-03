// shadow_reconciliation.go — P1-4：ASM 影子对账报表进网关 Admin
// （会话治理方案 2026-11 P1-4 剩余项「网关 turn 级投影对账报表进 Admin」）。
//
// 报表本体在 ASM 侧（internal/shadow/report.go 的
// /internal/diagnostics/shadow-reconciliation，P0-4 交付）；本 handler 把
// 它带给网关运营面：GET /api/admin/shadow-reconciliation →
// ASM_INTERNAL_ENDPOINT 同路径转发。网关只做转发不聚合——对账事实源在
// ASM，网关侧二次聚合会造出第二份漂移面。
//
// ASM_INTERNAL_ENDPOINT 与 outbox 投递同源（main.go 同一变量装配 ASM
// 内网地址）；未配置 ⇒ 503（不给空报表误导观测，对齐 ASM 侧 nil recorder
// 的 503 语义）。认证复用 admin 路由表的 superAdmin 包装（路由注册处）。
package admin

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// shadowReconciliationTimeout 是转发 ASM 的预算（报表是聚合查询，给足）。
const shadowReconciliationTimeout = 10 * time.Second

// shadowReconciliationUpstream 返回 ASM 内网基址（无尾斜杠），未配置 ⇒ ""。
func shadowReconciliationUpstream() string {
	return strings.TrimRight(strings.TrimSpace(os.Getenv("ASM_INTERNAL_ENDPOINT")), "/")
}

// handleShadowReconciliation 转发 ASM 的对账报表（GET-only，透传状态与
// body；上游失败按上游状态透传，网络错误 502）。
func (h *Handler) handleShadowReconciliation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	base := shadowReconciliationUpstream()
	if base == "" {
		writeError(w, http.StatusServiceUnavailable,
			"shadow reconciliation report unavailable: ASM_INTERNAL_ENDPOINT not configured")
		return
	}
	target := base + "/internal/diagnostics/shadow-reconciliation"
	if raw := r.URL.RawQuery; raw != "" {
		if _, err := url.ParseQuery(raw); err == nil {
			target += "?" + raw
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), shadowReconciliationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeInternalErr(w, "shadow reconciliation: build upstream request failed", err)
		return
	}
	// 透传排障关联头；不带网关 admin 凭据——ASM 内网面按 NetworkPolicy
	// 认证（与 ASM 自己的 diagnostics 一致）。
	if rid := r.Header.Get("X-Request-Id"); rid != "" {
		req.Header.Set("X-Request-Id", rid)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "ASM unreachable: "+err.Error())
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadGateway, "read ASM response: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Gw-Upstream", "asm-shadow-reconciliation")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
