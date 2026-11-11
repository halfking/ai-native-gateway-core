package core

import "time"

// BucketType 桶类型枚举
type BucketType string

const (
	// BucketTypeCredit 信用桶（用于 MaaS 计费的时间桶统计）
	BucketTypeCredit BucketType = "credit"

	// BucketTypeRateLimit 限流桶（用于 RPM 限流控制）
	BucketTypeRateLimit BucketType = "ratelimit"

	// BucketTypePriority 优先级桶（用于路由层的优先级分配）
	BucketTypePriority BucketType = "priority"
)

// TimeWindow 时间窗口定义
type TimeWindow struct {
	Start time.Time      // 窗口开始时间
	End   time.Time      // 窗口结束时间
	Size  time.Duration  // 窗口大小（1分钟/1小时/1天）
}

// BucketKey 桶的唯一标识
type BucketKey struct {
	Type       BucketType // 桶类型
	TenantID   string     // 租户ID
	ResourceID string     // 资源ID（如凭据ID、模型ID）
	Window     TimeWindow // 时间窗口
}

// String 返回桶键的字符串表示（用于缓存键）
func (k BucketKey) String() string {
	return string(k.Type) + ":" + k.TenantID + ":" + k.ResourceID + ":" + k.Window.Start.Format(time.RFC3339)
}

// BucketState 桶的状态信息
type BucketState struct {
	Capacity  int64                  // 总容量
	Used      int64                  // 已使用量
	Reserved  int64                  // 已预留量
	Available int64                  // 可用量 (Capacity - Used - Reserved)
	UpdatedAt time.Time              // 最后更新时间
	Metadata  map[string]interface{} // 扩展元数据
}

// DeltaOp 增量操作类型
type DeltaOp string

const (
	DeltaAdd      DeltaOp = "add"      // 增加
	DeltaSubtract DeltaOp = "subtract" // 减少
	DeltaSet      DeltaOp = "set"      // 设置
)

// BucketDelta 桶状态的增量变化
type BucketDelta struct {
	Amount    int64   // 变化量
	Operation DeltaOp // 操作类型
}

// BucketQuery 桶查询请求
type BucketQuery struct {
	Type       BucketType         // 桶类型
	TenantID   string             // 租户ID（可选，为空表示所有租户）
	ResourceID string             // 资源ID（可选）
	Window     TimeWindow         // 查询的时间窗口
	Action     string             // 查询动作（如 "backfill", "aggregate"）
	Params     map[string]interface{} // 额外参数
}

// BucketResult 桶查询结果
type BucketResult struct {
	RowsAffected int64                  // 影响的行数
	Data         []BucketState          // 查询到的桶状态列表
	Metadata     map[string]interface{} // 结果元数据
}

// AdmissionResult 准入控制结果
type AdmissionResult struct {
	Admitted         bool      // 是否准入
	Waiting          bool      // 是否需要等待
	Position         int       // 在队列中的位置
	Limit            int       // 限制值
	Remaining        int       // 剩余配额
	QueueRemaining   int       // 队列剩余容量
	EstimatedWaitSec int       // 预估等待时间（秒）
	Timestamp        time.Time // 结果时间戳
}

// BucketMetrics 桶的监控指标
type BucketMetrics struct {
	// 基础指标
	TotalRequests   int64 // 总请求数
	SuccessRequests int64 // 成功请求数
	FailedRequests  int64 // 失败请求数
	
	// 性能指标
	AvgLatency time.Duration // 平均延迟
	MaxLatency time.Duration // 最大延迟
	P50Latency time.Duration // P50 延迟
	P95Latency time.Duration // P95 延迟
	P99Latency time.Duration // P99 延迟
	
	// 缓存指标
	CacheHits   int64 // 缓存命中数
	CacheMisses int64 // 缓存未命中数
	
	// 容量指标
	CurrentCapacity int64 // 当前容量
	CurrentUsed     int64 // 当前使用量
	UsageRate       float64 // 使用率 (0-1)
	
	// 时间窗口
	WindowStart time.Time // 指标窗口开始时间
	WindowEnd   time.Time // 指标窗口结束时间
}

// Error types
type BucketError struct {
	Type    string // 错误类型
	Message string // 错误消息
	Cause   error  // 原因错误
}

func (e *BucketError) Error() string {
	if e.Cause != nil {
		return e.Type + ": " + e.Message + " (caused by: " + e.Cause.Error() + ")"
	}
	return e.Type + ": " + e.Message
}

// 常见错误类型
const (
	ErrTypeNotFound      = "not_found"
	ErrTypeQuotaExhausted = "quota_exhausted"
	ErrTypeTimeout       = "timeout"
	ErrTypeConcurrency   = "concurrency"
	ErrTypeStorage       = "storage"
	ErrTypeInvalidInput  = "invalid_input"
)

// Helper functions

// NewTimeWindow 创建时间窗口
func NewTimeWindow(start time.Time, size time.Duration) TimeWindow {
	return TimeWindow{
		Start: start,
		End:   start.Add(size),
		Size:  size,
	}
}

// CurrentMinuteWindow 获取当前分钟的时间窗口
func CurrentMinuteWindow() TimeWindow {
	now := time.Now().UTC()
	start := now.Truncate(time.Minute)
	return NewTimeWindow(start, time.Minute)
}

// CurrentHourWindow 获取当前小时的时间窗口
func CurrentHourWindow() TimeWindow {
	now := time.Now().UTC()
	start := now.Truncate(time.Hour)
	return NewTimeWindow(start, time.Hour)
}

// CurrentDayWindow 获取当前天的时间窗口
func CurrentDayWindow() TimeWindow {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return NewTimeWindow(start, 24*time.Hour)
}

// IsInWindow 检查时间是否在窗口内
func (w TimeWindow) IsInWindow(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

// Contains 检查是否包含另一个窗口
func (w TimeWindow) Contains(other TimeWindow) bool {
	return !other.Start.Before(w.Start) && !other.End.After(w.End)
}
