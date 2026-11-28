package billing

import (
	"context"
	"fmt"
	"time"
)

// TokenPlanEngine 实现包月套餐计费模式
//
// 计费逻辑：
//   - 固定月费（无论是否使用）
//   - 套餐内配额免费
//   - 超额部分按量计费
//
// 示例：
//   套餐：1000万tokens/月，月费¥1000
//   使用：1200万tokens
//   计费：¥1000（基础月费）+ 200万 × 超额费率
//
// 适用场景：
//   - 稳定的中大型客户
//   - 可预测的使用量
//   - 希望成本可控
type TokenPlanEngine struct {
	// PlanID 套餐ID（关联billing_plans表）
	PlanID int64

	// PlanName 套餐名称（用于展示）
	PlanName string

	// MonthlyQuota 月度配额（tokens或credits）
	MonthlyQuota int64

	// BasePrice 基础月费（分）
	BasePrice int64

	// OverageRate 超额费率（每个token/credit的价格，分）
	// 例如：0.015 表示每个token/credit收费0.015分
	OverageRate float64

	// Currency 货币
	Currency string

	// 配额管理器（用于跟踪实时使用量）
	quotaManager QuotaManager
}

// QuotaManager 配额管理接口
//
// 负责跟踪和管理租户的配额使用情况。
// 生产环境应使用Redis实现，确保高性能和实时性。
type QuotaManager interface {
	// GetUsage 获取当前周期的使用量
	GetUsage(ctx context.Context, tenantID string, period TimePeriod) (*QuotaUsage, error)

	// RecordUsage 记录使用量
	RecordUsage(ctx context.Context, tenantID string, amount int64) error

	// ResetQuota 重置配额（周期开始时调用）
	ResetQuota(ctx context.Context, tenantID string, period TimePeriod) error

	// GetRemainingQuota 获取剩余配额
	GetRemainingQuota(ctx context.Context, tenantID string, period TimePeriod) (int64, error)
}

// QuotaUsage 配额使用情况
type QuotaUsage struct {
	TenantID      string
	PeriodStart   time.Time
	PeriodEnd     time.Time
	TotalQuota    int64 // 总配额
	UsedQuota     int64 // 已使用
	RemainingQuota int64 // 剩余
	OverageAmount int64 // 超额量
}

// TimePeriod 时间周期
type TimePeriod struct {
	Start time.Time
	End   time.Time
}

// NewTokenPlanEngine 创建包月套餐引擎
func NewTokenPlanEngine(planID int64, planName string, monthlyQuota, basePrice int64, overageRate float64, currency string, quotaManager QuotaManager) *TokenPlanEngine {
	if currency == "" {
		currency = "CNY" // 套餐制默认人民币
	}

	return &TokenPlanEngine{
		PlanID:       planID,
		PlanName:     planName,
		MonthlyQuota: monthlyQuota,
		BasePrice:    basePrice,
		OverageRate:  overageRate,
		Currency:     currency,
		quotaManager: quotaManager,
	}
}

// Name 返回引擎标识符
func (e *TokenPlanEngine) Name() string {
	return fmt.Sprintf("token_plan_%d", e.PlanID)
}

// SupportedMode 返回支持的计费模式
func (e *TokenPlanEngine) SupportedMode() BillingMode {
	return ModeTokenPlan
}

// Calculate 计算包月套餐账单
//
// 计费逻辑：
// 1. 基础月费（固定）
// 2. 获取本周期使用量
// 3. 计算超额部分
// 4. 超额费用 = 超额量 × 超额费率
// 5. 总费用 = 基础月费 + 超额费用
func (e *TokenPlanEngine) Calculate(ctx context.Context, consumption *Consumption) (*BillingResult, error) {
	if consumption == nil {
		return nil, fmt.Errorf("consumption cannot be nil")
	}

	// 验证配置
	if e.MonthlyQuota <= 0 {
		return nil, fmt.Errorf("monthly_quota must be positive, got: %d", e.MonthlyQuota)
	}

	if e.BasePrice < 0 {
		return nil, fmt.Errorf("base_price cannot be negative, got: %d", e.BasePrice)
	}

	// 1. 基础月费（转换为主货币单位）
	baseFee := float64(e.BasePrice) / 100.0

	// 2. 获取本周期使用量
	period := TimePeriod{
		Start: consumption.PeriodStart,
		End:   consumption.PeriodEnd,
	}

	var totalUsed int64
	if e.quotaManager != nil {
		usage, err := e.quotaManager.GetUsage(ctx, consumption.TenantID, period)
		if err != nil {
			// 配额管理器失败，降级到consumption中的数据
			totalUsed = consumption.CreditsCharged
		} else {
			totalUsed = usage.UsedQuota
		}
	} else {
		// 无配额管理器，使用consumption数据
		totalUsed = consumption.CreditsCharged
	}

	// 3. 计算超额部分
	var overageAmount int64
	var overageFee float64

	if totalUsed > e.MonthlyQuota {
		overageAmount = totalUsed - e.MonthlyQuota
		overageFee = float64(overageAmount) * e.OverageRate / 100.0
	}

	// 4. 总费用
	totalAmount := baseFee + overageFee

	// 5. 构建账单明细
	breakdown := []LineItem{
		{
			Description: fmt.Sprintf("套餐月费 - %s", e.PlanName),
			Quantity:    1,
			UnitPrice:   baseFee,
			Amount:      baseFee,
			Category:    "base_fee",
		},
	}

	// 配额使用明细
	quotaUsageDesc := fmt.Sprintf("配额使用: %d / %d", totalUsed, e.MonthlyQuota)
	breakdown = append(breakdown, LineItem{
		Description: quotaUsageDesc,
		Quantity:    float64(totalUsed),
		UnitPrice:   0, // 套餐内免费
		Amount:      0,
		Category:    "quota_usage",
	})

	// 超额费用明细
	if overageAmount > 0 {
		breakdown = append(breakdown, LineItem{
			Description: fmt.Sprintf("超额使用: %d (超额费率: %.4f分/单位)", overageAmount, e.OverageRate),
			Quantity:    float64(overageAmount),
			UnitPrice:   e.OverageRate / 100.0,
			Amount:      overageFee,
			Category:    "overage",
		})
	}

	// 6. 构建结果
	result := &BillingResult{
		TotalAmount: totalAmount,
		Currency:    e.Currency,
		Breakdown:   breakdown,
		EngineName:  e.Name(),
		BillingMode: ModeTokenPlan,
		Metadata: map[string]interface{}{
			"plan_id":         e.PlanID,
			"plan_name":       e.PlanName,
			"monthly_quota":   e.MonthlyQuota,
			"total_used":      totalUsed,
			"overage_amount":  overageAmount,
			"base_fee":        baseFee,
			"overage_fee":     overageFee,
			"quota_remaining": e.MonthlyQuota - totalUsed,
			"quota_utilization": float64(totalUsed) / float64(e.MonthlyQuota) * 100,
			"period_start":    consumption.PeriodStart.Format("2006-01-02"),
			"period_end":      consumption.PeriodEnd.Format("2006-01-02"),
		},
	}

	return result, nil
}

// Validate 验证计费配置
func (e *TokenPlanEngine) Validate(config *BillingConfig) error {
	if config == nil {
		return &ValidationError{Field: "config", Message: "config cannot be nil"}
	}

	if config.BillingMode != ModeTokenPlan {
		return &ValidationError{
			Field:   "billing_mode",
			Message: fmt.Sprintf("expected token_plan, got %s", config.BillingMode),
		}
	}

	// 验证PlanID
	if config.PlanID == nil || *config.PlanID <= 0 {
		return &ValidationError{
			Field:   "plan_id",
			Message: "plan_id is required and must be positive",
		}
	}

	// 验证EngineConfig中的必需字段
	requiredFields := []string{"monthly_quota", "base_price", "overage_rate"}
	for _, field := range requiredFields {
		if _, exists := config.EngineConfig[field]; !exists {
			return &ValidationError{
				Field:   field,
				Message: fmt.Sprintf("%s is required", field),
			}
		}
	}

	// 验证monthly_quota
	monthlyQuota, ok := config.EngineConfig["monthly_quota"].(float64)
	if !ok {
		return &ValidationError{
			Field:   "monthly_quota",
			Message: "monthly_quota must be a number",
		}
	}
	if monthlyQuota <= 0 {
		return &ValidationError{
			Field:   "monthly_quota",
			Message: fmt.Sprintf("monthly_quota must be positive, got %.0f", monthlyQuota),
		}
	}

	// 验证base_price
	basePrice, ok := config.EngineConfig["base_price"].(float64)
	if !ok {
		return &ValidationError{
			Field:   "base_price",
			Message: "base_price must be a number",
		}
	}
	if basePrice < 0 {
		return &ValidationError{
			Field:   "base_price",
			Message: fmt.Sprintf("base_price cannot be negative, got %.0f", basePrice),
		}
	}

	// 验证overage_rate
	overageRate, ok := config.EngineConfig["overage_rate"].(float64)
	if !ok {
		return &ValidationError{
			Field:   "overage_rate",
			Message: "overage_rate must be a number",
		}
	}
	if overageRate < 0 {
		return &ValidationError{
			Field:   "overage_rate",
			Message: fmt.Sprintf("overage_rate cannot be negative, got %.4f", overageRate),
		}
	}

	return nil
}

// FromConfigTokenPlan 从配置创建套餐引擎
func FromConfigTokenPlan(config *BillingConfig, quotaManager QuotaManager) (*TokenPlanEngine, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	if config.PlanID == nil {
		return nil, fmt.Errorf("plan_id is required")
	}

	// 提取配置
	monthlyQuota := int64(config.EngineConfig["monthly_quota"].(float64))
	basePrice := int64(config.EngineConfig["base_price"].(float64))
	overageRate := config.EngineConfig["overage_rate"].(float64)

	planName := "Standard Plan"
	if name, ok := config.EngineConfig["plan_name"].(string); ok {
		planName = name
	}

	currency := "CNY"
	if curr, ok := config.EngineConfig["currency"].(string); ok && curr != "" {
		currency = curr
	}

	engine := NewTokenPlanEngine(
		*config.PlanID,
		planName,
		monthlyQuota,
		basePrice,
		overageRate,
		currency,
		quotaManager,
	)

	// 验证配置
	if err := engine.Validate(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return engine, nil
}

// EstimateMonthlyBill 估算月度账单（用于展示给客户）
//
// 根据预估使用量计算账单，帮助客户选择合适的套餐。
func (e *TokenPlanEngine) EstimateMonthlyBill(estimatedUsage int64) *BillingResult {
	baseFee := float64(e.BasePrice) / 100.0
	
	var overageAmount int64
	var overageFee float64

	if estimatedUsage > e.MonthlyQuota {
		overageAmount = estimatedUsage - e.MonthlyQuota
		overageFee = float64(overageAmount) * e.OverageRate / 100.0
	}

	totalAmount := baseFee + overageFee

	breakdown := []LineItem{
		{
			Description: fmt.Sprintf("套餐月费 - %s", e.PlanName),
			Quantity:    1,
			UnitPrice:   baseFee,
			Amount:      baseFee,
			Category:    "base_fee",
		},
	}

	if overageAmount > 0 {
		breakdown = append(breakdown, LineItem{
			Description: fmt.Sprintf("预估超额: %d", overageAmount),
			Quantity:    float64(overageAmount),
			UnitPrice:   e.OverageRate / 100.0,
			Amount:      overageFee,
			Category:    "overage_estimate",
		})
	}

	return &BillingResult{
		TotalAmount: totalAmount,
		Currency:    e.Currency,
		Breakdown:   breakdown,
		EngineName:  e.Name(),
		BillingMode: ModeTokenPlan,
		Metadata: map[string]interface{}{
			"plan_id":             e.PlanID,
			"plan_name":           e.PlanName,
			"monthly_quota":       e.MonthlyQuota,
			"estimated_usage":     estimatedUsage,
			"estimated_overage":   overageAmount,
			"quota_utilization":   float64(estimatedUsage) / float64(e.MonthlyQuota) * 100,
			"is_estimate":         true,
		},
	}
}
