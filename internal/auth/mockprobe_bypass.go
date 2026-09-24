// mockprobe_bypass.go —— Mock Probe 通道的鉴权旁路（2026-09-24，
// docs/design/2026-09-23-mock-probe-channel §二/§三 Step 3）。
//
// 背景：主 mux 外层是 simpleAPIKeyAuth（X-API-Key 静态校验）。mock 探测
// 客户端走网关自身入口验全链路，但其凭证是系统白名单 token
// "mock-probe-client"（Authorization: Bearer），不在 api_keys 表里——
// 因此需要一条显式旁路：
//
//   - MockProbeBypass：authChain 装配点使用。enabled 且请求命中
//     （/mock/ 前缀 + Bearer mock-probe-client）时绕过 API-key 门直进
//     mux；其余请求走原鉴权，行为零变化。
//   - MockEndpoint：每个 /mock/v1/* 端点自身的守卫。无论外层鉴权是否
//     启用（LLM_GATEWAY_API_KEY 未配时 simpleAPIKeyAuth 不挂），mock
//     端点都强制要求 Bearer mock-probe-client + 供应商 scope 白名单，
//     防止外部嗅探使用 mock 端点（设计 §六 风险表第 1 条）。
//
// token 是公开的系统标记（非机密），不落 api_keys 表，不参与限流器。
package auth

import (
	"net/http"
	"strings"
)

// MockProbeClientToken 是 mock 探测客户端的 Bearer 凭证（系统白名单，
// 非机密；docs/design/2026-09-23-mock-probe-channel §二 鉴权旁路）。
const MockProbeClientToken = "mock-probe-client"

// mockProbeSuppliers 是允许的 mock 供应商 code 白名单（scope 契约，
// 与 internal/providers/mock 的 CodeFast/CodeSlow 对齐——由
// providers/mock 的测试交叉断言防漂移）。
var mockProbeSuppliers = map[string]struct{}{
	"mock-fast": {},
	"mock-slow": {},
}

// IsMockProbeClient 判断请求是否携带 mock 探测客户端凭证
// （Authorization: Bearer mock-probe-client，大小写不敏感，容错空白）。
func IsMockProbeClient(r *http.Request) bool {
	if r == nil {
		return false
	}
	return bearerToken(r) == MockProbeClientToken
}

// EnforceMockProbeScope 判断供应商 code 是否在 mock probe 白名单内
// （{mock-fast, mock-slow}）。mock 端点在进入 handler 前必须通过它，
// 越权 code 一律拒绝。
func EnforceMockProbeScope(providerCode string) bool {
	_, ok := mockProbeSuppliers[providerCode]
	return ok
}

// bearerToken 提取 Authorization: Bearer <token>（无 Bearer 前缀或空值
// 返回空串）。
func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// MockProbeBypass 把 mock 探测请求绕过 API-key 门。
//
// 装配形态（cmd/gateway-v2 authChain）：
//
//	h := httpHandler(deps)                              // 主 mux
//	gate := simpleAPIKeyAuth(key)(h)                    // 原鉴权链
//	h = auth.MockProbeBypass(enabled, gate, h)          // 旁路包装
//
// enabled=false（子系统关闭）时不旁路任何请求：mock 端点未注册，
// /mock/* 请求走原鉴权后落 mux 404，语义与未部署该子系统完全一致。
func MockProbeBypass(enabled bool, apiGate, unprotected http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled && strings.HasPrefix(r.URL.Path, "/mock/") && IsMockProbeClient(r) {
			unprotected.ServeHTTP(w, r)
			return
		}
		apiGate.ServeHTTP(w, r)
	})
}

// MockEndpoint 是 /mock/v1/* 端点守卫：强制 POST + Bearer
// mock-probe-client + 供应商 scope 白名单，任一不满足即拒绝。
func MockEndpoint(supplier string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed, use POST"}`, http.StatusMethodNotAllowed)
			return
		}
		if !IsMockProbeClient(r) {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !EnforceMockProbeScope(supplier) {
			http.Error(w, `{"error":"forbidden mock supplier"}`, http.StatusForbidden)
			return
		}
		next(w, r)
	}
}
