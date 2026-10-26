package main

// maintain_api_bypass_contract_test.go — 「/maintain-api/* 匿名可达」契约门。
//
// ★ 这道门守的是什么（缺陷原文）：
//   AuthMiddleware 是全局 API-key 门，排在 mux 路由**之前**（cmd/gateway/main.go
//   的 middleware.NewBuilder() 链内，链尾 Then(publicSurface)，而 publicSurface
//   里才挂着 newMaintainGatewayHandler）。⇒ 任何没进该门 bypass 名单的前缀，
//   匿名请求都会在**到达 maintain 挂载之前**就被拒成 401 missing_key。
//
// ★ 事故形态（2026-10-23 实测，211 线上）：
//   访客未登录时，web 前端先探 /maintain-api/healthz 与 /maintain-api/menu/ops
//   （web/src/config/edition.ts 的 probeMaintainAvailable）。实测匿名请求拿到的是
//   **401 {"error":{"code":"missing_key"}}** —— 网关自己的 API-key 门抢先拒的，
//   而不是 maintain_proxy.go 设计的 **503 + maintain.not_configured** 信封。
//   ⇒ 前端两边都只看到「res.ok 为 false」⇒ **分不清「运维平台没配」与
//   「运维平台真在维护态」** ⇒ 首页常驻一条黄色告警「运维平台暂时不可用」。
//
// ★ 修法（middleware/auth_mw.go）：把 "/maintain-api"（exact）与
//   "/maintain-api/"（前缀）登记进该门的 bypass 名单。
//   ★ 关键歧义：PathPrefixes 里的 "/maintain/" **不匹配** "/maintain-api/..."
//   （"-" 不是 "/"）⇒ 两条都要显式登记，漏一条仍然 401。
//
// ★ 门必须承重（负控见 scripts/audit/mutate_maintain_api_bypass_gate.py）：
//   ① 正向腿：匿名 /maintain-api/* ⇒ 503 + maintain.not_configured（**不是** 401）；
//   ② 反向腿：数据面**不得**被顺手放开（/v1/*、/plugins/* 仍 401 missing_key）；
//   ③ 静态腿：bypass 名单里两条都在（改名单即红）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/middleware"
)

// maintainAuthChain 复刻 cmd/gateway/main.go 的关键链序：全局 API-key 门
// 包在 maintain 挂载**外面**（这是缺陷的成因，也是本门能观测到它的前提）。
func maintainAuthChain(legacy http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", legacy)
	return middleware.NewAuthMiddleware("test-global-api-key").Wrap(newMaintainGatewayHandler(mux, nil))
}

func TestMaintainAPIAnonymousProbeGetsNotConfiguredEnvelopeNot401(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")

	for _, path := range []string{"/maintain-api/healthz", "/maintain-api/menu/ops", "/maintain-api"} {
		t.Run(path, func(t *testing.T) {
			handler := maintainAuthChain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Errorf("legacy handler must not be reached for %s", path)
				w.WriteHeader(http.StatusTeapot)
			}))
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d (want 503 maintain.not_configured); body=%s",
					response.Code, response.Body.String())
			}
			body := response.Body.String()
			if strings.Contains(body, "missing_key") {
				t.Fatalf("global API-key gate answered before the maintain mount: %s", body)
			}
			var envelope struct {
				Code      string `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			}
			if err := json.Unmarshal([]byte(body), &envelope); err != nil {
				t.Fatalf("decode envelope: %v (body=%s)", err, body)
			}
			if envelope.Code != "maintain.not_configured" {
				t.Fatalf("code = %q (want maintain.not_configured)", envelope.Code)
			}
			if !envelope.Retryable {
				t.Fatal("retryable = false, want true")
			}
		})
	}
}

// 反向腿：这道豁免**只**覆盖 /maintain-api/*，不得顺手把数据面放开。
//
// ★ 为什么负控路径不选 /plugins/ 与 /mock/：这两个前缀**本来就在** bypass
//
//	名单里（它们是自守卫子系统 —— 端点自己校验，见 auth_mw.go 的 /mock/* 注释），
//	所以「匿名可达」是设计而非缺陷 ⇒ 拿它们当负控会得到**假红**。
//	真正必须 401 的是**数据面**（/v1/*、/v1beta/*）：它们不在名单里，
//	且是消耗真实上游凭据的面 ⇒ 任何「顺手放开」都必须当场变红。
func TestMaintainAPIBypassDoesNotOpenDataPlane(t *testing.T) {
	t.Setenv("MAINTAIN_SERVICE_URL", "")

	for _, path := range []string{"/v1/chat/completions", "/v1/models", "/v1beta/models"} {
		t.Run(path, func(t *testing.T) {
			handler := maintainAuthChain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Errorf("data-plane path %s must not be anonymously reachable", path)
				w.WriteHeader(http.StatusTeapot)
			}))
			request := httptest.NewRequest(http.MethodGet, path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d (want 401 missing_key); body=%s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "missing_key") {
				t.Fatalf("body does not carry missing_key: %s", response.Body.String())
			}
		})
	}
}

// 静态腿放在 middleware 包（middleware/maintain_api_bypass_test.go）：
// bypass 是未导出字段，同包才读得到；不为测试在生产代码上加导出访问器。
