package startup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Migration 727 (252 PG SQL 审计轮 2026-09-20) adds three slow-query indexes:
// F-A request_stage_events(created_at) for the retention DELETE full scan
// (10.5s mean / 30s timeout kills in production), and F-B the effective-
// session functional index that lets the request_logs view's
// `CASE WHEN session_id LIKE 'sys:%' ... END = $gw` predicate derive
// session_id = constant (687ms → 1.1ms in the A/B).
//
// The contract below pins the invariants that make 727 safe and effective:
//
//	C1  F-A targets exactly the retention predicate's column, and the
//	    predicate still exists in internal/trace/stage_events_retention.go
//	    (bound by parsing the worker source, not by copy-paste).
//	C2  F-B builds the SAME expression the view projects (bound to the 710
//	    view source), on all three layers: per-partition (\gexec), the
//	    independent hot table, and the parent ONLY shell + ATTACH loop.
//	    Parent-table indexes do not cascade to session_turns_hot (dual-write
//	    architecture) — losing the hot-table leg silently reintroduces the
//	    Seq Scan on the hottest surface.
//	C3  Every full index build is CONCURRENTLY (never blocks hot-path DML),
//	    and the file is NOT wrapped in BEGIN/COMMIT — CONCURRENTLY cannot
//	    run inside a transaction block. This is also why 727/728/729 are
//	    deliberately absent from the installer embeddata set: the installer
//	    runner applies every startup file with psql --single-transaction.
//	    Delivery path is the ops channel (apply-db-revision-sequence.sh /
//	    psql -f) plus the IF NOT EXISTS idempotence verified on 252.
//	C4  down drops exactly the three created indexes.
func TestMigration727SlowQueryIndexesContract(t *testing.T) {
	upBytes, err := os.ReadFile("727_sql_audit_slow_query_indexes.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(upBytes)
	upCompact := normalizeSQL(up)

	// ── C1: F-A index ↔ retention predicate ────────────────────────────
	faIndex := "CREATE INDEX CONCURRENTLY IF NOT EXISTS IDX_STAGE_EVENTS_CREATED_AT ON PUBLIC.REQUEST_STAGE_EVENTS (CREATED_AT)"
	if !strings.Contains(upCompact, faIndex) {
		t.Errorf("727 must create idx_stage_events_created_at CONCURRENTLY on request_stage_events(created_at)")
	}
	retentionSrc, err := os.ReadFile(filepath.Join("..", "..", "..", "internal", "trace", "stage_events_retention.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(normalizeGoSQL(string(retentionSrc)), "WHERE CREATED_AT < NOW() - $1::INTERVAL") {
		t.Errorf("retention worker no longer filters on created_at — re-audit migration 727 F-A against the new predicate")
	}

	// ── C2: F-B three layers with the view's expression ────────────────
	idxExpr := normalizedEffectiveSessionExpr(upCompact)
	wantExpr := "CASE WHEN SESSION_ID LIKE 'SYS:%' THEN NULL::TEXT ELSE SESSION_ID END"
	if idxExpr != wantExpr {
		t.Errorf("727 effective-session expression = %q, want %q", idxExpr, wantExpr)
	}
	// 第 1 段：对已存在分区 \gexec 逐个 CONCURRENTLY 建。
	for _, frag := range []string{"PG_INHERITS WHERE INHPARENT = 'PUBLIC.SESSION_TURNS'::REGCLASS", "\\GEXEC"} {
		if !strings.Contains(upCompact, frag) {
			t.Errorf("727 partition CONCURRENTLY stage lost fragment %q", frag)
		}
	}
	// 第 2 段：session_turns_hot 独立索引（父表索引不级联到 hot 表）。
	hotPos := strings.Index(upCompact, "CREATE INDEX CONCURRENTLY IF NOT EXISTS IDX_SESSION_TURNS_HOT_EFFECTIVE_SESSION")
	if hotPos < 0 {
		t.Error("727 must index session_turns_hot separately (dual-write hot table does not inherit parent indexes)")
	}
	// 第 3 段：父表 ONLY 壳 + ATTACH 循环。
	parentPos := strings.Index(upCompact, "CREATE INDEX IF NOT EXISTS IDX_SESSION_TURNS_EFFECTIVE_SESSION ON ONLY PUBLIC.SESSION_TURNS")
	attachPos := strings.Index(upCompact, "ALTER INDEX PUBLIC.IDX_SESSION_TURNS_EFFECTIVE_SESSION ATTACH PARTITION")
	if parentPos < 0 || attachPos < 0 {
		t.Error("727 must create the parent ONLY shell and ATTACH existing partition indexes")
	} else if parentPos < hotPos {
		t.Error("727 parent shell must come after the hot-table CONCURRENTLY build (comment-documented ordering)")
	}
	// 表达式与视图投影一致（绑定 710 视图源）。
	viewSrc, err := os.ReadFile("710_request_logs_view_session_family_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	viewCompact := normalizeSQL(string(viewSrc))
	m := regexp.MustCompile(`CASE WHEN T\.SESSION_ID LIKE 'SYS:%' THEN NULL ELSE T\.SESSION_ID END`).
		FindString(viewCompact)
	if m == "" {
		t.Fatal("710 view no longer projects the sys:% NULLing CASE expression — re-audit 727 F-B against the new projection")
	}
	if stripForCompare(m) != stripForCompare(idxExpr) {
		t.Errorf("727 index expression %q diverges from the view expression %q (planner can no longer derive session_id = const)", idxExpr, m)
	}

	// ── C3: non-transactional + CONCURRENTLY ───────────────────────────
	if strings.Contains(upCompact, "BEGIN;") || strings.Contains(upCompact, "COMMIT;") {
		t.Error("727 must NOT wrap its body in BEGIN/COMMIT — CREATE INDEX CONCURRENTLY cannot run inside a transaction block (the DO-block BEGIN has no semicolon and must not trip this check)")
	}

	// ── C4: down drops exactly the three created indexes ───────────────
	downBytes, err := os.ReadFile("727_sql_audit_slow_query_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	downCompact := normalizeSQL(string(downBytes))
	if got := strings.Count(downCompact, "DROP INDEX IF EXISTS"); got != 3 {
		t.Errorf("727 down must drop exactly 3 indexes, found %d", got)
	}
	for _, idx := range []string{
		"DROP INDEX IF EXISTS PUBLIC.IDX_STAGE_EVENTS_CREATED_AT",
		"DROP INDEX IF EXISTS PUBLIC.IDX_SESSION_TURNS_EFFECTIVE_SESSION",
		"DROP INDEX IF EXISTS PUBLIC.IDX_SESSION_TURNS_HOT_EFFECTIVE_SESSION",
	} {
		if !strings.Contains(downCompact, idx) {
			t.Errorf("727 down missing %q", idx)
		}
	}
}

// normalizedEffectiveSessionExpr extracts the functional index expression from
// 727 (either the \gexec format() form with doubled quotes/percent or the
// plain form) and normalizes it to the canonical comparison shape.
func normalizedEffectiveSessionExpr(upCompact string) string {
	m := regexp.MustCompile(`CASE WHEN SESSION_ID LIKE ''SYS:%%'' THEN NULL::TEXT ELSE SESSION_ID END`).
		FindString(upCompact)
	if m != "" {
		return strings.ReplaceAll(strings.ReplaceAll(m, "''", "'"), "%%", "%")
	}
	m = regexp.MustCompile(`CASE WHEN SESSION_ID LIKE 'SYS:%' THEN NULL::TEXT ELSE SESSION_ID END`).
		FindString(upCompact)
	return m
}

// stripForCompare removes the `T.` table alias and the `::TEXT` cast so the
// 727 index expression and the 710 view projection compare equal modulo
// aliasing and explicit typing (the index needs the cast, the view does not).
func stripForCompare(expr string) string {
	out := strings.ReplaceAll(expr, "T.", "")
	return strings.TrimSpace(strings.ReplaceAll(out, "::TEXT", ""))
}

// normalizeGoSQL collapses whitespace for Go-source SQL fragment matching.
func normalizeGoSQL(s string) string {
	return strings.Join(strings.Fields(strings.ToUpper(s)), " ")
}
