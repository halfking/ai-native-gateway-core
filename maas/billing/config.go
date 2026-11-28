package billing

import (
	"context"
	"time"
)

// BillingConfigLoader 负责从各种来源加载计费配置。
//
// 设计要点：
// - 支持多种查询方式（API Key、租户ID、用户ID）
// - 自动触发缓存回填
// - 支持批量预加载热门配置
type BillingConfigLoader interface {
	// LoadByAPIKey 通过API Key加载计费配置
	//
	// 这是最常用的查询路径，每个请求都会调用。
	// 实现应当与缓存系统紧密集成，优先从缓存读取。
	LoadByAPIKey(ctx context.Context, apiKey string) (*BillingConfig, error)

	// LoadByTenantID 通过租户ID加载计费配置
	//
	// 用于管理后台或批量操作。
	LoadByTenantID(ctx context.Context, tenantID string) (*BillingConfig, error)

	// LoadByUserID 通过用户ID加载计费配置
	//
	// 用于用户级别的计费查询。
	LoadByUserID(ctx context.Context, userID string) (*BillingConfig, error)

	// PreloadHotConfigs 预加载热门配置到缓存
	//
	// 基于请求统计自动识别热门API Key，提前加载到缓存。
	// limit: 最多加载多少个配置
	PreloadHotConfigs(ctx context.Context, limit int) error

	// InvalidateCache 失效指定的缓存
	//
	// 用于配置更新后强制刷新。
	InvalidateCache(ctx context.Context, tenantID string) error
}

// BillingConfig 表示一个租户/用户的完整计费配置。
//
// 这是计费系统的核心数据结构，包含：
// - 计费模式选择
// - 引擎特定参数
// - 折扣规则
// - 配额限制
// - 缓存策略提示
type BillingConfig struct {
	// 基础标识
	ID       int64  // 配置ID
	TenantID string // 租户ID
	UserID   string // 用户ID（可选，为空表示租户级配置）

	// 计费模式
	BillingMode BillingMode // pay_as_you_go, token_plan, booster_pack

	// 套餐关联（可选）
	PlanID *int64 // 关联的套餐ID

	// 引擎配置（JSON格式，不同引擎解析不同）
	EngineConfig map[string]interface{}

	// 折扣规则
	DiscountRules []DiscountRule

	// 配额限制
	QuotaLimits *QuotaLimits

	// 缓存策略提示
	CacheHint CacheHint

	// 时间戳
	CreatedAt time.Time
	UpdatedAt time.Time
	LoadedAt  time.Time // 加载到内存的时间
}

// DiscountRule 表示一个折扣规则
type DiscountRule struct {
	ID          int64   // 规则ID
	Type        string  // percentage, fixed_amount, campaign
	Value       float64 // 折扣值（百分比或固定金额）
	Condition   string  // 生效条件（如 "volume > 10000000"）
	Priority    int     // 优先级（数字越小优先级越高）
	ValidFrom   time.Time
	ValidUntil  time.Time
	Description string
}

// QuotaLimits 表示配额限制
type QuotaLimits struct {
	DailyMax   *int64 // 每日最大使用量（credits）
	MonthlyMax *int64 // 每月最大使用量
	HourlyMax  *int64 // 每小时最大使用量
}

// CacheHint 提供缓存策略建议
//
// 用于指导缓存系统如何处理这个配置：
// - 高优先级配置保持更长时间
// - 预加载提示标记需要主动加载的配置
type CacheHint struct {
	// Priority 缓存优先级 (1-10)
	// 10 = 最高优先级，永不驱逐
	// 5 = 普通优先级
	// 1 = 低优先级，优先驱逐
	Priority int

	// TTL 缓存时长
	// 超过此时间后需要重新加载
	TTL time.Duration

	// PreloadHint 是否应该被预加载
	// true = 系统启动时或定期预加载
	// false = 惰性加载
	PreloadHint bool
}

// BillingConfigCache 是计费配置的多级缓存接口。
//
// 缓存层次结构：
// L1 (内存) -> L2 (Redis) -> L3 (数据库)
//
// 设计要点：
// - 透明的多级缓存穿透
// - 自动回填机制
// - LRU驱逐策略
// - 统计信息收集
type BillingConfigCache interface {
	// Get 获取配置（自动穿透多级缓存）
	//
	// 查找顺序：L1 -> L2 -> L3 (database)
	// 未命中时自动从下一级加载并回填
	Get(ctx context.Context, key string) (*BillingConfig, error)

	// Set 设置配置到缓存
	//
	// 同时写入L1和L2，确保一致性
	Set(ctx context.Context, key string, config *BillingConfig) error

	// Delete 从缓存中删除配置
	//
	// 同时从L1和L2删除
	Delete(ctx context.Context, key string) error

	// InvalidateTenant 失效租户的所有相关缓存
	//
	// 用于租户配置更新后的批量失效
	InvalidateTenant(ctx context.Context, tenantID string) error

	// PreloadBatch 批量预加载配置
	//
	// 用于系统启动或定期预加载热数据
	PreloadBatch(ctx context.Context, keys []string, loader func(key string) (*BillingConfig, error)) error

	// Stats 获取缓存统计信息
	//
	// 用于监控和性能分析
	Stats() CacheStats

	// Close 关闭缓存（清理资源）
	Close() error
}

// CacheStats 缓存统计信息
type CacheStats struct {
	// L1 (内存) 统计
	L1Size      int     // 当前条目数
	L1Capacity  int     // 最大容量
	L1HitRate   float64 // 命中率
	L1Hits      int64   // 命中次数
	L1Misses    int64   // 未命中次数
	L1Evictions int64   // 驱逐次数

	// L2 (Redis) 统计
	L2HitRate float64
	L2Hits    int64
	L2Misses  int64

	// 总体统计
	TotalRequests int64 // 总请求数
	TotalHitRate  float64

	// 性能统计
	AvgGetLatency time.Duration // 平均获取延迟
}

// CacheKey 缓存键生成器
//
// 统一的键命名规范，确保不同查询路径能复用缓存。
type CacheKey struct{}

// ForAPIKey 生成API Key的缓存键
func (CacheKey) ForAPIKey(apiKey string) string {
	return "billing:apikey:" + apiKey
}

// ForTenant 生成租户的缓存键
func (CacheKey) ForTenant(tenantID string) string {
	return "billing:tenant:" + tenantID
}

// ForUser 生成用户的缓存键
func (CacheKey) ForUser(tenantID, userID string) string {
	return "billing:user:" + tenantID + ":" + userID
}

// ConfigValidation 配置验证接口
type ConfigValidation interface {
	// Validate 验证配置有效性
	Validate(config *BillingConfig) error
}

// DefaultConfigValidator 默认配置验证器
type DefaultConfigValidator struct{}

// Validate 实现配置验证
func (v *DefaultConfigValidator) Validate(config *BillingConfig) error {
	if config == nil {
		return &ValidationError{Field: "config", Message: "config cannot be nil"}
	}

	if config.TenantID == "" {
		return &ValidationError{Field: "tenant_id", Message: "tenant_id is required"}
	}

	if config.BillingMode == "" {
		return &ValidationError{Field: "billing_mode", Message: "billing_mode is required"}
	}

	// 验证计费模式是否有效
	validModes := map[BillingMode]bool{
		ModePayAsYouGo:  true,
		ModeTokenPlan:   true,
		ModeBoosterPack: true,
		ModeCustom:      true,
	}

	if !validModes[config.BillingMode] {
		return &ValidationError{
			Field:   "billing_mode",
			Message: "invalid billing mode: " + string(config.BillingMode),
		}
	}

	return nil
}
