// bg/vacuum_worker.go — 2026-08-29
//
// 审计修复 (2026-08-29)：P1-7 - v1 正文族 VACUUM自动化
//
// 2026-09-17 审计修订：v1 正文族已改为按月分区（叶子表由
// drop_old_request_logs_bodies_partitions 负责回收，分区 DROP 即归还空间），
// 对分区父表执行 VACUUM FULL 无存储可重写、只会空耗全集群
// VacuumFullMutex 窗口，故移除；本 worker 保留对 hot 表的普通 VACUUM。
//
// ★ 2026-10-20（迁移 890，v1 族退役）：v1 正文族整体被 DROP ⇒ 本 worker
// 唯一的目标表不再存在。**不留着让它每周撞一次 42P01**：见 vacuum 里的
// to_regclass 存在性探测（自描述，不依赖写门）。
//
// ★ 2026-10-24（v1 → session_turns 替换轮）：目标改指**会话族**的正文热表
// `public.session_bodies_hot`。
//
//	· 为什么不是「删掉这个 worker」：v1 退役后它每周只打一条「目标表不存在，
//	  跳过」的日志 —— 而会话族的正文热表有**同款病灶**（TOAST 膨胀，
//	  §143.96 登记未修）⇒ 这个 worker 正是那件事的现成载体。
//	· 为什么是 session_bodies_hot 而不是 session_turns_hot：正文（TOAST）
//	  才是膨胀源；轮次表本身是小行。
//	· 安全性质不变：普通 `VACUUM (ANALYZE)`（不取 ACCESS EXCLUSIVE）、
//	  存在性探测、5 分钟超时。
//
// 接入点：cmd/gateway/main.go 在 init bg services 时构造 + Start。

package bg

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// VacuumWorker 定期对**会话族**正文热表（public.session_bodies_hot）
// 执行普通 VACUUM。
//
// ★ 2026-10-24：目标由 v1 的 `_request_logs_bodies_hot` 改为
//
//	`session_bodies_hot`（v1 族随 890 退役 ⇒ 原目标不存在 ⇒ 每周空转）。
type VacuumWorker struct {
	db *pgxpool.Pool

	// lifecycle 由 BaseWorker 统一管理（审计报告 2026-08-31 模式 C）。
	*BaseWorker

	// 配置（业务字段，仍由本结构 mu 保护）
	mu           sync.Mutex
	interval     time.Duration // 执行间隔（默认 7 天）
	executeHour  int           // 执行时间（小时，0-23，默认 2）
	lastExecuted time.Time     // 上次执行时间
}

// NewVacuumWorker 构造 worker。
func NewVacuumWorker(db *pgxpool.Pool) *VacuumWorker {
	return &VacuumWorker{
		db:          db,
		BaseWorker:  NewBaseWorker("vacuum-worker"),
		interval:    7 * 24 * time.Hour, // 默认每周一次
		executeHour: 2,                  // 默认凌晨 2 点
	}
}

// SetInterval 设置执行间隔（用于测试）。
func (w *VacuumWorker) SetInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interval = d
}

// SetExecuteHour 设置执行时间（0-23 小时）。
func (w *VacuumWorker) SetExecuteHour(hour int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if hour >= 0 && hour <= 23 {
		w.executeHour = hour
	}
}

// Start 启动后台 goroutine。重复调用幂等；调用 Stop 之前不会重复启动。
func (w *VacuumWorker) Start(ctx context.Context) {
	if w == nil {
		return
	}
	if !w.BaseWorker.Start(ctx, w.run) {
		return
	}
	w.mu.Lock()
	interval, hour := w.interval, w.executeHour
	w.mu.Unlock()
	slog.Info("vacuum worker started",
		"interval", interval,
		"execute_hour", hour)
}

// Stop 取消并等待 goroutine 退出。
func (w *VacuumWorker) Stop() {
	if w == nil {
		return
	}
	w.BaseWorker.Stop()
}

func (w *VacuumWorker) run(ctx context.Context) {
	defer w.BaseWorker.NotifyStopped()

	// 每小时检查一次是否到了执行时间
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 启动后检查一次是否需要执行
	w.checkAndExecute(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.checkAndExecute(ctx)
		}
	}
}

func (w *VacuumWorker) checkAndExecute(ctx context.Context) {
	now := time.Now()

	// 检查是否到了执行时间
	// 1. 必须是指定的小时
	// 2. 距离上次执行至少超过了 interval
	// 配置字段的读写都在 mu 内完成；vacuum 在锁外执行，避免 30 分钟级的
	// VACUUM 长时间持锁阻塞 Stop/Start。
	w.mu.Lock()
	if now.Hour() != w.executeHour {
		w.mu.Unlock()
		return
	}

	if !w.lastExecuted.IsZero() && now.Sub(w.lastExecuted) < w.interval {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()

	// 执行 VACUUM；完成后才更新 lastExecuted（保持既有语义：执行失败时
	// 同一小时内仍可重试）
	w.vacuum(ctx)

	w.mu.Lock()
	w.lastExecuted = now
	w.mu.Unlock()
}

func (w *VacuumWorker) vacuum(ctx context.Context) {
	// nil-db 守卫：生产装配总是传入 pool，但测试与降级装配可能为 nil。
	// 无守卫时这里会 nil-deref panic 并带崩整个进程。
	if w.db == nil {
		slog.Warn("vacuum worker: no database pool configured, skipping VACUUM")
		return
	}

	// ★ 890（v1 族退役）：目标表已随 v1 族一起被 DROP ⇒ 直接 VACUUM 会
	// 每周期撞 42P01。这里用 to_regclass 做**存在性探测**而不是读写门：
	//
	//   - 写门（storage.request_logs_write_enabled）只说明「还在双写吗」，
	//     与「表还在吗」不是同一件事；890 之后两者永久脱钩（表没了，门还
	//     可能被翻回 true）⇒ 用门当退役信号会漏。
	//   - to_regclass 对不存在的表返回 NULL 而**不抛错** ⇒ 探测本身不会
	//     把 worker 带崩（对比 information_schema 视图或 pg_class 查询，
	//     那些在权限/类型上有边角差异）。
	//
	// 判据用「表不存在就跳过」而不是「表存在才 VACUUM」的同一形状：退役是
	// **终态**，不是故障，所以跳过时记 Info 而不是 Warn（§12 轮「跳过 ≠
	// 失败」的可观测性纪律：跳过必须出声，但不得被读成故障）。
	exists, err := w.vacuumTargetExists(ctx)
	if err != nil {
		slog.Warn("vacuum worker: target existence probe failed, skipping VACUUM",
			"error", err)
		return
	}
	if !exists {
		slog.Info("vacuum worker: 目标表不存在（正文热表未部署？），跳过 VACUUM",
			"target", vacuumTargetTable)
		return
	}

	start := time.Now()

	// 2026-09-17 audit: v1 正文族曾是 RANGE 分区父表
	// (monthly leaves, reclaimed by drop_old_request_logs_bodies_partitions).
	// VACUUM FULL against a partitioned parent either errors or no-ops —
	// there is no storage on the parent to rewrite — so the weekly job only
	// burned the cluster-wide VacuumFullMutex window. Space reclaim is the
	// partition-drop path's job; here we keep plain VACUUM on the hot table
	// (and the leaf-partition hygiene runs in bg/partition_manager.go).
	//
	// ★ 2026-10-24：会话族的 session_bodies_hot 是**普通表**（非分区父表），
	//   所以这条「普通 VACUUM 而非 VACUUM FULL」的纪律照样成立：
	//   FULL 会取 ACCESS EXCLUSIVE 并重写整表（周更窗口里不可接受）。
	hotCtx, hotCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer hotCancel()

	_, hotErr := w.db.Exec(hotCtx, "VACUUM (ANALYZE) "+vacuumTargetTable)
	elapsed := time.Since(start)
	if hotErr != nil {
		slog.Warn("vacuum worker: VACUUM on hot table failed",
			"error", hotErr,
			"elapsed_seconds", elapsed.Seconds())
		return
	}
	slog.Info("vacuum worker: VACUUM on hot table completed",
		"elapsed_seconds", elapsed.Seconds())
}

// vacuumTargetTable 是本 worker 唯一 VACUUM 的目标表。
//
// ★ 2026-10-24（v1 → session_turns 替换轮）：由 v1 的
//
//	`_request_logs_bodies_hot` 改为会话族的 `session_bodies_hot`
//	（v1 族随 890 退役 ⇒ 原目标不存在 ⇒ 本 worker 每周空转一条日志）。
//
// ⚠ 仍然保留「常量 + 存在性探测」这层，而不是把表名拼进 SQL 字符串：
//
//	§「判据必须与被测函数在同一层」的同款纪律 —— 常量要在被读的地方看得见，
//	存在性探测也要能独立指认目标。
const vacuumTargetTable = "session_bodies_hot"

// vacuumTargetExists 探测 vacuumTargetTable 是否仍然存在。
func (w *VacuumWorker) vacuumTargetExists(ctx context.Context) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var exists bool
	err := w.db.QueryRow(probeCtx,
		`SELECT to_regclass('public.'||$1) IS NOT NULL`, vacuumTargetTable).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}
