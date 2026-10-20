package sessionv2mirror

// abandoned_turn.go — the "started but never reached a terminal record" landing
// pad, applied to session_turns.is_abandoned (migration 821, audit §9.92/§9.93).

//
// # ★★ 登记债：触发源随 v1 族退役而消失（2026-10-21 记录）
//
// 本 pad 的**触发条件**是「telemetry 侧发现 v1 没有 t0 行」⇒ 该事实由
// `updateRequestLog` 的 upsert-race 分支执行 `SELECT EXISTS(...)` 查 v1 表得出。
//
// ★ 迁移 890（v1 族退役）把 v1 族整族 DROP，并**删掉了那段认领逻辑** ⇒
//   **现在没有任何代码会设置 `entry.T0Missing = true`** ⇒
//   ⇒ 本 pad **恒不触发**：`session_turns.is_abandoned` 从此恒为 NULL/false。
//
// ⇒ 这**不是 bug**（v1 族退役是既定决策），而是一条**能力缺失登记债**：
//
//	「有没有 t0」这个事实，在 v1 退役后**没有判据源**了。
//	会话族自己的对应事实是「turn_no 是否从 1 开始连续」——
//	但把它换算成「abandoned」需要一次语义设计（什么算「开始了却没到终态」）
//	⇒ 不在本轮范围（与 `internal/trace.FlushToPG` 的 R52「停写即丢弃」同口径）。
//
// ⇒ 本 pad 的**代码与接线都保留**（hook.go 仍在 w.Write 之后调用它），
//   因为它一旦有了新的判据源就能复活；把它删掉会让「复活」变成一次考古。
//
// ★ 本文件因此带**显式退役登记**，`internal/sessionv2mirror/` 的门会核它
//   （见 abandoned_turn_gate_test.go 的 TestAbandonedTurnTriggerSourceIsRetired）。
//
// # Why this lives here and not in telemetry
//
// The fact to record is "this request produced a terminal record, but v1 never
// had a t0 for it" — detected in telemetry's updateRequestLog upsert-race
// branch. Marking it, however, must happen **after the terminal turn exists**,
// and the turn is written *here*, asynchronously:
//
//	hook.go
//	  select { case sema <- struct{}{}: go func(){ runShadowWrite(...) }() }
//
// `shadowWriteDispatchAsync` defaults to true, so `w.Write` runs on a bounded
// goroutine pool while the telemetry path that detected the condition is
// synchronous. A marker UPDATE issued from the telemetry side therefore races
// the insert and loses — it matches 0 rows on essentially every attempt.
//
// That was the first implementation's defect: it reported `mark_no_row` on
// nearly every call and the column stayed NULL. The fix is to carry the fact
// on the entry (`telemetry.RequestLogEntry.T0Missing`) and apply the flag
// here, immediately after `w.Write` returns — same goroutine, and `Write`
// commits at session_writer_v2.go:762 before returning, so the row is both
// present and visible.
//
// # ⚠ The RLS GUCs are load-bearing, and the reason is not obvious
//
// Measured on llm-gateway-pg / llm_gateway (2026-10-04, as a non-superuser
// role via SET ROLE — see §9.93 for the probe): both faces have RLS enabled
// (`relrowsecurity = t`) and **three** policies each — two PERMISSIVE
// (tenant_isolation, super_admin_bypass) and one **RESTRICTIVE**
// (owner_filter). PostgreSQL ORs the permissive set and then ANDs the
// restrictive one, so a row is visible only when the tenant matches **and**
// the owner filter is satisfied.
//
// A bare `pool.Exec(UPDATE …)` runs on a pooled connection that carries none
// of those GUCs (the db package has no AfterConnect hook), so it matches 0 rows
// on every call and reports `mark_no_row` — reproducing, by a different
// mechanism, the exact failure this relocation was supposed to remove.
//
// The turn row is written by `Write` **on a different connection**, under a
// transaction that establishes its own GUC context. A separate statement can
// therefore never assume the row is visible to it. Hence the transaction plus
// setBypassGUCs below, which is this package's own established recipe for
// exactly this situation (replay.go:128).
//
// ⚠ A unit test with a fake pool cannot catch any of this, and neither can a
// test that connects as `llm_gateway` — that role is `rolsuper=t` **and**
// `rolbypassrls=t`, so RLS never applies to it. The probe role must be a
// non-superuser, non-bypass role that holds grants on both faces (`rls_probe`
// in the local container). This is the sharpest instance yet of "the local
// gauge may not measure the real system" (§9.53).
//
// # ⚠ tenant_id is the raw value, never defaulted
//
// `turn_writer.go` writes `rec.TenantID` verbatim as $3 with no defaulting, and
// `entryToProcessedRequest` copies `entry.TenantID` into the ProcessedRequest
// unchanged (hook.go:277). So the value that identifies the row is
// `req.TenantID` — including the empty string. Substituting a conventional
// "default" here (as the removed telemetry-side version did) makes the UPDATE
// search for `tenant_id='default'` while the row says `''`, which is a silent
// permanent miss for exactly the traffic the pad exists to catch.
//
// # Why both faces
//
// The writer only ever INSERTs into `session_turns_hot`; cold rows move to
// monthly partitions through `promote_session_turns_hot_to_partition`, which
// builds its column list from the catalog at runtime (migration 526) and
// therefore carries `is_abandoned` across automatically. Marking only hot would
// still be right for rows promoted *after* the mark, but not for rows already
// promoted when the mark runs — and 0 rows on the other face is
// indistinguishable from "nothing was abandoned".
//
// # Why a separate UPDATE and not a column on the INSERT
//
// Adding `is_abandoned` to turn_writer's INSERT column list would be one
// statement instead of two, but it would make **every** turn write depend on
// migration 821 being applied: before the migration, every INSERT would fail
// with 42703 and the whole shadow write would be lost. The 819 design this
// replaced was deliberately fail-open for exactly that reason, and a hot-path
// column reference is the opposite of that discipline.
//
// The UPDATE below is best-effort for the same reason: it runs after the
// primary write, in its own transaction, and a failure means only that the
// flag was not recorded — never that the turn was lost.

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// # ★ 机器可读的退役标记（2026-10-21）
//
// 下面的退役登记是**给人读的**注释；这一枚常量是**给机器钉的**。
//
// ★ 为什么两者都要：登记若只写在注释里，门只能查「某个关键词在不在」
//
//	—— 而**全文别处也会出现同一个词** ⇒ 门会恒绿、变成没有牙的门。
//	实测：把登记段首句整句抹掉，门**仍然绿**（因为别处还有「退役」二字）。
//	⇒ 登记必须**同时**落成一枚常量：门钉它的**值**，抹掉/改写它就红。
//
// ★ 它是**包级常量**（不是局部变量）⇒ 即便当前无人引用，Go 也不会报未使用。
const abandonedTurnTriggerRetired = "v1-family-retired@2026-10-20"

// abandonedTurnOps counts outcomes of the landing pad.
//
// The authoritative count is the is_abandoned column, not this counter
// (process-memory, resets on restart).
var abandonedTurnOps = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llm_gateway_abandoned_turn_ops_total",
		Help: "Outcomes of the session_turns abandoned landing pad (migration 821). " +
			"With the marker applied after the turn write in the same goroutine, " +
			"mark_no_row is an anomaly rather than a routine race: it means the row " +
			"is somewhere this code did not look (or RLS filtered it), so the pad is " +
			"under-reporting. mark_no_pool means the mirror outbox pool was never " +
			"initialised, so no mark was even attempted. Process-memory: resets on " +
			"restart; the authoritative count is the is_abandoned column.",
	},
	[]string{"op"},
)

func recordAbandonedTurnOp(op string) {
	abandonedTurnOps.WithLabelValues(op).Inc()
}

// markAbandonedTurnIfT0Missing sets is_abandoned on the turn that was just
// written, when the telemetry side recorded that v1 had no t0 for it.
//
// Call this from runShadowWrite **after** w.Write returned nil. Called before
// that it matches 0 rows and reports a spurious miss — which is precisely the
// bug this function's placement exists to avoid.
//
// tenantID must be the value the row was actually written with (req.TenantID),
// not a defaulted stand-in; see the file header.
func markAbandonedTurnIfT0Missing(ctx context.Context, pool *pgxpool.Pool, requestID, tenantID, sessionID string) {
	if pool == nil {
		// Degraded config (InitMirrorOutbox(nil)). Not a failure of the write,
		// but the pad genuinely did not run, and saying nothing would make it
		// indistinguishable from "wired and nothing to mark".
		recordAbandonedTurnOp("mark_no_pool")
		slog.Warn("sessionv2mirror: abandoned_turn skipped, mirror outbox pool is not initialised",
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
		recordAbandonedTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: abandoned_turn begin failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}
	//nolint:errcheck // best-effort marker; a rollback failure here changes nothing
	defer tx.Rollback(ctx)

	// setBypassGUCs is this package's own established helper for "a transaction
	// here must see RLS-protected rows" (replay.go:128) — reuse it rather than
	// inventing a second GUC recipe. The tenant GUC is set as well so the
	// statement is self-describing about which tenant's row it targets.
	if err := setBypassGUCs(ctx, tx); err != nil {
		recordAbandonedTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: abandoned_turn RLS GUC failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID); err != nil {
		recordAbandonedTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: abandoned_turn tenant GUC failed (turn is intact)",
			"request_id", requestID, "session_id", sessionID, "error", err)
		return
	}

	var marked int64
	for _, tbl := range []string{"public.session_turns_hot", "public.session_turns"} {
		tag, err := tx.Exec(ctx, `
			UPDATE `+tbl+`
			   SET is_abandoned = TRUE
			 WHERE request_id = $1
			   AND tenant_id  = $2
			   AND is_abandoned IS NOT TRUE
		`, requestID, tenantID)
		if err != nil {
			// Best-effort: the turn is already committed. Two shapes worth
			// separating in the log: the migration is missing (undefined
			// column / undefined table) versus a transient DB problem.
			recordAbandonedTurnOp("mark_failed")
			slog.Warn("sessionv2mirror: abandoned_turn flag write failed (turn is intact)",
				"request_id", requestID, "session_id", sessionID, "table", tbl, "error", err)
			return
		}
		marked += tag.RowsAffected()
	}

	if marked == 0 {
		recordAbandonedTurnOp("mark_no_row")
		slog.Warn("sessionv2mirror: terminal turn was written but not found on either face",
			"request_id", requestID, "session_id", sessionID, "tenant_id", tenantID)
		return
	}
	recordAbandonedTurnOp("mark")

	if err := tx.Commit(ctx); err != nil {
		// The rows are flagged; only the commit acknowledgement failed, so the
		// outcome is genuinely unknown. Count it as a failure rather than
		// reporting a clean mark.
		recordAbandonedTurnOp("mark_failed")
		slog.Warn("sessionv2mirror: abandoned_turn commit failed (outcome unknown)",
			"request_id", requestID, "session_id", sessionID, "marked", marked, "error", err)
	}
}
