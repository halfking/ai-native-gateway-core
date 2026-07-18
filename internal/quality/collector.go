// Package quality 提供供应商质量画像的数据采集和聚合功能
package quality

import (
	"context"
	"database/sql"
	"time"
)

// Collector 质量指标采集器接口
type Collector interface {
	Start(ctx context.Context) error
	CollectMinuteMetrics(ctx context.Context) error
	CollectHourMetrics(ctx context.Context) error
}

// collector 采集器实现
type collector struct {
	db             *sql.DB
	minuteInterval time.Duration
	hourInterval   time.Duration
	timeout        time.Duration
	enabled        bool
}

// New 创建质量指标采集器
func New(db *sql.DB, opts ...Option) Collector {
	c := &collector{
		db:             db,
		minuteInterval: 1 * time.Minute,
		hourInterval:   1 * time.Hour,
		timeout:        30 * time.Second,
		enabled:        true,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Option 配置选项
type Option func(*collector)

func WithMinuteInterval(d time.Duration) Option {
	return func(c *collector) { c.minuteInterval = d }
}

func WithHourInterval(d time.Duration) Option {
	return func(c *collector) { c.hourInterval = d }
}

func WithTimeout(d time.Duration) Option {
	return func(c *collector) { c.timeout = d }
}

func WithEnabled(enabled bool) Option {
	return func(c *collector) { c.enabled = enabled }
}

// Start 启动采集器
func (c *collector) Start(ctx context.Context) error {
	if !c.enabled {
		return nil
	}
	go c.runMinuteAggregator(ctx)
	go c.runHourAggregator(ctx)
	<-ctx.Done()
	return ctx.Err()
}

func (c *collector) CollectMinuteMetrics(ctx context.Context) error {
	return collectMinuteMetrics(ctx, c.db, c.timeout)
}

func (c *collector) CollectHourMetrics(ctx context.Context) error {
	return collectHourMetrics(ctx, c.db, c.timeout)
}

func (c *collector) runMinuteAggregator(ctx context.Context) {
	ticker := time.NewTicker(c.minuteInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.CollectMinuteMetrics(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (c *collector) runHourAggregator(ctx context.Context) {
	ticker := time.NewTicker(c.hourInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.CollectHourMetrics(ctx)
		case <-ctx.Done():
			return
		}
	}
}
