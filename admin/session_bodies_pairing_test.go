//go:build !integration

package admin

import (
	"os"
	"strings"
	"testing"
)

// 审计报告 §5.9（2026-10-01）：`request_logs_bodies.ts` 是**正文写入时间**，
// 不是轮次时间。实测同一个 request_id 在 `session_turns` 与
// `request_logs_bodies_with_current_month` 里的 ts **99.85% 不相等**
// （809,892 / 811,128，通常差 8~16 秒）。
//
// 后果：任何按 `(request_id, ts)` 配对正文的查询，几乎永远配不上，返回空。
// 而 `session_compare` / `session_export` / `sessionforensics` / `session_title`
// 全都只按 `request_id` 配对——那是既有且正确的口径。
//
// 这条守卫钉住「谁用哪个键」，因为最自然的下一步优化（把键收紧成元组）恰恰
// 会静默清空所有正文，而且不会报任何错。
func TestSessionBodyPairingKeysMatchTheirCallers(t *testing.T) {
	// 按 request_id 配对的调用方必须用 request_id 键的取数器。
	byID := map[string]bool{
		"querySessionBodiesByRequestID": true,
	}
	byTuple := map[string]bool{
		"querySessionBodiesByRequestIDAndTS": true,
	}
	_ = byTuple

	compareSrc := readAdminSource(t, "session_compare.go")
	for _, want := range []string{
		"querySessionBodiesByRequestID(ctx, q, requestIDs)",
		"body := bodies[requestID]",
	} {
		if !strings.Contains(compareSrc, want) {
			t.Errorf("session_compare.go must pair bodies by request_id; missing %q.\n"+
				"Tightening this to (request_id, ts) returns nothing for 99.85%% of turns — "+
				"the bodies ts is the body-write time, not the turn time (audit §5.9).", want)
		}
	}
	if strings.Contains(compareSrc, "bodies[newFallbackTurnKey(") {
		t.Error("session_compare.go looks up bodies by (request_id, ts) key; it must use request_id")
	}
	if strings.Contains(compareSrc, "bodies := bodies[requestID]") {
		t.Error("shadowing `bodies` by the map lookup — the phase-2 map is gone")
	}
	// Note on defence in depth: the mutation that reintroduces the tuple key
	// does not even reach this assertion — it fails to compile, because
	// querySessionBodiesByRequestID returns map[string]sessionBody and indexing
	// it with a fallbackTurnKey is a type error. The map key type IS the real
	// guard; the text assertions above are documentation that survives refactors
	// of the surrounding code.
	_ = byID

	// summary 保持其既有的元组口径——不在重构里夹带行为变更。
	summarySrc := readAdminSource(t, "session_summary_v2.go")
	if !strings.Contains(summarySrc, "querySessionBodiesByRequestIDAndTS(") {
		t.Error("session_summary_v2.go must keep its existing (request_id, ts) pairing " +
			"until the behaviour change is decided on its own (audit §5.9)")
	}
}

// TestSessionBodiesBatchSQLIsNotALefiJoin pins the shape that carries the
// performance contract. The three measured forms differ only in how the same
// view and columns are reached:
//
//	IN (SELECT unnest) → Index Scan using request_logs_bodies_2026_09_pkey     7 ms
//	unnest + LEFT JOIN → ColumnarScan over all 2,216,660 rows              10,365 ms
//
// The SQL text of the first two is nearly identical, so a text assertion alone
// cannot tell them apart in general — but "this file must never contain a LEFT
// JOIN" is a real, falsifiable constraint, and mutating it reddens the guard.
func TestSessionBodiesBatchSQLIsNotALefiJoin(t *testing.T) {
	for name, sql := range map[string]string{
		"byRequestID": sessionBodiesByRequestIDSQL,
		"byIDAndTS":   sessionBodiesByRequestIDAndTSSQL,
	} {
		for _, forbidden := range []string{"LEFT JOIN", "JOIN "} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("%s SQL contains %q; the batched fetch must be a semi-join or an "+
					"IN filter so the planner can use the (request_id, ts) primary key instead "+
					"of scanning the columnar partition (audit §5.6):\n%s", name, forbidden, sql)
			}
		}
		if !strings.Contains(sql, "request_logs_bodies_with_current_month") {
			t.Errorf("%s SQL must read the bodies view, got:\n%s", name, sql)
		}
	}
	// Two array/batch parameters maximum — an unbounded turn count must not be
	// able to blow the 65535 parameter ceiling.
	if strings.Contains(sessionBodiesByRequestIDAndTSSQL, "$3") {
		t.Errorf("tuple variant must bind exactly two array parameters:\n%s", sessionBodiesByRequestIDAndTSSQL)
	}
	if strings.Contains(sessionBodiesByRequestIDSQL, "$2") {
		t.Errorf("request_id variant must bind exactly one array parameter:\n%s", sessionBodiesByRequestIDSQL)
	}
}

// TestCompareKeepsTurnsWithNullClientModel is the guard for the compare path's
// 17.32% silent data loss.
//
// client_model was scanned into a bare `string`; a NULL made rows.Scan fail, and
// the `if err != nil { continue }` that followed dropped the turn entirely —
// 167,133 of 964,990 turns (17.32%, measured 2026-10-01) never reached the
// compare view. The scan is now *string, normalized with derefOrEmpty.
func TestCompareKeepsTurnsWithNullClientModel(t *testing.T) {
	src := readAdminSource(t, "session_compare.go")
	if strings.Contains(src, "clientModel      string\n") || strings.Contains(src, "clientModel   string\n") {
		t.Error("compareTurn.clientModel is a bare string again — NULL client_model turns " +
			"will be silently dropped again (17.32% of turns, audit §5.9)")
	}
	if !strings.Contains(src, "clientModel      *string") {
		t.Error("compareTurn.clientModel must be *string so a NULL scans instead of erroring")
	}
	if !strings.Contains(src, "derefOrEmpty(meta.clientModel)") {
		t.Error("the compare loop must normalize the nullable model names via derefOrEmpty")
	}
}

// readAdminSource reads a sibling source file. The pairing guards assert on
// production source text (rather than on query results) because the failure they
// guard against is a *plan* choice that only shows up against a real database —
// see TestSessionSummaryV2FallbackBodiesStaysOnIndexPath for the behavioural
// counterpart. Comments are stripped first, so a comment that names the right
// function does not satisfy the check.
func readAdminSource(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return stripGoComments(string(raw))
}
