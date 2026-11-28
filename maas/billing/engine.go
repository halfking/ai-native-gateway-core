package billing

import (
	"context"
	"time"
)

// BillingMode 表示计费模式
type BillingMode string

const (
	// ModePayAsYouGo 按量付费模式 - 根据实际使用量计费
	ModePayAsYouGo BillingMode = "pay_as_you_go"

	// ModeTokenPlan 包月套餐模式 - 固定月费 + 配额 + 超额计费
	ModeTokenPlan BillingMode = "token_plan"

	// ModeBoosterPack 增值包模式 - 预付费包 + 扣减使用
	ModeBoosterPack BillingMode = "booster_pack"

	// ModeCustom 自定义模式 - 支持自定义计费规则
	ModeCustom BillingMode = "custom"
)

// BillingEngine 是计费引擎的核心接口。
// 每个计费模式（按量付费、套餐制、增值包等）都需要实现此接口。
//
// 设计原则：
// - 接口职责单一：只负责计费计算
// - 无状态设计：每次调用独立，便于并发
// - 可测试性：输入输出明确，易于单元测试
type BillingEngine interface {
	// Name 返回计费引擎的唯一标识符
	//
	// 返回值示例: "pay_as_you_go_v1", "token_plan_standard"
	Name() string

	// SupportedMode 返回此引擎支持的计费模式
	SupportedMode() BillingMode

	// Calculate 根据消费数据计算账单金额
	//
	// 参数:
	//   ctx: 上下文，用于超时控制和取消
	//   consumption: 消费数据，包含时间段、用量、模型分布等
	//
	// 返回:
	//   result: 计费结果，包含总金额、明细、折扣等
	//   error: 计算失败时返回错误
	//
	// 注意:
	//   - 实现必须是线程安全的
	//   - 应当处理边界情况（零消费、负值等）
	//   - 货币转换应使用提供的FX服务
	Calculate(ctx context.Context, consumption *Consumption) (*BillingResult, error)

	// Validate 验证计费配置是否有效
	//
	// 在应用配置前调用此方法进行预检查。
	// 返回 nil 表示配置有效，否则返回描述性错误。
	Validate(config *BillingConfig) error
}

// Consumption 表示一个时间段内的消费数据。
// 这是所有计费引擎的通用输入结构。
type Consumption struct {
	// 租户标识
	TenantID string

	// 计费周期
	PeriodStart time.Time
	PeriodEnd   time.Time

	// 聚合用量（兼容现有系统）
	CreditsCharged int64 // 总信用点消耗
	RequestCount   int64 // 请求总数

	// Token级别用量
	TokenUsage TokenUsage

	// 按模型分解的用量（用于细粒度计费）
	ModelBreakdown []ModelConsumption

	// 元数据（扩展用）
	Metadata map[string]interface{}
}

// TokenUsage 表示token级别的使用情况
type TokenUsage struct {
	PromptTokens     int64 // 输入tokens
	CompletionTokens int64 // 输出tokens
	CacheReadTokens  int64 // 缓存读取tokens
	CacheWriteTokens int64 // 缓存写入tokens
	TotalTokens      int64 // 总tokens
}

// ModelConsumption 表示单个模型的消费数据
type ModelConsumption struct {
	ModelID        string
	ModelName      string
	ProviderID     int64
	ProviderName   string
	RequestCount   int64
	CreditsCharged int64
	TokenUsage     TokenUsage
	UpstreamCostUSD float64 // 上游成本
}

// BillingResult 表示计费计算的结果
type BillingResult struct {
	// 账单总金额（主要货币单位，如元）
	TotalAmount float64

	// 货币代码 (ISO 4217: USD, CNY, EUR等)
	Currency string

	// 账单明细行（用于透明化账单）
	Breakdown []LineItem

	// 应用的折扣（用于展示优惠信息）
	AppliedDiscounts []Discount

	// 计费引擎名称（用于审计和调试）
	EngineName string

	// 计费模式
	BillingMode BillingMode

	// 元数据（引擎特定信息）
	Metadata map[string]interface{}
}

// LineItem 表示账单明细行
type LineItem struct {
	Description string  // 描述，如 "API调用费用"
	Quantity    float64 // 数量
	UnitPrice   float64 // 单价
	Amount      float64 // 小计金额
	Category    string  // 分类，如 "usage", "overage", "base_fee"
}

// Discount 表示应用的折扣
type Discount struct {
	Name        string  // 折扣名称
	Type        string  // 类型: "percentage", "fixed_amount"
	Value       float64 // 折扣值
	Amount      float64 // 折扣金额（实际减免）
	Description string  // 说明
}

// BillingConfig 是计费配置的通用结构
type BillingConfig struct {
	// 计费模式
	Mode BillingMode

	// 模式特定配置（JSON格式）
	// 不同引擎解析成不同的结构
	ConfigJSON map[string]interface{}

	// 生效时间
	EffectiveFrom time.Time

	// 货币
	Currency string
}

// ValidationError 表示配置验证错误
type ValidationError struct {
	Field   string // 错误字段
	Message string // 错误消息
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}
