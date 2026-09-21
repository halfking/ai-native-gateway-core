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
// probeTrafficExclusionPredicateView below.
//
// Both format verbs take the same request_logs alias (pass it twice).
// Every usage scan must exclude probe traffic, otherwise probes count as
// usage and the scan keeps re-probing what only probes ever touched (INV-3).
const probeTrafficExclusionPredicate = "(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE)" +
	" AND COALESCE(%s.origin_stage, 'business') = 'business')"

// probeTrafficExclusionPredicateView is the frozen-113-column-contract variant
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
//   - origin_actor IN the probe gateway actors (session branch AND v1 branch:
//     probeGateway sends X-LLM-Origin-Actor: node-probe-worker, ActiveProbe
//     sends active-probe-worker; verified populated on the real view).
//
// Both format verbs take the same view alias (pass it twice).
const probeTrafficExclusionPredicateView = "(NOT COALESCE('probe' = ANY(%s.quality_flags), FALSE)" +
	" AND COALESCE(%s.task_type, '') <> 'probe_triggered'" +
	" AND COALESCE(%s.origin_actor, '') NOT IN ('node-probe-worker', 'active-probe-worker'))"

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
