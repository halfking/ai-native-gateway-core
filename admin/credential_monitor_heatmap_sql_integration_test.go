//go:build integration

package admin

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/bg"
)

// TestCredentialHeatmapSQL_ExecutesOnRealDatabase runs the production heatmap SQL
// against a real database. This is the gate that should have existed all along,
// for the same reason admin/session_export_sql_integration_test.go exists:
// pgxmock matches a query *string* and never lets PostgreSQL parse it, so every
// defect class that lives in the SQL text is invisible to unit tests.
//
// The concrete incident:
//
//	admin/credential_monitor_heatmap.go  buildHeatmapSQL
//	  ERROR: column rl.origin_stage does not exist          (42703)
//
// live for ≥2 weeks, on the default path, because exclude_self_test defaults to
// true. It stayed invisible because TestBuildHeatmapSQL_GuardsAgainst42803Regression
// asserts on substrings of the SQL and passed happily.
//
// The static guards cannot cover this shape: the view reference and the offending
// predicate are two separate literals joined at runtime, so no single-literal rule
// sees both. Executing the assembled statement does.
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestCredentialHeatmapSQL
func TestCredentialHeatmapSQL_ExecutesOnRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the heatmap query parses")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	now := time.Now().UTC()
	// ExcludeSelfTest: true is the load-bearing part — that arm is the one that
	// referenced origin_stage, and it is the default for every real call.
	cases := []struct {
		name string
		p    heatmapQueryParams
	}{
		{"exclude-self-test-default", heatmapQueryParams{
			TimeStart: now.Add(-24 * time.Hour), TimeEnd: now,
			BucketSeconds: 3600, ExcludeSelfTest: true,
		}},
		{"include-self-test", heatmapQueryParams{
			TimeStart: now.Add(-24 * time.Hour), TimeEnd: now,
			BucketSeconds: 3600, ExcludeSelfTest: false,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// runHeatmapQuery is the production execution path (same func the
			// handler calls), so this exercises the assembled SQL, not a copy.
			got, err := runHeatmapQuery(ctx, pool, tc.p)
			if err != nil {
				t.Fatalf("heatmap query failed: %v", err)
			}
			t.Logf("heatmap returned %d credential series", len(got))
		})
	}
}

// TestHeatmapExcludeSelfTestKeepsSessionBranchRows is the half a parse check
// cannot give you: a query can execute and still be silently wrong.
//
// The old predicate's second arm was `NOT ('probe' = ANY(rl.quality_flags))` with
// no COALESCE. The session branch of the view NULL-pads quality_flags, so
// `'probe' = ANY(NULL)` is NULL, `NOT NULL` is not TRUE, and every session row is
// dropped by WHERE. Measured on the local DB (2026-10-02, 7d window):
// 40,225 of 40,275 session-branch rows removed — i.e. the heatmap would have gone
// blind to essentially the entire migrated dataset while still returning 200.
//
// The invariant asserted here is deliberately NOT "kept == some other predicate's
// count". An earlier version of this test compared the full three-arm production
// predicate against a flags-only predicate and duly failed at 41,733 vs 50,283 —
// which is the CORRECT result, because the other two arms (task_type,
// origin_actor) legitimately exclude more rows. Comparing two different
// predicates proves nothing; it only looks like a defect.
//
// The real property is: **the exclusion's verdict on a row must not depend on
// quality_flags happening to be NULL.** So evaluate the identical production
// predicate twice over the same rows — once with the real (possibly NULL) flags,
// once with NULL replaced by an empty array — and require the counts to match. If
// the COALESCE is ever lost, the first count collapses and this goes red while the
// query still parses and still returns 200.
func TestHeatmapExcludeSelfTestKeepsSessionBranchRows(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that session rows survive exclusion")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pred := fmt.Sprintf(bg.ProbeTrafficExclusionPredicateView, "rl", "rl", "rl")
	var keptAsIs, keptDefaulted, nullFlags int64
	err = pool.QueryRow(ctx, fmt.Sprintf(`
		WITH actual AS (
		    SELECT quality_flags, task_type, origin_actor
		    FROM request_logs_with_current_month
		    WHERE credential_id IS NOT NULL AND ts > now() - interval '7 days'
		),
		defaulted AS (
		    SELECT COALESCE(quality_flags, '{}'::text[]) AS quality_flags,
		           task_type, origin_actor
		    FROM actual
		)
		SELECT
		    (SELECT count(*) FROM actual    rl WHERE %[1]s) AS kept_as_is,
		    (SELECT count(*) FROM defaulted rl WHERE %[1]s) AS kept_defaulted,
		    (SELECT count(*) FROM actual WHERE quality_flags IS NULL) AS null_flags
		`, pred)).Scan(&keptAsIs, &keptDefaulted, &nullFlags)
	if err != nil {
		t.Fatalf("counting query failed: %v", err)
	}
	t.Logf("7d window: %d rows have NULL quality_flags; production predicate keeps %d as-is / %d with NULL defaulted",
		nullFlags, keptAsIs, keptDefaulted)

	if keptAsIs != keptDefaulted {
		t.Errorf("the production exclusion keeps %d rows with real flags but %d with NULL "+
			"coalesced to an empty array — its verdict depends on quality_flags being NULL, "+
			"so the view's NULL-padded session branch (%d such rows) is silently excluded. "+
			"The flags arm needs COALESCE('probe' = ANY(...), FALSE).",
			keptAsIs, keptDefaulted, nullFlags)
	}
}

// TestPhysicalOnlyColumnsListMatchesRealDatabase reverse-validates the hardcoded
// list in view_source_column_contract_test.go against the real schema, so the list
// cannot silently rot: if the view ever gains one of these columns, or a column is
// dropped from _request_logs, this fails and forces the list to be updated.
func TestPhysicalOnlyColumnsListMatchesRealDatabase(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence the column list is current")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// ★ 2026-10-21（R67 / C7 改判）：**换瞄准点**。
	//
	// 旧判据是「`_request_logs` 减去视图」⇒ ① v1 族已随迁移 890 真删
	// （`_request_logs` / `_request_logs_hot` 在真库里**根本不存在**）
	// ⇒ string_agg 0 行 ⇒ NULL ⇒ **scan 先崩**，门根本走不到它自己写的
	// 「空结果就 SKIP」降级分支 ⇒ 那个 SKIP 一直是**不可达代码**；
	// ② 而且「v1 物理独有列」这个概念**整体已死**：实测清单 40 个列名里
	// 38 个在当前库中**一个都不存在**（既不在会话族、也不在视图）。
	//
	// ⇒ 但**禁令的意图**仍然成立且仍有牙：这些列名在 canonical 视图契约之外
	//   ⇒ 视图读方引用它们就是 42703（要么列不存在，要么列不在视图里）。
	// ⇒ 判据改成两条**同事实源**的：
	//   ① 清单里的每个列名都**不得**出现在 canonical 视图的列集里
	//      （= 静态禁令 TestNoPhysicalOnlyColumnsInViewSourcedSQL 的前提）；
	//   ② 当前族（session_turns ∪ session_turns_hot）的「物理独有列」
	//      必须**被清单覆盖**（= 清单没漏 ⇒ 静态禁令没漏报）。
	//   这两条都不再依赖任何已退役的关系 ⇒ 不会再因族退役而恒红。
	viewCols := readColumnSet(t, pool, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname='public' AND c.relname='request_logs_with_current_month'
		  AND a.attnum > 0 AND NOT a.attisdropped`)
	if len(viewCols) == 0 {
		t.Fatalf("canonical 视图 request_logs_with_current_month 报告零列 —— " +
			"前提不在场（视图不存在或形态异常），不能校验清单")
	}
	for _, col := range physicalOnlyRequestLogColumns {
		if viewCols[col] {
			t.Errorf("physicalOnlyRequestLogColumns lists %q but it IS a column of the canonical "+
				"view request_logs_with_current_month on the real schema — 视图读方引用它不再 42703，"+
				"该列应从清单里移出（否则静态禁令在守一个已经不成立的禁令）", col)
		}
	}

	// ② 完整性：当前族的物理独有列必须被清单覆盖。
	familyOnly := readColumnSet(t, pool, `
		SELECT a.attname
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname='public' AND c.relname IN ('session_turns','session_turns_hot')
		  AND a.attnum > 0 AND NOT a.attisdropped
		  AND NOT EXISTS (
		    SELECT 1 FROM pg_attribute v
		    JOIN pg_class vc ON vc.oid = v.attrelid
		    JOIN pg_namespace vn ON vn.oid = vc.relnamespace
		    WHERE vn.nspname='public' AND vc.relname='request_logs_with_current_month'
		      AND v.attname = a.attname AND v.attnum > 0 AND NOT v.attisdropped)`)
	if len(familyOnly) == 0 {
		t.Fatalf("当前族的物理独有列集合为空 —— 前提不在场（族不存在或视图已覆盖全部列），不能校验清单")
	}
	listed := map[string]bool{}
	for _, c := range physicalOnlyRequestLogColumns {
		listed[c] = true
	}
	for col := range familyOnly {
		if !listed[col] {
			t.Errorf("当前族的物理独有列 %q 不在 physicalOnlyRequestLogColumns 里 —— "+
				"清单漏了它 ⇒ 静态禁令 TestNoPhysicalOnlyColumnsInViewSourcedSQL 会漏报", col)
		}
	}
	t.Logf("validated %d listed columns against the live schema "+
		"(canonical view %d 列 / 当前族物理独有 %d 列)",
		len(physicalOnlyRequestLogColumns), len(viewCols), len(familyOnly))
}

// readColumnSet 跑一条「返回一列列名」的查询并收成集合。
//
// ★ COALESCE(string_agg(...), ”) 是刻意的：空结果集会让 string_agg 产出 **NULL**
//
//	（不是空串）⇒ 扫进非空 string 会报 "cannot scan NULL into *string"。
//	旧判据就死在这上面（且它的「空就 SKIP」降级分支在 scan 之后 ⇒ 不可达）。
//	这里把 NULL 收成空串 ⇒ 「真的没有」与「查炸了」不再压成同一个形状。
func readColumnSet(t *testing.T, pool *pgxpool.Pool, q string) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}
