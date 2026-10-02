// bg/auto_route_settle_worker.go — settle auto-route selections into rewards.
//
// Ref: docs/AUTO_ROUTE_FEEDBACK_OPTIMIZATION.md §2.3
//
// auto_route_selections rows are written at decision time, when the outcome is
// not yet known. This worker fills in the outcome and computes the reward that
// the affinity rollup then learns from.
//
// Per sweep (every 5 minutes):
//  1. Compute per-task-type COHORT baselines (p95 latency, p75 cost) across all
//     models. Cohort, not per-model: scoring a model against its own p95 always
//     yields ~neutral and cannot separate a fast model from a slow one, which
//     was the latent defect in the existing tuning_signals path (D2).
//  2. Claim a batch of unsettled rows older than the settle delay.
//  3. Join request_logs for success / latency / cost.
//  4. Where the session has settled and this model served >=80% of it, fold in
//     a routing-only session health component (see admin.RoutingHealthConfig:
//     compliance/injection/PII/toxicity/abandonment are excluded because they
//     are properties of the traffic, not of the model).
//  5. Compute reward and stamp settled_at.
//
// Rows whose request never appears in request_logs (dropped telemetry, or a
// request that died before logging) are stamped settled with a NULL reward
// after the abandon window, so the unsettled backlog cannot grow without bound.

package bg

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/admin/distlock"
	"github.com/kaixuan/llm-gateway-go/autoroute"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// settleInterval is the sweep cadence.
	settleInterval = 5 * time.Minute

	// settleDelay is how long to wait after the decision before settling, so
	// request_logs has been written and the row is not settled prematurely.
	settleDelay = 2 * time.Minute

	// settleBatchSize bounds one sweep.
	settleBatchSize = 500

	// settleAbandonAfter is when a row with no matching request_log is given up
	// on and stamped settled with a NULL reward. It MUST stay comfortably inside
	// the hot-table retention (lifecycle.hot_retention_hours, default 8h): the
	// "UPDATE/DELETE only in hot" partition invariant forbids writing promoted
	// rows, so every selection has to reach a terminal state (rewarded or
	// abandoned) before the hourly promote moves it to the partition parent.
	// 4h leaves two promote cycles of margin for a missed sweep.
	settleAbandonAfter = 4 * time.Hour

	// baselineWindow is the lookback for cohort baselines.
	baselineWindow = 24 * time.Hour

	// settleDistLockTTL bounds how long the Redis-elected leader holds the
	// settle token (R31 audit §四#1). Must exceed the sweep timeout (4m) so a
	// slow-but-alive sweep never loses its lease mid-cycle; distlock
	// auto-renews at ttl/3 while the process is alive, so this is really just
	// the crash-recovery bound (dead leader → token free within TTL).
	settleDistLockTTL = 6 * time.Minute
)

var (
	autoRouteSettledTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settled_total",
			Help: "Auto-route selections settled, by outcome. R42 caliber note: synthetic-actor selections (goal-*/loopback) are stamped settled with reward=NULL in the DB but deliberately NOT counted under outcome=abandoned here — sweep-log abandoned counts exceed this metric by design",
		},
		[]string{"outcome"}, // rewarded / abandoned
	)

	autoRouteSettleLagSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "llmgw_autoroute_settle_lag_seconds",
			Help:    "Seconds between the routing decision and its settlement",
			Buckets: []float64{60, 120, 300, 600, 1800, 3600, 21600, 86400},
		},
	)

	autoRouteRewardScore = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "llmgw_autoroute_reward",
			Help:    "Distribution of routing-attributed reward",
			Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0},
		},
		[]string{"task_type"},
	)

	// autoRouteSettleRetryState 暴露 retry 信号的三态（审计 §9.41/§9.42）。
	//
	// 为什么必须有它：retry 项占 reward 权重 0.10，而「没测到」在旧实现里
	// 等价于「测到完美」。没有这个计数器，运维从 reward 分布上**看不出**
	// 有多少样本的 retry 项是中性 0.5、多少是实测值——只能看到分布整体偏移。
	//
	// 标签是闭集三值，基数恒定（GW-00）。
	autoRouteSettleRetryState = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "llmgw_autoroute_settle_retry_state_total",
			Help: "Settled selections by retry-signal state: measured (retry term from real data) | unmeasured (session had no countable rows) | unavailable (LATERAL produced nothing).",
		},
		[]string{"state"},
	)
)

// retry 信号的三态取值（闭集，与 autoRouteSettleRetryState 的 state 标签对齐）。
const (
	retryStateMeasured    = "measured"    // model_reqs > 0 ⇒ retryScore = 1 - ratio
	retryStateUnmeasured  = "unmeasured"  // model_reqs == 0 ⇒ retryScore = 0.5 中性
	retryStateUnavailable = "unavailable" // LATERAL 无产出 ⇒ retryScore = 0.5 中性
)

// taskBaseline is the per-task-type cohort baseline used to normalise latency
// and cost into comparable [0,1] scores.
type taskBaseline struct {
	P95LatencyMs int
	P75CostUSD   float64
}

// AutoRouteSettleWorker backfills outcomes and rewards.
type AutoRouteSettleWorker struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}

	// MEDIUM-4 fix: guard against Start() never being called (Stop would block
	// forever on a never-closed done channel) and against double Start() (would
	// launch a second run() that double-closes done). Mirrors the pattern in
	// bg/tuning_store_refresher.go which fixed this same regression in
	// 2026-07-27.
	stopOnce sync.Once
	started  atomic.Bool

	// distLock is the optional Redis-backed leader election manager (R31
	// audit §四#1). Nil (or Enabled()==false) makes every instance sweep
	// exactly as before this change.
	distLock distlock.Manager
}

// NewAutoRouteSettleWorker constructs the worker.
func NewAutoRouteSettleWorker(db *pgxpool.Pool) *AutoRouteSettleWorker {
	return &AutoRouteSettleWorker{db: db, done: make(chan struct{})}
}

// SetDistLock wires the Redis-backed distributed lock manager used for
// cross-instance sweep dedup (token-bucket leader election, R31 audit §四#1).
// Optional: when never called, or called with a manager whose Enabled() is
// false, every instance sweeps exactly as before. MUST be called before
// Start(): the field is read unsynchronized by the sweep goroutine (R34
// 2026-09-17 audit — the previous "safe after Start" wording promised a
// happens-before edge that does not exist).
func (w *AutoRouteSettleWorker) SetDistLock(mgr distlock.Manager) {
	w.distLock = mgr
}

// Start launches the sweep loop. Returns immediately. Idempotent: a second
// call is a no-op.
func (w *AutoRouteSettleWorker) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
	slog.Info("auto-route settle worker started", "interval", settleInterval.String())
}

// Stop cancels and waits for the loop to exit. Safe on a never-Started worker
// and safe to call twice.
func (w *AutoRouteSettleWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if !w.started.Load() {
		// run() never launched, so done is never closed.
		return
	}
	<-w.done
}

func (w *AutoRouteSettleWorker) run(ctx context.Context) {
	// Panic guard (audit 2026-09-05 G-#1): a sweep panic must not kill the
	// process; it would also skip close(w.done) below and hang Stop forever.
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("auto-route settle worker panic", "recover", rec)
		}
	}()
	defer close(w.done)

	ticker := time.NewTicker(settleInterval)
	defer ticker.Stop()

	// Stagger the first sweep so boot is not contended by every worker at once.
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
		w.safeSweep(ctx)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.safeSweep(ctx)
		}
	}
}

// safeSweep wraps one sweep cycle in a per-cycle panic guard (R36 2026-09-17
// audit, closes R34 遗留#6): the outer run() recover only prevents a process
// kill — a single panicking sweep still unwound the goroutine and left the
// worker permanently dead in-process. Recovering HERE keeps the ticker loop
// alive; the run() recover stays as the last-resort net for the loop body.
func (w *AutoRouteSettleWorker) safeSweep(ctx context.Context) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("auto-route settle sweep panic (cycle skipped, worker alive)", "recover", rec)
		}
	}()
	w.sweep(ctx)
}

func (w *AutoRouteSettleWorker) sweep(ctx context.Context) {
	if w.db == nil {
		return
	}
	sweepCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()

	// R31 (audit §四#1): token-bucket cross-instance dedup — one leader per
	// tick redeems the token, followers skip without queueing. Without Redis
	// both instances sweep as before (the harm was doubled batch JOINs and
	// counters counted once per instance, not correctness).
	if h := acquireSweepDistLock(sweepCtx, w.distLock, "auto_route_settle", settleDistLockTTL, "auto_route_settle"); h != nil {
		defer h.Release(context.WithoutCancel(sweepCtx))
		if !h.IsLeader() {
			slog.Info("auto-route settle skipped, redis token held by another instance")
			return
		}
	}

	src := currentSettleSource()
	baselines, cohortRows, err := w.loadTaskBaselines(sweepCtx)
	autoRouteSettleBaselineCohortRows.WithLabelValues(src.Family).Set(float64(cohortRows))
	if err != nil {
		// Without baselines latency/cost fall back to neutral rather than being
		// scored wrongly, so this is a degradation, not a failure.
		slog.Warn("auto-route settle: cohort baselines unavailable, using neutral", "error", err)
		baselines = map[string]taskBaseline{}
	}
	// §9.44: an empty map is NOT an error, so the branch above never fires for
	// the state that actually matters. Under stop-write the session family can
	// carry zero is_auto_request rows in the baseline window (measured: 2178
	// rows on the v1 side, 0 on the session side over the same 24h) — and then
	// every reward silently scores latency and cost as neutral 0.5, i.e. the
	// cohort stops distinguishing fast from slow models with no error anywhere.
	// Say it out loud once per sweep instead.
	if len(baselines) == 0 {
		slog.Warn("auto-route settle: cohort baselines EMPTY (every reward scores latency+cost as neutral 0.5)",
			"family", src.Family, "turns_table", src.TurnsTable, "cohort_rows", cohortRows)
	}

	settled, abandoned, err := w.settleBatch(sweepCtx, baselines)
	if err != nil {
		slog.Warn("auto-route settle sweep failed", "error", err)
		return
	}
	if settled > 0 || abandoned > 0 {
		slog.Info("auto-route selections settled",
			"rewarded", settled, "abandoned", abandoned)
	}
}

// Hot-only settlement (audit 2026-09-05 D-#1): the settle UPDATE used to
// follow rows that the 8h promote had already moved to the partition parent
// (settleAbandonAfter was 24h), so writeReward/abandon wrote the PARENT —
// violating the "update/delete only in hot" partition invariant. The
// abandon window now fits inside hot retention (see settleAbandonAfter) and
// this worker only ever reads AND writes auto_route_selections_hot.
//
// All three queries here read request_logs_hot ONLY — DO NOT add
// `UNION ALL request_logs`. The request_logs partitions use the citus
// columnar access method, and a UNION ALL containing the partitioned PARENT
// fails at plan time with:
//     ERROR: invalid perminfoindex 0 in RTE with relid 0
// (PG 17.10 / citus 13.3, verified 2026-08-11; leaf partitions are fine, the
// parent is not). Hot-only is sufficient: this worker never looks further
// back than settleAbandonAfter (4h) and baselineWindow (24h, effectively
// capped by the 8h hot retention). See docs/HANDOFF_478_CRITICAL_FIXES.md
// CRITICAL-1.
//
//   - loadTaskBaselines: percentiles of latency/cost over recent hot rows.
//     Percentiles do NOT merge across separate tables; one statement is correct.
//   - settleBatch: outcome join (LEFT JOIN ... ON rl.request_id = ...).
//     All settled rows are still inside the hot window.
//   - settleBatch LATERAL: count + retry_count over the session.
//
// If a future change ever needs to look past hot retention through _hot, the
// answer is not to reintroduce this union. Either run two queries and merge
// in Go (lookup-style merge is safe; percentile merge is not), or use
// LATERAL per partition explicitly.
//
// # S4 停写后的数据源：两个直觉替代方案都被实测否决（审计 §9.40）
//
// 停写后本 worker 会**全量 abandon**（三个读点全部落空 → p.success == nil →
// 过 settleAbandonAfter 即盖 settled_at、reward 留 NULL，产物与「正常放弃」
// 逐字段同形）。§9.35 给三个响应加了 `outcome_source` 陈旧基线标记让它可见，
// 但**可见 ≠ 正确**。以下是 2026-10-02 真库实测的移植评估，动手前先读这段：
//
// **(a) 不能改成读 710 视图 `request_logs_with_current_month`。**
// 它不是 drop-in。真库 EXPLAIN 实测：settleBatch 这条 LEFT JOIN 的计划里，
// 该视图的 v1 臂（citus 父表 request_logs）被展开成 **7 个叶子分区 Seq Scan**
// （request_logs_2026_07 … request_logs_default）。本 worker 每 30 秒跑一次、
// 每次 100 行，扛不住。
// 注意：上面那段注释描述的 `invalid perminfoindex` 报错在这条查询上**没有复现**
// —— 实测到的不是报错，而是**更坏的东西：计划**。一个「没报错但计划烂掉」的
// 替代方案比报错那个更危险，因为它更容易被接受。
//
// **(b) 不能简单改成读会话族——它不是等价替换。**
// 真库 2026-09 分区实测（1747 条 auto_route_selections）：
//   · 99.3% 的 request_id 在会话臂有对应行；
//   · success / latency_ms 100% 有值；cost_usd 会话臂**反而更好**（100% vs v1 3.6%）；
//   · session 身份可平移：会话臂 `session_id` 100% 有值，710 视图的会话臂就是
//     `CASE WHEN t.session_id ~~ 'sys:%' THEN NULL ELSE t.session_id END AS gw_session_id`；
//   · **但 `canonical_id`：v1 32.3% 有值，会话臂 1.9%，且会话族里根本没有这一列**
//     （session_turns / session_turn_details 都没有）⇒ **settleBatch 的 LATERAL
//     retry_count 腿无法平移**。这是架构缺口，不是工程问题。
//   · **且 `is_auto_request`：v1 99.9% vs 会话臂 83.0%** ⇒ loadTaskBaselines 的
//     cohort 会缩水约 17%，p95/p75 基线随之改变。
//
// 顺带更正一个可以验证的细节：`origin_actor` 在**两侧都是 0**（v1 0/1746、
// 会话臂 0/1734），所以 `SQLExcludeSyntheticActors` 对这批行**早就空转**——
// 它既不是移植引入的新问题，也不构成阻止移植的理由；但「排除合成流量」这个
// 假设在本 worker 上**已经不成立**，应当单独记账。
//
// 结论：正确的移植必须先决定 `canonical_id` 缺口怎么办（回填会话侧，还是改写
// retry_count 的定义），而那会改动 **reward 语义**。在此之前不要动这三个读点。
// 由 TestAutoRouteSettleWorkerDoesNotUseThe710View 与
// TestAutoRouteSettleWorkerSourceConstraintIsDocumented 钉住本段结论。

// loadTaskBaselines computes cohort p95 latency and p75 cost per task type over
// the recent window, across every model.
//
// These are the baselines the reward function normalises against. Using the
// cohort (rather than each model's own history) is what lets the reward
// distinguish a fast model from a slow one — a per-model baseline would score
// every model ~neutral against its own history.
//
// The second return value is the number of rows that actually formed the
// cohort. §9.44: an **empty** baseline map is not an error, so the caller
// cannot tell "no traffic" from "query failed" — and an empty map makes every
// reward fall back to neutral 0.5 for latency AND cost (see
// ComputeRoutingRewardWithWeights), which silently stops distinguishing fast
// from slow models. The count is what makes that state measurable; see
// autoRouteSettleBaselineCohortRows.
func (w *AutoRouteSettleWorker) loadTaskBaselines(ctx context.Context) (map[string]taskBaseline, int, error) {
	// GROUP BY task_type yields a NULL group for rows with NULL task_type
	// (request_logs_hot.task_type is nullable). pgx v5 cannot scan NULL into
	// string and a scan failure is iteration-fatal, so the whole baseline
	// map would be lost; COALESCE keeps the group scannable and the
	// taskType != "" skip below drops it (245 2026-09-16 audit).
	//
	// count(*) rides along in the same query rather than in a second round
	// trip: the sweep runs every settleInterval and a second query would be a
	// per-sweep extra RTT purely to produce a number.
	src := currentSettleSource()
	rows, err := w.db.Query(ctx, settleBaselinesSQL(src), baselineWindow.String())
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make(map[string]taskBaseline)
	cohortRows := 0
	for rows.Next() {
		var (
			taskType string
			b        taskBaseline
			n        int
		)
		// Scan failures in pgx v5 are FATAL to iteration (rows.fatal sets the
		// error and stops yielding rows). A `continue` here would silently drop
		// every remaining row. Treat any scan error as the sweep-fatal condition
		// it actually is and let rows.Err() report it.
		if err := rows.Scan(&taskType, &b.P95LatencyMs, &b.P75CostUSD, &n); err != nil {
			return nil, 0, err
		}
		cohortRows += n
		if taskType != "" {
			out[taskType] = b
		}
	}
	return out, cohortRows, rows.Err()
}

// pendingSelection is one row awaiting settlement, joined with its outcome.
type pendingSelection struct {
	id            int64
	partitionDate time.Time
	requestID     string
	taskType      string
	canonicalID   *int64 // as written at decision time; nil on cache-reuse path
	ts            time.Time

	// From request_logs_hot; nil when the request was never logged.
	success     *bool
	latencyMs   *int
	costUSD     *float64
	originActor *string // R38: filter synthetic actors (goal-*/auto-*-generator/session-summary)

	// rlCanonicalID / rlTenantID are the values the settle worker backfills
	// onto auto_route_selections.canonical_id / tenant_id when the decision-time
	// value is missing. Both are nullable in request_logs_hot (canonical_id) and
	// left NULL by some legacy inserts (tenant_id).
	rlCanonicalID *int64
	rlTenantID    *string

	// Session attribution inputs.
	sessionHealth  *int // session_summaries.health_score, NULL until computed
	sessionErrors  *int
	sessionReqs    *int
	modelReqsInSes *int
	retryCount     *int
}

// retryCountPerRowSQL 返回「这一行贡献了多少次重试」的 SQL 表达式（≥0）。
//
// # 这里曾经有一个活的运行时错误（2026-10-02 审计 §9.41）
//
// 旧表达式是：
//
//	GREATEST(COALESCE(jsonb_array_length(r2.routing_attempts), 1) - 1, 0)
//
// 它假设 `routing_attempts` 是 JSON **数组**。它不是。写方
// executors.RoutingAttemptsTracker.ToJSONBytes 产出的是
//
//	{"attempts": [ ... ]}
//
// —— 一个**对象**，数组包在 `attempts` 键下。于是
// `jsonb_array_length` 对每一行非空值都抛
// `ERROR: cannot get array length of a non-array`（PG 17.10 真库实测）。
//
// **失败范围不是一行，是整条查询**：LATERAL 在 settleBatch 的主查询里，
// 一行抛错就中止整条语句 ⇒ 那一批最多 settleBatchSize(500) 条 selection
// 全部不结算。调用方只 slog.Warn 后 return，而下一轮重查的是**同一批**
// （ORDER BY ts LIMIT N WHERE settled_at IS NULL）⇒ 反复卡在同一处。
//
// 真库实测影响面（2026-09 分区，1747 条 selection）：564 条有 canonical_id
// 的**全部**能匹配到行，其中 **460 条（81.6%）** 的匹配行带 routing_attempts
// ⇒ 约 26% 的 selection 会毒化它所在的整个批次。
//
// 注意 executors/routing_tracker.go 的注释里也写着
// `retry_count = jsonb_array_length(routing_attempts) - 1`——**那份注释与
// 写方自己的输出矛盾**，是同一个错误的第二处化身，已一并订正。
//
// # 为什么 LATERAL 不再按 canonical_id 收窄（2026-10-02 实测后移除）
//
// 原来还有两条：
//
//	AND s.canonical_id IS NOT NULL AND r2.canonical_id = s.canonical_id
//
// 意图是「只数**这个模型**的请求」，避免一个模型继承同会话里别的模型的失败。
// 真库 10 天 auto 流量实测（11,634 个会话）把这个意图的性价比量清了：
//
//	单请求会话            11,299  97.12%
//	多请求·同模型           334   2.87%
//	多请求·**跨模型**        1   **0.01%**
//
// 也就是说：保留该条件，**67.7%** 的 selection 因 `canonical_id IS NULL` 而
// LATERAL 恒空 → `model_reqs = 0` → retry 信号**根本没测到**；而它防住的
// 跨模型污染只占 **0.01%**。**代价 67.7%，收益 0.01%。**
//
// 移除后实测：1183 条无 canonical_id 的 selection 里 **1182 条**能匹配到行，
// 平均请求数 1.00（会话本就只有 1 个请求，不存在「混进别的模型」）。
//
// 残余风险要说清：本数据集被探针流量主导（探针天然单请求会话），因此
// 「跨模型只占 0.01%」这个结论对**当前流量形态**成立。若日后 auto 流量中
// 多轮对话占比大幅上升，这个比例会变，条件可能需要以别的形式加回来——
// 那时应当用 RetryMeasured（见下）区分「测到 0」与「没测到」，而不是靠
// 匹配不上来隐式表达。
//
// # retry 信号的三态
//
// LATERAL 命中与否不再隐式决定 retry 项。`computeSelectionReward` 显式产出：
//
//	measured     —— model_reqs > 0，retryScore = 1 - retry/model_reqs
//	unmeasured   —— model_reqs == 0（会话确实没有可数的行）⇒ retryScore = 0.5 中性
//	unavailable  —— retryCount / modelReqsInSes 为 NULL（LATERAL 没能产出）⇒ 0.5 中性
//
// 三者里只有 measured 参与区分，其余一律落回 0.5。**旧实现把 unmeasured 当成
// 「测到 0 次重试」⇒ retryScore = 1.0 满分**，白拿 0.10 权重（审计 §9.41）。
//
// 状态经 `llmgw_autoroute_settle_retry_state_total{state}` 暴露。
// 之所以走指标而不是 reward_source 列：那张表有 CHECK 约束
// `reward_source IN ('request','session')`（db/db.go），扩展取值需要迁移。
//
// # 为什么 retryCountPerRowSQL 同时保留 'array' 分支
//
// `array` 形态在真库 340,917 行里**从未出现过**（最早 2026-09-03 即为 object），
// 所以这一支是防御性的、当前走不到。留着它是因为删掉它会让「万一历史数据或
// 未来写方改回数组」的假想情形直接退化成 0 重试（静默错值），而多一个
// CASE 分支的代价是零。

// 外层 `WHERE jsonb_typeof(...) = 'array'` 是必需的：`- > 'attempts'` 在
// `{"attempts": "x"}` 这种畸形值上会返回非数组，再调 jsonb_array_length 依旧抛错。
// 有了它，最坏情况退化成「这行算 0 次重试」而不是「整条查询中止」。
func retryCountPerRowSQL(alias string) string {
	col := "routing_attempts"
	if alias != "" {
		col = alias + ".routing_attempts"
	}
	return `GREATEST(COALESCE((
	    SELECT jsonb_array_length(v.a) FROM (VALUES (
	        CASE jsonb_typeof(` + col + `)
	            WHEN 'array'  THEN ` + col + `
	            WHEN 'object' THEN ` + col + ` -> 'attempts'
	        END)) AS v(a)
	    WHERE jsonb_typeof(v.a) = 'array'
	), 1) - 1, 0)`
}

func (w *AutoRouteSettleWorker) settleBatch(
	ctx context.Context,
	baselines map[string]taskBaseline,
) (settled, abandoned int, err error) {
	// One query gathers the row, its request outcome, and the session context
	// needed for attribution. LEFT JOINs throughout: a missing session or a
	// not-yet-computed health score must not hide the row.
	//
	// Outcome comes from request_logs_hot only: both the 2-minute settle lag and
	// the 4h abandon horizon sit inside the hot window, and the columnar parent
	// of request_logs cannot participate in a set operation (see the file-level
	// requestLogSource comment + docs/HANDOFF_478_CRITICAL_FIXES.md).
	//
	// retry_count is the count of routing_attempts entries beyond the first —
	// i.e. actual credential-level failovers for THIS request. It is NOT
	// "calls to this model minus one": a normal multi-turn session making N
	// sequential calls is N requests, not N-1 retries. Earlier code made that
	// mistake and penalised healthy conversation flows.
	src := currentSettleSource()
	rows, qErr := w.db.Query(ctx, settlePendingSQL(src), settleDelay.String(), settleBatchSize)
	if qErr != nil {
		return 0, 0, qErr
	}
	defer rows.Close()

	pending := make([]pendingSelection, 0, settleBatchSize)
	for rows.Next() {
		var p pendingSelection
		// Scan errors in pgx v5 terminate iteration. Treat as sweep-fatal.
		if scanErr := rows.Scan(
			&p.id, &p.partitionDate, &p.requestID, &p.taskType, &p.canonicalID, &p.ts,
			&p.success, &p.latencyMs, &p.costUSD,
			&p.originActor,
			&p.rlCanonicalID, &p.rlTenantID,
			&p.sessionHealth, &p.sessionErrors, &p.sessionReqs,
			&p.modelReqsInSes, &p.retryCount,
		); scanErr != nil {
			return 0, 0, scanErr
		}
		pending = append(pending, p)
	}
	if rErr := rows.Err(); rErr != nil {
		return 0, 0, rErr
	}

	now := time.Now()
	for _, p := range pending {
		if p.success == nil {
			// No request_log yet. Give it time, then abandon so the backlog
			// (and the idx_ars_unsettled index) cannot grow forever.
			if now.Sub(p.ts) > settleAbandonAfter {
				if aErr := w.abandon(ctx, p); aErr == nil {
					abandoned++
					autoRouteSettledTotal.WithLabelValues("abandoned").Inc()
				}
			}
			continue
		}

		// R38: skip synthetic actors (goal shadow rounds + internal loopbacks).
		// These are real auto DECISIONS but not user-driven traffic. Filtering
		// here (after the LEFT JOIN) preserves the abandon path for rows whose
		// request_logs have not yet been written.
		if p.originActor != nil && autoroute.IsSyntheticActor(*p.originActor) {
			// Stamp settled with NULL reward so the row leaves the unsettled
			// index and does not block future batches. No metrics increment —
			// synthetic rounds are excluded from the training/observation surface.
			if aErr := w.abandon(ctx, p); aErr == nil {
				abandoned++
			}
			continue
		}

		// §9.44: a miss here is not an error — it silently scores the latency and
		// cost terms as neutral 0.5, which is indistinguishable downstream from
		// "measured, and it came out neutral". Count it so the cohort's failure
		// to discriminate fast from slow models is visible as a rate.
		base, hasBase := baselines[p.taskType]
		family := currentSettleSource().Family
		if !hasBase || base.P95LatencyMs <= 0 {
			autoRouteSettleBaselineNeutral.WithLabelValues(baselineTermLatency, family).Inc()
		}
		if !hasBase || base.P75CostUSD <= 0 {
			autoRouteSettleBaselineNeutral.WithLabelValues(baselineTermCost, family).Inc()
		}

		reward, source, retryState := computeSelectionReward(p, base)
		if uErr := w.writeReward(ctx, p, reward, source); uErr != nil {
			slog.Debug("auto-route settle write failed", "request_id", p.requestID, "error", uErr)
			continue
		}
		settled++
		autoRouteSettledTotal.WithLabelValues("rewarded").Inc()
		autoRouteSettleLagSeconds.Observe(now.Sub(p.ts).Seconds())
		autoRouteRewardScore.WithLabelValues(p.taskType).Observe(reward)
		autoRouteSettleRetryState.WithLabelValues(retryState).Inc()
		autoRouteSettleSource.WithLabelValues(family).Inc()
	}

	return settled, abandoned, nil
}

// computeSelectionReward turns a settled row into a reward in [0,1].
//
// Returns the reward, its attribution source ("session" when session-level
// health was folded in, "request" when only per-request signals were
// available), and the retry signal's tri-state (see retryState* constants).
//
// The tri-state exists because the retry term is 0.10 of the reward and
// "unmeasured" used to score as 1.0 — the maximum — so two thirds of settled
// selections collected a full retry bonus for a signal nobody measured
// (audit §9.41). Only `measured` now differentiates; the other two fall back
// to neutral 0.5.
func computeSelectionReward(p pendingSelection, base taskBaseline) (float64, string, string) {
	in := autoroute.RewardInput{
		HealthComponent: -1, // -1 => neutral; overridden below when attributable
		RetryMeasured:   false,
	}

	if p.success != nil && *p.success {
		in.Success = 1.0
	}
	if p.latencyMs != nil {
		in.LatencyMs = *p.latencyMs
	}
	if p.costUSD != nil {
		in.CostUSD = *p.costUSD
	}
	in.P95BaselineMs = base.P95LatencyMs
	in.P75BaselineCost = base.P75CostUSD

	// Retry tri-state. Order matters: a nil pointer means the LATERAL produced
	// nothing at all (unavailable), which is a different failure from "the
	// session genuinely had no countable rows" (unmeasured) — and under
	// stop-write it is the state the worker will be in permanently.
	retryState := retryStateUnavailable
	switch {
	case p.modelReqsInSes == nil || p.retryCount == nil:
		retryState = retryStateUnavailable
	case *p.modelReqsInSes > 0:
		in.RetryRatio = float64(*p.retryCount) / float64(*p.modelReqsInSes)
		in.RetryMeasured = true
		retryState = retryStateMeasured
	default:
		retryState = retryStateUnmeasured
	}

	source := "request"
	// Attribute session health only when the session has settled AND this model
	// carried the clear majority of it. Otherwise a model that served one call
	// of twenty would inherit other models' failures.
	if p.sessionHealth != nil && p.modelReqsInSes != nil && p.sessionReqs != nil &&
		autoroute.ShouldAttributeSession(*p.modelReqsInSes, *p.sessionReqs) {
		in.HealthComponent = routingOnlyHealth(*p.sessionHealth, p)
		source = "session"
	}

	return autoroute.ComputeRoutingReward(in), source, retryState
}

// routingOnlyHealth converts session health into the [0,1] component the reward
// may use.
//
// session_summaries.health_score is 0-100 but includes penalties a model cannot
// be blamed for (abandonment, compliance, injection, PII, toxicity). Rather than
// reverse-engineering those out of the stored total — the individual penalty
// items are not persisted — this recomputes a routing-only view from the
// error-rate signal that IS available, and uses the stored score only as a
// sanity ceiling.
func routingOnlyHealth(storedHealth int, p pendingSelection) float64 {
	// Error rate is the routing-relevant part we can reconstruct exactly.
	errRate := 0.0
	if p.sessionErrors != nil && p.sessionReqs != nil && *p.sessionReqs > 0 {
		errRate = float64(*p.sessionErrors) / float64(*p.sessionReqs)
	}
	routing := 1.0 - errRate

	// The stored score is a ceiling: if the overall session went badly for
	// reasons we are excluding, do not credit the model with a perfect score.
	ceiling := float64(storedHealth) / 100.0
	if ceiling < 0 {
		ceiling = 0
	}
	if ceiling > 1 {
		ceiling = 1
	}
	// Average of the two: reflects routing quality while staying anchored to
	// the session's actual observed outcome.
	return (routing + ceiling) / 2.0
}

func (w *AutoRouteSettleWorker) writeReward(
	ctx context.Context,
	p pendingSelection,
	reward float64,
	source string,
) error {
	// COALESCE preserves any decision-time value (future Fix D path), so a
	// later write from a non-null decision never gets overwritten by a NULL
	// from request_logs_hot (e.g. legacy rows written before canonical_id was
	// a populated column).
	//
	// Hot-only by partition invariant (audit 2026-09-05 D-#1): settlement
	// completes inside the hot window (settleAbandonAfter < hot retention), so
	// the promoted partition parent is never written.
	_, err := w.db.Exec(ctx, `
		UPDATE auto_route_selections_hot
		SET success       = $1,
		    latency_ms    = $2,
		    cost_usd      = $3,
		    reward        = $4,
		    reward_source = $5,
		    canonical_id  = COALESCE(canonical_id, $6),
		    tenant_id     = COALESCE(tenant_id,    $7),
		    settled_at    = NOW()
		WHERE id = $8 AND partition_date = $9
		  AND settled_at IS NULL
	`, p.success, p.latencyMs, p.costUSD, reward, source,
		p.rlCanonicalID, p.rlTenantID, p.id, p.partitionDate)
	return err
}

// abandon stamps a row settled with no reward, so it stops being scanned and is
// excluded from learning (the affinity rollup requires reward IS NOT NULL).
// Hot-only by partition invariant (see writeReward).
func (w *AutoRouteSettleWorker) abandon(ctx context.Context, p pendingSelection) error {
	_, err := w.db.Exec(ctx, `
		UPDATE auto_route_selections_hot
		SET settled_at = NOW(), reward_source = 'request'
		WHERE id = $1 AND partition_date = $2 AND settled_at IS NULL
	`, p.id, p.partitionDate)
	return err
}
