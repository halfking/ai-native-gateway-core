package startup

import (
	"os"
	"strings"
	"testing"
)

// stripSQLCommentsFor637 drops line comments so substring assertions target
// real SQL rather than inline annotations (same pattern as 626/631 tests).
func stripSQLCommentsFor637(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// TestMigration637MakesTodayPromotedRowsVisible locks in the P0 fix from the
// 2026-09-01 24h-audit round 2: the unified view's partition branch must NOT
// filter partition_date anymore, because a row written and promoted on the
// same day would otherwise vanish from the view until midnight (promote is a
// move — hot row deleted as parent row is inserted — so the branches cannot
// overlap and the filter only ever hid rows).
func TestMigration637MakesTodayPromotedRowsVisible(t *testing.T) {
	data, err := os.ReadFile("637_session_bodies_unified_today_visible.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLCommentsFor637(string(data))

	mustContain := []string{
		// 视图契约保持 625 的形状
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"FROM public.session_bodies_hot",
		"UNION ALL",
		"FROM public.session_bodies",
		"partition_date",
		"ALTER VIEW public.session_bodies_unified SET (security_invoker = true)",
		// DO 块自检：列数 + security_invoker + 结构断言
		"v_colcount <> 12",
		"'security_invoker=true' = ANY(reloptions)",
		"pg_get_viewdef",
		// 事务包裹
		"BEGIN",
		"COMMIT",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("migration 637 missing %q", want)
		}
	}

	// 关键断言：分区分支不能再带 partition_date 过滤（625 的
	// `WHERE partition_date <= CURRENT_DATE - INTERVAL '1 day'` 必须消失）。
	// 注意：DO 块里的运行时自检 LIKE '%CURRENT_DATE - INTERVAL%' 属于防御
	// 逻辑本身，因此断言收窄到过滤条件 "partition_date <= CURRENT_DATE"。
	if strings.Contains(body, "partition_date <= CURRENT_DATE") {
		t.Fatal("migration 637 must not filter the partition branch by partition_date — promoted-today rows would be invisible until midnight")
	}
	// 分区分支必须是裸表引用（FROM public.session_bodies 后面不能跟 WHERE）。
	if strings.Contains(body, "FROM public.session_bodies\nWHERE") {
		t.Fatal("migration 637 partition branch must not carry a WHERE clause")
	}
}

// TestMigration637DownRestores625FilteredView pins the downgrade target: the
// 625 view body WITH the partition_date filter, still never dropped and still
// security_invoker=true (the flag is not reset — resetting would silently
// change RLS semantics for admin readers).
func TestMigration637DownRestores625FilteredView(t *testing.T) {
	data, err := os.ReadFile("637_session_bodies_unified_today_visible.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLCommentsFor637(string(data))

	for _, want := range []string{
		"CREATE OR REPLACE VIEW public.session_bodies_unified",
		"FROM public.session_bodies_hot",
		"FROM public.session_bodies",
		"partition_date <= CURRENT_DATE - INTERVAL '1 day'",
		"ALTER VIEW public.session_bodies_unified SET (security_invoker = true)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("migration 637 down missing %q", want)
		}
	}
	for _, forbidden := range []string{"DROP VIEW", "DROP TABLE", "RESET (security_invoker)"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("migration 637 down must not %q — downstream readers depend on the view", forbidden)
		}
	}
}

// TestMigration638GuardsSessionBodiesPromote locks in the P1 fix: the
// session_bodies promote function gains the same retention/batch guards that
// migration 628 added for candidate_failure_logs, while keeping 626's
// idempotent conflict semantics (ON CONFLICT DO NOTHING + delete only the
// rows actually inserted).
func TestMigration638GuardsSessionBodiesPromote(t *testing.T) {
	data, err := os.ReadFile("638_session_bodies_promote_guard.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLCommentsFor637(string(data))

	mustContain := []string{
		"CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition",
		// guard 关键行（对照 628 的模式）
		"IF retention_window IS NULL OR retention_window <= interval '0 seconds' THEN",
		"RAISE EXCEPTION 'retention_window must be positive'",
		"IF batch_size IS NULL OR batch_size < 1 THEN",
		"RAISE EXCEPTION 'batch_size must be >= 1'",
		// 626 幂等语义保持
		"pg_try_advisory_xact_lock",
		"FOR UPDATE SKIP LOCKED",
		"ON CONFLICT (id, partition_date) DO NOTHING",
		"DELETE FROM public.session_bodies_hot h",
		"USING inserted i",
		"h.partition_date = i.partition_date",
		// 事务 + 后置断言
		"BEGIN",
		"COMMIT",
		"to_regprocedure('public.promote_session_bodies_hot_to_partition(interval,integer)')",
	}
	for _, want := range mustContain {
		if !strings.Contains(body, want) {
			t.Errorf("migration 638 missing %q", want)
		}
	}

	// 反向断言：不能回退到 615 的老删除语义（会把 ON CONFLICT 跳过的行
	// 一并删掉，静默丢数据）。
	if strings.Contains(body, "WHERE id IN (SELECT id FROM inserted)") {
		t.Fatal("migration 638 must keep 626's delete-only-inserted semantics")
	}
}

// TestMigration638DownRestores626Body pins the downgrade target: migration
// 626's unguarded function body, still with the idempotent conflict
// semantics.
func TestMigration638DownRestores626Body(t *testing.T) {
	data, err := os.ReadFile("638_session_bodies_promote_guard.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := stripSQLCommentsFor637(string(data))

	for _, want := range []string{
		"CREATE OR REPLACE FUNCTION public.promote_session_bodies_hot_to_partition",
		"pg_try_advisory_xact_lock",
		"ON CONFLICT (id, partition_date) DO NOTHING",
		"USING inserted i",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("migration 638 down missing %q", want)
		}
	}
	// down 版本不应再带 guard（guard 属于 638 的正向改动）。
	for _, forbidden := range []string{
		"RAISE EXCEPTION 'retention_window must be positive'",
		"RAISE EXCEPTION 'batch_size must be >= 1'",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("migration 638 down must restore the unguarded 626 body, found %q", forbidden)
		}
	}
}
