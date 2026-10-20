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

// DualReadDiff summarizes the divergence between V1 (public._request_logs)
// and V2 (public.session_turns) for one session during the cut-over
// observation window. Non-zero TokenDiff / CostDiff means V1 and V2
// disagree, which must be reconciled before flipping the primary read.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：这个类型描述的比较**已不存在**。
// Compare 现在返回错误（而不是零差异的 diff）——见该方法的注释。
type DualReadDiff struct {
	SessionID string
	Compared  int     // number of V1 rows seen for the session
	TokenDiff int64   // V1 total tokens - V2 total tokens
	CostDiff  float64 // V1 total cost_usd - V2 total cost_usd
	SampleAt  time.Time
}

// DualReadDetail is the storage-plan-v2 S2 row-level reconciliation
// (plan §4 S2: dual_read_validator 对账扩展「turns vs _request_logs 等值」;
// §8-C/D 的端点化形态). Compare is the aggregate view; CompareDetail adds
// the per-request_id set difference and per-field drift samples that make
// a "7 天零漂移" gate auditable:
//   - OnlyInV1: _request_logs rows with no session_turns counterpart
//     (mirror loss / no-session traffic not yet covered by D4 synthetics);
//   - OnlyInV2: turns rows with no _request_logs counterpart;
//   - drifted matched rows per field (tokens / cost / success /
//     credits_charged), each with up to SampleLimit request_ids.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：这个对账**退役**。
// 上面三类差集都需要「另一侧」；v1 族被 DROP 之后没有另一侧。
// ⇒ CompareDetail 现在**不做任何比较**，如实报告 V1Retired=true，
// 且 ZeroDriftEvaluable=false（= 本次运行没有验证任何漂移）。
// 零差异与「没做对账」必须长得不一样，否则响应读起来像「两侧一致」。
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
	//
	// ★ 2026-10-20（v1 族退役）：对账本身退役 ⇒ ZeroDriftEvaluable 恒为 false、
	// ZeroDrift 恒为 false。两者含义不变（false = 没有证据，不是「有漂移」），
	// 但**理由变了**：不再是「v1 冻结所以比不了」，而是「第二侧已经不存在」。
	ZeroDrift          bool `json:"zero_drift"`
	ZeroDriftEvaluable bool `json:"zero_drift_evaluable"`
	V1WritesEnabled    bool `json:"v1_writes_enabled"`

	// V1Retired 为真 = v1 族已整体退役（迁移 890），**本次运行没有做任何对账**。
	//
	// 它与「对账结果是零差异」必须可区分：退役态下 v1_rows / v2_rows /
	// 各个差集与漂移计数都是**未测量**，不是「测到了 0」。
	V1Retired bool `json:"v1_retired"`
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
	//
	// ★ 2026-10-20（迁移 890，v1 族整体退役）：对账退役 ⇒ S4Ready **恒为
	// false**，且它恒为 false 的理由是「没有第二侧可比」，不是「测到了漂移」。
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
	//
	// ★ 2026-10-20（v1 族退役）：退役态下它恒为 -1，含义是「**未测量**」——
	// 第二侧不存在，连「窗口里有没有 v1 流量」这个问题都不存在了。
	V1CoveragePP float64 `json:"v1_coverage_pp"`

	// V1Retired 为真 = v1 族已整体退役（迁移 890），**本次运行没有做任何对账**。
	//
	// ★ 它与「对账结果是零漂移」必须可区分：本结构里所有与 v1 有关的数字
	// 在退役态下都是**未测量**（0 / -1），不是「测到了 0」。
	// 「没验证」与「验证了、没有问题」长得一样，就是本仓反复吃过亏的那条
	// （S4 的真空为绿）。所以这里给一个显式标记，而不是靠数字自证。
	V1Retired bool `json:"v1_retired"`
}

// mirrorDriftClassSQL classifies a drifting V1 row into the three buckets.
// Kept as one expression so the aggregate and the breakdown cannot disagree.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：本文件**不再使用**它——
// 对账退役后没有「漂移的 v1 行」这个集合。保留这条转发是为了让既有门
// （cmd/gateway/dual_read_class_parity_test.go 等按这个名字引用它）继续
// 编译并**自行变红**，而不是让整个 cmd/gateway 的测试包编译失败。
// SSOT 仍在 db.MirrorDriftClassSQL（admin 侧在用）。
const mirrorDriftClassSQL = db.MirrorDriftClassSQL

// s4GateReasonV1FamilyRetired 是退役态的 void 原因（2026-10-20，迁移 890）。
//
// 它与 dual_read_gate.go 里那三条**不是同一类**：
//
//	s4GateReasonV1WritesDisabled   v1 还在，只是没在写 ⇒ 「这次没法评估漂移」
//	s4GateReasonNoV1Traffic         v1 还在，窗口里恰好没流量 ⇒ 同上
//	s4GateReasonInsufficientV1Coverage / …WindowTooShort
//	                               v1 还在，样本不够 ⇒ 同上
//	s4GateReasonV1FamilyRetired     **第二侧已经不存在** ⇒ 不是「没法评估」，
//	                               是「没有东西可评估」
//
// 之所以单列一条：前三条都在说「证据不足」，这一条在说「题目没了」。
// 复用前三条中的任何一条，都会让读者去找「把 v1 写打开」这个 remedy——
// 而那个 remedy 在退役之后**有害**（v1 族已 DROP）。
const s4GateReasonV1FamilyRetired = "v1_family_retired"

// ★ 2026-10-20（迁移 890，v1 族整体退役）：「漂移的 v1 行」这个集合
// **不再存在**，所以本文件原先那条对账范围 SQL（读 v1 族两个面、
// 配两个 NOT EXISTS 反连接）已删除。
//
// 删它的理由不是「v1 表空了」——空表也还能查；是**那一族被 DROP 了**：
// 任何提到它的语句都会 42P01（整条语句失败，不是那条腿失败）。
// 而即便查得出来，零行的一侧也不能支撑任何「零漂移」的结论。
// ⇒ 对账退役，端点如实报告「没有对账可做」（见 SummarizeFrom）。

// Summarize computes the population-level drift summary over the last
// windowHours (1..720, default 168 = 7 days). tenant "" = all tenants.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：对账退役 ⇒ 这个端点**不再测量**，
// 如实报告「没有对账可做」。原先的成本注记（每行 v1 两个 NOT EXISTS 索引
// 探针）随之作废：现在**一条查询都不发**。
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

// ★ 2026-10-20（迁移 890，v1 族整体退役）：原先那条「按小时量 v1 覆盖率」的
// SQL 已删除——它读 v1 族的两个面，那一族被 DROP 之后整条语句 42P01。
// 而覆盖率这个量本身也失去了对象：没有第二侧，「窗口里 v1 覆盖了多少小时」
// 这个问题不存在了（V1CoveragePP 恒为 -1 = 未测量）。
//
// 保留这段注记是因为它记着一条**仍然有效**的教训：把「阈值调松」当成
// 「修好覆盖率不足」是修错了方向——真正的修法是让测量本身不失真。
// 覆盖率规则（dual_read_gate.go 的 s4MinV1CoveragePP）随对账一起退役；
// 若将来要恢复任何「按小时的覆盖面」度量，先读那段注记里的三条坑。

// SummarizeFrom is Summarize with the observation instant supplied by the
// caller. It exists for callers that must take **two** measurements of the
// same thing and compare them — see the note on dualReadQuerier and
// TestS4GateStopsClaimingReadyAfterStopWrite.
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**对账退役**，本函数**不发任何查询**。
//
// 为什么不是「把 v1 侧换成会话侧、继续比」：那会比的是**同一族的两张表**
// （session_turns_hot vs session_turns），它的差集是**晋级延迟**，不是镜像漂移——
// 把晋级延迟当成漂移，会让 S4 门永远红；而把它当成「零漂移」，就是本仓反复
// 吃过亏的「真空为绿」。⇒ 诚实做法是**不测**，并把「没测」如实说出来。
//
// 为什么不能只把 v1 那一腿删掉、留下会话侧的数字：那些数字是**未测量**，
// 而响应里如果只有数字没有标记，读者会把「没量」读成「量了，0」。
// ⇒ 显式的 v1_retired 标记 + s4_gate_void=true + v1_coverage_pp=-1。
//
// 窗口参数（windowHours / now）仍然被如实回显：它们描述的是「本来会看的窗口」，
// 便于把退役前后的读数并排看。
func (v *DualReadValidator) SummarizeFrom(ctx context.Context, tenant string, now time.Time, windowHours int) (*MirrorDriftSummary, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("dual-read validator not configured")
	}
	windowHours = clampWindowHours(windowHours)
	start := now.Add(-time.Duration(windowHours) * time.Hour)

	// 门控读数只取一次：settings.GetPlatformBool 每次调用都是一次
	// settings_kv 往返（Global.EffectiveValue），取两次既多一次 DB 往返，
	// 又让「两次读到的值可能不同」成为一个本不该存在的分支。
	//
	// ★ 注意语义：v1_writes_enabled 如实反映**开关读数**，而 v1_retired
	// 说的是**对账路径已退役**。两者不是一回事：开关开着而族已退役，
	// 意味着「v1 写入会失败」——那是要有人去处理的配置错误，
	// 两个字段都如实报出来，矛盾就摆在那里而不是被藏起来。
	sum := &MirrorDriftSummary{
		WindowHours: windowHours,
		WindowStart: start,
		SampledAt:   now,
		// -1 = 未测量（不是「测到 0%」）。
		V1CoveragePP:     -1,
		V1Retired:        true,
		V1WritesEnabled:  currentV1WritesEnabled(),
		S4Ready:          false,
		S4GateVoid:       true,
		S4GateVoidReason: s4GateReasonV1FamilyRetired,
	}
	return sum, nil
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
//
// ★ 2026-10-20（迁移 890，v1 族整体退役）：**这个比较退役**，本方法返回错误。
//
// 为什么是错误而不是「零差异的 diff」：零差异在这个语义下是**假的**——
// 它会被读成「两侧一致」，而事实是「只有一侧，而且它没被读过」。
// ⇒ 与其让一个不读 v1 的实现返回 (0, 0)，不如让它**响亮地失败**，
// 让调用方知道这里已经没有什么可比的（CompareDetail / Summarize 走 HTTP，
// 那里不能是 500，所以它们返回带 v1_retired 标记的报告）。
func (v *DualReadValidator) Compare(ctx context.Context, tenant, session string, lastN int) (DualReadDiff, error) {
	d := DualReadDiff{SessionID: session, SampleAt: time.Now()}
	if v == nil || v.db == nil {
		return d, fmt.Errorf("dual-read: nil pool")
	}
	return d, fmt.Errorf("dual-read: v1 family retired — no dual-read to perform")
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

	// ★ 2026-10-20（迁移 890，v1 族整体退役）：**对账退役**，下面那段
	// 「两侧 CTE + 三个查询」的比较**整体删除**，本方法**不发任何查询**。
	//
	// 删掉的理由（不是「v1 表空了」）：
	//   - v1 族被 DROP ⇒ 任何提到它的语句 42P01（整条语句失败）；
	//   - 就算能查，「只有一侧」也比不出漂移：零行的一侧不是「零漂移」的证据。
	//
	// 响应里剩下的数字全部是**未测量**（0），所以必须靠 v1_retired 标记与
	// zero_drift_evaluable=false 把「没做对账」与「对账结果为零」区分开。
	//
	// 顺带说明：v2 侧（会话族）**仍然可用**，本次没有用它，是因为剩下的
	// 「v2 有几行」在「没有 v1」的前提下没有可比对象——报了反而像是在做比较。
	d.V1Retired = true
	d.V1WritesEnabled = currentV1WritesEnabled()
	// ZeroDriftEvaluable=false = 本次运行**没有验证任何漂移**；
	// ZeroDrift=false = 没有证据说漂移为零（两者不是一回事，见字段注释）。
	d.ZeroDriftEvaluable = false
	d.ZeroDrift = false
	return d, nil
}

// ★ 2026-10-20（迁移 890）：原先那三个 NULL↔0 / NULL↔false 归一化助手
// （normI / normF / normB）随漂移计数一起删除——它们唯一的用途是把
// 「两侧字段是否相等」判出来，而那个判断已经不存在了。
// 留着它们会让下一个读代码的人以为对账还在跑。
