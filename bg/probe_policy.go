// Package bg — probe_policy.go
//
// 错误门控探测策略 v3（2026-09-20 探测量优化专项，方案见
// docs/probe/2026-09-20-probe-volume-optimization.md）共享 SQL 谓词。
//
// 核心不变量：成功即停放（park），失败才武装（arm）。
//   - INV-1 healthy-parked 行任何调度器不得触达；
//   - INV-2 新失败立即重武装（NodeProbeWorker.Submit 的 ON CONFLICT 扩展）；
//   - INV-3 周期扫描只探 3 天内（非探测流量）使用过的模型；
//   - INV-4 同凭据 24h 内 ≥2 个不同模型探测成功 → 其它模型早停；
//   - INV-5 周期扫描只面向有错误证据的凭据。
//
// 谓词集中在此，避免各扫描器/调度器口径漂移。
package bg

import "fmt"

// nodeProbeHealthyParkedSQL renders the healthy-parked predicate (INV-1) for a
// node_probe_state row aliased by alias. A row is healthy-parked when the last
// probe round succeeded, no error code is recorded, and no failure counter is
// pending. Success writers (MarkNodeProbeHealthy / mirrorNodeProbeState /
// runOne) put rows into this shape and push next_retry_at 30 days out; every
// scheduler below must EXCLUDE these rows from generating new probes.
func nodeProbeHealthyParkedSQL(alias string) string {
	return "(" + alias + ".last_direct_ok = TRUE" +
		" AND COALESCE(" + alias + ".last_err_code, '') = ''" +
		" AND COALESCE(" + alias + ".consecutive_failures, 0) = 0)"
}

// nodeProbeSubmitUpsertSQL renders NodeProbeWorker.Submit's non-queue upsert
// (INV-2 re-arm semantics). Extracted verbatim from the former inline literal
// (R50 F20, 2026-09-21) so behavior tests can execute the real statement
// against a real database instead of pinning its text:
//   - paused / expired / healthy-parked rows re-arm at now+5s (paused and
//     parked rows also reset consecutive_failures and last_err_code);
//   - mid-ladder rows (future next_retry_at with error evidence) keep their
//     schedule and counter so the backoff chain can advance.
func nodeProbeSubmitUpsertSQL() string {
	return `
		INSERT INTO node_probe_state (credential_id, raw_model_name, next_retry_at, next_retry_seconds, paused, in_flight_until, consecutive_failures, last_err_code)
		VALUES ($1, $2, now() + interval '5 seconds', 5, FALSE, NULL, 0, NULL)
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE
		SET next_retry_at = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.next_retry_at <= now()
		          OR (` + nodeProbeHealthyParkedSQL("node_probe_state") + `)
		        THEN now() + interval '5 seconds'
		        ELSE node_probe_state.next_retry_at
		    END,
		    next_retry_seconds = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.next_retry_at <= now()
		          OR (` + nodeProbeHealthyParkedSQL("node_probe_state") + `)
		        THEN 5
		        ELSE node_probe_state.next_retry_seconds
		    END,
		    in_flight_until = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR node_probe_state.next_retry_at <= now()
		          OR (` + nodeProbeHealthyParkedSQL("node_probe_state") + `)
		        THEN NULL
		        ELSE node_probe_state.in_flight_until
		    END,
		    paused = FALSE,
		    -- Reset the counter when the cycle is being restarted from a
		    -- paused or healthy-parked row. For already-expired ladder rows
		    -- the worker (runOne) owns the counter and increments it by 1 per
		    -- round; touching it here would collapse the ladder back to rung 1.
		    consecutive_failures = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR (` + nodeProbeHealthyParkedSQL("node_probe_state") + `)
		        THEN 0
		        ELSE node_probe_state.consecutive_failures
		    END,
		    last_err_code = CASE
		        WHEN node_probe_state.paused = TRUE
		          OR (` + nodeProbeHealthyParkedSQL("node_probe_state") + `)
		        THEN NULL
		        ELSE node_probe_state.last_err_code
		    END,
		    updated_at = now()
	`
}

// nodeProbeErrorEvidenceSQL renders the row-level error-evidence predicate for
// a node_probe_state row aliased by alias: the row carries a recorded failure,
// an active failure counter, or was never confirmed healthy. pumpDueStatesToQueue
// only enqueues rows matching this — healthy-parked rows stay out of the queue.
func nodeProbeErrorEvidenceSQL(alias string) string {
	return "(COALESCE(" + alias + ".last_err_code, '') <> ''" +
		" OR COALESCE(" + alias + ".consecutive_failures, 0) > 0" +
		" OR " + alias + ".last_direct_ok IS DISTINCT FROM TRUE)"
}

// credentialFailureEvidenceSQL renders the credential-level error-evidence
// predicate (INV-5): the credential logged at least one candidate failure
// within the given SQL interval expression (e.g. "interval '24 hours'").
// credIDExpr is the SQL expression yielding the credential id the EXISTS
// correlates on (e.g. "c.id" or "used.id" — pass the full expression, not
// just an alias, so CTEs that expose credential_id work too).
func credentialFailureEvidenceSQL(credIDExpr, sqlInterval string) string {
	return `EXISTS (
		SELECT 1 FROM candidate_failure_logs_with_current_month cfl
		WHERE cfl.credential_id = ` + credIDExpr + `
		  AND cfl.ts >= now() - ` + sqlInterval + `
	)`
}

// credentialTwoProbeSuccessGateSQL renders the INV-4 two-consecutive-success
// gate as a NOT EXISTS: it matches credentials that already have ≥2 distinct
// models whose LATEST completed node_probe_run succeeded within 24 hours.
// Callers use it to EXCLUDE such credentials (or their sibling models) from
// scheduled scans — "连续两个模型都探测成功，其它模型不需要探测".
//
// The per-model latest run is derived with DISTINCT ON over the
// (credential_id, raw_model_name, started_at DESC) index; the success filter
// applies AFTER the latest-run selection, so a model whose most recent run
// failed is disqualified even if an earlier run in the window succeeded.
// credIDExpr is the full SQL expression yielding the credential id
// (e.g. "c.id", "u.id", "cmb.credential_id").
func credentialTwoProbeSuccessGateSQL(credIDExpr string) string {
	return `EXISTS (
		SELECT 1 FROM (
			SELECT DISTINCT ON (npr.raw_model_name)
			       npr.raw_model_name, npr.success
			FROM node_probe_runs npr
			WHERE npr.credential_id = ` + credIDExpr + `
			  AND npr.completed_at > now() - interval '24 hours'
			ORDER BY npr.raw_model_name, npr.started_at DESC
		) latest_run
		WHERE latest_run.success = TRUE
		HAVING count(*) >= 2
	)`
}

// probeTrafficExclusionPredicate matches request_logs rows that were NOT
// produced by the probe system itself. Two arms (R49 audit fix, 2026-09-20):
//   - 'probe' quality flag — set on the SYNTHETIC direct-round rows written by
//     active_probe_emitter;
//   - origin_stage <> 'business' — the gateway round of a probe flows through
//     the normal pipeline where it is stamped origin_stage='node_probe' (and
//     never gets the quality flag), so the flag arm alone left those rows
//     counting as "usage" in every scanner (INV-3 was nominal only).
//
// BOTH arms reference origin_stage, so this predicate is only valid against
// the physical request_logs tables (hot / parent / partitions) where migration
// 428 added the column and the X-LLM-Origin-Stage bridge populates it
// (verified on the real DB: 11.6k node_probe rows stamped vs 3.1k business).
// It must NOT be used against the canonical 113-column view — see
// ProbeTrafficExclusionPredicateView below.
//
// Both format verbs take the same request_logs alias (pass it twice).
// Every usage scan must exclude probe traffic, otherwise probes count as
// usage and the scan keeps re-probing what only probes ever touched (INV-3).
const probeTrafficExclusionPredicate = "(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE)" +
	" AND COALESCE(%s.origin_stage, 'business') = 'business')"

// ProbeTrafficExclusionPredicateView is the frozen-113-column-contract variant
// for request_logs_with_current_month. R49 self-audit correction
// (2026-09-20): origin_stage is NOT part of the view's frozen 113-column
// contract (neither projected from session_turns nor present in the pre-485
// frozen v1 chain), so the physical-table predicate above 42703s against the
// view on every environment. The view-safe marker set — all three present in
// canonicalColumnOrderV2 on every branch — mirrors the mirror's own probe
// classification (synthetic_session.go: "origin_stage/origin_actor/task_type
// 含 probe"):
//   - 'probe' quality flag (v1 branch, direct-round synthetic rows);
//   - task_type = 'probe_triggered' (ActiveProbeWorker rows);
//   - origin_actor IN the probe gateway actors (session branch AND v1 branch).
//     R52 审计修正 (2026-09-22): the gateway actor set is FOUR, not two —
//     node-probe-worker (legacy runOne rounds), probe-service (unified-queue
//     gateway round, active_probe_executor.go), credential-selfcheck-worker
//     (selfcheck HTTP through the local gateway), plus active-probe-worker
//     kept for historical rows. Missing probe-service/selfcheck let those
//     rounds count as "usage" in every view-based scan (INV-3 leak).
//
// Exported (was unexported until 2026-10-02) so out-of-package read faces
// stop hand-copying the spelling: admin/credential_monitor_heatmap.go
// re-inlined the PHYSICAL variant into a view query in R50 (2026-09-21), which
// 42703'd on every call (ExcludeSelfTest defaults true). A local copy has no
// drift guard — the R50 call-site guard scanned bg files only, so the admin
// regression passed the whole suite. admin already imports bg; there is no
// import cycle.
//
// THREE format verbs, one per arm — pass the alias THREE times. (The previous
// comment said "twice"; a two-arg call silently renders %!s(MISSING) into the
// SQL, which then fails as a syntax error far from the real mistake.)
const ProbeTrafficExclusionPredicateView = "(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE)" +
	" AND COALESCE(%s.task_type, '') <> 'probe_triggered'" +
	" AND COALESCE(%s.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker', 'probe-service', 'credential-selfcheck-worker'))"

// probeTrafficExclusionPredicateSession 是 settle 基线 cohort 在**会话族**上的
// 探针排除谓词（审计 §9.73.4/§9.73.5，2026-10-03 裁决「分族修」）。
//
// # 为什么必须单列一条，而不是复用上面两条中的任何一条
//
// 两条现成谓词都引用 `quality_flags`，而**会话族那张表没有这一列**：
//
//	迁移 707 给 session_turns / session_turns_hot 加了 origin_stage、origin_actor、
//	is_auto_request 等 55 列，**没有** quality_flags；
//	252 生产实测：session_turns 104 列，有 origin_stage / task_type / origin_actor，
//	无 quality_flags（request_logs_hot 是 156 列，四列俱全）。
//
// ⇒ 对会话族用上面任何一条都会 42703，**每一次结算轮询都会失败**。
// 这不是理论风险：R50 记录的 admin 回归就是同一个坑（`ExcludeSelfTest` 默认值
// 让物理谓词对视图 42703，每次调用都炸）。
//
// # 臂的构成：为什么是这三条
//
// 采用 `synthetic_session.go` 自己的探针分类（origin_stage / origin_actor /
// task_type 含 probe），即上面三条里凡是该族**真的有的列**都取：
//
//	origin_stage  ← 物理谓词那条臂（探针网关轮打 node_probe，没有 quality 标记）
//	task_type     ← 视图谓词那条臂（ActiveProbeWorker 行）
//	origin_actor  ← 视图谓词那条臂（四个探针 gateway actor，R52 订正后是四个）
//
// **不是**「挑几条看起来够用的」，而是「该族有的探针标记全部取」——
// 少取一条就是一类探针静默进入 cohort，而 cohort 是基线，静默污染它等于
// 静默改变 reward。
//
// ⚠ **诚实标注**：会话族在 252 上 `is_auto_request` 为 0 行（§9.44 实测），
// 所以这条谓词的**探针分类效果未被生产数据验证过**，被验证的只有
// 「引用的三列在该族表上确实存在」（迁移 707 + 252 列清单双向核对）。
// 由 TestProbeTrafficExclusionPredicateColumnsExistOnTheirFamily 钉住后者。
const probeTrafficExclusionPredicateSession = "(COALESCE(%s.origin_stage, 'business') = 'business'" +
	" AND COALESCE(%s.task_type, '') <> 'probe_triggered'" +
	" AND COALESCE(%s.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker', 'probe-service', 'credential-selfcheck-worker'))"

// probeTrafficExclusionPredicateFor 按数据源族返回探针排除谓词。
//
// # 存在的理由
//
// settle worker 的三条腿都由 `settleSourceSpec` 驱动（§9.43），而**读点表随 S4
// 写门在 request_logs_hot ⇄ session_turns_hot 之间切换**。谓词的可用列集
// 随表而变，所以「用哪条谓词」必须由**同一个**族标签决定，不能各调用点自己判断。
//
// 以前没有这个分派，是因为 cohort 那条 SQL **完全不看探针**（审计 §9.73.4：
// 24h 生产窗口 8,270 行 cohort 里 8,269 行是探针，而 `probe_triggered`
// 根本不在 selection 的 task_type 词表里 ⇒ 99.99% 的基线服务 0 条结算）。
//
// ⚠ 未知族**不静默回落到 v1**：静默回落会让「新增一个族却忘了配谓词」变成
// 一次 42703 或一次静默污染，而不是一次可定位的错误。unknown 返回空串 + false，
// 由调用方（与本包的族闭集门）负责暴露。
func probeTrafficExclusionPredicateFor(family, alias string) (string, bool) {
	switch family {
	case settleFamilyV1:
		return fmt.Sprintf(probeTrafficExclusionPredicate, alias, alias), true
	case settleFamilySession:
		return fmt.Sprintf(probeTrafficExclusionPredicateSession, alias, alias, alias), true
	default:
		return "", false
	}
}

// probeFailureEvidenceWindowSQL is the credential-level failure-evidence
// window (INV-5): how far back a candidate failure still counts as "this
// credential has errors and needs scheduled verification". Kept as a shared
// constant so guarded files (e.g. credential_probe_v2.go, where a bare
// interval literal would trip the ManualBalanceProtectionPredicate drift
// guard) reference one spelling.
const probeFailureEvidenceWindowSQL = "interval '24 hours'"

// probeUsageWindowInterval is the usage window for probe scoping (INV-3):
// only models with real (non-probe) traffic on the credential within the
// last 3 days are probeable by scheduled scans.
const probeUsageWindowInterval = "interval '3 days'"

// nodeProbeParkInterval is how far a successful probe / business success
// parks next_retry_at. 30 days is deliberately beyond every scheduler's
// practical horizon: the row stays inspectable (last_direct_ok=TRUE) but no
// pump / reconciler / scanner will reach it. A new real failure re-arms the
// row to now()+5s via Submit's healthy-parked branch (INV-2).
const nodeProbeParkInterval = "interval '30 days'"
