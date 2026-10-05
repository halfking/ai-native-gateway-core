package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

// DualReadDiff summarizes the divergence between V1 (public.request_logs)
// and V2 (public.session_turns) for one session during the cut-over
// observation window. Non-zero TokenDiff / CostDiff means V1 and V2
// disagree, which must be reconciled before flipping the primary read.
type DualReadDiff struct {
	SessionID string
	Compared  int     // number of V1 rows seen for the session
	TokenDiff int64   // V1 total tokens - V2 total tokens
	CostDiff  float64 // V1 total cost_usd - V2 total cost_usd
	SampleAt  time.Time
}

// DualReadDetail is the storage-plan-v2 S2 row-level reconciliation
// (plan §4 S2: dual_read_validator 对账扩展「turns vs request_logs 等值」;
// §8-C/D 的端点化形态). Compare is the aggregate view; CompareDetail adds
// the per-request_id set difference and per-field drift samples that make
// a "7 天零漂移" gate auditable:
//   - OnlyInV1: request_logs rows with no session_turns counterpart
//     (mirror loss / no-session traffic not yet covered by D4 synthetics);
//   - OnlyInV2: turns rows with no request_logs counterpart;
//   - drifted matched rows per field (tokens / cost / success /
//     credits_charged), each with up to SampleLimit request_ids.
//
// Both sides read the BASE tables (hot ∪ parent), not the compatibility
// views: post-710 request_logs_with_current_month already contains turns
// rows, so view-vs-turns would self-compare and hide V1-side drift; the
// turns view predates 707 and lacks credits_charged.
type DualReadDetail struct {
	SessionID string    `json:"session_id"`
	Tenant    string    `json:"tenant"`
	SampleAt  time.Time `json:"sampled_at"`

	V1Rows int `json:"v1_rows"`
	V2Rows int `json:"v2_rows"`

	OnlyInV1Count int      `json:"only_in_v1_count"`
	OnlyInV2Count int      `json:"only_in_v2_count"`
	OnlyInV1      []string `json:"only_in_v1,omitempty"`
	OnlyInV2      []string `json:"only_in_v2,omitempty"`

	TokenDriftCount   int               `json:"token_drift_count"`
	CostDriftCount    int               `json:"cost_drift_count"`
	SuccessDriftCount int               `json:"success_drift_count"`
	CreditsDriftCount int               `json:"credits_drift_count"`
	DriftSamples      []DualDriftSample `json:"drift_samples,omitempty"`

	// 计费等值合计（plan §8-D）：停写 gate 的硬指标。
	V1CreditsTotal int64 `json:"v1_credits_total"`
	V2CreditsTotal int64 `json:"v2_credits_total"`

	// ZeroDrift 为真 = 双侧行集相等且无字段漂移（§8-C/D 同判）。
	//
	// ⚠️ 2026-10-02：ZeroDriftEvaluable 为假时，ZeroDrift **不代表「有漂移」**。
	// S4 停写后 v1 冻结、v2 继续增长，任何新轮次在 v1 侧都没有对应行，
	// OnlyInV2 必然单调增长 ⇒ ZeroDrift 永久为 false。而 spec 的退出条件正是
	// 「dual_read_validator 对账 7 天零漂移」——照字面读，这道门在停写之后
	// 永远无法宣告完成。与 Summarize 的真空为绿是同一根因的两个方向：
	// 一个恒真、一个恒假，都不是观测。
	ZeroDrift          bool `json:"zero_drift"`
	ZeroDriftEvaluable bool `json:"zero_drift_evaluable"`
	V1WritesEnabled    bool `json:"v1_writes_enabled"`
}

// DualDriftSample is one drifted request with the offending fields.
type DualDriftSample struct {
	RequestID string   `json:"request_id"`
	TokenV1   *int64   `json:"token_v1,omitempty"`
	TokenV2   *int64   `json:"token_v2,omitempty"`
	CostV1    *float64 `json:"cost_v1,omitempty"`
	CostV2    *float64 `json:"cost_v2,omitempty"`
	SuccessV1 *bool    `json:"success_v1,omitempty"`
	SuccessV2 *bool    `json:"success_v2,omitempty"`
	CreditsV1 *int64   `json:"credits_v1,omitempty"`
	CreditsV2 *int64   `json:"credits_v2,omitempty"`
}

// DualReadValidator compares V1 and V2 read paths for a session.
//
// It is safe to construct without a DB pool (Compare returns a "nil pool"
// error); this lets the admin handler wire it up unconditionally.
type DualReadValidator struct {
	db dualReadQuerier
}

// dualReadQuerier is the slice of pgx a DualReadValidator needs.
//
// It is an interface rather than *pgxpool.Pool so a caller can hand in a
// transaction instead of a pool. That is not hypothetical tidiness: the S4
// gate's own regression test needs to take two measurements — one with v1
// writes on, one with them off — and assert the *measurements* are identical
// while the *verdicts* differ. Run against the pool those two land on
// different snapshots, and on a database with a live writer the numbers move
// between them: the test failed roughly 1 run in 8 on the local real database
// before this existed, which is a flaky gate rather than a gate, and flaky
// gates get disabled.
//
// A REPEATABLE READ transaction pins both reads to one snapshot, so the
// assertion keeps its full strength instead of being loosened into a
// tolerance. *pgxpool.Pool satisfies it unchanged, so no caller had to move.
type dualReadQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewDualReadValidator returns a validator backed by the given pool.
//
// ⚠ The nil check here is load-bearing, not defensive boilerplate. Storing a
// nil *pgxpool.Pool directly into the `db` interface would produce a
// **non-nil interface holding a nil pointer**, so `v.db == nil` inside
// Summarize would be false and the guard would let a nil pool through to a
// method call. That is not hypothetical: widening `db` from a concrete pool to
// an interface introduced exactly that panic in
// TestDualReadValidator_NilPoolCompare, and the offline constructor test
// caught it immediately. A nil pool must become a nil interface, which means
// testing it here, before the box.
func NewDualReadValidator(db *pgxpool.Pool) *DualReadValidator {
	return &DualReadValidator{db: unboxNilQuerier(db)}
}

// unboxNilQuerier turns an interface holding a **typed nil** into a genuinely
// nil interface, so that `v.db == nil` keeps meaning "not configured".
//
// It has to be done here, at every entry point, rather than in one
// constructor: NewDualReadValidatorOn takes the interface directly, so a caller
// can box a nil *pgxpool.Pool just as easily as NewDualReadValidator can. A
// check in only one of the two constructors is the kind of gate that passes
// review and still lets the bug through.
//
// A type switch rather than reflect: the set of concrete types this interface
// is ever satisfied by is closed and tiny, and reflection here would be a
// per-call cost on a code path that is already the slow one.
func unboxNilQuerier(q dualReadQuerier) dualReadQuerier {
	switch t := q.(type) {
	case nil:
		return nil
	case *pgxpool.Pool:
		if t == nil {
			return nil
		}
	case pgx.Tx:
		// pgx.Tx is itself an interface, so a nil one arrives here boxed.
		if t == nil {
			return nil
		}
	}
	return q
}

// NewDualReadValidatorOn returns a validator reading through q, which may be a
// pool or a transaction. Callers that need two measurements of the same instant
// must pass one transaction, not two pool reads.
func NewDualReadValidatorOn(q dualReadQuerier) *DualReadValidator {
	return &DualReadValidator{db: unboxNilQuerier(q)}
}

// RegisterRoutes wires the reconciliation endpoints:
//
//	GET /api/admin/sessions/{id}/dual-read?limit=10   单会话行级对账
//	GET /api/admin/sessions/dual-read-drift?hours=168 全量镜像漂移（population）
//
// limit caps the per-side missing/drift sample arrays (1..50, default 10).
func (v *DualReadValidator) RegisterRoutes(mux *http.ServeMux, adminMw func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("/api/admin/sessions/{id}/dual-read", adminMw(v.handleDualRead))
	mux.Handle("/api/admin/sessions/dual-read-drift", adminMw(v.handleDualReadDrift))
}

// ── population-level mirror drift (2026-09-30 审计新增) ────────────────
//
// Compare/CompareDetail 是**单会话**对账：必须先知道查哪个 session 才会去查。
// 2026-09-30 的会话请求数据复审正是因此漏掉了镜像漏写——没人知道该查哪个会话。
// 这次缺口是手写 SQL 才发现的：sessions_v2.enabled=true / shadow_write=true
// 之下，仍有 20,660 个会话的 38,878 行从未进入 session 族。
//
// 更关键的是，「没进 session 族」有两种完全不同的性质，必须分开报：
//
//	按设计排除（不是缺陷）
//	  · work_type ∈ {session_title, session_summary} 的网关内部回环 ——
//	    hook.go IsInternalAutoEntry 明确排除，标题/摘要不是用户轮次；
//	  · request_status='in_progress' 且无 error_kind 的非终态占位行 ——
//	    hook.go 的终态闸门排除，镜像它会永久记成 success=false/status=500
//	    并吃掉后续成功补写。
//	真漏写（缺陷）
//	  · 其余全部：终态失败（rate_limited/failure）与成功请求。
//	    isTerminalFailure 已覆盖前两者，所以它们本该被镜像。
//
// 把这个区分做成端点，是为了让「S4 能不能开」变成一个可查询的判据，
// 而不是每次靠人临时写 SQL。

// MirrorDriftBucket is one slice of the drift breakdown.
type MirrorDriftBucket struct {
	Key   string `json:"key"`
	Rows  int64  `json:"rows"`
	Class string `json:"class"` // internal_loopback | non_terminal | genuine_loss
}

// MirrorDriftSummary is the population-level view over a time window.
type MirrorDriftSummary struct {
	WindowHours int       `json:"window_hours"`
	WindowStart time.Time `json:"window_start"`
	SampledAt   time.Time `json:"sampled_at"`

	V1Rows               int64 `json:"v1_rows"`
	V1RowsWithoutTurns   int64 `json:"v1_rows_without_turns"`
	SessionsWithoutTurns int64 `json:"sessions_without_turns"`

	// 三类互斥且求和 = V1RowsWithoutTurns。
	InternalLoopbackRows int64 `json:"internal_loopback_rows"`
	NonTerminalRows      int64 `json:"non_terminal_rows"`
	GenuineLossRows      int64 `json:"genuine_loss_rows"`
	GenuineLossSessions  int64 `json:"genuine_loss_sessions"`

	ByWorkType      []MirrorDriftBucket `json:"by_work_type,omitempty"`
	ByRequestStatus []MirrorDriftBucket `json:"by_request_status,omitempty"`

	// S4Ready 为真 = 窗口内没有真漏写，**且本次运行确实验证了这件事**。
	// S4 停写门控的前置判据。
	//
	// ⚠️ 「且本次运行确实验证了这件事」是 2026-10-02 补的硬条件。此前本字段
	// 单纯是 `GenuineLossRows == 0`，而停写会让这个条件**恒真**——S4 一关，
	// 窗口内 V1 行恒为 0，于是无行可缺、GenuineLoss 恒 0、Ready 恒 true，
	// 且响应 JSON 与健康态逐字节相同。判定规则见 dual_read_gate.go。
	S4Ready bool `json:"s4_ready"`

	// 以下三项让「没验证」与「验证了、有漂移」在响应里可区分。把两者都折成
	// s4_ready=false 会把「不知道」伪装成「不安全」，而这正是本门最初犯的错的
	// 镜像版本。
	V1WritesEnabled  bool   `json:"v1_writes_enabled"`
	S4GateVoid       bool   `json:"s4_gate_void"`
	S4GateVoidReason string `json:"s4_gate_void_reason,omitempty"`

	// V1CoveragePP is the share of traffic-bearing hours in the window that
	// had v1 data (§9.235). It is the **sample size** behind s4_ready, and it
	// is the field whose absence made the older binary rule look safe: a window
	// with v1 traffic in 1% of its hours satisfied "the window contained v1
	// traffic" and then answered "ready" on that basis.
	//
	// Reported whether or not the gate voided, so a healthy-looking run can be
	// compared against its own sample size without a second query. `-1` means
	// "not measured because the window had no traffic-bearing hours at all",
	// which is distinct from a measured 0.
	V1CoveragePP float64 `json:"v1_coverage_pp"`
}

// mirrorDriftClassSQL classifies a drifting V1 row into the three buckets.
// Kept as one expression so the aggregate and the breakdown cannot disagree.
//
// The two exclusion arms are a statement-by-statement transcription of the
// Go gates in internal/sessionv2mirror/hook.go — NOT an independent guess at
// "what looks internal". Divergence here is not cosmetic: a row the hook
// deliberately skipped but this expression calls genuine_loss keeps s4_ready
// false forever and blocks the cutover that is actually safe.
//
//	internal_loopback ← hook.go:102 `!synthetic && IsInternalAutoEntry(entry)`
//	    IsInternalAutoEntry (telemetry/internal_loopback.go:23-39):
//	      1. requires is_auto_request IS TRUE (a NULL is NOT internal);
//	      2. request_type  ∈ {title_gen, summary};
//	      3. origin_actor  ∈ {auto-title-generator, auto-summary-generator,
//	                          session-summary};
//	      4. otherwise task_type IS NULL/'' (taskless auto entry).
//	    work_type is deliberately NOT an arm: the Go gate never reads it, so
//	    keying on it would mask real loss (is_auto_request=TRUE + task_type
//	    set + work_type='session_title' is a business turn the hook mirrors).
//	non_terminal ← hook.go:71 `!entry.Success && !isTerminalFailure(entry)`
//	    isTerminalFailure (hook.go:991-1002) is true when request_status ∈
//	    {failure, rate_limited} OR error_kind is non-empty; the row is
//	    mirrored when Success is true. Negating both yields the arm below.
//
// SSOT: db.MirrorDriftClassSQL. 定义搬到 db 包是因为 admin 侧也需要同一个
// 排除谓词（session_summary_v2 的 fallback turns 腿，见审计缺陷 7），
// 留在 package main 就只能复制一份。
const mirrorDriftClassSQL = db.MirrorDriftClassSQL

// mirrorDriftScopeSQL yields the drifting V1 rows for the window: rows that
// have a gw_session_id but no session_turns counterpart. Reads BASE tables
// (hot ∪ parent) on both sides for the same reason as CompareDetail — the
// 710 view already contains turns rows, so view-vs-view would self-compare.
const mirrorDriftScopeSQL = `
SELECT rl.request_id, rl.gw_session_id, rl.tenant_id::text AS tenant_id,
       rl.work_type, rl.request_status, ` + mirrorDriftClassSQL + ` AS drift_class
FROM (
    SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
           is_auto_request, origin_actor, request_type, task_type, success, ts
    FROM request_logs_hot
    WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
    UNION ALL
    SELECT request_id, gw_session_id, tenant_id, work_type, request_status, error_kind,
           is_auto_request, origin_actor, request_type, task_type, success, ts
    FROM request_logs
    WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
) rl
WHERE ($1 = '' OR rl.tenant_id = $1)
  AND NOT EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = rl.request_id)
  AND NOT EXISTS (SELECT 1 FROM session_turns     tp WHERE tp.request_id = rl.request_id)`

// Summarize computes the population-level drift summary over the last
// windowHours (1..720, default 168 = 7 days). tenant "" = all tenants.
//
// Cost note: the two NOT EXISTS probes are index lookups on
// session_turns(request_id) per V1 row in the window. That is fine for the
// default 7-day window but grows linearly; the endpoint is a diagnostic and
// an S4 pre-gate, not a hot-path metric.
func (v *DualReadValidator) Summarize(ctx context.Context, tenant string, windowHours int) (*MirrorDriftSummary, error) {
	return v.SummarizeFrom(ctx, tenant, time.Now(), windowHours)
}

// SummarizeFrom is Summarize with the observation instant supplied by the
// caller. It exists for callers that must take **two** measurements of the
// same thing and compare them — see the note on dualReadQuerier and
// TestS4GateStopsClaimingReadyAfterStopWrite.
//
// Both halves of the comparison have to be pinned, and finding that out took
// two wrong turns worth recording:
//
//   - REPEATABLE READ alone is not enough. The window start is derived from
//     the Go clock, so two calls milliseconds apart have slightly different
//     24h windows and a row falls off the boundary between them. The snapshot
//     is identical and the answer is still wrong.
//   - Pinning the window alone is not enough either, for the ordinary reason:
//     the database has a live writer (measured, §9.235), so the contents move.
//
// So: same snapshot (a transaction) AND same window (this function). With only
// the transaction, the S4 gate test failed roughly 1 run in 3 on the local real
// database; before that, on two pool reads, roughly 1 in 8.
// clampWindowHours bounds the requested window to [1, 720].
//
// Extracted as a named function so TestS4WindowClamp can pin it directly. It
// was previously inline, and mutation P7 deleted it with every gate still
// green: rule 4 catches the verdict for any value below 24, so nothing
// downstream noticed that the *measurement* had degenerated to a
// single-bucket window reporting 100% coverage. A rule masking a broken
// measurement is worse than no rule, because it reads as coverage.
func clampWindowHours(h int) int {
	if h < 1 {
		return 1
	}
	if h > 720 {
		return 720
	}
	return h
}

// v1CoverageHoursSQL measures, per hour of the window, whether v1 has rows and
// whether either side has rows (§9.235 rule 3). One statement so numerator and
// denominator come from the same snapshot.
//
// ★ Both storage faces are read, and that is load-bearing, not stylistic.
// `request_logs_hot` is a **separate, disjoint table** (§9.160.7): reading only
// the parent misses the newest rows, so a window that ends in the last hours
// would score V1CoveragePP ≈ 0 and the gate would report a *false* void.
//
// That failure is tempting to "fix" by lowering s4MinV1CoveragePP, which
// destroys the rule instead of the bug. §9.238 re-measured this window on the
// real database: v1 genuinely wrote **zero** rows for 5 days (2026-09-06
// 21:00:27 → 2026-09-11 21:21:10) while session_turns and request_wal both
// kept writing — i.e. the false void and the true void look identical from the
// verdict string. Only the measurement tells them apart, so the measurement
// must not be the thing that silently degrades.
//
// TestS4GateCoverageSQLReadsBothFaces pins the four relations.
const v1CoverageHoursSQL = `
		WITH h AS (
			SELECT generate_series($2::timestamptz, $3::timestamptz, interval '1 hour') AS b
		), per_hour AS (
			SELECT
			  EXISTS (SELECT 1 FROM (
			        SELECT tenant_id FROM request_logs_hot WHERE ts >= h.b AND ts < h.b + interval '1 hour'
			        UNION ALL
			        SELECT tenant_id FROM request_logs      WHERE ts >= h.b AND ts < h.b + interval '1 hour'
			      ) rv WHERE $1 = '' OR rv.tenant_id::text = $1) AS has_v1,
			  EXISTS (SELECT 1 FROM (
			        SELECT tenant_id FROM session_turns_hot WHERE ts >= h.b AND ts < h.b + interval '1 hour'
			        UNION ALL
			        SELECT tenant_id FROM session_turns      WHERE ts >= h.b AND ts < h.b + interval '1 hour'
			      ) sv WHERE $1 = '' OR sv.tenant_id::text = $1) AS has_session
			FROM h
		)
		SELECT
		  count(*) FILTER (WHERE has_v1),
		  count(*) FILTER (WHERE has_v1 OR has_session)
		FROM per_hour`

func (v *DualReadValidator) SummarizeFrom(ctx context.Context, tenant string, now time.Time, windowHours int) (*MirrorDriftSummary, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("dual-read validator not configured")
	}
	windowHours = clampWindowHours(windowHours)
	start := now.Add(-time.Duration(windowHours) * time.Hour)

	sum := &MirrorDriftSummary{
		WindowHours: windowHours,
		WindowStart: start,
		SampledAt:   now,
		// -1 until measured; a zero here would be indistinguishable from a
		// measured "v1 covered none of the window's hours", which is a real
		// and much worse state.
		V1CoveragePP: -1,
	}

	// V1Rows in window (denominator, before the anti-join). Mirrors the scope
	// filter of mirrorDriftScopeSQL so the ratio is meaningful.
	if err := v.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT tenant_id FROM request_logs_hot
			WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
			UNION ALL
			SELECT tenant_id FROM request_logs
			WHERE ts >= $2 AND gw_session_id IS NOT NULL AND gw_session_id <> ''
		) x
		WHERE ($1 = '' OR x.tenant_id::text = $1)`,
		tenant, start,
	).Scan(&sum.V1Rows); err != nil {
		return nil, fmt.Errorf("v1 rows: %w", err)
	}

	// v1 coverage of the window, over hours that saw traffic on EITHER side
	// (§9.235). Rule 3 of the S4 pre-gate needs this: a window where v1 wrote
	// for one hour out of a hundred has v1Rows > 0 and would pass the older
	// binary "any v1 traffic" rule, then report s4_ready=true on a 1% sample.
	//
	// ⚠ The series must span `generate_series(start, now, …)` — the window
	// START and its END. Passing `start` as both arguments yields a one-hour
	// series, and then coverage is 100% whenever that single hour happened to
	// contain v1 data: rule 3 becomes dead code that reports "fully covered"
	// forever. The offline control pair never saw it, because it feeds the
	// verdict function a number instead of computing one; what caught it was
	// the real-database assertion that V1CoveragePP must actually be measured.
	//
	// ⚠ $1 (tenant) must be **referenced** by the query, not merely passed.
	// A parameter that appears in no expression leaves PostgreSQL with nothing
	// to infer its type from: `could not determine data type of parameter $1
	// (42P18)`. The first version of this query filtered nothing and had this
	// bug, and it only surfaced because a real-database test called Summarize —
	// the offline control pair was perfectly happy.
	//
	// The tenant filter is not decoration either: V1Rows above is tenant-scoped
	// (`$1 = '' OR tenant_id::text = $1`), so an unfiltered coverage number
	// would be measuring a different population than the row count it is
	// supposed to qualify.
	//
	// One statement, so the numerator and the denominator come from the same
	// snapshot — the same rule that §9.234 had to add to its own gate after a
	// full-suite run went red while an isolated run stayed green. v1 rows and
	// v1 traffic-hours are two different measurements; taking them at different
	// instants can push the ratio across the floor.
	var covered, trafficBearing int64
	if err := v.db.QueryRow(ctx, v1CoverageHoursSQL, tenant, start, now).Scan(&covered, &trafficBearing); err != nil {
		return nil, fmt.Errorf("v1 coverage: %w", err)
	}
	if trafficBearing > 0 {
		sum.V1CoveragePP = 100 * float64(covered) / float64(trafficBearing)
	}

	// Class breakdown + row total, computed in one pass.
	rows, err := v.db.Query(ctx, `
		SELECT drift_class, COUNT(*), COUNT(DISTINCT gw_session_id)
		FROM (`+mirrorDriftScopeSQL+`) d
		GROUP BY drift_class`, tenant, start)
	if err != nil {
		return nil, fmt.Errorf("drift breakdown: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var class string
		var n, sess int64
		if err := rows.Scan(&class, &n, &sess); err != nil {
			return nil, fmt.Errorf("drift scan: %w", err)
		}
		sum.V1RowsWithoutTurns += n
		switch class {
		case "internal_loopback":
			sum.InternalLoopbackRows = n
		case "non_terminal":
			sum.NonTerminalRows = n
		default:
			sum.GenuineLossRows = n
			sum.GenuineLossSessions = sess
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("drift rows: %w", err)
	}

	// Sessions entirely absent from the session family (any class) — this is
	// what a session-scoped native read would return empty for.
	if err := v.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT gw_session_id) FROM (`+mirrorDriftScopeSQL+`) d`,
		tenant, start,
	).Scan(&sum.SessionsWithoutTurns); err != nil {
		return nil, fmt.Errorf("sessions without turns: %w", err)
	}

	sum.ByWorkType, err = v.driftBuckets(ctx, "COALESCE(work_type, '<null>')", tenant, start)
	if err != nil {
		return nil, err
	}
	sum.ByRequestStatus, err = v.driftBuckets(ctx, "COALESCE(request_status, '<null>')", tenant, start)
	if err != nil {
		return nil, err
	}

	// 门控读数只取一次：settings.GetPlatformBool 每次调用都是一次
	// settings_kv 往返（Global.EffectiveValue），取两次既多一次 DB 往返，
	// 又让「两次读到的值可能不同」成为一个本不该存在的分支。
	v1WritesOn := currentV1WritesEnabled()
	sum.V1WritesEnabled = v1WritesOn
	verdict := s4GateVerdictOf(s4GateInput{
		v1Rows:       sum.V1Rows,
		genuineLoss:  sum.GenuineLossRows,
		v1WritesOn:   v1WritesOn,
		v1CoveragePP: sum.V1CoveragePP,
		windowHours:  sum.WindowHours,
	})
	sum.S4Ready = verdict.Ready
	sum.S4GateVoid = verdict.Void
	sum.S4GateVoidReason = verdict.Reason
	return sum, nil
}

// driftBuckets groups the drifting rows by one projected key, tagging each
// bucket with its dominant class so the breakdown explains itself.
func (v *DualReadValidator) driftBuckets(ctx context.Context, keyExpr, tenant string, start time.Time) ([]MirrorDriftBucket, error) {
	rows, err := v.db.Query(ctx, `
		SELECT `+keyExpr+` AS k, drift_class, COUNT(*) AS n
		FROM (`+mirrorDriftScopeSQL+`) d
		GROUP BY 1, 2
		ORDER BY 3 DESC`, tenant, start)
	if err != nil {
		return nil, fmt.Errorf("drift buckets: %w", err)
	}
	defer rows.Close()
	out := make([]MirrorDriftBucket, 0, 8)
	for rows.Next() {
		var b MirrorDriftBucket
		if err := rows.Scan(&b.Key, &b.Class, &b.Rows); err != nil {
			return nil, fmt.Errorf("drift bucket scan: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (v *DualReadValidator) handleDualReadDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	hours := 168
	if raw := r.URL.Query().Get("hours"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &hours); err != nil || hours < 1 {
			http.Error(w, "invalid hours", http.StatusBadRequest)
			return
		}
	}
	tenant := r.URL.Query().Get("tenant")

	sum, err := v.Summarize(r.Context(), tenant, hours)
	if err != nil {
		slog.Error("dual-read drift summary failed", "hours", hours, "err", err)
		http.Error(w, "dual-read drift summary failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sum)
}

func (v *DualReadValidator) handleDualRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		if limit > 50 {
			limit = 50
		}
	}
	tenant := r.URL.Query().Get("tenant")

	detail, err := v.CompareDetail(r.Context(), tenant, sessionID, limit)
	if err != nil {
		// R74：err.Error() 不拼进 500 响应（admin 端点，对齐 admin 包
		// internal_error.go 模式）——固定文案 + 服务端锚点。
		slog.Error("dual-read compare failed", "session_id", sessionID, "err", err)
		http.Error(w, "dual-read compare failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
}

// Compare runs the V1 vs V2 diff for (tenant, session).
// lastN bounds how many V1 rows we look at; 0 / negative defaults to 10.
func (v *DualReadValidator) Compare(ctx context.Context, tenant, session string, lastN int) (DualReadDiff, error) {
	d := DualReadDiff{SessionID: session, SampleAt: time.Now()}
	if v.db == nil {
		return d, fmt.Errorf("dual-read: nil pool")
	}
	if lastN <= 0 {
		lastN = 10
	}

	var count int
	if err := v.db.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs
		WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
	`, tenant, session).Scan(&count); err != nil {
		return d, fmt.Errorf("count v1: %w", err)
	}
	d.Compared = count

	err := v.db.QueryRow(ctx, `
		WITH v1 AS (
			SELECT COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
			       COALESCE(cost_usd, 0) AS cost
			FROM public.request_logs
			WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
		),
		v2 AS (
			SELECT COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
			       COALESCE(cost_usd, 0) AS cost
			FROM public.session_turns_with_current_month
			WHERE tenant_id=$1 AND session_id=$2
		)
		SELECT
			COALESCE((SELECT sum(total_tokens) FROM v1) - (SELECT sum(total_tokens) FROM v2), 0),
			COALESCE((SELECT sum(cost) FROM v1) - (SELECT sum(cost) FROM v2), 0)
	`, tenant, session).Scan(&d.TokenDiff, &d.CostDiff)
	if err != nil {
		return d, fmt.Errorf("diff query: %w", err)
	}
	return d, nil
}

// CompareDetail runs the row-level V1 vs V2 reconciliation for one session.
// tenant may be empty (super-admin cross-tenant sweep); session is the
// server-side session key (= gw_session_id for mirrored traffic, 'sys:…'
// for D4 synthetics).
func (v *DualReadValidator) CompareDetail(ctx context.Context, tenant, session string, sampleLimit int) (DualReadDetail, error) {
	d := DualReadDetail{
		SessionID: session,
		Tenant:    tenant,
		SampleAt:  time.Now(),
	}
	if v.db == nil {
		return d, fmt.Errorf("dual-read: nil pool")
	}
	if sampleLimit <= 0 {
		sampleLimit = 10
	}
	if sampleLimit > 50 {
		sampleLimit = 50
	}

	// Base tables both sides. v1: request_logs_hot holds the pre-promote
	// window, request_logs the promoted partitions. v2: same family shape.
	// token 表达式双侧同构（NULLIF(COALESCE(p,0)+COALESCE(c,0),0)）以便
	// 等值比较而非口径比较。
	const v1CTE = `
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.request_logs_hot
		WHERE gw_session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
		UNION ALL
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.request_logs
		WHERE gw_session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
	`
	const v2CTE = `
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.session_turns_hot
		WHERE session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
		UNION ALL
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.session_turns
		WHERE session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
	`

	// Row counts + credit totals (plan §8-D).
	err := v.db.QueryRow(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`)
		SELECT
			(SELECT count(*) FROM v1), (SELECT count(*) FROM v2),
			COALESCE((SELECT sum(credits_charged) FROM v1), 0),
			COALESCE((SELECT sum(credits_charged) FROM v2), 0)
	`, tenant, session).Scan(&d.V1Rows, &d.V2Rows, &d.V1CreditsTotal, &d.V2CreditsTotal)
	if err != nil {
		return d, fmt.Errorf("counts query: %w", err)
	}

	// Set difference both ways (plan §8-C).
	if err := v.db.QueryRow(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`)
		SELECT
		  count(*) FILTER (WHERE side = 'v1'),
		  count(*) FILTER (WHERE side = 'v2')
		FROM (
		  (
		    SELECT request_id, 'v1' AS side FROM v1 WHERE request_id IS NOT NULL
		    EXCEPT
		    SELECT request_id, 'v1' FROM v2 WHERE request_id IS NOT NULL
		  )
		  UNION ALL
		  (
		    SELECT request_id, 'v2' AS side FROM v2 WHERE request_id IS NOT NULL
		    EXCEPT
		    SELECT request_id, 'v2' FROM v1 WHERE request_id IS NOT NULL
		  )
		) d
	`, tenant, session).Scan(&d.OnlyInV1Count, &d.OnlyInV2Count); err != nil {
		return d, fmt.Errorf("set-diff query: %w", err)
	}

	// Matched-row field drift + samples (single scan).
	rows, err := v.db.Query(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`),
		m AS (
		  SELECT COALESCE(v1.request_id, v2.request_id) AS request_id,
		         v1.tokens AS tok1, v2.tokens AS tok2,
		         v1.cost_usd AS cost1, v2.cost_usd AS cost2,
		         v1.success AS succ1, v2.success AS succ2,
		         v1.credits_charged AS cred1, v2.credits_charged AS cred2
		  FROM v1 FULL OUTER JOIN v2 ON v1.request_id = v2.request_id
		  WHERE v1.request_id IS NOT NULL AND v2.request_id IS NOT NULL
		)
		SELECT request_id, tok1, tok2, cost1, cost2, succ1, succ2, cred1, cred2
		FROM m
		-- 计费/用量数值按 NULL↔0 归一化比较：v1 失败行 cost/credits 为 NULL
		-- 而 turns 落 0（占位零值）不是计费事实漂移；真实数值差异仍然报告。
		WHERE COALESCE(tok1, 0)  IS DISTINCT FROM COALESCE(tok2, 0)
		   OR COALESCE(cost1, 0) IS DISTINCT FROM COALESCE(cost2, 0)
		   OR COALESCE(succ1, false) IS DISTINCT FROM COALESCE(succ2, false)
		   OR COALESCE(cred1, 0) IS DISTINCT FROM COALESCE(cred2, 0)
		LIMIT $3
	`, tenant, session, sampleLimit)
	if err != nil {
		return d, fmt.Errorf("drift query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s DualDriftSample
		if err := rows.Scan(&s.RequestID, &s.TokenV1, &s.TokenV2, &s.CostV1, &s.CostV2,
			&s.SuccessV1, &s.SuccessV2, &s.CreditsV1, &s.CreditsV2); err != nil {
			return d, fmt.Errorf("scan drift sample: %w", err)
		}
		d.DriftSamples = append(d.DriftSamples, s)
	}
	if err := rows.Err(); err != nil {
		return d, fmt.Errorf("drift samples: %w", err)
	}
	for _, s := range d.DriftSamples {
		if normI(s.TokenV1) != normI(s.TokenV2) {
			d.TokenDriftCount++
		}
		if normF(s.CostV1) != normF(s.CostV2) {
			d.CostDriftCount++
		}
		if normB(s.SuccessV1) != normB(s.SuccessV2) {
			d.SuccessDriftCount++
		}
		if normI(s.CreditsV1) != normI(s.CreditsV2) {
			d.CreditsDriftCount++
		}
	}
	// Drift counts are capped by sampleLimit; when the cap is hit, report the
	// capped value conservatively (the zero-drift gate treats >0 as fail).
	//
	// Evaluability, not the verdict, is what the S4 gate changes here: with
	// v1 writes off the v1 side is a frozen snapshot, so a set difference
	// against it measures "time passed", not "data disagrees". Callers must
	// read ZeroDriftEvaluable before treating false as evidence of drift.
	d.V1WritesEnabled = currentV1WritesEnabled()
	d.ZeroDriftEvaluable = d.V1WritesEnabled
	if !d.ZeroDriftEvaluable {
		d.ZeroDrift = false
		return d, nil
	}
	d.ZeroDrift = d.OnlyInV1Count == 0 && d.OnlyInV2Count == 0 && len(d.DriftSamples) == 0
	return d, nil
}

// NULL↔0 / NULL↔false normalizers mirroring the drift WHERE clause.
func normI(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func normF(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func normB(v *bool) bool {
	if v == nil {
		return false
	}
	return *v
}
