package sessionv2mirror

// final_success_turn.go — the session family's final-success landing pad,
// applied to session_turns.is_final_success (audit §9.203).
//
// # What was broken
//
// `session_turns.is_final_success` was **never written by anything**. Measured
// on llm-gateway-pg / llm_gateway (2026-10-05):
//
//	v1 request_logs_hot (7d):  463 rows with is_final_success = TRUE / 4,532
//	v2 session_turns (all):       0 rows with is_final_success NOT NULL / 1,689,308
//
// Not-null count 0 (not just true-count 0) — the column is never written at
// all. The whole schema was built for it: every face carries the partial
// unique index `(tenant_id, session_id, partition_date) WHERE is_final_success`
// (uq_session_turns_hot_final_success, uq_session_turns_final_success, and one
// per monthly partition), and `turn_writer.go` already INSERTs the column. The
// uniqueness guarantee was enforced against a set that was always empty.
//
// # Why it is user-visible
//
// `admin/session_online.go` moved this endpoint onto the session family's
// native source on 2026-09-30 (querySessionTimeline). It reads
// `COALESCE(rl.is_final_success, FALSE)` and feeds deriveTurnOutcome, whose
// first two arms are `final_success` and `superseded_success`. With the column
// permanently NULL both arms are dead: every successful turn has been labelled
// plain `success` since. So the 09-30 migration silently dropped two outcomes
// from the timeline API — the API change was correct, the data it now reads
// was never produced.
//
// # Why this lives here and not in telemetry
//
// Exactly the abandoned_turn.go argument, and for the same mechanical reason:
// the terminal turn is written here, asynchronously, long after the v1 claim
// committed. A marker UPDATE issued from the telemetry side races the insert
// and loses — it matches 0 rows on essentially every attempt. So telemetry
// carries the fact (`RequestLogEntry.FinalSuccessClaimed`, set only when the
// v1 UPDATE actually matched a row) and this applies it after `w.Write`
// returns — same goroutine, and Write commits before returning.
//
// # v1 is the single arbiter — deliberately
//
// The session side does **not** run its own claim. It mirrors the decision v1
// already made, and that is what keeps the two families consistent, which is
// the whole point of the migration: one session, one final success, one answer
// regardless of which family a reader happens to hit.
//
// It is also safe, because v1's claim is already race-proof: the partial
// unique index uq_<partition>_final_success_session rejects a second winner
// with 23505, which the claim's savepoint absorbs (see
// claimSessionFinalSuccessExec). At most one request_id per session can carry
// the grant, so the session-side index can never be violated by this mark.
// Defence in depth: even a hypothetical double grant is rejected by
// uq_session_turns_hot_final_success, and markTurnFinalSuccess treats that
// 23505 as a distinct, non-alarming outcome rather than a transient failure.
//
// # Why both faces
//
// The writer only INSERTs into `session_turns_hot`; cold rows move to monthly
// partitions through `promote_session_turns_hot_to_partition`, which builds
// its column list from the catalog at runtime (migration 526) and therefore
// carries `is_final_success` across automatically. Marking only hot would
// still be right for rows promoted *after* the mark, but not for rows already
// promoted when the mark runs — and 0 rows on the other face is
// indistinguishable from "no session ever claimed final success".
//
// # ⚠ tenant_id is the raw value, never defaulted
//
// Same invariant as abandoned_turn.go: turn_writer.go writes rec.TenantID
// verbatim and entryToProcessedRequest copies entry.TenantID unchanged, so the
// value that identifies the row is req.TenantID — including the empty string.
// Defaulting it to "default" here is a silent permanent miss for exactly the
// traffic this pad exists to cover.
//
// # The RLS GUCs are load-bearing
//
// Both faces have RLS enabled with a RESTRICTIVE owner_filter policy (see
// abandoned_turn.go for the full measurement). A bare pool.Exec carries none of
// the GUCs — the db package has no AfterConnect hook — so it matches 0 rows on
// every call. Hence the transaction plus setBypassGUCs, this package's own
// established recipe (replay.go:128).

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// finalSuccessTurnOps counts outcomes of the final-success landing pad.
//
// The authoritative count is the is_final_success column, not this counter
// (process-memory, resets on restart).
var finalSuccessTurnOps = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_session_final_success_ops_total",
		Help: "Outcomes of the session_turns final-success landing pad (audit " +
			"§9.203). It mirrors the v1 final-success claim; it does not claim on " +
			"its own. mark_no_row is an anomaly rather than a routine race: the " +
			"claim was granted, so the turn must exist somewhere this code did " +
			"not look. mark_noop is the benign case (the turn already holds the " +
			"mark, i.e. a repeated hand-off of the same entry) and is kept apart " +
			"from mark_no_row precisely so the anomaly alert stays sharp. " +
			"mark_superseded means the session-side partial unique index " +
			"rejected a second mark, which means two v1 winners existed for one " +
			"session and the first mark stands. Process-memory: resets on " +
			"restart; the authoritative count is the is_final_success column.",
	},
	[]string{"op"},
)

func recordFinalSuccessTurnOp(op string) {
	finalSuccessTurnOps.WithLabelValues(op).Inc()
}

// markTurnFinalSuccess sets is_final_success on the turn that was just
// written, when the v1 side granted the final-success claim for it.
//
// Call this from runShadowWrite (and replayOne) **after** w.Write returned
// nil. Called before that it matches 0 rows and reports a spurious miss —
// which is precisely the bug this function's placement exists to avoid.
//
// Best-effort for the same reason the turn write is: a failure here means the
// mark was not recorded, never that the turn was lost. The turn row is
// already committed and this statement cannot un-commit it.
func markTurnFinalSuccess(ctx context.Context, pool *pgxpool.Pool, requestID, tenantID, sessionID string) {
	if pool == nil {
		// Degraded config (InitMirrorOutbox(nil)). Not a failure of the write,
		// but the pad genuinely did not run, and saying nothing would make it
		// indistinguishable from "wired and nothing to mark".
		recordFinalSuccessTurnOp("mark_no_pool")
		slog.Warn("sessionv2mirror: final_success pad skipped, mirror outbox pool is not initialised",
			"request_id", requestID, "session_id", sessionID)
		return
	}
	if requestID == "" || sessionID == "" {
		return
	}

	// One transaction so the RLS GUCs apply to every statement below. The db
	// package sets none per connection (no AfterConnect hook), and a pooled
	// connection cannot carry them for a single Exec.
	tx, err := pool.Begin(ctx)
	if err != nil {
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad begin failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}
	//nolint:errcheck // best-effort marker; a rollback failure here changes nothing
	defer tx.Rollback(ctx)

	// Reuse this package's own helper rather than inventing a second GUC
	// recipe (replay.go:128). The tenant GUC is set as well so the statement
	// is self-describing about which tenant's row it targets.
	if err := setBypassGUCs(ctx, tx); err != nil {
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad RLS GUC failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad tenant GUC failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}

	var marked int64
	for _, tbl := range []string{"public.session_turns_hot", "public.session_turns"} {
		tag, err := tx.Exec(ctx, `
			UPDATE `+tbl+`
			   SET is_final_success = TRUE
			 WHERE request_id = $1
			   AND tenant_id  = $2
			   AND is_final_success IS NOT TRUE
		`, requestID, tenantID)
		if err != nil {
			// 23505 = the session-side partial unique index rejected a second
			// winner for this session. v1's own claim should make this
			// impossible, so it is worth its own label rather than being
			// buried in mark_failed: it means the two families disagree about
			// who won, and the first mark stands.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				recordFinalSuccessTurnOp("mark_superseded")
				slog.Warn("sessionv2mirror: final_success pad rejected by session-side unique index "+
					"(another turn of this session already holds the mark; v1 should not have granted two)",
					"request_id", requestID, "session_id", sessionID, "table", tbl)
			} else {
				recordFinalSuccessTurnOp("mark_failed")
				slog.Warn("sessionv2mirror: final_success pad write failed (turn is intact)",
					"request_id", requestID, "session_id", sessionID, "table", tbl, "error", err)
			}
			return
		}
		marked += tag.RowsAffected()
	}

	if marked == 0 {
		// ⚠ 「0 行」有两种完全不同的含义，混为一谈会制造假警报：
		//   (a) 行真的不在 —— claim 授予了却没有对应 turn，这是真异常；
		//   (b) 行在、且已经是 TRUE —— 幂等重跑，什么都没做，这是正常。
		// 第一版把两者都报成 mark_no_row + WARN，于是「重复处理同一个 entry」
		// 会打出一条「v1 认领了但 turn 不在两张脸上」的假警，而那正是这条
		// 告警要抓的真故障。必须先分辨。
		var exists int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM session_turns_hot
			         WHERE request_id = $1 AND tenant_id = $2)
			     + (SELECT count(*) FROM session_turns
			         WHERE request_id = $1 AND tenant_id = $2)
		`, requestID, tenantID).Scan(&exists); err != nil {
			recordFinalSuccessTurnOp("mark_no_row")
			slog.Warn("sessionv2mirror: final_success pad could not confirm the turn after a 0-row mark",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID, "error", err)
			return
		}
		if exists > 0 {
			// 幂等重跑：行在且已标记。正常，不告警。
			recordFinalSuccessTurnOp("mark_noop")
			slog.Debug("sessionv2mirror: final_success pad was a no-op, the turn already holds the mark",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
			return
		}
		recordFinalSuccessTurnOp("mark_no_row")
		slog.Warn("sessionv2mirror: v1 granted the final-success claim but the turn was not found on either face",
			"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
		return
	}
	recordFinalSuccessTurnOp("mark")

	if err := tx.Commit(ctx); err != nil {
		// The rows are marked; only the commit acknowledgement failed, so the
		// outcome is genuinely unknown. Count it as a failure rather than
		// reporting a clean mark.
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad commit failed (outcome unknown)",
			"request_id", requestID, "session_id", sessionID, "marked", marked, "error", err)
	}
}
