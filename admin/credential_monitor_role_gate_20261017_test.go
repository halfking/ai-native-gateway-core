package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
)

// 12h-audit 20261017：/api/credentials/{monitor-summary,sliding-window,
// sliding-window/batch} 三端点的角色门控。路由挂 AdminMiddleware（任意已认
// 证 JWT 角色可达），而三者的租户过滤都是 `tenant_admin → 自己租户，其余
// → ""（see all）`——role=user 拿空串读全租户数据；sliding-window 的
// Redis 快路径（`!isTenantAdmin`）连 tenant tag 都没有。这是 audit #154
// 给 model-history 加角色门控时自立的同一标准，本组把它补齐到同族三端点。
//
// 判据钉四件事（把门拆掉任何一条都红）：
// ① role=user → 403（且先于任何数据访问：nil DB 下不会落到 503）；
// ② tenant_admin → 通过门（落nil-DB 503 / 参数校验 400，非 403）；
// ③ super_admin / admin_key → 通过门；
// ④ batch 的门在 readJSON 之前（role=user 空 body 也是 403 而非 400）。

func reqWithRole(method, path string, role string, tenantID string) *http.Request {
	req := newReq(method, path, nil)
	return SetAuthContext(req, &AuthContext{Role: role, TenantID: tenantID})
}

func TestSlidingWindowFamily_UserRoleForbidden(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		handler func(w http.ResponseWriter, r *http.Request)
	}{
		{
			name:    "monitor-summary",
			method:  http.MethodGet,
			path:    "/api/credentials/monitor-summary",
			handler: (&CredentialMonitorHandlers{h: &Handler{}}).handleMonitorSummary,
		},
		{
			name:    "sliding-window",
			method:  http.MethodGet,
			path:    "/api/credentials/sliding-window?credential_id=1&model=gpt",
			handler: (&CredentialMonitorHandlers{h: &Handler{}}).handleSlidingWindow,
		},
		{
			name:    "sliding-window/batch",
			method:  http.MethodPost,
			path:    "/api/credentials/sliding-window/batch",
			handler: (&CredentialMonitorHandlers{h: &Handler{}}).handleSlidingWindowBatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/role=user", func(t *testing.T) {
			rr := rec()
			tc.handler(rr, reqWithRole(tc.method, tc.path, "user", "acme"))
			if rr.Code != http.StatusForbidden {
				t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "administrator role required") {
				t.Fatalf("expected administrator role required, got %s", rr.Body.String())
			}
		})
	}
}

func TestSlidingWindowFamily_AdminRolesPassGate(t *testing.T) {
	// monitor-summary 走真查询路径才能证明「通过门」：pgxmock + 冷缓存。
	newSummaryHandler := func() *CredentialMonitorHandlers {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatalf("pgxmock.NewPool: %v", err)
		}
		t.Cleanup(func() { mock.Close() })
		mock.ExpectQuery(`SELECT[\s\S]*FROM credentials c`).
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(summaryMockRow()).
			WillDelayFor(0)
		return &CredentialMonitorHandlers{h: &Handler{}, summaryDB: mock}
	}

	t.Run("monitor-summary/tenant_admin passes and reads own tenant", func(t *testing.T) {
		m := newSummaryHandler()
		monitorSummaryCache.mu.Lock()
		monitorSummaryCache.entries = nil
		monitorSummaryCache.flights = nil
		monitorSummaryCache.mu.Unlock()
		rr := rec()
		m.handleMonitorSummary(rr, reqWithRole(http.MethodGet,
			"/api/credentials/monitor-summary", "tenant_admin", "acme"))
		if rr.Code == http.StatusForbidden {
			t.Fatalf("tenant_admin must pass the role gate, got 403: %s", rr.Body.String())
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("sliding-window/super_admin passes gate to nil-DB 503", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}} // db intentionally nil
		rr := rec()
		m.handleSlidingWindow(rr, reqWithRole(http.MethodGet,
			"/api/credentials/sliding-window?credential_id=1&model=gpt", "super_admin", ""))
		if rr.Code == http.StatusForbidden {
			t.Fatalf("super_admin must pass the role gate, got 403")
		}
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 (nil db), got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("sliding-window/admin_key passes gate to nil-DB 503", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}}
		rr := rec()
		m.handleSlidingWindow(rr, reqWithRole(http.MethodGet,
			"/api/credentials/sliding-window?credential_id=1&model=gpt", "admin_key", ""))
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("admin_key expected 503 (nil db), got %d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("batch/tenant_admin passes gate into request validation", func(t *testing.T) {
		m := &CredentialMonitorHandlers{h: &Handler{}}
		req := newReq(http.MethodPost, "/api/credentials/sliding-window/batch", strings.NewReader("{}"))
		req = SetAuthContext(req, &AuthContext{Role: "tenant_admin", TenantID: "acme"})
		rr := rec()
		m.handleSlidingWindowBatch(rr, req)
		// 门后第一站是 readJSON/参数校验（空对象 → items required 400），
		// 不是 403 —— 证明门只在 role 维度拦截。
		if rr.Code == http.StatusForbidden {
			t.Fatalf("tenant_admin must pass the batch role gate, got 403")
		}
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "items required") {
			t.Fatalf("expected 400 items required, got %d body=%s", rr.Code, rr.Body.String())
		}
	})
}

// 承重对照：门若被挪到 readJSON 之后，role=user 的**空 body** batch 会先
// 撞 400 而非 403 —— 本测把它钉死在 readJSON 之前。
func TestSlidingWindowBatch_GateBeforeJSONParse(t *testing.T) {
	m := &CredentialMonitorHandlers{h: &Handler{}}
	req := newReq(http.MethodPost, "/api/credentials/sliding-window/batch", nil) // 无 body
	req = SetAuthContext(req, &AuthContext{Role: "user", TenantID: "acme"})
	rr := rec()
	m.handleSlidingWindowBatch(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("gate must run before JSON parse: expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}
