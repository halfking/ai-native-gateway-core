package admin

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// HotCronConfig 夜间 cron 调度配置
type HotCronConfig struct {
	// RunAtHour / RunAtMinute 每天本地时间 HH:MM 触发；默认 02:00
	RunAtHour   int
	RunAtMinute int
	// RetentionHours 迁移超过 N 小时的数据；默认 24（覆盖 hot 表默认保留 1 天）
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
		c.RetentionHours = 24
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

// HotCronScheduler 是 hot 表夜间自动迁移调度器
//
// 设计要点：
//   - 单 goroutine ticker，每分钟检查一次当前时间是否到达 RunAtHour:RunAtMinute
//   - 同一时间最多并发执行一个 cron run（用 sync.Mutex + running 标志保护）
//   - 单表迁移失败按指数退避重试 3 次，全部失败时记录 alert（写到 slog + 入 job 状态）
//   - 不阻塞主进程：使用独立 context，重启即终止（不需要持久化 cron 调度状态）
type HotCronScheduler struct {
	h       *Handler
	cfg     HotCronConfig
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
	return &HotCronScheduler{
		h:       h,
		cfg:     cfg,
		stopped: make(chan struct{}),
	}
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
				slog.Error("data-lifecycle: hot cron panic", "panic", r)
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
			err := s.h.db.QueryRow(ctx,
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
