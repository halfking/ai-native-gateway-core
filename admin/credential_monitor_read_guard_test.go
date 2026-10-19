package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// 读侧角色门补洞（2026-10-20 12h 审计轮）
//
// 背景：`/api/credentials/decisions` 与 `/api/credentials/routing-log` 都注册在
// AdminMiddleware 之下，而该中间件**只认证不检 role**；这两个处理器此前
// **完全没有角色门**，而它们的租户过滤只在 `IsTenantAdmin(r)` 为真时才生效
// ⇒ role=user 的调用方 tenant_id 为空 ⇒ 读到**全租户**的路由决策 /
// 路由探针 / 状态变更日志（request_id、model、outbound_model、error_class、
// chosen_provider_id），且这些表没有 RLS 兜底。
//
// 与 audit #154 在 model-history 上关掉的洞是同一个；同族的读侧
// handleMonitorSummary 早已补上同款门（`!IsTenantAdmin && !IsSuperAdminOrLegacy`
// ⇒ 403）。
//
// 本文件钉死三件事：
//  ① role=user 对这两个读路由一律 403（不得读到任何租户的数据）；
//  ② tenant_admin / super_admin / admin_key 必须**穿过**角色门
//     （落到 nil-db 的 503）——否则「门存在」可能只是恒真/恒挡的假绿；
//  ③ 角色门必须**早于** nil-db 检查：未授权者得 403，不是 503
//     （503 会泄露「DB 是否已配置」）。
// ─────────────────────────────────────────────────────────────────────────────

// credentialMonitorPrivilegedReadHandlers 返回本组**读**路由的处理器。
func credentialMonitorPrivilegedReadHandlers(m *CredentialMonitorHandlers) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"decisions":   m.handleCredentialDecisions,
		"routing log": m.handleCredentialRoutingLog,
	}
}

// readReq 造一个带角色的 GET 请求。
func readReq(role string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/test", nil)
	return SetAuthContext(req, &AuthContext{Role: role, TenantID: "tenant-a", IsJWT: true})
}

func TestCredentialMonitorPrivilegedReads_RoleGateRejectsPlainUser(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	handlers := credentialMonitorPrivilegedReadHandlers(m)
	if len(handlers) != 2 {
		t.Fatalf("受门保护的读处理器只有 %d 个，期望 2 个 —— 门的作用域被改小了", len(handlers))
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, readReq("user"))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("role=user 必须 403，实际 %d（body=%s）—— "+
					"未授权调用方读到了全租户的路由/决策日志", rec.Code, rec.Body.String())
			}
		})
	}
}

// 放行侧：门必须真的存在，且**早于** nil-db 门（未授权者 403，不泄露 DB 配置）。
func TestCredentialMonitorPrivilegedReads_RoleGateAdminsPassAndPrecedeNilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	handlers := credentialMonitorPrivilegedReadHandlers(m)
	for name, h := range handlers {
		for _, role := range []string{"tenant_admin", "super_admin", "admin_key"} {
			t.Run(name+"/"+role, func(t *testing.T) {
				rec := httptest.NewRecorder()
				h(rec, readReq(role))
				// 穿过角色门后应落到 nil-db 的 503（DB 未配置）。
				if rec.Code != http.StatusServiceUnavailable {
					t.Fatalf("role=%s 应穿过角色门落到 nil-db 的 503，实际 %d（body=%s）",
						role, rec.Code, rec.Body.String())
				}
			})
		}
	}
}
