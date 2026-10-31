package admin

// extension_points_test.go —— P2-2 的 admin **薄壳**读端门。
//
// AUDIT-GUARD: 本文件是 P2-2 薄壳的审计门 ⇒ 其所在包 ./admin 必须被登记进
// Makefile 的门禁清单（§3.17 起登记进 GUARD_PACKAGES_SLOW）。
//
// ★ 合并逻辑与载荷形状的门在 `internal/extpointledger`（包内）与
// `scripts/ursmcheck/magpie_p2_extension_ledger_test.go`（CI 必跑）；
// 本文件只钉**薄壳本身**：路由真的注册了、真的被鉴权包着、响应是合法 JSON。
//
// ★ 为什么薄壳的门不放在 `scripts/ursmcheck`：
// `handleExtensionPoints` 是**未导出**方法 ⇒ 外部包调不到 ⇒ 只能在本包里测。
//
// ★★★★ 2026-10-24 §3.17 订正（原注释的三处错误，勿回退）：
//
//	① 「⇒ ★ **本包不在 `GUARD_PACKAGES` 里** ⇒ 这道门在 CI 上**不跑**」
//	   ——★ **错**。本包现在**登记在 `GUARD_PACKAGES_SLOW`**（慢门，1800s 预算）。
//	② 「⇒ ⇒ 所以它只做**兜底**，**主要**判据在台账包与 ursmcheck 里」
//	   ——★ **仍然成立**（薄壳只覆盖序列化 + 路由 + 鉴权三件事），但
//	   「不跑」的说法已废：慢门在本地 `make guards` 与官方门 `verify.sh`
//	   的 `go test ./...` 里都跑它。
//	③ 「⇒ ⇒ ★★ **整个 `admin` 包的测试没有任何 CI 路径**」
//	   ——★ **错**，见审计档 §3.17.2：官方门 `verify.sh` 跑的是
//	   `go test ./... -count=1 -timeout=1800s` ⇒ **`./admin` 在其中**
//	   （实测：红过的那条 `TestTurnsSessionSQLHasNoFormatArtifacts`
//	   在 `-short` 模式下同样复现 ⇒ 它在官方门上**会被看见**）。
//	   ⇒ ★ 真正缺的不是「admin 的红没人看」，而是「**审计守卫体系整体在 CI 上空转**」
//	   ⇒ 已由 `.workflow/main-verify.yml` 接 `make guards-fast` 修复。
//
// ★ 2026-10-24 变异 M4 暴露的缺口（本门已补强）：
// 初版只**直接调** `h.handleExtensionPoints` ⇒ ★ 路由被摘掉时它仍然绿
// ⇒ 而它的文件头却写着「路由挂上了、鉴权套上了」⇒ ★ **名实不符**。
// ⇒ 现补：**真的注册路由**（`RegisterRoutes`）⇒ 无凭据请求该路径
//   ⇒ 期望 **401**（路由在 + 被鉴权包着）；路由不在 ⇒ 404 ⇒ 门红。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// TestHandleExtensionPointsIsThinJSONShell 薄壳：返回合法 JSON，
// 且**行数与裁决表一致**。
func TestHandleExtensionPointsIsThinJSONShell(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleExtensionPoints(rec, httptest.NewRequest(http.MethodGet, "/api/admin/extension-points", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		ExtensionPoints []map[string]any `json:"extension_points"`
		LedgerScope     map[string]any   `json:"ledger_scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON：%v; body=%s", err, rec.Body.String())
	}
	if len(payload.ExtensionPoints) != len(extpoints.Mechanisms()) {
		t.Fatalf("extension_points 条数 = %d，want %d",
			len(payload.ExtensionPoints), len(extpoints.Mechanisms()))
	}
	if len(payload.LedgerScope) == 0 {
		t.Error("ledger_scope 不能为空（口径必须写在响应里）")
	}
}

// TestExtensionPointsRouteIsRegisteredAndProtected：
// ★ **真的注册路由** ⇒ 无凭据请求该路径 ⇒ 期望 **401**。
//
// ⇒ 401 同时证明两件事：**路由存在**（否则是 404）+ **鉴权包装在**（否则是 200）。
// ★ 零值 Handler + 无 token ⇒ 中间件在碰 DB **之前**就 401（`admin/auth.go`）
// ⇒ ⇒ 本门零外部依赖、零生产写入。
func TestExtensionPointsRouteIsRegisteredAndProtected(t *testing.T) {
	h := &Handler{}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/extension-points", nil))

	switch rec.Code {
	case http.StatusUnauthorized:
		// ★ 期望值：路由在、鉴权在。
	case http.StatusNotFound:
		t.Fatal("404 ⇒ 路由没注册（★ 变异 M4 的形态）")
	case http.StatusOK:
		t.Fatal("200 ⇒ ★ 路由没被鉴权包住（公开端点）")
	default:
		t.Fatalf("status = %d，want 401（无凭据）; body=%s", rec.Code, rec.Body.String())
	}
}
