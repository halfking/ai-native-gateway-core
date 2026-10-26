package sessionv2mirror

// final_success_turn.go — the session family's final-success landing pad,
// applied to session_turns.is_final_success (audit §9.203).
//
// # ★ 认领形态在迁移 890（v1 族退役）后变了（2026-10-21 记录）
//
// 旧形态：v1 的终态成功认领在 `updateRequestLog` 的 upsert-race 分支里，
// 该分支会 `fallback := *entry` 再 `insertRequestLog(&fallback)` ⇒
// 认领记在 **copy** 上 ⇒ 必须把 flag 从 copy **拷回** caller's entry。
//
// 新形态：v1 的终态成功认领**整段删除**，认领资格直接挂到 entry 上：
//
//	if shouldClaimFinalSuccess(entry) {
//	    entry.FinalSuccessClaimed = true
//	}
//
// ⇒ **不再有 copy，也不再需要拷回**；会话族自己仲裁
//   （session_turns 的 partial 唯一索引 + 本 pad）。
//
// ⇒ 因此旧门里「`entry.FinalSuccessClaimed = fallback.FinalSuccessClaimed`
//   必须存在」这条断言**已随旧形态一同退役** ⇒ 门改成钉**新形态**
//   （final_success_turn_gate_test.go 的 TestFinalSuccessClaimIsSetOnTheEntry）。
//
// # What was broken
//
// `session_turns.is_final_success` was **never written by anything**. Measured
// on llm-gateway-pg / llm_gateway (2026-10-05):
//
//	v1 _request_logs_hot (7d):  463 rows with is_final_success = TRUE / 4,532
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
// # ★ §143.105（2026-10-24）—— 上段的「v1 不会授予两次」前提已随 890 过期
//
// 迁移 890 删掉了 v1 侧的认领 UPDATE（claimSessionFinalSuccess* 整段退役），
// 认领资格改为 shouldClaimFinalSuccess **直接置位**：每个带 gw_session_id 的
// 成功请求都资格认领。仲裁全部交给会话族的部分唯一索引 —— 「每天每会话
// 只有第一条成功认领站住，其余确定性落选」。于是对一个每天几千个成功轮的
// 长活会话（252 实测 gw_omp-global-kxpms），「落选」是**常态**而不是
// 「v1 不该授予两次」的异常。
//
// ⇒ 本轮把落选从 23505 异常路径改写为 UPDATE 谓词里的跨面半连接
//   （NOT EXISTS 两臂），落选时 0 行命中 + mark_superseded（Debug）。
//   附带效果：hot 侧不再产生「跨面已有持标行却仍被标记」的第二条 final 行
//   ⇒ promote 侧 23505（897 头注的场景⑤）的生成源被掐断 —— 897 自愈存量，
//   本改动阻止增量。谁站住的语义逐字不变。
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
			"mark_superseded means the session-side partial unique index rejected a " +
			"second mark, or the §143.104 UPDATE pre-check deterministically " +
			"refused one: for a multi-turn session the first successful claim of " +
			"the day stands and every later successful turn is superseded — a " +
			"routine outcome since 890 (every successful request qualifies), not " +
			"a grant anomaly. Process-memory: resets on " +
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
// It reports whether the mark reached a definitive state: true for a
// committed mark, an idempotent no-op, a superseded claim, or a payload that
// can never be marked; false only for infrastructure failures (begin/GUC/
// exec/commit), where a retry can still change the outcome. Callers that own
// a compensation row must keep the row on false (audit 2026-10-04 §R43/L3).
func markTurnFinalSuccess(ctx context.Context, pool *pgxpool.Pool, requestID, tenantID, sessionID string) bool {
	if pool == nil {
		// Degraded config (InitMirrorOutbox(nil)). Not a failure of the write,
		// but the pad genuinely did not run, and saying nothing would make it
		// indistinguishable from "wired and nothing to mark".
		recordFinalSuccessTurnOp("mark_no_pool")
		slog.Warn("sessionv2mirror: final_success pad skipped, mirror outbox pool is not initialised",
			"request_id", requestID, "session_id", sessionID)
		return false
	}
	if requestID == "" || sessionID == "" {
		// Not retryable: the payload will never carry these ids.
		return true
	}

	// One transaction so the RLS GUCs apply to every statement below. The db
	// package sets none per connection (no AfterConnect hook), and a pooled
	// connection cannot carry them for a single Exec.
	tx, err := pool.Begin(ctx)
	if err != nil {
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad begin failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return false
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
		return false
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad tenant GUC failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return false
	}

	var marked int64
	for _, tbl := range []string{"public.session_turns_hot", "public.session_turns"} {
		// ★ §143.105（2026-10-24）：把「同会话同天已有别的 final 行」从 23505
		// 异常路径改写成**确定性 0 行路径** —— UPDATE 直接带跨面半连接。
		//
		// 背景（252 生产日志 10-22/10-23 实测 5,961 次 23505，其中 5,014 次集中在
		// gw_omp-global-kxpms 一个多轮会话）：迁移 890 之后**每个**成功请求都携带
		// 认领资格，而 (tenant_id, session_id, partition_date) WHERE is_final_success
		// 的部分唯一索引每天每会话只放行一条 ⇒ 多轮会话的其余成功轮**必然**撞
		// 23505 ⇒ 每次都是 PG 日志 ERROR + slog.Warn + 事务回滚，且 hot 侧被
		// 放行的第二条 final 行（索引看不见已被 promote 的跨面持标行）会成为
		// promote 侧 23505 的生成源 —— 897 在下游自愈它的存量，这里在上游
		// 阻止它生成。两脸任何一张已有**别的行**持标 ⇒ 本行 0 行命中 ⇒ 走
		// 下面三分分支报 mark_superseded。谁站住不变（第一条认领站住，与
		// 897「先到者站住」一致）；冲突从「写完才发现」提前到「写之前就不写」。
		// 23505 处理保留为并发窗口的最终防线（两个 pad 同时标同一会话）。
		tag, err := tx.Exec(ctx, `
			UPDATE `+tbl+` t
			   SET is_final_success = TRUE
			 WHERE t.request_id = $1
			   AND t.tenant_id  = $2
			   AND t.is_final_success IS NOT TRUE
			   AND NOT EXISTS (
			       SELECT 1 FROM public.session_turns_hot f
			        WHERE f.tenant_id = t.tenant_id
			          AND f.session_id = t.session_id
			          AND f.partition_date = t.partition_date
			          AND f.is_final_success
			          AND f.request_id <> t.request_id)
			   AND NOT EXISTS (
			       SELECT 1 FROM public.session_turns g
			        WHERE g.tenant_id = t.tenant_id
			          AND g.session_id = t.session_id
			          AND g.partition_date = t.partition_date
			          AND g.is_final_success
			          AND g.request_id <> t.request_id)
		`, requestID, tenantID)
		if err != nil {
			// 23505 = the session-side partial unique index rejected a second
			// winner for this session. After the NOT EXISTS pre-check above this
			// can only be a genuine concurrent race (two pads marking the same
			// session+date at the same instant), so it keeps its own label and
			// stays at Warn — rare by construction, actionable when it fires.
			var pgErr *pgconn.PgError
			superseded := false
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				superseded = true
				recordFinalSuccessTurnOp("mark_superseded")
				slog.Warn("sessionv2mirror: final_success pad rejected by session-side unique index "+
					"(another turn of this session already holds the mark; concurrent race — "+
					"the first mark stands)",
					"request_id", requestID, "session_id", sessionID, "table", tbl)
			} else {
				recordFinalSuccessTurnOp("mark_failed")
				slog.Warn("sessionv2mirror: final_success pad write failed (turn is intact)",
					"request_id", requestID, "session_id", sessionID, "table", tbl, "error", err)
			}
			if superseded {
				// Definitive: the first mark stands, a retry cannot change it.
				return true
			}
			return false
		}
		marked += tag.RowsAffected()
	}

	if marked == 0 {
		// ⚠ 「0 行」有三种完全不同的含义，混为一谈会制造假警报：
		//   (a) 行真的不在 —— claim 授予了却没有对应 turn，这是真异常；
		//   (b) 行在、且已经是 TRUE —— 幂等重跑，什么都没做，这是正常；
		//   (c) 行在、未标，但同会话同天已有**别的行**持标 —— §143.104 起
		//       这是多轮会话的**常态结局**（每天第一条成功认领站住，其余被
		//       UPDATE 谓词确定性拒绝），不是异常。第一版把 (a)(b) 都报成
		//       mark_no_row + WARN；第二版把 (c) 报成 23505 + WARN（890 后
		//       每个成功请求都带认领资格 ⇒ 忙会话每天几千条假警）。三分。
		var rowsWithID, alreadyMarked int
		if err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE is_final_success)
			  FROM (
			       SELECT is_final_success FROM public.session_turns_hot
			        WHERE request_id = $1 AND tenant_id = $2
			       UNION ALL
			       SELECT is_final_success FROM public.session_turns
			        WHERE request_id = $1 AND tenant_id = $2
			  ) s
		`, requestID, tenantID).Scan(&rowsWithID, &alreadyMarked); err != nil {
			recordFinalSuccessTurnOp("mark_no_row")
			slog.Warn("sessionv2mirror: final_success pad could not confirm the turn after a 0-row mark",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID, "error", err)
			return false
		}
		switch {
		case rowsWithID == 0:
			recordFinalSuccessTurnOp("mark_no_row")
			slog.Warn("sessionv2mirror: v1 granted the final-success claim but the turn was not found on either face",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
			// Definitive for this row: with the turn committed by the caller's
			// Write, a retry would still find nothing to mark.
			return true
		case alreadyMarked > 0:
			// 幂等重跑：行在且已标记。正常，不告警。
			recordFinalSuccessTurnOp("mark_noop")
			slog.Debug("sessionv2mirror: final_success pad was a no-op, the turn already holds the mark",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
			return true
		default:
			// 行在、未标，UPDATE 却 0 行命中 ⇒ 另一行持标（同面或跨面）。
			// 常态结局，不告警（252 实测忙会话可达 ~200 次/小时，全按异常
			// 报警等于没有报警；量化的权威是 is_final_success 列本身）。
			recordFinalSuccessTurnOp("mark_superseded")
			slog.Debug("sessionv2mirror: final_success pad superseded, another turn of this session "+
				"already holds the mark for this date (first claim stands)",
				"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
			return true
		}
	}
	recordFinalSuccessTurnOp("mark")

	if err := tx.Commit(ctx); err != nil {
		// The rows are marked; only the commit acknowledgement failed, so the
		// outcome is genuinely unknown. Count it as a failure rather than
		// reporting a clean mark.
		recordFinalSuccessTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: final_success pad commit failed (outcome unknown)",
			"request_id", requestID, "session_id", sessionID, "marked", marked, "error", err)
		return false
	}
	return true
}
