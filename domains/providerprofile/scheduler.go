package providerprofile

import (
	"context"
	"fmt"
	"time"
)

// SchedulerService 调度器服务接口
// 用于注册定时任务到网关的调度系统
type SchedulerService interface {
	// RegisterCollectorJob 注册采集任务
	RegisterCollectorJob(collector Collector) error
	
	// RegisterAggregatorJob 注册聚合任务
	RegisterAggregatorJob(aggregator Aggregator) error
	
	// RegisterCleanupJob 注册清理任务
	RegisterCleanupJob(metricsStore MetricsStore) error
}

// JobScheduler 任务调度器
type JobScheduler struct {
	collector    Collector
	aggregator   Aggregator
	metricsStore MetricsStore
}

// NewJobScheduler 创建任务调度器
func NewJobScheduler(collector Collector, aggregator Aggregator, metricsStore MetricsStore) *JobScheduler {
	return &JobScheduler{
		collector:    collector,
		aggregator:   aggregator,
		metricsStore: metricsStore,
	}
}

// RunLightweightCollection 执行轻量级采集任务
// 调度: 每2小时执行一次
func (s *JobScheduler) RunLightweightCollection(ctx context.Context) error {
	if err := s.collector.CollectMetrics(ctx); err != nil {
		return fmt.Errorf("lightweight collection failed: %w", err)
	}
	return nil
}

// RunDailyAggregation 执行每日聚合任务
// 调度: 每天凌晨4点执行
func (s *JobScheduler) RunDailyAggregation(ctx context.Context) error {
	// 聚合昨天的数据
	yesterday := time.Now().AddDate(0, 0, -1)
	
	if err := s.aggregator.AggregateDailyProfiles(ctx, yesterday); err != nil {
		return fmt.Errorf("daily aggregation failed: %w", err)
	}
	return nil
}

// RunCleanup 执行清理任务
// 调度: 每周日凌晨5点执行
func (s *JobScheduler) RunCleanup(ctx context.Context) error {
	deleted, err := s.metricsStore.CleanupOldMetrics(ctx)
	if err != nil {
		return fmt.Errorf("cleanup failed: %w", err)
	}
	
	// TODO: 添加日志记录
	_ = deleted // 记录删除的行数
	
	return nil
}

// ScheduleConfig 调度配置
type ScheduleConfig struct {
	// LightweightCollectionCron 轻量级采集cron表达式，默认 "0 */2 * * *" (每2小时)
	LightweightCollectionCron string
	
	// DailyAggregationCron 每日聚合cron表达式，默认 "0 4 * * *" (每天凌晨4点)
	DailyAggregationCron string
	
	// CleanupCron 清理任务cron表达式，默认 "0 5 * * 0" (每周日凌晨5点)
	CleanupCron string
}

// DefaultScheduleConfig 返回默认调度配置
func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		LightweightCollectionCron: "0 */2 * * *",   // 每2小时
		DailyAggregationCron:      "0 4 * * *",     // 每天凌晨4点
		CleanupCron:               "0 5 * * 0",     // 每周日凌晨5点
	}
}

// JobRegistry 任务注册器
// 用于在网关启动时注册所有供应商画像相关的定时任务
type JobRegistry struct {
	scheduler JobScheduler
	config    ScheduleConfig
}

// NewJobRegistry 创建任务注册器
func NewJobRegistry(scheduler JobScheduler, config ScheduleConfig) *JobRegistry {
	return &JobRegistry{
		scheduler: scheduler,
		config:    config,
	}
}

// RegisterAllJobs 注册所有任务
// 此方法应在网关启动时调用，将任务注册到网关的调度系统
func (r *JobRegistry) RegisterAllJobs(cronScheduler CronScheduler) error {
	// 1. 注册轻量级采集任务
	if err := cronScheduler.AddJob(
		"profile:collect:lightweight",
		r.config.LightweightCollectionCron,
		r.scheduler.RunLightweightCollection,
	); err != nil {
		return fmt.Errorf("register lightweight collection job: %w", err)
	}

	// 2. 注册每日聚合任务
	if err := cronScheduler.AddJob(
		"profile:aggregate:daily",
		r.config.DailyAggregationCron,
		r.scheduler.RunDailyAggregation,
	); err != nil {
		return fmt.Errorf("register daily aggregation job: %w", err)
	}

	// 3. 注册清理任务
	if err := cronScheduler.AddJob(
		"profile:cleanup",
		r.config.CleanupCron,
		r.scheduler.RunCleanup,
	); err != nil {
		return fmt.Errorf("register cleanup job: %w", err)
	}

	return nil
}

// CronScheduler 网关的Cron调度器接口
// 这是网关现有调度系统的抽象接口
type CronScheduler interface {
	// AddJob 添加定时任务
	AddJob(name string, cronExpr string, handler func(context.Context) error) error
}
