package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func TestMaskSensitiveValue(t *testing.T) {
	cases := []struct {
		typ, raw, want string
	}{
		{"phone", "13800138000", "*******8000"},
		{"email", "ab@example.com", "a***@example.com"},
		{"secret", "supersecret", "[secret len=11]"},
		{"name", "张三丰", "张*丰"},
	}
	for _, c := range cases {
		got := maskSensitiveValue(c.typ, c.raw)
		if got != c.want {
			t.Fatalf("mask(%q,%q)=%q want %q", c.typ, c.raw, got, c.want)
		}
	}
}

func TestServeSessionSanitizeMatches_Redis(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	rc := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rc.Close() })

	sid := "gw_sess_test"
	legacy := sanitize.SanitizeRedisKey(sid)
	if err := rc.HSet(t.Context(), legacy, "{SENSITIVE:phone:1}", "13800138000").Err(); err != nil {
		t.Fatal(err)
	}
	if err := rc.HSet(t.Context(), legacy, "{SENSITIVE:email:1}", "u@example.com").Err(); err != nil {
		t.Fatal(err)
	}

	h := &Handler{redisClient: rc}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/sessions/"+sid+"/sanitize-matches", nil)
	rec := httptest.NewRecorder()
	h.serveSessionSanitizeMatches(rec, req, sid)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "{SENSITIVE:phone:1}") {
		t.Fatalf("expected placeholder in body: %s", body)
	}
	if strings.Contains(body, "13800138000") || strings.Contains(body, "u@example.com") {
		t.Fatalf("raw secret must not appear: %s", body)
	}
	if !strings.Contains(body, "*******8000") {
		t.Fatalf("expected masked phone in body: %s", body)
	}
}

// TestPickSanitizeSessionTenant（F-5，2026-11-04 审计）：钉住会话租户解析
// 的三档判定 —— session_id 不是全局唯一（跨租户可撞名，生产成立过一次），
// 旧口径 `ORDER BY ts DESC LIMIT 1` 在撞名时猜任意一行。
// 口径与 session_closed_outbox.pickSessionTenant 同族：
// 唯一 ⇒ 采用；空 ⇒ 放弃；多租户同名 ⇒ 放弃（不猜）；
// 租户作用域调用方 ⇒ 只认本租户候选。
func TestPickSanitizeSessionTenant(t *testing.T) {
	t.Run("unscoped_unique_adopted", func(t *testing.T) {
		if got := pickSanitizeSessionTenant([]string{"tenant-a"}, ""); got != "tenant-a" {
			t.Fatalf("got %q, want tenant-a", got)
		}
	})
	t.Run("unscoped_empty_refused", func(t *testing.T) {
		if got := pickSanitizeSessionTenant(nil, ""); got != "" {
			t.Fatalf("no candidate must refuse, got %q", got)
		}
	})
	t.Run("unscoped_ambiguous_refused", func(t *testing.T) {
		// 跨租户同名 ⇒ 必须放弃解析，绝不选其中之一。
		if got := pickSanitizeSessionTenant([]string{"tenant-a", "tenant-b"}, ""); got != "" {
			t.Fatalf("ambiguous candidates must refuse, got %q", got)
		}
	})
	t.Run("unscoped_blank_refused", func(t *testing.T) {
		if got := pickSanitizeSessionTenant([]string{"  "}, ""); got != "" {
			t.Fatalf("blank tenant must refuse, got %q", got)
		}
	})
	t.Run("scoped_own_tenant_adopted", func(t *testing.T) {
		if got := pickSanitizeSessionTenant([]string{"tenant-a"}, "tenant-a"); got != "tenant-a" {
			t.Fatalf("got %q, want tenant-a", got)
		}
	})
	t.Run("scoped_foreign_tenant_refused", func(t *testing.T) {
		if got := pickSanitizeSessionTenant([]string{"tenant-b"}, "tenant-a"); got != "" {
			t.Fatalf("foreign tenant must refuse, got %q", got)
		}
	})
}

// TestSessionTenantLookupSQLShapes（F-5）：SQL 形状门（文本级断言，
// 手法同 maas/campaign_reconcile_test.go）。
//   - scoped 查询：hot ∪ parent 两臂都必须带 tenant_id = $2 等值约束
//     （回退成无约束 LIMIT 1 ⇒ 撞名时仍会猜行 ⇒ 门红）；
//   - distinct 查询：必须 DISTINCT 枚举、无 LIMIT 1（不猜行），
//     且两臂照旧并读 hot ∪ parent。
func TestSessionTenantLookupSQLShapes(t *testing.T) {
	scopedArms := strings.Count(sessionTenantScopedSQL, "AND tenant_id = $2")
	if scopedArms != 2 {
		t.Fatalf("scoped SQL tenant predicates = %d, want 2 (hot + parent arms)", scopedArms)
	}
	for _, arm := range []string{"session_turns_hot", "session_turns "} {
		if !strings.Contains(sessionTenantScopedSQL, arm) {
			t.Fatalf("scoped SQL missing arm %q", arm)
		}
	}
	if !strings.Contains(sessionTenantDistinctSQL, "SELECT DISTINCT tenant_id") {
		t.Fatalf("distinct SQL must enumerate DISTINCT tenant_id")
	}
	if strings.Contains(sessionTenantDistinctSQL, "LIMIT 1") {
		t.Fatalf("distinct SQL must not guess a row (no LIMIT 1)")
	}
	for _, arm := range []string{"session_turns_hot", "session_turns "} {
		if !strings.Contains(sessionTenantDistinctSQL, arm) {
			t.Fatalf("distinct SQL missing arm %q", arm)
		}
	}
}
