// Package bg — Cost Reconciliation Worker (M3 CO-3, 2026-08-15)
//
// 激活 provider_cost_reconciliation（供应商成本对账，doc 19 §3 CO-3）：
//
//	_request_logs_hot + _request_logs --按 provider+月聚合--> 对账表
//	管理端导入供应商账单 ------------------------------> 对账表（provider_* 列）
//	差异率超阈值 -------------------------------------> provider_events 告警事件
//
// 集成方式与 ProfileAggregator 相同：initProviderProfile（cmd/gateway/
// provider_profile_init.go）在 provider_profile.enabled 与
// provider_profile.cost_reconciliation.enabled 同时开启时启动本 worker，
// 默认关闭（行为与 main 完全一致）。
package bg

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
)

// costReconciliationSlotName 是本 worker 在 public.llm_gateway_task_state
// 里的共享节流槽行键（迁移 840）。
//
// ★ 为什么需要（2026-10-17 实测，生产 172.16.2.210 共用）：
//
//	pg_stat_statements 里 `WITH deduped AS (SELECT DISTINCT ON (request_id) …)`
//	= **87 次 / 均值 129.2s / 最大 533.9s / 累计 187.3 分钟**（PG 启动仅 11.8 天）。
//
//	单次为什么这么贵：`usage_ledger_2026_09` 有 1,416,772 行，EXPLAIN 显示走
//	`usage_ledger_2026_09_request_id_idx` 并**逐行回表**（leading 列 request_id 的
//	索引不携带 payload，ts 范围过滤推不下去）。
//
//	而本 worker **原先没有任何跨实例协调**——没有 advisory 锁、没有节流槽、
//	没有租约。后果是三份重复：
//	  1. **多副本重复**：154 / 245 各跑一遍完全相同的 2 个月聚合（每 tick
//	     聚合当前月 + 上一月，CostReconciliationMonthsToAggregate），
//	     2 台 × 1 次/天 × 2 月 = 4 次/天，而 11.8 天实测 87 次 ≈ 7.4 次/天；
//	  2. **重启即补跑**：Start() 里 `w.reconcile(ctx)` 在 ticker 之前无条件执行，
//	     每次部署/重启都立刻触发一整轮 129s × 2 的重聚合；
//	  3. 两条副本的完成时刻不同，**对账表的刷新时刻也随实例数放大漂移**。
//
//	★ 本仓已有解决同类问题的**成型范式**，见 partition_manager.go 的
//	`analyzeThrottleTaskName`（迁移 840）：`claim_llm_gateway_task_slot` 是单语句
//	原子操作（INSERT … ON CONFLICT DO UPDATE … WHERE … RETURNING），
//	两个实例并发时只有一个能拿到 true。照搬，不另造机制。
//	锁与槽是**正交**的两件事：锁防「同时」，槽防「重复」——本 worker 缺的是
//	「重复」这一半。
//
// ★ 缺表（迁移 840 未应用）时**降级为照常执行**，不是跳过或报错：
//
//	这正是 840 的 .down.sql 回滚后的目标行为，与 partition_manager 一致。
const costReconciliationSlotName = "provider_cost_reconciliation"

// 每月的聚合超时（见 reconcile 里的 context.WithTimeout）。
const reconcileMonthTimeout = 10 * time.Minute

// worstCaseReconcileDuration 是一轮 reconcile 的最坏耗时：
// 聚合两个月，每月各自一个 reconcileMonthTimeout。
//
// 用途只有一个——判断「节流窗口是否短于一趟跑完」。短于它，对端可能在本趟
// 尚未结束时再认领成功，节流形同虚设。默认 interval 是 86400s，远大于此；
// 只有把 interval 配到 20 分钟以内才会触发那条 Warn。
//
// 这里**不**用它去抬升窗口：interval 是运维显式配置，擅自改写等于擅自降频。
// 正确做法是把风险**说出来**（Start() 的 Warn），让运维自己决定。
var worstCaseReconcileDuration = 2 * reconcileMonthTimeout

// CostReconciliationWorker 周期性聚合网关侧用量到对账表。
type CostReconciliationWorker struct {
	reconciler *providerprofile.CostReconciler
	interval   time.Duration
	cancel     context.CancelFunc
	done       chan struct{}

	// db 是共享节流槽用的连接池。**可为 nil** —— 此时本 worker 单实例运行，
	// 退化成节流前的老行为（每 tick 都聚合）。单测与任何不想引入跨实例
	// 语义的调用方都可以不传。
	db *pgxpool.Pool
}

// NewCostReconciliationWorkerFromReconciler 用已构造的 reconciler 创建
// worker（cmd/gateway 里 worker 与管理端账单导入共用同一实例）。
func NewCostReconciliationWorkerFromReconciler(reconciler *providerprofile.CostReconciler, interval time.Duration) *CostReconciliationWorker {
	return &CostReconciliationWorker{
		reconciler: reconciler,
		interval:   interval,
		done:       make(chan struct{}),
	}
}

// WithSharedThrottleDB 接入共享节流槽。必须在 Start() 之前调用。
//
// 为什么用 option 而不是加进构造函数：现有调用方与测试都用双参构造，
// 改签名会波及它们；而节流是**跨实例部署形态**的属性
// （只有多副本共享同一 PostgreSQL 时才有意义），由装配层显式接上更贴合语义。
func (w *CostReconciliationWorker) WithSharedThrottleDB(db *pgxpool.Pool) *CostReconciliationWorker {
	w.db = db
	return w
}

// Start begins the reconciliation loop. 启动时立即跑一次，使开启开关当天
// 就能查到当前月对账数据（与 ProfileAggregator 的首跑语义一致）。
func (w *CostReconciliationWorker) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	Go("cost_reconciliation_worker.run", func() { w.run(ctx) })
	slog.Info("provider cost reconciliation worker started",
		"interval", w.interval,
		"shared_throttle", w.db != nil,
		"throttle_window", w.throttleWindow())
	if w.db != nil && w.interval <= worstCaseReconcileDuration {
		// 不静默改写 interval：那是运维显式配的，改掉等于擅自降频。
		// 但「窗口短于一趟最坏耗时」会让对端在本趟未结束时就能再认领，
		// 节流形同虚设——这属于**必须被看见**的配置风险，不是错误。
		slog.Warn("provider cost reconciliation: interval is not longer than the worst-case "+
			"run duration, so the shared throttle window may expire mid-run and let a peer "+
			"start concurrently (costly, not corrupting — the upsert is keyed by provider+month)",
			"interval", w.interval,
			"worst_case_run", worstCaseReconcileDuration,
			"months_per_run", len(CostReconciliationMonthsToAggregate(time.Now())))
	}
}

// Stop gracefully stops the worker.
func (w *CostReconciliationWorker) Stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		slog.Info("provider cost reconciliation worker stopped")
	}
}

func (w *CostReconciliationWorker) run(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reconcile(ctx)
		}
	}
}

func (w *CostReconciliationWorker) reconcile(ctx context.Context) {
	if !w.claimSharedSlot(ctx) {
		return
	}
	for _, month := range CostReconciliationMonthsToAggregate(time.Now()) {
		reconCtx, cancel := context.WithTimeout(ctx, reconcileMonthTimeout)
		if err := w.reconciler.AggregateMonth(reconCtx, month); err != nil {
			slog.Error("provider cost reconciliation failed",
				"error", err, "month", month.Format("2006-01"))
		} else {
			slog.Info("provider cost reconciliation completed", "month", month.Format("2006-01"))
		}
		cancel()
	}
}

// claimSharedSlot 认领跨实例共享节流槽。返回 false 表示「本轮跳过」。
//
// 决策全部委托给 decideSharedSlotRun（本函数只负责与 DB 打交道），
// 便于在没有连接池的情况下对**降级腿**做单测——降级恰恰是最容易被
// 「顺手收紧」改坏、且改坏后没有任何告警的地方。
func (w *CostReconciliationWorker) claimSharedSlot(ctx context.Context) bool {
	if w.db == nil {
		// 单实例 / 单测：没有跨实例语义，直接跑。
		return decideSharedSlotRun(false, nil, false)
	}

	window := w.throttleWindow()
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var claimed bool
	err := w.db.QueryRow(claimCtx,
		"SELECT claim_llm_gateway_task_slot($1, $2::interval)",
		costReconciliationSlotName, window.String(),
	).Scan(&claimed)

	run := decideSharedSlotRun(claimed, err, true)
	switch {
	case err != nil && isUndefinedTable(err):
		slog.Warn("provider cost reconciliation: shared throttle table missing "+
			"(migration 840 not applied?) — running unthrottled",
			"task", costReconciliationSlotName)
	case err != nil:
		slog.Warn("provider cost reconciliation: slot claim failed — running unthrottled",
			"task", costReconciliationSlotName, "error", err)
	case !run:
		// ★ Info 而不是 Debug：数「实际执行次数」时看不到跳过，会让人把
		//   「没跑」与「被跳过」混为一谈（与 partition_manager 同理）。
		slog.Info("provider cost reconciliation skipped (peer holds shared throttle slot)",
			"task", costReconciliationSlotName, "window", window.String())
	}
	return run
}

// decideSharedSlotRun 是节流决策的**全部**规则，纯函数、无 IO：
//
//	hasDB=false            → 跑（单实例，没有跨实例语义）
//	err != nil             → 跑（降级；见下）
//	claimed=true           → 跑（认领到了）
//	claimed=false, err=nil → 跳过（对端在窗口内已认领）
//
// ★ 「err 就跑」是刻意的默认方向：节流是**省钱**措施，不是正确性措施。
//
//	把认领失败处理成跳过，会让迁移 840 未应用 / 连接抖动时对账**静默不跑**，
//	而对账表不再更新不会产生任何告警——停摆的表现只是「数字不再变新」。
//	宁可多花 129s×2，不可静默停摆。这与 840 的 .down.sql 回滚目标
//	（回到节流前、两台各跑一遍）也是同一个方向。
func decideSharedSlotRun(claimed bool, err error, hasDB bool) bool {
	if !hasDB || err != nil {
		return true
	}
	return claimed
}

// throttleWindow 是本次认领使用的窗口。
//
// 默认取 worker 自身的 interval —— 目标就是「整个集群每个间隔跑一次」。
// 非正间隔必须兜底：把它原样当 interval 传给 claim 会让窗口永不推进
// ⇒ **永远跳过**，那比多跑几遍危险得多。
func (w *CostReconciliationWorker) throttleWindow() time.Duration {
	if w.interval > 0 {
		return w.interval
	}
	return 24 * time.Hour
}

// CostReconciliationMonthsToAggregate 返回给定时刻应聚合的月份首日列表：
// 当前月（数据持续累计，需保持最新）+ 上一月（月初后补齐最终聚合）。
func CostReconciliationMonthsToAggregate(now time.Time) []time.Time {
	current := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	previous := current.AddDate(0, -1, 0)
	return []time.Time{current, previous}
}
