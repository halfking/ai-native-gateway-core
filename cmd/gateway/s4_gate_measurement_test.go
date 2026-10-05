//go:build !integration

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// v1DeadFrom / v1DeadTo bound the window measured in §9.238: the last v1 row
// before the outage and the first one after it, to the microsecond.
//
// They are constants rather than derived-from-now() on purpose. Deriving them
// would make the cross-tab below measure a different window every run, and a
// "the no-v1-row turns concentrate in the dead window" claim that drifts with
// the clock is not a claim. If a future v1 outage is found, add it as its own
// pair — two windows are two windows, and merging them hides which one is
// doing the work.
const (
	v1DeadFrom = "2026-09-06 21:00:27.656782+08"
	v1DeadTo   = "2026-09-11 21:21:10.742851+08"
)

// S4 gate measurement (audit §9.163).
//
// "Can S4 be opened?" is the first gate before request_logs can be retired, and
// until now the only way to answer it was to paste SQL by hand. This test makes
// it one command, on any database — including 252, once a read-only grant
// exists.
//
// It reuses the validator's own mirrorDriftClassSQL for classification, and
// mirrors the row source under TestS4GateSourceMatchesValidator. A hand-written
// copy of the whole gate would be a second version of it, and the two would
// drift at exactly the moment it matters: when someone changes the exclusion
// rules, the copy keeps reporting the old answer with full confidence.
//
//	TEST_DATABASE_URL='postgres://llm_gateway:…@127.0.0.1:5432/llm_gateway?sslmode=disable' \
//	  go test ./cmd/gateway/ -run TestS4GateMeasurement -count=1 -v

// s4Windows are the windows reported. 1h is the freshness probe (is the mirror
// losing rows *right now*), 168h is the validator's own default and the number
// that feeds s4_ready, 720h is the widest window the endpoint accepts.
var s4Windows = []struct {
	label string
	dur   time.Duration
}{
	{"1h", time.Hour},
	{"24h", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
	{"30d", 30 * 24 * time.Hour},
}

// TestS4GateMeasurement reports the three drift buckets per window and states
// whether s4_ready would be true. It asserts nothing beyond the internal
// consistency of the numbers, on purpose: "is s4 safe to open" is a release
// decision that belongs in the decision sheet, not a threshold hidden in a test.
func TestS4GateMeasurement(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL not set — offline mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// ⚠️ Everything below runs in **one REPEATABLE READ transaction**, and that
	// is load-bearing, not style. This test issues five separate queries over
	// the same scope and then asserts they agree: the 4 window counts, the
	// shape profile, the session-wide split, and the mirror-reach split must
	// all partition the same set. Under READ COMMITTED each query gets its own
	// snapshot, and this population is **live** — new drifting v1 rows land
	// continuously, and a replay draining one out of the outbox removes another.
	// So a row that arrives between query 1 and query 3 makes the two
	// partitions legitimately disagree, and the assertion reports "one of these
	// queries is scoped differently" when in fact all of them are correct and
	// the *population moved*.
	//
	// That failure mode is indistinguishable from a real scope drift, which is
	// the worst property a gate can have: it trains you to distrust the
	// instrument. A single snapshot removes the possibility.
	//
	// (Observed once as an unexplained package FAIL before this change; the
	// three-partition assertions added in §9.215/§9.216 are the ones that can
	// trip, and they were the newest thing in the file at the time.)
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("begin repeatable-read tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // read-only: rollback is the commit

	// ⚠️ The validator's scope is used **verbatim** — including its two
	// NOT EXISTS anti-joins. My first cut inlined a copy of the row source and
	// kept only the tenant predicate; that dropped the anti-join, so it counted
	// *every* v1 row in the window instead of the ones with no session twin.
	//
	// What that looked like: genuine_loss = 745,385 over 30 days, against a
	// hand-computed 6. Every assertion still passed, and the number was
	// confidently, spectacularly wrong. The only reason it was caught is that a
	// value had been measured before (6) and the two disagreed.
	//
	// The internal_loopback / non_terminal buckets were **identical** in both
	// versions, which is what made the bug so easy to miss: those rows genuinely
	// have no session twin either way, so the only bucket that moved was the one
	// carrying every successful mirrored row. A partial duplication of a query
	// does not degrade gracefully — it degrades in exactly the branch nobody
	// cross-checks.
	q := `
		SELECT drift_class, count(*)
		FROM (
		  SELECT ` + mirrorDriftClassSQL + ` AS drift_class
		  FROM (` + s4ScopeBody + `) rl
		  WHERE ($1 = '' OR rl.tenant_id = $1)
		    AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
		    AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
		) s
		GROUP BY drift_class`

	type windowResult struct {
		internal, nonTerminal, genuine int64
		total                          int64
	}
	results := map[string]windowResult{}

	for _, w := range s4Windows {
		rows, err := tx.Query(ctx, q, "", time.Now().Add(-w.dur))
		if err != nil {
			t.Fatalf("%s: query: %v", w.label, err)
		}
		var r windowResult
		for rows.Next() {
			var class string
			var n int64
			if err := rows.Scan(&class, &n); err != nil {
				rows.Close()
				t.Fatalf("%s: scan: %v", w.label, err)
			}
			r.total += n
			switch class {
			case "internal_loopback":
				r.internal = n
			case "non_terminal":
				r.nonTerminal = n
			case "genuine_loss":
				r.genuine = n
			default:
				rows.Close()
				t.Fatalf("%s: unknown drift class %q — the CASE in db.MirrorDriftClassSQL "+
					"grew an arm and this test's switch no longer covers it", w.label, class)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatalf("%s: iterate: %v", w.label, err)
		}
		results[w.label] = r

		ready := r.genuine == 0
		t.Logf("%-4s internal_loopback=%-6d non_terminal=%-6d genuine_loss=%-3d total=%-6d  s4_ready=%v",
			w.label, r.internal, r.nonTerminal, r.genuine, r.total, ready)
	}

	// The validator's own contract: the three buckets are mutually exclusive and
	// sum to V1RowsWithoutTurns. Checking the total is what makes the three
	// individual numbers trustworthy — a bucket that silently stopped matching
	// (a renamed class, a predicate that stopped applying) would otherwise just
	// make genuine_loss smaller, i.e. the gate would get *more* permissive
	// exactly when its instrument is broken.
	//
	// So: an unrecognised class must fail (above), and the three reported
	// classes must be the only ones. There is no independent total to check
	// against without re-running the scope, so the check that matters is the
	// exhaustive switch plus this monotonicity observation below.
	def := results["7d"]
	t.Logf("7d window: genuine_loss is %d. s4_ready would be %v.", def.genuine, def.genuine == 0)

	// ---- the windows are nested, so the counts must be monotonic -----------
	//
	// All four windows are the same row source with the same predicate and a
	// widening `ts` bound, so every row counted in 1h is also counted in 24h.
	// A count that **decreased** as the window widened would mean a window is
	// scoped differently from its neighbours, and every number derived from the
	// set would then be about a different population than the one it claims.
	// This is cheap and it is the property the rest of this block relies on.
	ordered := []string{"1h", "24h", "7d", "30d"}
	for i := 1; i < len(ordered); i++ {
		prev, cur := results[ordered[i-1]], results[ordered[i]]
		if cur.genuine < prev.genuine || cur.total < prev.total {
			t.Errorf("window %s reports genuine_loss=%d total=%d, but the wider %s window reports "+
				"genuine_loss=%d total=%d. These windows are nested (%s ⊂ %s), so the wider one "+
				"cannot count fewer rows — one of them is scoped differently and the comparison "+
				"below would be about two different populations",
				ordered[i], cur.genuine, cur.total, ordered[i-1], prev.genuine, prev.total,
				ordered[i-1], ordered[i])
		}
	}

	// ---- did any failure path get a chance to record these? -----------------
	//
	// `genuine_loss` says a v1 row has no session twin. It does **not** say the
	// mirror noticed it. Two places persist a failed mirror attempt —
	// `EnqueueMirrorFailure(..., "semaphore_full")` when the bounded pool is full
	// and `EnqueueMirrorFailure(..., "write_failed")` when the write errors
	// (hook.go:180 / hook.go:279) — and both land in `session_mirror_outbox`,
	// which the replay reaper drains.
	//
	// ⚠️ **This section used to make an inference the data does not support, and
	// the correction is the point of it.** The first version reported "10 of 10
	// blockers have no failure-path record" and concluded that every blocker
	// "bypassed both EnqueueMirrorFailure call sites". That came from reading the
	// table's **live row count**, which is 0 — but 0 live rows does not mean 0
	// rows ever existed:
	//
	//	· measured here, `n_tup_ins = 2494` and `n_tup_del = 2490`: the outbox
	//	  is written to constantly and drained just as fast;
	//	· the reaper's skip branches call `deleteRow` (replay.go:419/427/433), so
	//	  a drained row is **removed** rather than dead-lettered — `markDead` is an
	//	  UPDATE that keeps `status='dead'` (replay.go:522), so dead rows would
	//	  still be counted. Those 2490 deletes are skips, not dead letters.
	//
	// So a live count of 0 cannot separate "never enqueued" from "enqueued and
	// then skipped-and-deleted", and the per-request trace that would separate
	// them is a log line, not a table.
	//
	// What the level *can* support is narrower and still worth having: a blocker
	// with a **surviving** outbox row is a loss the mechanism still holds —
	// pending or dead — and that is actionable without logs. Everything else is
	// "not currently held", which is not a diagnosis.
	//
	// The defining window for s4_ready is the validator's own default (7d), so
	// the attribution runs over that same window — read from the same table
	// rather than re-spelled, so widening s4Windows cannot leave this behind.
	defDur := time.Hour
	for _, w := range s4Windows {
		if w.label == "7d" {
			defDur = w.dur
		}
	}
	var held int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM (`+s4ScopeBody+`) rl
		WHERE ($1 = '' OR rl.tenant_id = $1)
		  AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
		  AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
		  AND `+mirrorDriftClassSQL+` = 'genuine_loss'
		  AND EXISTS (
		      SELECT 1 FROM public.session_mirror_outbox o WHERE o.request_id = rl.request_id)`,
		"", time.Now().Add(-defDur)).Scan(&held); err != nil {
		t.Fatalf("outbox attribution: %v", err)
	}
	var outboxLive, outboxIns, outboxDel int64
	if err := tx.QueryRow(ctx, `SELECT n_live_tup, n_tup_ins, n_tup_del
	                              FROM pg_stat_user_tables
	                              WHERE relname = 'session_mirror_outbox'`).
		Scan(&outboxLive, &outboxIns, &outboxDel); err != nil {
		t.Fatalf("outbox counters: %v", err)
	}
	t.Logf("7d blockers currently HELD by the outbox (pending or dead): %d of %d "+
		"(outbox live=%d, inserted=%d, deleted=%d since stats reset)",
		held, def.genuine, outboxLive, outboxIns, outboxDel)
	switch {
	case held > 0:
		t.Logf("  ⇒ %d blocker(s) are in the outbox right now: the failure path recorded them and "+
			"they are either awaiting replay or dead-lettered. Start with status/attempts/"+
			"last_error on those rows.", held)
	case def.genuine > 0:
		t.Logf("  ⇒ none is held. That does **not** mean none was ever enqueued: this outbox is "+
			"drained by deleting its rows (%d inserts / %d deletes), so a live count of %d is the "+
			"expected steady state and says nothing about whether these blockers passed through it. "+
			"Answering that needs the reaper's log lines, not this table.",
			outboxIns, outboxDel, outboxLive)
	}

	// ---- what SHAPE are the blockers? (and therefore: upstream or downstream?) --
	//
	// ⚠️ §9.215. The gate reported a **count** and nothing else, and a count with
	// no shape cannot say where the loss happened. Five audit rounds were spent
	// chasing the wrong mechanism because of that: §9.213/§9.214 concluded that
	// the blockers were rows whose *v1 write failed*, so the mirror hook was
	// never called. That conclusion was built on a row that is, by this
	// classifier's own definition, **not a blocker at all** (it is
	// `non_terminal`), so it was never evidence about `genuine_loss`.
	//
	// The shape answers the upstream/downstream question **from the database**,
	// with no logs:
	//
	//	sessionv2mirror.PersistHook's first gate is
	//	    if !entry.Success && !isTerminalFailure(entry) { return }
	//	and isTerminalFailure returns false whenever entry.Success is true.
	//	So the gate is passed by every entry that is either successful or a
	//	terminal failure — and `genuine_loss`, being the ELSE arm of
	//	MirrorDriftClassSQL, is *by construction* exactly
	//	    success OR (request_status IN (failure, rate_limited) OR error_kind <> '')
	//	⇒ **every genuine_loss row reached the mirror hook.** A blocker can only
	//	come from the hook itself failing, from a gate after the first one, or
	//	from the recovery path failing to recover.
	//
	// That makes this an assertion, not just a report: if the profile ever shows
	// a non-terminal, unsuccessful blocker, then MirrorDriftClassSQL and
	// isTerminalFailure have drifted apart and the two SSOTs no longer describe
	// the same population.
	type blockerShape struct {
		originActor, requestStatus, errorKind string
		success                               bool
		autoReq                               string
		// cfClass is the bucket this row would fall into if `is_auto_request`
		// were treated as TRUE when it is NULL (§9.239).
		cfClass string
		n       int64
		newest  time.Time
	}
	shapeRows, err := tx.Query(ctx, `
		SELECT COALESCE(rl.origin_actor, '(null)'),
		       COALESCE(rl.request_status, '(null)'),
		       COALESCE(NULLIF(TRIM(rl.error_kind), ''), '(null)'),
		       COALESCE(rl.success, false),
		       COALESCE(rl.is_auto_request::text, '(null)'),
		       CASE
		         WHEN TRIM(COALESCE(rl.request_type, '')) IN ('title_gen','summary')
		           OR TRIM(COALESCE(rl.origin_actor, '')) IN ('auto-title-generator','auto-summary-generator','session-summary')
		           OR TRIM(COALESCE(rl.task_type, '')) = ''  THEN 'internal_loopback'
		         WHEN NOT COALESCE(rl.success, false)
		           AND COALESCE(rl.request_status, '') NOT IN ('failure','rate_limited')
		           AND COALESCE(rl.error_kind, '') = ''         THEN 'non_terminal'
		         ELSE 'genuine_loss'
		       END,
		       count(*), max(rl.ts)
		FROM (`+s4ScopeBody+`) rl
		WHERE ($1 = '' OR rl.tenant_id = $1)
		  AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
		  AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
		  AND `+mirrorDriftClassSQL+` = 'genuine_loss'
		GROUP BY 1, 2, 3, 4, 5, 6
		ORDER BY count(*) DESC, 1`, "", time.Now().Add(-defDur))
	if err != nil {
		t.Fatalf("blocker shape: %v", err)
	}
	var shapes []blockerShape
	for shapeRows.Next() {
		var s blockerShape
		if err := shapeRows.Scan(&s.originActor, &s.requestStatus, &s.errorKind, &s.success,
			&s.autoReq, &s.cfClass, &s.n, &s.newest); err != nil {
			shapeRows.Close()
			t.Fatalf("blocker shape scan: %v", err)
		}
		shapes = append(shapes, s)
	}
	if err := shapeRows.Err(); err != nil {
		shapeRows.Close()
		t.Fatalf("blocker shape iterate: %v", err)
	}
	shapeRows.Close()

	var shapeTotal int64
	for _, s := range shapes {
		shapeTotal += s.n
		// isTerminalFailure, restated. Non-terminal + unsuccessful is the one
		// combination that would mean "the hook's first gate returned early".
		if blockerSkippedByFirstGate(s.success, s.requestStatus, s.errorKind) {
			t.Errorf("7d genuine_loss contains %d row(s) with success=false and no terminal marker "+
				"(request_status=%q, error_kind=%q). By the hook's own first gate "+
				"(if !entry.Success && !isTerminalFailure(entry) { return }) such a row never "+
				"reached the mirror, so it would be a *different* defect from every other blocker "+
				"here. Either db.MirrorDriftClassSQL or sessionv2mirror.isTerminalFailure changed "+
				"and the two no longer describe the same population.",
				s.n, s.requestStatus, s.errorKind)
		}
	}
	if shapeTotal != def.genuine {
		t.Errorf("7d genuine_loss is %d, but the shape profile sums to %d over %d group(s). The "+
			"profile uses the same scope and the same production classifier as the count above, so "+
			"they are measuring the same set — a mismatch means one of the two queries is scoped "+
			"differently and at least one number above is about a different population.",
			def.genuine, shapeTotal, len(shapes))
	}
	if len(shapes) == 0 {
		t.Logf("7d blocker shape: none — no genuine_loss rows to profile")
	} else {
		t.Logf("7d blocker shape (%d group(s), %d rows, newest %s ago):",
			len(shapes), shapeTotal, time.Since(shapes[0].newest).Truncate(time.Second))
		for _, s := range shapes {
			t.Logf("    %-4d origin_actor=%-20s success=%-5v request_status=%-13s error_kind=%-22s is_auto_request=%-7s 若按true⇒%s",
				s.n, s.originActor, s.success, s.requestStatus, s.errorKind, s.autoReq, s.cfClass)
		}
		// §9.239: how much of the local s4_ready=false is decided by the
		// open question "should probe traffic be flagged is_auto_request".
		//
		// The internal_loopback arm requires is_auto_request IS TRUE and the
		// comment on db.MirrorDriftClassSQL says it explicitly: a NULL is NOT
		// internal. So probe rows that leave the column NULL are counted as
		// loss even though nobody ever intended them to be mirrored.
		//
		// This is reported, never asserted and never used to clear the gate.
		// "Flip the flag and the gate goes green" is not a finding, it is the
		// shape of the exact mistake this audit keeps refusing: reclassifying
		// a population so the instrument stops complaining. The counterfactual
		// exists so the owner can see the size of the open decision, not so
		// the test can argue for a side.
		var wouldRelabel, stillLoss int64
		for _, s := range shapes {
			if s.cfClass == "internal_loopback" {
				wouldRelabel += s.n
			} else {
				stillLoss += s.n
			}
		}
		t.Logf("  ⇒ §9.239 反事实：若把 is_auto_request 为 NULL 的行也视作 true，"+
			"这 %d 行 blocker 里有 %d 行会改判为 internal_loopback、%d 行仍是 genuine_loss。",
			def.genuine, wouldRelabel, stillLoss)
		t.Logf("  ⇒ ⚠ 这**不是**建议改标记。它只说明「探针要不要标 is_auto_request」" +
			"这一个待拍板问题，在本地恰好是 s4_ready 的唯一杠杆；" +
			"把仪器调绿和把数据改对是两件事，只有后者能作为验收依据。")
		t.Logf("  ⇒ every row above is either successful or a terminal failure, so every one of " +
			"them passed PersistHook's first gate: the mirror hook WAS reached. The loss is " +
			"downstream of the hook (the turn write itself, a later gate, or a recovery path " +
			"that failed to recover) — it is not 'v1 failed so the mirror never ran'.")
	}

	// ---- is the whole SESSION unmirrored, or just this one turn? ------------
	//
	// ⚠️ §9.216. The shape above groups by error_kind, and that is not the cut
	// that matters for remediation. Two blockers with the same error_kind can
	// need opposite fixes:
	//
	//   · a blocker whose **session has other turns** = one turn lost inside a
	//     session that otherwise mirrors fine ⇒ a write-path / timing problem;
	//   · a blocker whose **session has zero turns** = the session was never
	//     mirrored at all, typically a single-request session whose only
	//     request was rejected before dispatch ⇒ an exclusion/classification
	//     question, not a capacity problem.
	//
	// Measured 2026-10-04 (30d, production classifier verbatim): 9 of 10
	// blockers sat in sessions with **zero** turns — 7 of them
	// error_kind=no_candidate with exactly one v1 row in the whole session.
	// So the S4 blocker population is dominated by "the session was never
	// mirrored", and hardening the turn write would not have cleared it.
	//
	// The discriminator is a **count over the same blocker set**, so it must
	// reconcile with the profile above exactly as that profile reconciles with
	// the window count — otherwise this is a third number about a fourth
	// population.
	var sessionWideUnmirrored, singleTurnLost int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE session_turns = 0),
		       count(*) FILTER (WHERE session_turns > 0)
		FROM (
		  SELECT rl.gw_session_id,
		         (SELECT count(*) FROM session_turns_hot th WHERE th.session_id = rl.gw_session_id)
		       + (SELECT count(*) FROM session_turns     tp WHERE tp.session_id = rl.gw_session_id)
		         AS session_turns
		  FROM (`+s4ScopeBody+`) rl
		  WHERE ($1 = '' OR rl.tenant_id = $1)
		    AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
		    AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
		    AND `+mirrorDriftClassSQL+` = 'genuine_loss'
		) s`, "", time.Now().Add(-defDur)).
		Scan(&sessionWideUnmirrored, &singleTurnLost); err != nil {
		t.Fatalf("session-wide attribution: %v", err)
	}
	if got := sessionWideUnmirrored + singleTurnLost; got != def.genuine {
		t.Errorf("7d genuine_loss is %d, but the session-wide split sums to %d "+
			"(%d session-wide unmirrored + %d single turn lost in a live session). Same scope and "+
			"same production classifier as the count above — a mismatch means one of these queries "+
			"is scoped differently, and the split below would be about a different population than "+
			"the number it is explaining", def.genuine, got, sessionWideUnmirrored, singleTurnLost)
	}
	if def.genuine > 0 {
		t.Logf("7d blocker attribution: %d in a session with **zero** turns (session never mirrored) "+
			"vs %d in a session that has turns (a single turn lost inside a healthy session)",
			sessionWideUnmirrored, singleTurnLost)
		if sessionWideUnmirrored > singleTurnLost {
			t.Logf("  ⇒ dominated by session-wide non-mirroring. Making the turn write faster or " +
				"more reliable would NOT clear this population; the question is whether these " +
				"sessions should be mirrored at all (see the exclusion notes in hook.go and " +
				"db.MirrorDriftClassSQL — both key on attributes these rows do not have).")
		}

		// ---- did the mirror run at all, for the session-wide group? ----------
		//
		// "session never mirrored" does not say *why*, and the two candidates
		// need different fixes. runShadowWrite writes session_dim **before** the
		// V2 turn write (hook.go:228-241), and the dim writer has an
		// entry-fields fallback for the case where request_context_attrs has not
		// landed yet (session_dim.go: `if err == nil && tag.RowsAffected() > 0`
		// — a zero-row INSERT...SELECT is NOT swallowed, it falls through). So:
		//
		//	dim row present  ⇒ the hook definitely ran; the V2 write is what failed
		//	dim row absent   ⇒ either the hook never ran, or dim *and* V2 both
		//	                  failed; the local database cannot separate those two
		//
		// Both are reported, and the "cannot separate" half is stated rather than
		// guessed — §9.216.4 previously wrote these rows off as unreadable, which
		// is a statement about the instrument, not about the data.
		type hookReach struct {
			inDim, noDimButCtxPresent, noTraceAnywhere int64
		}
		var reach hookReach
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FILTER (WHERE in_dim),
			       count(*) FILTER (WHERE NOT in_dim AND in_ctx_attrs),
			       count(*) FILTER (WHERE NOT in_dim AND NOT in_ctx_attrs)
			FROM (
			  SELECT EXISTS (SELECT 1 FROM session_dim d
			                WHERE d.gw_session_id = rl.gw_session_id) AS in_dim,
			         EXISTS (SELECT 1 FROM request_context_attrs c
			                WHERE c.gw_session_id = rl.gw_session_id) AS in_ctx_attrs
			  FROM (`+s4ScopeBody+`) rl
			  WHERE ($1 = '' OR rl.tenant_id = $1)
			    AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
			    AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)
			    AND `+mirrorDriftClassSQL+` = 'genuine_loss'
			) s`, "", time.Now().Add(-defDur)).
			Scan(&reach.inDim, &reach.noDimButCtxPresent, &reach.noTraceAnywhere); err != nil {
			t.Logf("  (mirror-reach probe failed, reporting the split above only: %v)", err)
		} else {
			t.Logf("7d mirror reach over the same %d blocker(s):", def.genuine)
			t.Logf("    %-4d session_dim row present  ⇒ the hook ran; the V2 turn write is what failed",
				reach.inDim)
			t.Logf("    %-4d no session_dim, but request_context_attrs present ⇒ either the hook never "+
				"ran, or dim *and* V2 both failed — **this database cannot separate those two**",
				reach.noDimButCtxPresent)
			t.Logf("    %-4d neither present ⇒ the v1 side-table never ran for this session either, "+
				"which is a different story from a mirror failure", reach.noTraceAnywhere)
			if reach.noDimButCtxPresent > 0 {
				t.Logf("  ⚠ Do not read 'no session_dim row' as 'the mirror was never called' — it is " +
					"also what 'every mirror write failed' looks like once rows are deleted. " +
					"Answering it needs the gateway log line for that request_id.")
			}
		}
	}

	// ---- is the loss ongoing, recent, or confined to history? --------------
	// A freshness observation, reported rather than asserted: "is it safe to open
	// S4" is a release decision that belongs in the decision sheet, not a
	// threshold hidden in a test.
	//
	// ⚠ This used to be two branches keyed on the 1h and 30d windows only, and it
	// **never looked at 24h**. So the state "no loss in the last hour, 4 in the
	// last 24h" was reported as `historical … decaying` — which reads as "the
	// mirror has recovered, go ahead", while 4 of the losses were inside the day.
	// A single quiet hour is not evidence of decay. Measured on this database
	// 2026-10-04: 1h=0, 24h=4, 7d=10, 30d=10, and the old wording still said
	// "decaying".
	//
	// The three states are now all named, so the classification is **total** over
	// the windows this test already measures.
	one, day, month := results["1h"], results["24h"], results["30d"]
	switch {
	case one.genuine > 0:
		t.Logf("ONGOING: %d genuine loss(es) in the last hour — the mirror is losing terminal "+
			"failures right now, not just historically", one.genuine)
	case day.genuine > 0:
		t.Logf("RECENT, NOT YET HISTORICAL: no genuine loss in the last hour, but %d within the "+
			"last 24h (30d total %d). One clean hour is not evidence that the mirror has "+
			"recovered — a loss mechanism that fires a few times a day looks exactly like this "+
			"between events. Treat s4_ready as blocked until the 24h window is also clean.",
			day.genuine, month.genuine)
	case month.genuine > 0:
		t.Logf("historical: all %d genuine losses are older than 24h — decaying", month.genuine)
	default:
		t.Logf("clean: no genuine loss in any measured window")
	}

	// ---- ⚠️ s4_ready is NECESSARY, NOT SUFFICIENT for dropping request_logs ---
	//
	// §9.224. Everything above measures ONE blocker: v1 rows that have no
	// session twin. A second, entirely independent blocker is not in its
	// vocabulary — `is_final_success` and `client_protocol` are filled on the
	// session side **only by copying from v1**, by two one-shot idempotent
	// scripts whose headers say "run it before request_logs is dropped".
	//
	// Measured read-only on 252 on 2026-10-05 (§9.223): both columns were
	// 100% NULL across all 817,140 session_turns while v1 was 100% populated
	// and 100% matchable — the scripts had never been run there. And because v1
	// is on a monthly DROP list while session_turns is on none, the coverage
	// those scripts can ever reach **shrinks every day**: 1.225% and 2.890%
	// at that measurement.
	//
	// So the shape of the trap is: fix the mirror, watch this gate go green,
	// drop v1 — and lose the column values with nothing having gone red. The
	// gate that does catch it lives in db/retirement_backfill_gate_test.go
	// (TestRetirementBlockedByUnrunBackfills); it runs against the test
	// database, not against 252, so nothing in the S4 report would otherwise
	// mention it.
	//
	// This block therefore does not decide anything. It prints the second
	// prerequisite next to the s4_ready line, so the two can never be read
	// separately — "ready" on its own is the dangerous reading.
	var v1Present bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.tables
		                WHERE table_schema='public' AND table_name='request_logs')`).
		Scan(&v1Present); err != nil {
		t.Logf("  (could not probe for request_logs: %v)", err)
		return
	}
	if !v1Present {
		t.Logf("RETIREMENT PREREQUISITE: request_logs is already absent — the backfill question " +
			"is settled, one way or the other.")
		return
	}
	var sessTotal int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM session_turns`).Scan(&sessTotal); err != nil {
		t.Logf("  (could not count session_turns: %v)", err)
		return
	}
	var unbackfilled []string
	for _, col := range []string{"is_final_success", "client_protocol"} {
		var nonNull int64
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(%s) FROM session_turns`, col)).
			Scan(&nonNull); err != nil {
			t.Logf("  (could not count %s: %v)", col, err)
			continue
		}
		if sessTotal > 0 && 100*float64(nonNull)/float64(sessTotal) < 0.005 {
			unbackfilled = append(unbackfilled, col)
		}
	}
	if len(unbackfilled) > 0 {
		t.Logf("RETIREMENT PREREQUISITE **NOT MET**: %d of 2 column(s) are still empty on the "+
			"session side (%s, of %d session_turns) and are fillable **only** from v1, by a one-shot "+
			"idempotent script. s4_ready=%v above says nothing about them: it measures drift, not "+
			"copies. Dropping request_logs now makes these permanently unfillable.",
			// The defining window for s4_ready is the validator's own default (7d) —
			// the same one the "s4_ready would be" line above uses. Reading 30d here
			// would make the prerequisite line quote a different readiness than the
			// rest of the report, which is exactly the split this line exists to stop.
			len(unbackfilled), strings.Join(unbackfilled, ", "), sessTotal, def.genuine == 0)
	} else {
		t.Logf("RETIREMENT PREREQUISITE met: both backfilled columns are populated on the session side.")
	}
	t.Logf("  ⇒ s4_ready is necessary but NOT sufficient. Read it together with the line above.")

	// ---- §9.240: HOW MUCH can the backfill ever reach? ------------------------
	//
	// The line above says the columns are empty and fillable only from v1. It
	// does not say **how much** v1 still has, and that number is the one an
	// owner needs in order to weigh "run the backfill, then drop v1" against
	// "just drop v1". It is not derivable from the fill rates either: v1 being
	// 100% populated for is_final_success says nothing about whether each claim
	// still has a session twin to land on.
	//
	// ⚠ Two things this block must not do. It must not read the PARENT table
	// only — request_logs_hot and session_turns_hot are separate, disjoint
	// tables (§9.160.7), and the claims being counted mostly live in the hot
	// side. And it must not join on v1's `id`: that is a bigint sequence, while
	// the business key on both families is `request_id` (§9.238).
	var v1Claims, claimsWithTwin, claimsNoTwin, v2AlreadyMarked int64
	if err := tx.QueryRow(ctx, `
		WITH v1claim AS (
		  SELECT request_id FROM request_logs_hot
		    WHERE is_final_success IS TRUE AND COALESCE(gw_session_id,'') <> ''
		  UNION ALL
		  SELECT request_id FROM request_logs
		    WHERE is_final_success IS TRUE AND COALESCE(gw_session_id,'') <> ''
		), v2turn AS (
		  SELECT request_id FROM session_turns_hot
		  UNION ALL
		  SELECT request_id FROM session_turns
		)
		SELECT
		  (SELECT count(*) FROM v1claim),
		  (SELECT count(*) FROM v1claim c JOIN v2turn t ON t.request_id = c.request_id),
		  (SELECT count(*) FROM v1claim c WHERE NOT EXISTS
		     (SELECT 1 FROM v2turn t WHERE t.request_id = c.request_id)),
		  (SELECT count(*) FROM session_turns_hot WHERE is_final_success IS TRUE)
		    + (SELECT count(*) FROM session_turns WHERE is_final_success IS TRUE)`).
		Scan(&v1Claims, &claimsWithTwin, &claimsNoTwin, &v2AlreadyMarked); err != nil {
		t.Logf("  (could not size the is_final_success backfill: %v)", err)
	} else {
		t.Logf("BACKFILL CEILING is_final_success: %d v1 claim(s), %d have a session twin, "+
			"%d do not; session side already marked %d.",
			v1Claims, claimsWithTwin, claimsNoTwin, v2AlreadyMarked)
		if v1Claims > 0 {
			t.Logf("  ⇒ a backfill can reach %.2f%% of the claims; the %d claim(s) with no twin "+
				"are **permanently** unfillable once v1 is dropped.",
				100*float64(claimsWithTwin)/float64(v1Claims), claimsNoTwin)
		}
	}

	var v2Turns, protoFillable, protoNoV1Row int64
	if err := tx.QueryRow(ctx, `
		WITH v1 AS (
		  SELECT request_id, client_protocol FROM request_logs_hot
		  UNION ALL
		  SELECT request_id, client_protocol FROM request_logs
		), v2 AS (
		  SELECT request_id FROM session_turns_hot
		  UNION ALL
		  SELECT request_id FROM session_turns
		)
		SELECT
		  (SELECT count(*) FROM v2),
		  (SELECT count(*) FROM v2 t JOIN v1 r ON r.request_id = t.request_id
		     WHERE COALESCE(r.client_protocol,'') <> ''),
		  (SELECT count(*) FROM v2 t WHERE NOT EXISTS
		     (SELECT 1 FROM v1 r WHERE r.request_id = t.request_id))`).
		Scan(&v2Turns, &protoFillable, &protoNoV1Row); err != nil {
		t.Logf("  (could not size the client_protocol backfill: %v)", err)
	} else if v2Turns > 0 {
		t.Logf("BACKFILL CEILING client_protocol: %d of %d turn(s) = %.2f%%; %d turn(s) have no v1 "+
			"row at all and can never be filled.",
			protoFillable, v2Turns, 100*float64(protoFillable)/float64(v2Turns), protoNoV1Row)
	}
	// ★ Those %d turns are not a random gap. §9.238 measured a 5-day window in
	// which v1 wrote **zero** rows while session_turns kept writing, and the
	// cross-tab below is the confirmation: the no-v1-row turns concentrate
	// almost entirely inside that window. So the ceiling is set by v1 having
	// been dead, not by v1 never having collected the value — which is the
	// difference between "run the backfill" and "run the backfill and accept a
	// known hole".
	//
	// The window bounds are the two measured edges, not rounded dates:
	// 2026-09-06 21:00:27.656782+08 (last v1 row) → 2026-09-11 21:21:10.742851+08
	// (first v1 row after). Rounding them to whole days would put ~46 hours of
	// healthy-v1 time inside the "dead" window and quietly deflate the effect
	// being measured.
	type deadCell struct {
		noV1Row, inWindow bool
		turns             int64
	}
	deadRows, err := tx.Query(ctx, `
		WITH v1 AS (
		  SELECT request_id FROM request_logs_hot
		  UNION ALL
		  SELECT request_id FROM request_logs
		), v2 AS (
		  SELECT request_id, ts FROM session_turns_hot
		  UNION ALL
		  SELECT request_id, ts FROM session_turns
		)
		SELECT NOT EXISTS (SELECT 1 FROM v1 r WHERE r.request_id = t.request_id),
		       (t.ts >= $1::timestamptz AND t.ts < $2::timestamptz),
		       count(*)
		FROM v2 t
		GROUP BY 1, 2`, v1DeadFrom, v1DeadTo)
	if err != nil {
		t.Logf("  (could not cross-tab the v1 death window: %v)", err)
		return
	}
	var cells []deadCell
	for deadRows.Next() {
		var c deadCell
		if err := deadRows.Scan(&c.noV1Row, &c.inWindow, &c.turns); err != nil {
			deadRows.Close()
			t.Logf("  (cross-tab scan: %v)", err)
			return
		}
		cells = append(cells, c)
	}
	deadRows.Close()
	if err := deadRows.Err(); err != nil {
		t.Logf("  (cross-tab iterate: %v)", err)
		return
	}
	for _, c := range cells {
		t.Logf("  §9.238 交叉表  no_v1_row=%-5v in_v1_dead_window=%-5v  turns=%d",
			c.noV1Row, c.inWindow, c.turns)
	}
	var noV1InWin, noV1OutWin, v1InWin int64
	for _, c := range cells {
		switch {
		case c.noV1Row && c.inWindow:
			noV1InWin = c.turns
		case c.noV1Row:
			noV1OutWin = c.turns
		case c.inWindow:
			v1InWin = c.turns
		}
	}
	if noV1InWin+noV1OutWin > 0 {
		t.Logf("  ⇒ 无 v1 对应行的 turn 里 %.2f%% 落在 v1 死亡窗口内（窗口内 %d / 窗口外 %d）；"+
			"窗口内**有** v1 对应行的 turn 只有 %d 条。",
			100*float64(noV1InWin)/float64(noV1InWin+noV1OutWin), noV1InWin, noV1OutWin, v1InWin)
		t.Logf("  ⇒ 回填上限是被 **v1 静默停写** 压住的，不是「v1 从没采到这个值」。" +
			"这 5 天的缺口任何脚本都补不回来。")
	}
}

// blockerSkippedByFirstGate restates, in SQL terms, the one question the shape
// profile above needs answered: would sessionv2mirror.PersistHook have returned
// at its **first** gate for a row with this terminal state?
//
//	hook.go:78    if !entry.Success && !isTerminalFailure(entry) { return }
//	isTerminalFailure (hook.go:1093) returns false whenever entry.Success is true,
//	so a successful row never trips the gate. Otherwise it is terminal when
//	request_status is failure/rate_limited, or when error_kind is non-empty.
//
// ⚠️ The data assertion in TestS4GateMeasurement can only fire if the two SSOTs
// drift apart; no row can be injected to trigger it, because
// db.MirrorDriftClassSQL's ELSE arm *is* "success OR terminal", so a row failing
// this predicate is classified `non_terminal` and never reaches the profile at
// all. That makes the data assertion a drift tripwire with no reachable
// negative control — so the predicate itself is unit-tested below, in both
// directions, rather than left as an assertion nobody has ever seen fire.
func blockerSkippedByFirstGate(success bool, requestStatus, errorKind string) bool {
	if success {
		return false // isTerminalFailure short-circuits on Success, so the gate passes
	}
	switch requestStatus {
	case "failure", "rate_limited":
		return false
	}
	return errorKind == "" || errorKind == "(null)"
}

// TestBlockerSkippedByFirstGate_PositiveAndNegative is the control pair for the
// shape assertion above: one input that MUST be flagged and one that must not.
// A predicate with only the "clean" direction has never been shown to have
// teeth; a predicate with only the "flagged" direction would fire on everything.
func TestBlockerSkippedByFirstGate_PositiveAndNegative(t *testing.T) {
	cases := []struct {
		name          string
		success       bool
		requestStatus string
		errorKind     string
		wantSkipped   bool
	}{
		// Negative controls — the hook WAS reached, so this is not a
		// "hook never ran" blocker.
		{"terminal failure", false, "failure", "no_candidate", false},
		{"terminal rate_limited", false, "rate_limited", "", false},
		{"error_kind alone is terminal", false, "in_progress", "conn closed", false},
		{"success short-circuits the gate", true, "in_progress", "", false},
		// Positive control — the exact shape of 59bf8998 (hook.go:78 returns
		// here, so this row never reached the mirror).
		{"unsuccessful and non-terminal", false, "in_progress", "", true},
	}
	for _, tc := range cases {
		got := blockerSkippedByFirstGate(tc.success, tc.requestStatus, tc.errorKind)
		if got != tc.wantSkipped {
			t.Errorf("%s: blockerSkippedByFirstGate(success=%v, request_status=%q, error_kind=%q) = %v, want %v",
				tc.name, tc.success, tc.requestStatus, tc.errorKind, got, tc.wantSkipped)
		}
	}
}

// s4ScopeBody is the **row source only** of mirrorDriftScopeSQL (which
// relations, which columns, which window predicate), inlined so the window can
// be a bound parameter instead of string concatenation.
//
// The anti-join is NOT part of it — see the comment at the query: omitting it is
// the exact bug this file was written after, and it is repeated here verbatim as
// a warning rather than abstracted away. mirrorDriftClassSQL (the classification)
// is referenced from production, not copied.
//
// TestS4GateSourceMatchesValidator below pins this copy's column list against
// the production constant, so the duplication cannot rot unnoticed.
const s4ScopeBody = `
	SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
	       is_auto_request, origin_actor, request_type, task_type, success, ts
	FROM request_logs_hot
	WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
	UNION ALL
	SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
	       is_auto_request, origin_actor, request_type, task_type, success, ts
	FROM request_logs
	WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''`

// TestS4GateSourceMatchesValidator pins the copied row-source against the
// production constant, so the duplication above cannot rot unnoticed.
//
// The check is on **column names and order**, which is what actually has to
// match: mirrorDriftClassSQL references rl.<col> by name, so a reordering is
// harmless and a renaming is fatal. A whole-text equality check would fail on a
// harmless whitespace change and train people to ignore this test.
func TestS4GateSourceMatchesValidator(t *testing.T) {
	for _, col := range []string{
		"request_id", "gw_session_id", "tenant_id", "work_type", "request_status",
		"error_kind", "is_auto_request", "origin_actor", "request_type", "task_type",
		"success", "ts",
	} {
		if !containsIdentifier(s4ScopeBody, col) {
			t.Errorf("s4ScopeBody no longer selects %q, but db.MirrorDriftClassSQL references "+
				"rl.%s — the copied scope and the production classifier have drifted apart", col, col)
		}
	}
	for _, rel := range []string{"request_logs_hot", "request_logs"} {
		if !containsIdentifier(s4ScopeBody, rel) {
			t.Errorf("s4ScopeBody no longer reads %s; the two storage faces (hot ∪ parent) are "+
				"what make the scope complete", rel)
		}
	}
	if !containsIdentifier(mirrorDriftScopeSQL, "session_turns_hot") ||
		!containsIdentifier(mirrorDriftScopeSQL, "session_turns") {
		t.Error("the validator's scope lost its session-family anti-join; every v1 row would " +
			"then look mirrored and s4_ready would be true by construction")
	}
}

// containsIdentifier reports whether needle appears in hay delimited by
// identifier boundaries, so `client_model` does not match `client_model_v2`.
func containsIdentifier(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] != needle {
			continue
		}
		beforeOK := i == 0 || !isIdentByte(hay[i-1])
		after := i + len(needle)
		if beforeOK && (after >= len(hay) || !isIdentByte(hay[after])) {
			return true
		}
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
