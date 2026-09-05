package bg

// consistency_worker.go — 2026-09-05 round2 审计 B-#2 接线：lite 跨介质
// 一致性周期对账 worker。
//
// 背景：storage.ReconcileTurnArtifacts / RepairTurnArtifacts 补偿原语此前
// 零生产调用方（B-#2），lite 跨介质不一致（崩溃孤儿 body / 丢 body）在
// 生产中永不检出。本 worker 以低频（默认每日）对「近期活跃且已空闲」的
// session 逐个跑 Reconcile：
//
//   - 默认 RepairReportOnly：结构化 slog 输出报告（consistent=false 的
//     会话汇总 Missing/Orphan 数），不动任何数据（恒安全）；
//   - 仅当配置显式开启 delete_orphans 时才执行 RepairDeleteOrphanBodies，
//     且天然享受 storage.RepairTurnArtifacts 的 TOCTOU 双保险（删除前
//     meta 复检 + mtime 宽限，G-#9）与 B-#6 的缺删除器报错语义；
//   - 空闲阈值（默认 10 分钟）是第一道护栏：lite 写序为 body 先落盘、
//     meta 后提交（cmd/gateway/lite_telemetry_sink.go），最后活动早于阈值
//     的会话不会再出现合法的在途写入窗口；
//   - 单轮会话数有上限（bounded，默认 500，最近活跃优先），防止首跑扫
//     全库；超出部分留待下一轮继续。
//
// 生命周期照抄 bg/bodies_trimmer.go：Start(ctx) 阻塞式（调用方 `go w.Start(ctx)`
// 启动），首跑延迟 initialDelay（默认 10 分钟，避开启动期资源争用），
// 之后按 interval 周期执行；ctx 取消后优雅退出并输出"已停止"。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// 一致性对账 worker 的默认参数（与 config.LiteConsistencyConfig 的
// ApplyLiteDefaults 默认值对齐；worker 侧再兜底一次，保证零值构造也安全）。
const (
	// defaultConsistencyInterval 对账周期：每日一次（低频）。
	defaultConsistencyInterval = 24 * time.Hour
	// defaultConsistencyIdleThreshold 空闲阈值：把在途写入挡在对账窗外。
	defaultConsistencyIdleThreshold = 10 * time.Minute
	// defaultConsistencyMaxSessions 单轮对账会话数上限（bounded）。
	defaultConsistencyMaxSessions = 500
	// defaultConsistencyInitialDelay 启动后首跑延迟，避开启动争资源。
	defaultConsistencyInitialDelay = 10 * time.Minute
)

// ConsistencyWorker 定期对空闲会话跑跨介质一致性对账（审计 B-#2）。
// 零值 action 即 storage.RepairReportOnly（默认安全）。
type ConsistencyWorker struct {
	sessions storage.IdleSessionLister // 空闲会话枚举（SQLiteSessionStore 实现）
	turns    storage.TurnsStore        // turn 元数据读端（Reconcile/复检）
	bodies   storage.BodiesStore       // body 文件侧（需实现 BodiesLister）
	action   storage.RepairAction      // 孤儿处理策略，默认 RepairReportOnly

	idleThreshold time.Duration // 空闲阈值，默认 10 分钟
	maxSessions   int           // 单轮会话数上限，默认 500
	interval      time.Duration // 对账周期，默认 24h
	initialDelay  time.Duration // 启动后首跑延迟，默认 10 分钟

	// 最近一次 RunOnce 的统计快照，供测试与监控读取。
	lastSessionsChecked int
	lastInconsistent    int
	lastOrphans         int
	lastMissing         int
	lastDeleted         int
}

// NewConsistencyWorker 构造一致性对账 worker：默认 report-only、每日一次、
// 空闲阈值 10 分钟、单轮上限 500、首跑延迟 10 分钟。各维度可用 With* 覆盖。
func NewConsistencyWorker(sessions storage.IdleSessionLister, turns storage.TurnsStore, bodies storage.BodiesStore) *ConsistencyWorker {
	return &ConsistencyWorker{
		sessions:      sessions,
		turns:         turns,
		bodies:        bodies,
		action:        storage.RepairReportOnly,
		idleThreshold: defaultConsistencyIdleThreshold,
		maxSessions:   defaultConsistencyMaxSessions,
		interval:      defaultConsistencyInterval,
		initialDelay:  defaultConsistencyInitialDelay,
	}
}

// WithAction 覆盖孤儿处理策略（默认 RepairReportOnly）。仅显式传入
// storage.RepairDeleteOrphanBodies 才会删数据。
func (w *ConsistencyWorker) WithAction(a storage.RepairAction) *ConsistencyWorker {
	w.action = a
	return w
}

// WithIdleThreshold 覆盖空闲阈值（>0 才生效），返回自身以便链式调用。
func (w *ConsistencyWorker) WithIdleThreshold(d time.Duration) *ConsistencyWorker {
	if d > 0 {
		w.idleThreshold = d
	}
	return w
}

// WithMaxSessions 覆盖单轮会话数上限（>0 才生效）。
func (w *ConsistencyWorker) WithMaxSessions(n int) *ConsistencyWorker {
	if n > 0 {
		w.maxSessions = n
	}
	return w
}

// WithInterval 覆盖对账周期（>0 才生效），与 BodiesTrimmer.WithInterval 同惯例。
func (w *ConsistencyWorker) WithInterval(d time.Duration) *ConsistencyWorker {
	if d > 0 {
		w.interval = d
	}
	return w
}

// WithInitialDelay 覆盖启动后首跑延迟（>0 才生效），主要供测试缩短等待。
func (w *ConsistencyWorker) WithInitialDelay(d time.Duration) *ConsistencyWorker {
	if d > 0 {
		w.initialDelay = d
	}
	return w
}

// 只读快照：供装配层测试与监控读取（字段本身保持不可变使用）。

// Action 返回孤儿处理策略。
func (w *ConsistencyWorker) Action() storage.RepairAction { return w.action }

// Interval 返回对账周期。
func (w *ConsistencyWorker) Interval() time.Duration { return w.interval }

// IdleThreshold 返回空闲阈值。
func (w *ConsistencyWorker) IdleThreshold() time.Duration { return w.idleThreshold }

// MaxSessions 返回单轮会话数上限。
func (w *ConsistencyWorker) MaxSessions() int { return w.maxSessions }

// Start 阻塞式运行对账循环：先等 initialDelay（默认 10 分钟，避开启动期
// 争资源），执行首轮，之后按 ticker 周期执行；ctx 取消时优雅退出。
// 设计为由调用方 `go w.Start(ctx)` 启动，内部不再另起新协程。
func (w *ConsistencyWorker) Start(ctx context.Context) {
	slog.Info("consistency worker 已启动",
		"action", w.action.String(),
		"interval", w.interval.String(),
		"idle_threshold", w.idleThreshold.String(),
		"max_sessions", w.maxSessions,
		"initial_delay", w.initialDelay.String())

	select {
	case <-ctx.Done():
		slog.Info("consistency_worker: 已停止（首跑前退出）")
		return
	case <-time.After(w.initialDelay):
	}

	if err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("consistency_worker: 首轮对账失败", "error", err)
	}

	tk := time.NewTicker(w.interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("consistency_worker: 已停止")
			return
		case <-tk.C:
			if err := w.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("consistency_worker: 对账失败", "error", err)
			}
		}
	}
}

// RunOnce 执行一轮对账（供测试与手动触发）：枚举空闲会话（最近活跃优先、
// 有界），逐会话 Reconcile；不一致会话输出结构化告警并按 action 处置孤儿。
// 枚举失败返回 error；单会话对账/修复失败只告警并继续其余会话（尽力而为）。
func (w *ConsistencyWorker) RunOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	idleBefore := time.Now().Add(-w.idleThreshold)
	sessions, err := w.sessions.ListIdleSessions(ctx, idleBefore, w.maxSessions)
	if err != nil {
		return fmt.Errorf("consistency_worker: 枚举空闲会话失败: %w", err)
	}

	var (
		inconsistent int
		orphans      int
		missing      int
		deleted      int
	)
	for _, sess := range sessions {
		if err := ctx.Err(); err != nil {
			return err
		}
		report, err := storage.ReconcileTurnArtifacts(ctx, sess.TenantID, sess.ID, w.bodies, w.turns)
		if err != nil {
			// 单会话失败（如 bodies 未实现 ListTurns）不拖垮整轮。
			slog.Warn("consistency_worker: 单会话对账失败，跳过",
				"tenant", sess.TenantID, "session", sess.ID, "error", err)
			continue
		}
		if report.Consistent {
			continue
		}
		inconsistent++
		orphans += len(report.OrphanBodies)
		missing += len(report.MissingBodies)
		slog.Warn("consistency_worker: 检出跨介质不一致",
			"tenant", report.TenantID,
			"session", report.SessionID,
			"missing_bodies", report.MissingBodies,
			"orphan_bodies", report.OrphanBodies,
			"turns_with_meta", len(report.TurnsWithMeta),
			"turns_with_body", len(report.TurnsWithBody))

		if w.action != storage.RepairDeleteOrphanBodies {
			continue // report-only：只报告不动数据（默认，恒安全）
		}
		res, err := storage.RepairTurnArtifacts(ctx, report, w.bodies, w.turns, storage.RepairDeleteOrphanBodies, nil)
		if err != nil {
			slog.Warn("consistency_worker: 孤儿修复失败",
				"tenant", report.TenantID, "session", report.SessionID, "error", err)
			continue
		}
		deleted += len(res.Deleted)
		if len(res.SkippedInFlight) > 0 || len(res.SkippedByGrace) > 0 {
			slog.Info("consistency_worker: 孤儿删除部分被安全护栏跳过",
				"tenant", report.TenantID,
				"session", report.SessionID,
				"skipped_in_flight", res.SkippedInFlight,
				"skipped_by_grace", res.SkippedByGrace)
		}
	}

	w.lastSessionsChecked = len(sessions)
	w.lastInconsistent = inconsistent
	w.lastOrphans = orphans
	w.lastMissing = missing
	w.lastDeleted = deleted

	if inconsistent > 0 || deleted > 0 {
		slog.Info("consistency_worker: 本轮对账完成",
			"sessions_checked", len(sessions),
			"max_sessions", w.maxSessions,
			"idle_before", idleBefore.Format(time.RFC3339),
			"inconsistent_sessions", inconsistent,
			"orphan_bodies", orphans,
			"missing_bodies", missing,
			"deleted_orphans", deleted,
			"action", w.action.String())
	}
	return nil
}
