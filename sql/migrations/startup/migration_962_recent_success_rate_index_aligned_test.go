package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration962_RecentSuccessRatePredicateIsIndexAligned pins migration 962.
//
// The defect it fixes (2026-11-12 audit round 5, 245 + 154 production):
//
//	GET /api/credentials/monitor-summary ⇒ 500
//	{"error":{"detail":"internal error (see server logs)"}}
//	log: admin handler internal error  caller=credential_monitor.go:762
//	     err="rows iteration failed: timeout: context deadline exceeded"  duration_ms=15002
//
// Root cause chain, all measured on the live shared database (252):
//
//	① admin/credential_monitor.go:687 gives the handler a 15s context → anything
//	   slower than 15s becomes a 500 (not a slow page).
//	② The summary-mode query calls `CROSS JOIN LATERAL recent_success_rate(...)`
//	   once per (credential × model) row → 74 active credentials × 2015
//	   model_offers → 1959 function calls.
//	③ Each call filtered session_turns_hot with
//	       WHERE credential_id ~ '^[0-9]+$' AND credential_id::bigint = $1
//	   while the existing index (migration 729) is
//	       ((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC)
//	   ⇒ the predicate is NOT the same expression as the index key
//	   ⇒ the planner could not use that index
//	   ⇒ every call degraded into "scan the whole 3h window, then filter"
//	   ⇒ EXPLAIN ANALYZE: 13184 ms, "Rows Removed by Filter: 1876" per call.
//	④ Re-shaped to the index's own expression, the same query measured 113 ms
//	   with the index used ("Index Scan using idx_session_turns_hot_credential_ts",
//	   Rows Removed by Filter: 18) → 116× faster, and the 500 disappears.
//
// ★ Why this test is SHAPE-based instead of a hand-written pin:
//   the whole bug is "the predicate and the index key must be the SAME
//   expression". Pinning a literal string would only protect the string, not
//   the invariant. So this test extracts the index expression from the
//   migration that creates it (729) and requires the function predicates to
//   carry the same expression — if somebody re-shapes the predicate away from
//   the index again, this test goes red.
//
// ★ The down file is allowed to carry the OLD (index-unusable but semantically
//   identical) predicate: that is what "rollback" means here. So the
//   old-form assertion is scoped to the UP file only.
func TestMigration962_RecentSuccessRatePredicateIsIndexAligned(t *testing.T) {
	readFile := func(t *testing.T, name string) string {
		t.Helper()
		b, err := os.ReadFile(name)
		require.NoError(t, err, "%s should exist", name)
		return string(b)
	}

	const upName = "962_recent_success_rate_index_aligned.sql"
	const downName = "962_recent_success_rate_index_aligned.down.sql"
	const idxName = "729_sql_audit_session_turns_credential_ts_index.sql"

	up := readFile(t, upName)
	down := readFile(t, downName)
	idx := readFile(t, idxName)

	// ── ① the index expression, read from the migration that creates it ────
	//
	// 729:
	//   CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_hot_credential_ts
	//     ON public.session_turns_hot ((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC);
	idxRe := regexp.MustCompile(`(?is)CREATE\s+INDEX(?:\s+CONCURRENTLY)?(?:\s+IF\s+NOT\s+EXISTS)?\s+idx_session_turns_hot_credential_ts\s+ON\s+public\.session_turns_hot\s*\(\((.*?)\)\s*,\s*ts\s+DESC\)`)
	m := idxRe.FindStringSubmatch(idx)
	require.Len(t, m, 2, "migration 729 must still create idx_session_turns_hot_credential_ts with a (expression, ts DESC) key")

	// Normalise: PG rewrites a CASE without ELSE to `ELSE NULL::bigint END`,
	// and the migration file may omit the ELSE branch entirely. Both spellings
	// are the same expression, so compare them after folding the ELSE branch
	// and the whitespace away.
	normalizeExpr := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.ReplaceAll(s, "ELSE NULL::bigint END", "END")
		s = strings.ReplaceAll(s, "ELSE NULL END", "END")
		s = strings.Join(strings.Fields(s), " ")
		return s
	}
	idxExpr := normalizeExpr(m[1])

	// The CASE expression as it may appear inside a function predicate: the
	// ELSE branch is optional in the source and PG always normalises it to
	// `ELSE NULL::bigint END`, so both spellings must fold to the same text.
	caseRe := regexp.MustCompile(`(?is)CASE\s+WHEN\s+credential_id\s*~\s*'\^\[0-9\]\+\$'\s+THEN\s+credential_id::bigint(?:\s+ELSE\s+NULL(?:::bigint)?)?\s+END`)

	// ── ② both function predicates must carry that same expression ─────────
	for _, fn := range []string{"public.recent_success_rate", "public.recent_cache_hit_rate"} {
		fnRe := regexp.MustCompile(`(?is)CREATE\s+OR\s+REPLACE\s+FUNCTION\s+` + regexp.QuoteMeta(fn) + `\s*\(.*?\)\s*RETURNS\s+TABLE\(.*?\).*?AS\s+\$\$(.*?)\$\$;`)
		body := fnRe.FindStringSubmatch(up)
		require.Len(t, body, 2, "migration 962 must redefine %s", fn)

		// The predicate must use the index expression (same shape, after
		// normalising the optional ELSE branch on both sides).
		pred := caseRe.FindString(body[1])
		require.NotEmpty(t, pred,
			"%s's predicate must carry the index expression of idx_session_turns_hot_credential_ts — "+
				"the index key is ((%s), ts DESC); a differently-shaped predicate makes the planner "+
				"fall back to scanning the whole window per call (13.2s → 500 on 245/154)", fn, idxExpr)
		require.Equal(t, idxExpr, normalizeExpr(pred),
			"%s's predicate expression must be the SAME as the index key expression", fn)
	}

	// ── ③ the index-unusable spelling must be gone from the UP file ────────
	//
	// `credential_id::bigint = p_credential_id` next to a regex guard is the
	// shape that produced the defect. It may appear in the DOWN file (that is
	// the rollback target) but must not appear in the UP file.
	oldForm := regexp.MustCompile(`(?is)credential_id\s*~\s*'\^\[0-9\]\+\$'\s*\n\s*AND\s+credential_id::bigint\s*=`)
	require.NotRegexp(t, oldForm, up,
		"migration 962 UP must not reintroduce the regex-guard + credential_id::bigint = $1 predicate "+
			"(it cannot match idx_session_turns_hot_credential_ts and degrades to a full-window scan per call)")
	require.Regexp(t, oldForm, down,
		"migration 962 DOWN must restore the pre-962 predicate shape (rollback target)")

	// ── ④ the semantically-equivalent guard must still be present ───────────
	//
	// The CASE branch is what keeps "non-numeric credential_id" from matching:
	// it yields NULL and `NULL = p_credential_id` is not true. If somebody
	// drops the regex from the CASE, non-numeric ids would start matching.
	require.Contains(t, up, "CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint ELSE NULL::bigint END",
		"the CASE branch must keep the regex guard so non-numeric credential_id still cannot match")

	// ── ⑤ both functions must be redefined, and the ledger line must exist ──
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION public.recent_success_rate(")
	require.Contains(t, up, "CREATE OR REPLACE FUNCTION public.recent_cache_hit_rate(")
	require.Contains(t, up, "INSERT INTO public.schema_migrations (version, description)",
		"migrations >= 952 must self-register their ledger row")
	require.Contains(t, up, "'962'")
}
