// session_closed_outbox_test.go — session.closed.v1 的租户归属口径回归门
// （2026-11-03 12h 审计 P1-2）。
//
// 钉住「session_id 不是全局唯一 ⇒ 租户必须枚举判定、歧义必须拒绝」这条：
// public.sessions 按月分区，唯一键是 (session_id, partition_date)
// ⇒ 跨租户可同名、同一租户跨月可同名。
// 旧口径 `WHERE session_id=$1 LIMIT 1` 在多行时返回任意一行
// ⇒ session.closed.v1 会挂到别的租户名下（跨租户污染）。
package admin

import "testing"

// TestPickSessionTenantRejectsAmbiguity 钉住三条判定：
// 唯一 ⇒ 采用；空 ⇒ 拒绝；多租户同名 ⇒ 拒绝（不猜）。
func TestPickSessionTenantRejectsAmbiguity(t *testing.T) {
	t.Run("unique", func(t *testing.T) {
		got, err := pickSessionTenant("s-1", []string{"tenant-a"})
		if err != nil {
			t.Fatalf("unique tenant rejected: %v", err)
		}
		if got != "tenant-a" {
			t.Fatalf("tenant = %q, want tenant-a", got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if _, err := pickSessionTenant("s-1", nil); err == nil {
			t.Fatal("no tenant row must be rejected (fail-open, no event)")
		}
	})

	t.Run("ambiguous", func(t *testing.T) {
		// 跨租户同名 ⇒ 必须拒绝发布，绝不选其中之一。
		if _, err := pickSessionTenant("s-1", []string{"tenant-a", "tenant-b"}); err == nil {
			t.Fatal("ambiguous tenant must be rejected (cross-tenant write forbidden)")
		}
	})

	t.Run("blank", func(t *testing.T) {
		if _, err := pickSessionTenant("s-1", []string{"  "}); err == nil {
			t.Fatal("blank tenant must be rejected")
		}
	})
}
