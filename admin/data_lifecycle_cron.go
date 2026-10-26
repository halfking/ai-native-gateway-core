package admin

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// HotCronConfig 夜间 cron 调度配置
type HotCronConfig struct {
	// RunAtHour / RunAtMinute 每天本地时间 HH:MM 触发；默认 02:00
	RunAtHour   int
	RunAtMinute int
	// RetentionHours 迁移超过 N 小时的数据；默认 8（统一 hot 表保留窗口）
	RetentionHours int
	// BatchSize 单批迁移行数；默认 500
	BatchSize int
	// MaxRetries 单表迁移失败重试次数；默认 3
	MaxRetries int
	// RetryBackoff 首次重试等待时间，后续按 2x 指数退避
	RetryBackoff time.Duration
	// Enabled 是否启用 cron；可通过环境变量 HOT_CRON_DISABLED=1 关掉
	Enabled bool
}

// defaults 应用默认值。
//
// 设计：零值（0）一律视为「未设置」并应用默认值（除 RunAtMinute=0 是合法的「整点」之外）。
// 用户如果显式想禁用重试，可设 MaxRetries=-1 触发 3 次重试；显式想设 RunAtHour=0（午夜），
// 需在 NewHotCronScheduler 之外直接修改 cfg.RunAtHour 后传入。
func (c *HotCronConfig) defaults() {
	if c.RunAtHour < 0 || c.RunAtHour > 23 {
		c.RunAtHour = 2
	}
	if c.RunAtMinute < 0 || c.RunAtMinute > 59 {
		c.RunAtMinute = 0
	}
	if c.RetentionHours <= 0 {
		c.RetentionHours = defaultHotRetentionHours
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 500
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 3
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = 30 * time.Second
	}
}

// hotCronDB 是 hot cron 的最小查询面：能力探针（查 pg_proc）与 promote 调用
// 各只需要 QueryRow 一个方法。
//
// 为什么要开窄接口（2026-10-21，§19.20.24）：
// Handler.db 是**具体的** *pgxpool.Pool，无法在单测里注入 ⇒ 这条路径此前
// 只能靠「构造一个 db=nil 的 Handler，让它 nil 解引用 panic」来"覆盖"
// （见 data_lifecycle_cron_test.go 的旧 TestMaybeRunAt_MatchTime）⇒
// 测出来的只是「panic 被 recover 兜住了」，行为一个都没钉住。
// 与 handler.go 的 controlledBodyDB / topologyStatsDB 同款范式。
type hotCronDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// HotCronScheduler 是 hot 表夜间自动迁移调度器
//
// 设计要点：
//   - 单 goroutine ticker，每分钟检查一次当前时间是否到达 RunAtHour:RunAtMinute
//   - 同一时间最多并发执行一个 cron run（用 sync.Mutex + running 标志保护）
//   - 单表迁移失败按指数退避重试 3 次，全部失败时记录 alert（写到 slog + 入 job 状态）
//   - 不阻塞主进程：使用独立 context，重启即终止（不需要持久化 cron 调度状态）
type HotCronScheduler struct {
	h   *Handler
	cfg HotCronConfig
	// db 是 h.db 的窄接口视图（见 hotCronDB）。生产上由 NewHotCronScheduler
	// 从 h.db 接线；单测可直接注入假实现。
	db      hotCronDB
	cancel  context.CancelFunc
	stopped chan struct{}

	mu       sync.Mutex
	running  bool // 当前是否有 cron run 在执行
	lastRun  *time.Time
	lastErr  string
	runCount int
}

// NewHotCronScheduler 构造调度器（不启动）
//
// 注意：构造时不应用 defaults()；由调用方决定是否使用默认值。
// HotCronConfigFromEnv() 会负责应用默认值。
func NewHotCronScheduler(h *Handler, cfg HotCronConfig) *HotCronScheduler {
	// 把零值 RunAtHour (0) 当作「未设置」，让 defaults() 应用默认值；
	// 0 是合法的小时（凌晨 0 点），但用户很少需要，正好与"忘记设置"语义对齐。
	if cfg.RunAtHour == 0 {
		cfg.RunAtHour = -1
	}
	// 同样把 MaxRetries=0 当作「未设置」应用默认 3（用户想禁用重试可设 -1）
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = -1
	}
	cfg.defaults()
	s := &HotCronScheduler{
		h:       h,
		cfg:     cfg,
		stopped: make(chan struct{}),
	}
	// 接线：db 取 h.db 的窄接口视图。
	//
	// ⚠ 必须显式判 h.db != nil 再赋值：否则 h.db（一个 nil 的 *pgxpool.Pool）
	// 会被装进接口 ⇒ s.db 变成「非 nil 的接口持有 nil 指针」⇒ s.db == nil 为假
	// ⇒ runOnce 开头的 nil 守卫会失效 ⇒ 又回到 nil 解引用 panic 那条老路。
	if h != nil && h.db != nil {
		s.db = h.db
	}
	return s
}

// Start 启动 cron 调度 goroutine。
//
// 立即检查一次（应对服务启动时已经过了 RunAtHour:RunAtMinute 的情况），
// 然后每分钟 ticker 一次。
func (s *HotCronScheduler) Start(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	s.cancel = cancel

	go func() {
		defer close(s.stopped)
		// 启动时检查是否需要立即补跑（如果上次 cron 错过了）
		s.maybeRun(ctx)

		// 1 分钟 ticker
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case t := <-ticker.C:
				s.maybeRunAt(ctx, t)
			}
		}
	}()
	slog.Info("data-lifecycle: hot cron started",
		"run_at", fmt.Sprintf("%02d:%02d", s.cfg.RunAtHour, s.cfg.RunAtMinute),
		"retention_hours", s.cfg.RetentionHours,
		"batch_size", s.cfg.BatchSize,
		"max_retries", s.cfg.MaxRetries)
}

// Stop 停止 cron 调度；等待当前 run 结束
func (s *HotCronScheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.stopped
}

// maybeRunAt 在 ticker 触发时检查当前时间是否到达
func (s *HotCronScheduler) maybeRunAt(ctx context.Context, t time.Time) {
	if t.Hour() != s.cfg.RunAtHour {
		return
	}
	if t.Minute() != s.cfg.RunAtMinute {
		return
	}
	s.maybeRun(ctx)
}

// maybeRun 检查并启动一次 cron run；若已有 run 在执行则跳过
func (s *HotCronScheduler) maybeRun(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.runCount++
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
			if r := recover(); r != nil {
				// ★ 必须带堆栈（2026-10-21，§19.20.24）：此前只记 panic 值。
				// panic 值常常是 "runtime error: invalid memory address or nil
				// pointer dereference" 这种**不带位置**的文本 ⇒ 线上真 panic
				// 时无从定位是哪一行炸的。debug.Stack() 在 panic 尚未展开时
				// 调用 ⇒ 栈里**同时含原始出错点**与本 defer 帧。
				slog.Error("data-lifecycle: hot cron panic", "panic", r, "stack", string(debug.Stack()))
			}
		}()
		s.runOnce(ctx)
	}()
}

// runOnce 执行一次完整的夜间迁移循环
func (s *HotCronScheduler) runOnce(ctx context.Context) {
	start := time.Now().UTC()
	slog.Info("data-lifecycle: hot cron run start",
		"retention_hours", s.cfg.RetentionHours,
		"tables", len(hotPromoteTableMap))

	// ★ 没有 DB 句柄 ⇒ 直接跳过，并**记 ERROR**（这是真故障，不是噪声）。
	//
	// 可达性（2026-10-21 实测，§19.20.24）：生产上唯一构造点是
	// cmd/gateway/main.go:NewHotCronScheduler(adminHandler, cfg)，而 adminHandler
	// 由 admin.NewHandler(adminDB, ...) 构造、db 是活连接池 ⇒ **生产不可达**。
	// 它守的是「构造出错（h 为 nil / db 没接线）」这类编程错误：此前这类错误
	// 表现为 s.h.db 的 nil 解引用 panic ⇒ 被 recover 兜住 ⇒ 只留一条不带位置
	// 的 ERROR ⇒ 定位不到根因。守卫把它变成一条自解释的 ERROR。
	if s.db == nil {
		slog.Error("data-lifecycle: hot cron run skipped (no database handle)",
			"tables", len(hotPromoteTableMap))
		return
	}

	totalRows := int64(0)
	failures := make(map[string]string)

	for table := range hotPromoteTableMap {
		// 检查 ctx 是否取消
		if ctx.Err() != nil {
			slog.Warn("data-lifecycle: hot cron cancelled mid-loop", "table", table)
			break
		}

		// 跳过已经有 running 任务在执行的表（避免重复迁移）
		if existing := s.h.findRunningJobForTable(table); existing != nil {
			slog.Info("data-lifecycle: hot cron skip table (manual job in progress)",
				"table", table, "running_job_id", existing.ID)
			continue
		}

		// ★ 890（v1 族真删）后的**能力探针**：promote 函数不存在 ⇒ 该表/该族
		// 已经不存在（表与函数被一起 DROP）⇒ **SKIP**，不是 ERROR。
		//
		// ⚠ 为什么需要它（缺陷本体）：自动 hot cron 原本直接调 promote 函数，
		// 而 890 把 v1 族与它的 promote 函数一起 DROP ⇒ 每轮 cron 都得到
		// 42883（function promote_request_logs_hot_to_partition does not
		// exist）⇒ 每轮 2 条 WARN + 1 条 ERROR 的噪声，而被调用的表**根本
		// 不存在**、没有任何东西可搬 ⇒ 纯噪声 + 掩盖真实故障。
		//
		// ⇒ 探针让「函数没了」变成一条 INFO 的 SKIP，同时保留真正执行失败
		// 时的 ERROR 语义（这是本门要守的不变量：噪声不得冒充故障）。
		fnName := hotPromoteTableMap[table]
		var fnPresent bool
		if err := s.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
				WHERE n.nspname = 'public' AND p.proname = $1)
		`, fnName).Scan(&fnPresent); err != nil {
			slog.Warn("data-lifecycle: hot cron skip table (promote function probe failed)",
				"table", table, "function", fnName, "error", err)
			continue
		}
		if !fnPresent {
			slog.Info("data-lifecycle: hot cron skip table (promote function absent — table/family retired)",
				"table", table, "function", fnName)
			continue
		}

		migrated, err := s.runOneTableWithRetry(ctx, table)
		if err != nil {
			failures[table] = err.Error()
			slog.Error("data-lifecycle: hot cron failed for table after retries",
				"table", table, "error", err)
			// 失败也写入 job 状态供前端查询
			s.recordCronJob(table, HotJobStatusFailed, 0, 0, err.Error())
			continue
		}
		totalRows += migrated
		slog.Info("data-lifecycle: hot cron table done",
			"table", table, "migrated", migrated)
		s.recordCronJob(table, HotJobStatusSuccess, migrated, 1, "")
	}

	now := time.Now().UTC()
	s.mu.Lock()
	s.lastRun = &now
	if len(failures) > 0 {
		// 把第一个失败的 error 摘要写到 lastErr
		for _, e := range failures {
			s.lastErr = fmt.Sprintf("%d table(s) failed; first error: %s", len(failures), e)
			break
		}
	} else {
		s.lastErr = ""
	}
	s.mu.Unlock()

	slog.Info("data-lifecycle: hot cron run complete",
		"total_migrated", totalRows,
		"failed_tables", len(failures),
		"duration_seconds", int64(time.Since(start).Seconds()))
}

// runOneTableWithRetry 对单张表执行迁移，失败时按指数退避重试最多 MaxRetries 次。
//
// 重试策略：每次失败后等待 backoff × 2^i（i 为重试次数），并在 backoff 上叠加 ±20% 抖动
// 防止多实例同时重试打爆 DB。
func (s *HotCronScheduler) runOneTableWithRetry(ctx context.Context, table string) (int64, error) {
	fnName := hotPromoteTableMap[table]
	retentionInterval := fmt.Sprintf("%d hours", s.cfg.RetentionHours)

	var lastErr error
	for attempt := 0; attempt <= s.cfg.MaxRetries; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		var total int64
		// 单表重试也按 batch 循环执行，把 hot 表清空到 retention 之外
		for {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			var migrated int64
			err := s.db.QueryRow(ctx,
				fmt.Sprintf("SELECT %s($1::interval, $2::int)", fnName),
				retentionInterval,
				s.cfg.BatchSize,
			).Scan(&migrated)
			if err != nil {
				lastErr = fmt.Errorf("batch error: %w", err)
				break
			}
			if migrated == 0 {
				break
			}
			total += migrated
		}

		if lastErr == nil {
			return total, nil
		}

		slog.Warn("data-lifecycle: hot cron table attempt failed",
			"table", table, "attempt", attempt+1, "error", lastErr)

		if attempt >= s.cfg.MaxRetries {
			break
		}

		// 指数退避 + ±20% 抖动
		backoff := s.cfg.RetryBackoff * time.Duration(1<<attempt)
		jitter := time.Duration(float64(backoff) * (1 + (rand.Float64()*0.4 - 0.2)))
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(jitter):
		}
		lastErr = nil // 重置以进入下一轮 batch 循环
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("retries exhausted")
	}
	return 0, lastErr
}

// recordCronJob 把 cron 触发的一次表迁移写入 hotJobMgr（status="success" / "failed"），
// 让前端能看到 cron 的执行历史和累计统计。
func (s *HotCronScheduler) recordCronJob(table string, status HotJobStatus, migrated int64, batches int, errMsg string) {
	now := time.Now().UTC()
	job := &HotPromoteJob{
		ID:              fmt.Sprintf("cron-%s-%d", table, now.UnixNano()),
		TableName:       table,
		Status:          status,
		RetentionHours:  s.cfg.RetentionHours,
		BatchSize:       s.cfg.BatchSize,
		StartedAt:       now.Add(-time.Duration(batches) * time.Second),
		FinishedAt:      &now,
		UpdatedAt:       now,
		DurationSeconds: int64(batches),
		TotalMigrated:   migrated,
		BatchesExecuted: batches,
		Message:         "executed by nightly cron",
		TriggeredBy:     "cron",
	}
	if errMsg != "" {
		job.Error = errMsg
	}
	s.h.hotJobMgr.add(job)
}

// Stats 返回调度器当前状态（用于 /api/admin/data-lifecycle/hot/cron/stats）
type HotCronStats struct {
	Enabled      bool       `json:"enabled"`
	RunAt        string     `json:"run_at"`
	RetentionHrs int        `json:"retention_hours"`
	BatchSize    int        `json:"batch_size"`
	RunningNow   bool       `json:"running_now"`
	LastRunAt    *time.Time `json:"last_run_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	RunCount     int        `json:"run_count"`
}

// Stats 获取调度器当前状态
func (s *HotCronScheduler) Stats() HotCronStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return HotCronStats{
		Enabled:      s.cfg.Enabled,
		RunAt:        fmt.Sprintf("%02d:%02d", s.cfg.RunAtHour, s.cfg.RunAtMinute),
		RetentionHrs: s.cfg.RetentionHours,
		BatchSize:    s.cfg.BatchSize,
		RunningNow:   s.running,
		LastRunAt:    s.lastRun,
		LastError:    s.lastErr,
		RunCount:     s.runCount,
	}
}
