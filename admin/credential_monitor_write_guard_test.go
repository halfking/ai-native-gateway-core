package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// 写侧角色门（2026-10-19）
//
// 背景：本组写路由注册在 AdminMiddleware 之下，而该中间件**只认证不检 role**
//（admin/auth.go:86）；读侧已在 audit #153/#154 收口（super_admin / admin_key /
// tenant_admin，role=user 403），**写侧此前完全没有门**，且 promote / demote /
// set-concurrency / model-toggle 的 UPDATE 不带租户过滤 ⇒ 任意已认证用户可
// 跨租户改写凭据状态。
//
// 本文件钉死三件事：
//  ① 六个写处理器对 tenant_admin 一律 403（既定约定：tenant_admin 只读）；
//  ② 六个写处理器对 role=user 一律 403（与读侧同一下限）；
//  ③ super_admin 必须**穿过**角色门（落到 nil-db 的 503）——
//     否则「门存在」可能只是恒真/恒挡的假绿。
//  ④ 角色门必须**早于** nil-db 检查：未授权者得 403，不是 503
//     （503 会泄露「DB 是否已配置」）。
//  ⑤ summary 缓存失效必须连带清掉 provider 全量条目（:c0:）。
// ─────────────────────────────────────────────────────────────────────────────

// credentialMonitorWriteHandlers 返回本组 6 个写处理器，
// 连同**能通过入参校验**的请求体（用于验证放行侧确实走到了 DB 判空）。
// 缺一个都会让下面的门退化成「只测了其中几个」。
func credentialMonitorWriteHandlers(m *CredentialMonitorHandlers) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"promote":               m.handlePromote,
		"demote":                m.handleDemote,
		"set concurrency auto":  m.handleSetConcurrencyAuto,
		"model toggle":          m.handleModelToggle,
		"clear manual disabled": m.handleClearManualDisabled,
		"set manual disabled":   m.handleSetManualDisabled,
	}
}

// credentialMonitorWriteBodies 给出每个处理器**能通过入参校验**的 JSON 体。
// 放行侧断言必须靠它才能落到 nil-db 的 503，而不是停在 400。
func credentialMonitorWriteBodies() map[string]string {
	return map[string]string{
		"promote":               `{"credential_id":1,"reason":"r"}`,
		"demote":                `{"credential_id":1,"reason":"r"}`,
		"set concurrency auto":  `{"credential_id":1,"concurrency_limit_auto":1,"reason":"r"}`,
		"model toggle":          `{"credential_id":1,"raw_model_name":"m","action":"offline","reason":"r"}`,
		"clear manual disabled": `{"credential_id":1,"reason":"r"}`,
		"set manual disabled":   `{"credential_id":1,"manual_disabled":true,"reason":"r"}`,
	}
}

// writeReq 造一个带角色与请求体的 POST 请求。
func writeReq(role, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/credentials/test", strings.NewReader(body))
	return SetAuthContext(req, &AuthContext{Role: role, TenantID: "tenant-a", IsJWT: true})
}

func TestCredentialMonitorWriteFamily_RoleGate(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	handlers := credentialMonitorWriteHandlers(m)
	bodies := credentialMonitorWriteBodies()

	if len(handlers) != 6 {
		t.Fatalf("写处理器族只有 %d 个，期望 6 个 —— 门的作用域被改小了", len(handlers))
	}

	for name, handler := range handlers {
		body := bodies[name]
		t.Run(name, func(t *testing.T) {
			t.Run("tenant_admin 不可写", func(t *testing.T) {
				rr := httptest.NewRecorder()
				handler(rr, writeReq("tenant_admin", body))
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), "read-only") {
					t.Fatalf("403 理由应为「tenant_admin 只读」，实际=%s", rr.Body.String())
				}
			})

			t.Run("role=user 不可写", func(t *testing.T) {
				rr := httptest.NewRecorder()
				handler(rr, writeReq("user", body))
				if rr.Code != http.StatusForbidden {
					t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), "administrator role required") {
					t.Fatalf("403 理由应为「administrator role required」，实际=%s", rr.Body.String())
				}
			})

			// 允许侧的**正向**断言：门必须真的放行 super_admin，
			// 并一路走到 nil-db 判空（503）。若这里只断言「不是 403」，
			// 门被改成「一律 403」也会全绿 ⇒ 那是假绿。
			t.Run("super_admin 穿过角色门", func(t *testing.T) {
				rr := httptest.NewRecorder()
				handler(rr, writeReq("super_admin", body))
				if rr.Code == http.StatusForbidden {
					t.Fatalf("super_admin 被角色门误伤：%s", rr.Body.String())
				}
				if rr.Code != http.StatusServiceUnavailable {
					t.Fatalf("status=%d body=%s, want 503（super_admin 应穿过角色门、停在 nil-db 判空）",
						rr.Code, rr.Body.String())
				}
			})
		})
	}
}

// 角色门必须早于 nil-db 检查：未授权者得 403，不能得 503
// （503 会泄露「DB 是否已配置」，与读侧 role gate 判据同一条口径）。
func TestCredentialMonitorWriteFamily_GatePrecedesNilDB(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	bodies := credentialMonitorWriteBodies()
	for name, handler := range credentialMonitorWriteHandlers(m) {
		t.Run(name, func(t *testing.T) {
			for _, role := range []string{"tenant_admin", "user"} {
				rr := httptest.NewRecorder()
				handler(rr, writeReq(role, bodies[name]))
				if rr.Code != http.StatusForbidden {
					t.Fatalf("role=%s status=%d body=%s, want 403（门必须早于 nil-db 检查）",
						role, rr.Code, rr.Body.String())
				}
			}
		})
	}
}

// summary 缓存失效必须连带清掉 **provider 全量** 条目（key 里的 :c0:）。
// 只删 `:c<id>:` 会让该 provider 的聚合摘要在 TTL(30s) 内继续对外给旧值。
func TestSummaryCacheInvalidateDropsProviderWideEntry(t *testing.T) {
	now := time.Now()
	c := &summaryCache{
		ttl:     time.Minute,
		entries: map[string]summaryCacheEntry{},
	}
	providerWide := monitorSummaryCacheKey(monitorSummarySQLParams{ProviderID: 7, CredentialID: 0, Mode: "core"})
	otherProviderWide := monitorSummaryCacheKey(monitorSummarySQLParams{ProviderID: 9, CredentialID: 0, Mode: "core"})
	changed := monitorSummaryCacheKey(monitorSummarySQLParams{ProviderID: 7, CredentialID: 42, Mode: "detail"})
	unrelated := monitorSummaryCacheKey(monitorSummarySQLParams{ProviderID: 7, CredentialID: 43, Mode: "detail"})

	for _, k := range []string{providerWide, otherProviderWide, changed, unrelated} {
		c.entries[k] = summaryCacheEntry{value: map[string]any{}, created: now, expires: now.Add(time.Minute)}
	}

	c.invalidateCredential(42)

	for _, k := range []string{providerWide, otherProviderWide, changed} {
		if _, ok := c.entries[k]; ok {
			t.Fatalf("key %q 在凭据 42 失效后仍留在缓存里（陈旧值会被继续命中）", k)
		}
	}
	if _, ok := c.entries[unrelated]; !ok {
		t.Fatalf("key %q 被误删：失效只应影响被改凭据与 provider 全量条目", unrelated)
	}

	// 前提仍在场：`:c0:` 必须真的出现在 provider 全量条目的 key 里。
	// 否则上面那条断言可能在「根本没有 :c0: 形态」的情况下恒绿。
	if !strings.Contains(providerWide, ":c0:") {
		t.Fatalf("provider 全量 key %q 不含 :c0: —— 判据的前提不成立", providerWide)
	}
}
