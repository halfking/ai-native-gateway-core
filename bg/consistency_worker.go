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
//     全库；超过上限时按轮转偏移（OFFSET）跨轮分页覆盖，N 个空闲会话在
//     ceil(N/500) 轮内全部进入对账窗，老会话不会被永久饿死
//     （2026-09-05 round2 复审 F1，替代旧实现"恒取最新一页"）。
//
// 生命周期照抄 bg/bodies_trimmer.go：Start(ctx) 阻塞式（调用方 `go w.Start(ctx)`
// 启动），首跑延迟 initialDelay（默认 10 分钟，避开启动期资源争用），
// 之后按 interval 周期执行；ctx 取消后优雅退出并输出"已停止"。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
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

// ConsistencyRunStats 是最近一轮对账的统计快照（LastRunStats 返回的值类型，
// 2026-09-05 round2 复审 F4：替代原先无锁、无 getter 的散装统计字段）。
type ConsistencyRunStats struct {
	// SessionsChecked 本轮枚举并检查的空闲会话数。
	SessionsChecked int
	// Inconsistent 检出跨介质不一致的会话数。
	Inconsistent int
	// Orphans 检出的孤儿 body 总数（有 body 无 meta）。
	Orphans int
	// Missing 检出的丢失 body 总数（有 meta 无 body，只报告不补偿）。
	Missing int
	// Deleted delete 模式下实际删除的孤儿 body 数。
	Deleted int
	// Vanished 删除前发现文件已自行消失的孤儿数（无须删除，计入报告）。
	Vanished int
}

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

	// runMu 保护下列运行期可变状态（2026-09-05 round2 复审 F4）：统计字段
	// 由 RunOnce 尾部写入、LastRunStats 读取；idleOffset 由 RunOnce 读写。
	// 生产路径 Start 串行调用 RunOnce 本无竞态，加锁是为了让导出的 RunOnce
	// 与监控读取（LastRunStats）并发调用时无数据竞争。
	runMu sync.Mutex

	// 最近一次 RunOnce 的统计快照，经 LastRunStats() 读取（供测试与监控）。
	lastSessionsChecked int
	lastInconsistent    int
	lastOrphans         int
	lastMissing         int
	lastDeleted         int
	lastVanished        int

	// idleOffset 是空闲会话枚举的轮转偏移（2026-09-05 round2 复审 F1）：
	// 单轮只取 maxSessions 条（updated_at 倒序、LIMIT/OFFSET 分页），每轮按
	// 返回条数前移；返回不足一页说明已覆盖到最老端，回绕 0 下轮从头再扫。
	// 这样 N 个空闲会话在 ceil(N/maxSessions) 轮内全部进入对账窗，消除
	// 「恒取最新一页导致老会话永久饿死」的方向性盲区。
	//
	// 语义注记：重启后偏移归零可接受（重复对账已看过的会话无害，Reconcile
	// 幂等只读）；轮间新会话进入/老会话被触碰导致的窗口漂移也可接受——本
	// 机制的目标是跨轮全覆盖，不保证单轮内的精确游标语义。
	idleOffset int
}

// NewConsistencyWorker 构造一致性对账 worker：默认 report-only、每日一次、
// 空闲阈值 10 分钟、单轮上限 500、首跑延迟 10 分钟。各维度可用 With* 覆盖。
// 任一依赖为 nil 时 panic fail-fast（跟随包内 NewGoalRunActionScheduler 的
// 非法入参惯例，2026-09-05 round2 复审 F9：把装配错误从运行期提前到启动期）。
func NewConsistencyWorker(sessions storage.IdleSessionLister, turns storage.TurnsStore, bodies storage.BodiesStore) *ConsistencyWorker {
	if sessions == nil {
		panic("bg: consistency worker requires non-nil idle session lister")
	}
	if turns == nil {
		panic("bg: consistency worker requires non-nil turns store")
	}
	if bodies == nil {
		panic("bg: consistency worker requires non-nil bodies store")
	}
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

// LastRunStats 返回最近一轮对账的统计快照（值拷贝，2026-09-05 round2 复审
// F4）：供监控读取；RunOnce 中途因 ctx 取消提前返回时不更新，保持上一轮值。
func (w *ConsistencyWorker) LastRunStats() ConsistencyRunStats {
	w.runMu.Lock()
	defer w.runMu.Unlock()
	return ConsistencyRunStats{
		SessionsChecked: w.lastSessionsChecked,
		Inconsistent:    w.lastInconsistent,
		Orphans:         w.lastOrphans,
		Missing:         w.lastMissing,
		Deleted:         w.lastDeleted,
		Vanished:        w.lastVanished,
	}
}

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

// RunOnce 执行一轮对账（供测试与手动触发）：按轮转偏移枚举空闲会话
// （最近活跃优先、有界分页，2026-09-05 round2 复审 F1），逐会话 Reconcile；
// 不一致会话输出结构化告警并按 action 处置孤儿。枚举失败返回 error；单会话
// 对账/修复失败只告警并继续其余会话（尽力而为）。
func (w *ConsistencyWorker) RunOnce(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	w.runMu.Lock()
	offset := w.idleOffset
	w.runMu.Unlock()

	idleBefore := time.Now().Add(-w.idleThreshold)
	sessions, err := w.sessions.ListIdleSessions(ctx, idleBefore, w.maxSessions, offset)
	if err != nil {
		return fmt.Errorf("consistency_worker: 枚举空闲会话失败: %w", err)
	}

	var (
		inconsistent int
		orphans      int
		missing      int
		deleted      int
		vanished     int
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
		vanished += len(res.Vanished)
		if len(res.SkippedInFlight) > 0 || len(res.SkippedByGrace) > 0 || len(res.Vanished) > 0 {
			slog.Info("consistency_worker: 孤儿删除部分被安全护栏跳过",
				"tenant", report.TenantID,
				"session", report.SessionID,
				"skipped_in_flight", res.SkippedInFlight,
				"skipped_by_grace", res.SkippedByGrace,
				"vanished", res.Vanished)
		}
	}

	// 轮转推进 + 统计快照同一把锁写入（F1/F4）：返回满一页说明可能还有更老
	// 会话，偏移前移；返回不足一页说明本轮已覆盖到最老端，回绕 0 下轮从头。
	w.runMu.Lock()
	if len(sessions) < w.maxSessions {
		w.idleOffset = 0
	} else {
		w.idleOffset = offset + len(sessions)
	}
	w.lastSessionsChecked = len(sessions)
	w.lastInconsistent = inconsistent
	w.lastOrphans = orphans
	w.lastMissing = missing
	w.lastDeleted = deleted
	w.lastVanished = vanished
	w.runMu.Unlock()

	if inconsistent > 0 || deleted > 0 {
		slog.Info("consistency_worker: 本轮对账完成",
			"sessions_checked", len(sessions),
			"session_offset", offset,
			"max_sessions", w.maxSessions,
			"idle_before", idleBefore.Format(time.RFC3339),
			"inconsistent_sessions", inconsistent,
			"orphan_bodies", orphans,
			"missing_bodies", missing,
			"deleted_orphans", deleted,
			"vanished_orphans", vanished,
			"action", w.action.String())
	}
	return nil
}
