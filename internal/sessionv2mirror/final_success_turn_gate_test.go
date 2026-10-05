//go:build !integration

package sessionv2mirror

// final_success_turn_gate_test.go — 2026-10-05（审计 §9.203）接线门。
//
// 这道门只证明**接线**存在，不证明标记真的落库（那是
// final_success_turn_realdb_test.go 的事）。之所以仍要有它：落点链跨三个文件
// 四个函数（telemetry 认领 → entry 标志 → runShadowWrite / replayOne → 标记），
// 每一处都可能被人重构时静默摘掉，而摘掉之后**编译通过、单测通过、真库门也会
// 因为「没写」而看不出来**——真库门量的是标记函数本身，不是有没有人调用它。
//
// 与 abandoned_turn_gate_test.go 同款：断言代码形状，不跑数据库。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestFinalSuccessFlagIsSetOnTheCallersEntry pins the exact bug class that
// T0Missing already fell into once: setting the flag on a copy.
//
// claimSessionFinalSuccessExec receives whatever pointer the current
// persistence function holds. On the normal paths that IS the caller's entry
// (and therefore the one firePersistedHooks hands to the mirror). But
// updateRequestLog's upsert-race branch does `fallback := *entry` and re-enters
// insertRequestLog(&fallback) — there the claim records on the copy while the
// hook still reads the original. A mark set only inside the claim therefore
// silently disappears for every request that took the fallback branch.
func TestFinalSuccessFlagIsSetOnTheCallersEntry(t *testing.T) {
	code := repoFile(t, "domains/hooks/observability/telemetry/client.go")

	if !strings.Contains(code, "entry.FinalSuccessClaimed = true") {
		t.Fatalf("client.go never sets entry.FinalSuccessClaimed = true — the v1 " +
			"claim grants the mark but nothing tells the session family about it, " +
			"so session_turns.is_final_success stays permanently NULL")
	}
	if !strings.Contains(code, "entry.FinalSuccessClaimed = fallback.FinalSuccessClaimed") {
		t.Fatalf("the updateRequestLog upsert-race branch no longer copies the flag " +
			"back from `fallback`. On that branch insertRequestLog receives " +
			"&fallback, so the claim records on the copy while firePersistedHooks " +
			"reads the caller's entry — the mark would be lost for every request " +
			"that took it")
	}
}

// TestFinalSuccessPadIsNotAnElseOfTheAbandonedPad guards the two pads against
// being collapsed into one branch.
//
// They are independent facts: a request can both have had no t0 and have won
// its session's final success. Writing `else if` (or nesting the second mark
// inside the T0Missing block) would make one mask the other, and the symptom
// would be a plausible-looking column that is populated for only a fraction of
// the rows it should cover — exactly the failure that made this whole column
// invisible in the first place.
func TestFinalSuccessPadIsNotAnElseOfTheAbandonedPad(t *testing.T) {
	code := repoFile(t, "internal/sessionv2mirror/hook.go")

	mark := strings.Index(code, "markTurnFinalSuccess(")
	if mark < 0 {
		t.Fatalf("runShadowWrite no longer calls markTurnFinalSuccess — the pad is " +
			"dead code and every new final-success claim is silently dropped on the " +
			"session side")
	}
	write := strings.Index(code, "err := w.Write(ctx, req)")
	if write < 0 {
		t.Fatalf("cannot locate `err := w.Write(ctx, req)` in hook.go; the ordering " +
			"assertion below would be vacuous")
	}
	if mark < write {
		t.Fatalf("markTurnFinalSuccess is called BEFORE w.Write — the turn row does " +
			"not exist yet, so the UPDATE matches 0 rows on essentially every " +
			"attempt (this is why the pad is placed where it is; see " +
			"final_success_turn.go)")
	}
	if strings.Contains(code, "} else if entry.FinalSuccessClaimed") {
		t.Fatalf("the final-success pad is an else-if of the abandoned pad — the two " +
			"are independent facts and one would mask the other")
	}
	// Both guards must be on err == nil; a mark attempted after a failed write
	// is a guaranteed mark_no_row.
	guard := code[mark-200 : mark]
	if !strings.Contains(guard, "entry.FinalSuccessClaimed") {
		t.Fatalf("the markTurnFinalSuccess call is not guarded on entry.FinalSuccessClaimed " +
			"between w.Write and the call — every turn would be marked, not just the " +
			"claimed one")
	}
}

// TestFinalSuccessPadRunsOnTheReplayPath pins the compensation path.
//
// registerFinalSuccessClaimOutbox registers the outbox row in the SAME
// transaction as a granted claim, and that outbox is precisely the repair path
// for "the claim happened but the mirror write never landed". If the replay
// path does not apply the mark, the one mechanism designed to close that hole
// reopens it for this column.
func TestFinalSuccessPadRunsOnTheReplayPath(t *testing.T) {
	code := repoFile(t, "internal/sessionv2mirror/replay.go")

	if !strings.Contains(code, "markTurnFinalSuccess(") {
		t.Fatalf("replayOne no longer calls markTurnFinalSuccess — replayed turns of a " +
			"granted claim would be written without the mark, which is the exact " +
			"G2 gap registerFinalSuccessClaimOutbox exists to close")
	}
	if !strings.Contains(code, "if entry.FinalSuccessClaimed {") {
		t.Fatalf("replayOne does not guard the pad on entry.FinalSuccessClaimed — every " +
			"replayed turn would be marked")
	}
	// The flag has to survive the outbox round-trip, i.e. it must be serialised.
	client := repoFile(t, "domains/hooks/observability/telemetry/client.go")
	if !strings.Contains(client, `json:"final_success_claimed,omitempty"`) {
		t.Fatalf("RequestLogEntry.FinalSuccessClaimed is not serialised — the outbox " +
			"payload would drop it and the replay path could never see that a " +
			"replayed turn is supposed to be the final success")
	}
	// The pool field is what makes the replay call well-typed: replayDB is an
	// interface, and a nil *pgxpool.Pool boxed into one compares != nil.
	if !strings.Contains(code, "pool      *pgxpool.Pool") {
		t.Fatalf("MirrorOutboxReaper no longer keeps the *pgxpool.Pool — replayOne " +
			"cannot pass a non-nil-checkable handle to markTurnFinalSuccess")
	}
}
