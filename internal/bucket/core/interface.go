package core

import (
	"context"
	"time"
)

// BucketProvider 桶提供者接口
// 每种桶类型（credit/ratelimit/priority）都需要实现此接口
type BucketProvider interface {
	// Name 返回桶提供者的名称
	Name() string

	// Type 返回桶类型
	Type() BucketType

	// GetState 获取指定桶的当前状态
	GetState(ctx context.Context, key BucketKey) (*BucketState, error)

	// UpdateState 更新桶状态
	// 返回更新后的状态
	UpdateState(ctx context.Context, key BucketKey, delta BucketDelta) (*BucketState, error)

	// Query 执行桶查询操作（如聚合、回填等）
	Query(ctx context.Context, query BucketQuery) (*BucketResult, error)

	// Reset 重置桶状态
	Reset(ctx context.Context, key BucketKey) error

	// GetMetrics 获取桶的监控指标
	GetMetrics(ctx context.Context) (*BucketMetrics, error)
}

// BucketCache 桶缓存接口
type BucketCache interface {
	// Get 从缓存获取桶状态
	Get(key BucketKey) (*BucketState, bool)

	// Set 设置桶状态到缓存
	Set(key BucketKey, state *BucketState, ttl time.Duration) error

	// Delete 删除缓存
	Delete(key BucketKey) error

	// Clear 清空所有缓存
	Clear() error

	// GetStats 获取缓存统计信息
	GetStats() CacheStats
}

// CacheStats 缓存统计信息
type CacheStats struct {
	Hits      int64 // 命中次数
	Misses    int64 // 未命中次数
	Evictions int64 // 淘汰次数
	Size      int   // 当前大小
	MaxSize   int   // 最大容量
}

// BucketStorage 桶存储接口
type BucketStorage interface {
	// Load 从持久化存储加载桶状态
	Load(ctx context.Context, key BucketKey) (*BucketState, error)

	// Save 保存桶状态到持久化存储
	Save(ctx context.Context, key BucketKey, state *BucketState) error

	// LoadBatch 批量加载桶状态
	LoadBatch(ctx context.Context, keys []BucketKey) (map[string]*BucketState, error)

	// SaveBatch 批量保存桶状态
	SaveBatch(ctx context.Context, states map[BucketKey]*BucketState) error

	// Delete 删除桶状态
	Delete(ctx context.Context, key BucketKey) error

	// Query 执行查询
	Query(ctx context.Context, query BucketQuery) (*BucketResult, error)
}

// AdmissionController 准入控制器接口（用于限流桶）
type AdmissionController interface {
	// Admit 请求准入
	// amount: 请求的配额数量
	// 返回准入结果
	Admit(ctx context.Context, key BucketKey, amount int64) (AdmissionResult, error)

	// AdmitWithWait 请求准入，如果配额不足则等待
	// maxWait: 最大等待时间
	AdmitWithWait(ctx context.Context, key BucketKey, amount int64, maxWait time.Duration) (AdmissionResult, error)

	// Release 释放预留的配额
	Release(ctx context.Context, key BucketKey, amount int64) error

	// GetQueueSize 获取当前队列大小
	GetQueueSize(ctx context.Context, key BucketKey) (int, error)
}

// MetricsCollector 指标收集器接口
type MetricsCollector interface {
	// RecordRequest 记录一次请求
	RecordRequest(bucketType BucketType, success bool, latency time.Duration)

	// RecordCacheHit 记录缓存命中
	RecordCacheHit(bucketType BucketType, hit bool)

	// RecordQuotaUsage 记录配额使用
	RecordQuotaUsage(bucketType BucketType, tenantID string, used int64, capacity int64)

	// RecordAdmission 记录准入结果
	RecordAdmission(bucketType BucketType, admitted bool, waitTime time.Duration)

	// GetMetrics 获取指标
	GetMetrics(bucketType BucketType) (*BucketMetrics, error)

	// Reset 重置指标
	Reset(bucketType BucketType)
}

// AlertRule 告警规则接口
type AlertRule interface {
	// Name 返回规则名称
	Name() string

	// Evaluate 评估告警条件
	// 返回是否触发告警
	Evaluate(ctx context.Context, metrics *BucketMetrics) (bool, error)

	// GetThreshold 获取阈值
	GetThreshold() float64

	// GetMessage 获取告警消息
	GetMessage(metrics *BucketMetrics) string
}

// AlertManager 告警管理器接口
type AlertManager interface {
	// RegisterRule 注册告警规则
	RegisterRule(rule AlertRule) error

	// UnregisterRule 注销告警规则
	UnregisterRule(name string) error

	// Evaluate 评估所有规则
	Evaluate(ctx context.Context, bucketType BucketType) error

	// GetActiveAlerts 获取当前活跃的告警
	GetActiveAlerts() []Alert
}

// Alert 告警信息
type Alert struct {
	RuleName  string                 // 规则名称
	Severity  AlertSeverity          // 严重级别
	Message   string                 // 告警消息
	Timestamp time.Time              // 触发时间
	Metadata  map[string]interface{} // 额外信息
}

// AlertSeverity 告警严重级别
type AlertSeverity string

const (
	AlertSeverityInfo     AlertSeverity = "info"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityCritical AlertSeverity = "critical"
)
