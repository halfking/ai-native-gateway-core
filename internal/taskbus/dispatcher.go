package taskbus

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Dispatcher 轮询 Store，按 TaskType 路由到 Registry 中的 Handler。
type Dispatcher struct {
	store        Store
	registry     *Registry
	maxAttempts  int
	pollInterval time.Duration
	batchSize    int
	logger       *slog.Logger
	// ★ r1041 轮（2026-10-28）：sweepEvery = 每隔多少轮做一次回收。
	// ★ 0 = 不回收（★ 那就退化成旧行为 ⇒ 内存无界增长 ⇒ 所以生产必须 > 0）。
	sweepEvery int
	sweepTick  int
}

// Sweeper 是**可选**的回收接口（★ 不加进 Store —— ★ Store 是持久化契约，
// ★ 回收是内存实现的专属关注点 ⇒ ★ 混进同一个接口会让 DB 实现也被迫实现它）。
//
// ★ r1041：MemoryStore 实现它（★ 旧实现里完成的条目永不删除 ⇒ 内存无界增长）。
type Sweeper interface {
	// Sweep 回收超保留窗口的已完成任务，返回回收条数。
	Sweep(now time.Time) int
}

// sweepIntervalDefault = 每 60 轮回收一次。
//
// ★ 权衡：★ 太频繁 ⇒ 回收本身有成本；★ 太稀疏 ⇒ 内存峰值偏高。
// ★ 60 轮 × 100ms = 6 秒一次 ⇒ 对「10 分钟保留窗口」来说足够密。
const sweepIntervalDefault = 60

// DispatcherConfig 配置。
type DispatcherConfig struct {
	Store        Store
	Registry     *Registry
	MaxAttempts  int
	PollInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
	// ★ r1041：SweepEvery = 每多少轮回收一次（0 = 缺省 60；< 0 = 关闭）。
	SweepEvery int
}

// NewDispatcher 构造；缺省 MaxAttempts=5 / Poll=100ms（测试友好）/ Batch=32。
func NewDispatcher(cfg DispatcherConfig) *Dispatcher {
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 32
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	// ★ r1041：★ 回收默认**开启**（每 60 轮一次）——
	// ★ 旧行为（永不回收）等于「内存随流量单调增长」⇒ ⇒ 不该是缺省。
	if cfg.SweepEvery == 0 {
		cfg.SweepEvery = sweepIntervalDefault
	}
	return &Dispatcher{
		store:        cfg.Store,
		registry:     cfg.Registry,
		maxAttempts:  cfg.MaxAttempts,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		logger:       cfg.Logger,
		sweepEvery:   cfg.SweepEvery,
	}
}

// DispatchOnce 拉取一批并处理（G4 / 单测用）。
func (d *Dispatcher) DispatchOnce(ctx context.Context) (handled int, err error) {
	if d == nil || d.store == nil || d.registry == nil {
		return 0, fmt.Errorf("taskbus.Dispatcher: not configured")
	}
	batch, err := d.store.ClaimBatch(ctx, d.batchSize, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	for _, task := range batch {
		if err := d.handleOne(ctx, task); err != nil {
			d.logger.Warn("taskbus handle failed",
				"task_type", task.TaskType, "id", task.ID, "error", err)
		}
		handled++
	}
	d.refreshGauges(ctx)
	d.maybeSweep(ctx)
	return handled, nil
}

// maybeSweep 按周期触发回收（★ r1041：★ 这是「内存不再无界增长」的那一刀）。
//
// ★ 只对实现了 Sweeper 的 Store 生效（★ 内存实现才有这个关注点）。
func (d *Dispatcher) maybeSweep(ctx context.Context) {
	if d.sweepEvery <= 0 {
		return
	}
	sw, ok := d.store.(Sweeper)
	if !ok {
		return
	}
	d.sweepTick++
	if d.sweepTick < d.sweepEvery {
		return
	}
	d.sweepTick = 0
	if n := sw.Sweep(time.Now().UTC()); n > 0 {
		d.logger.Info("taskbus swept completed tasks", "count", n)
	}
}

// Start 阻塞轮询直到 ctx 取消。
func (d *Dispatcher) Start(ctx context.Context) error {
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := d.DispatchOnce(ctx); err != nil && ctx.Err() == nil {
				d.logger.Warn("taskbus.DispatchOnce", "error", err)
			}
		}
	}
}

func (d *Dispatcher) handleOne(ctx context.Context, task Task) error {
	h, ok := d.registry.Handler(task.TaskType)
	if !ok {
		_ = d.store.MarkDLQ(ctx, task.ID, "no handler registered")
		RecordHandled(task.TaskType, "dlq")
		return fmt.Errorf("no handler for %q", task.TaskType)
	}
	if err := h.Handle(ctx, task); err != nil {
		attempts := task.Attempts + 1
		if attempts >= d.maxAttempts {
			_ = d.store.MarkDLQ(ctx, task.ID, err.Error())
			RecordHandled(task.TaskType, "dlq")
			return err
		}
		backoff := time.Duration(1<<uint(attempts)) * time.Second
		_ = d.store.MarkFailed(ctx, task.ID, attempts, time.Now().UTC().Add(backoff), err.Error())
		RecordHandled(task.TaskType, "error")
		return err
	}
	_ = d.store.MarkSent(ctx, task.ID)
	RecordHandled(task.TaskType, "ok")
	return nil
}

func (d *Dispatcher) refreshGauges(ctx context.Context) {
	for _, tt := range d.registry.TaskTypes() {
		p, _ := d.store.CountByStatus(ctx, tt, StatusPending)
		f, _ := d.store.CountByStatus(ctx, tt, StatusFailed)
		q, _ := d.store.CountByStatus(ctx, tt, StatusDLQ)
		SetStatusGauges(tt, p, f, q)
	}
}
