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
}

// DispatcherConfig 配置。
type DispatcherConfig struct {
	Store        Store
	Registry     *Registry
	MaxAttempts  int
	PollInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
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
	return &Dispatcher{
		store:        cfg.Store,
		registry:     cfg.Registry,
		maxAttempts:  cfg.MaxAttempts,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		logger:       cfg.Logger,
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
	return handled, nil
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
