package billing

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// BoosterPackEngine 实现增值包计费模式
//
// 计费逻辑：
//   - 客户预先购买增值包（如100万tokens，¥800）
//   - 使用时优先从增值包扣减
//   - 增值包耗尽后，按量付费或停止服务
//   - 支持多个增值包并存
//
// 扣减策略：
//   - FIFO: 先购买的先使用
//   - ExpireFirst: 先过期的先使用
//
// 适用场景：
//   - 预付费客户
//   - 成本敏感型客户
//   - 不确定使用量的客户
type BoosterPackEngine struct {
	// DeductionStrategy 扣减策略
	DeductionStrategy string // "fifo" 或 "expire_first"

	// FallbackMode 增值包耗尽后的处理模式
	// - "pay_as_you_go": 自动切换到按量付费
	// - "stop": 停止服务
	// - "credit": 允许负余额（赊账）
	FallbackMode string

	// FallbackRate 回退到按量付费时的费率（分/token）
	FallbackRate float64

	// Currency 货币
	Currency string

	// 增值包管理器
	packManager PackManager
}

// PackManager 增值包管理接口
//
// 负责管理租户的增值包库存和扣减。
type PackManager interface {
	// GetPacks 获取租户的所有有效增值包
	GetPacks(ctx context.Context, tenantID string) ([]*Pack, error)

	// DeductFromPacks 从增值包扣减
	// 返回：实际扣减量，剩余需扣减量
	DeductFromPacks(ctx context.Context, tenantID string, amount int64, strategy string) (deducted int64, remaining int64, err error)

	// GetTotalRemaining 获取总剩余量
	GetTotalRemaining(ctx context.Context, tenantID string) (int64, error)

	// AddPack 购买新增值包
	AddPack(ctx context.Context, pack *Pack) error

	// ExpirePacks 过期增值包
	ExpirePacks(ctx context.Context, tenantID string) error
}

// Pack 表示一个增值包
type Pack struct {
	PackID        int64     // 增值包ID
	TenantID      string    // 租户ID
	PackType      string    // 类型：token_pack, credit_pack
	QuotaAmount   int64     // 总配额
	RemainingAmount int64   // 剩余配额
	PriceCents    int64     // 购买价格（分）
	Currency      string    // 货币
	PurchasedAt   time.Time // 购买时间
	ExpiresAt     *time.Time // 过期时间（可选）
	Status        string    // 状态：active, depleted, expired
}

// NewBoosterPackEngine 创建增值包引擎
func NewBoosterPackEngine(deductionStrategy, fallbackMode string, fallbackRate float64, currency string, packManager PackManager) *BoosterPackEngine {
	if deductionStrategy == "" {
		deductionStrategy = "fifo" // 默认FIFO
	}

	if fallbackMode == "" {
		fallbackMode = "pay_as_you_go" // 默认按量付费
	}

	if currency == "" {
		currency = "CNY"
	}

	return &BoosterPackEngine{
		DeductionStrategy: deductionStrategy,
		FallbackMode:      fallbackMode,
		FallbackRate:      fallbackRate,
		Currency:          currency,
		packManager:       packManager,
	}
}

// Name 返回引擎标识符
func (e *BoosterPackEngine) Name() string {
	return "booster_pack"
}

// SupportedMode 返回支持的计费模式
func (e *BoosterPackEngine) SupportedMode() BillingMode {
	return ModeBoosterPack
}

// Calculate 计算增值包账单
//
// 计费逻辑：
// 1. 获取本周期使用量
// 2. 从增值包扣减
// 3. 如果有剩余，根据fallbackMode处理
//    - pay_as_you_go: 计算按量付费金额
//    - stop: 返回错误
//    - credit: 允许负余额
func (e *BoosterPackEngine) Calculate(ctx context.Context, consumption *Consumption) (*BillingResult, error) {
	if consumption == nil {
		return nil, fmt.Errorf("consumption cannot be nil")
	}

	if e.packManager == nil {
		return nil, fmt.Errorf("pack manager not configured")
	}

	usageAmount := consumption.CreditsCharged

	// 1. 从增值包扣减
	deducted, remaining, err := e.packManager.DeductFromPacks(
		ctx,
		consumption.TenantID,
		usageAmount,
		e.DeductionStrategy,
	)
	if err != nil {
		return nil, fmt.Errorf("deduct from packs: %w", err)
	}

	// 2. 构建账单明细
	breakdown := []LineItem{}

	// 增值包抵扣明细
	if deducted > 0 {
		breakdown = append(breakdown, LineItem{
			Description: fmt.Sprintf("增值包抵扣: %d", deducted),
			Quantity:    float64(deducted),
			UnitPrice:   0, // 增值包已预付费
			Amount:      0,
			Category:    "pack_deduction",
		})
	}

	// 3. 处理剩余部分
	var totalAmount float64
	var fallbackFee float64

	if remaining > 0 {
		switch e.FallbackMode {
		case "pay_as_you_go":
			// 按量付费
			fallbackFee = float64(remaining) * e.FallbackRate / 100.0
			totalAmount = fallbackFee

			breakdown = append(breakdown, LineItem{
				Description: fmt.Sprintf("超出增值包部分: %d (按量付费)", remaining),
				Quantity:    float64(remaining),
				UnitPrice:   e.FallbackRate / 100.0,
				Amount:      fallbackFee,
				Category:    "fallback_payg",
			})

		case "stop":
			return nil, fmt.Errorf("insufficient pack balance: need %d, only have %d", usageAmount, deducted)

		case "credit":
			// 允许负余额（赊账）
			breakdown = append(breakdown, LineItem{
				Description: fmt.Sprintf("赊账: %d", remaining),
				Quantity:    float64(remaining),
				UnitPrice:   0,
				Amount:      0,
				Category:    "credit_overdraft",
			})
		}
	}

	// 4. 获取当前剩余总量
	totalRemaining, _ := e.packManager.GetTotalRemaining(ctx, consumption.TenantID)

	// 5. 构建结果
	result := &BillingResult{
		TotalAmount: totalAmount,
		Currency:    e.Currency,
		Breakdown:   breakdown,
		EngineName:  e.Name(),
		BillingMode: ModeBoosterPack,
		Metadata: map[string]interface{}{
			"usage_amount":        usageAmount,
			"deducted_from_packs": deducted,
			"fallback_amount":     remaining,
			"fallback_fee":        fallbackFee,
			"fallback_mode":       e.FallbackMode,
			"total_remaining":     totalRemaining,
			"deduction_strategy":  e.DeductionStrategy,
			"period_start":        consumption.PeriodStart.Format("2006-01-02"),
			"period_end":          consumption.PeriodEnd.Format("2006-01-02"),
		},
	}

	return result, nil
}

// Validate 验证计费配置
func (e *BoosterPackEngine) Validate(config *BillingConfig) error {
	if config == nil {
		return &ValidationError{Field: "config", Message: "config cannot be nil"}
	}

	if config.BillingMode != ModeBoosterPack {
		return &ValidationError{
			Field:   "billing_mode",
			Message: fmt.Sprintf("expected booster_pack, got %s", config.BillingMode),
		}
	}

	// 验证扣减策略
	if strategy, ok := config.EngineConfig["deduction_strategy"].(string); ok {
		if strategy != "fifo" && strategy != "expire_first" {
			return &ValidationError{
				Field:   "deduction_strategy",
				Message: fmt.Sprintf("invalid strategy: %s (must be 'fifo' or 'expire_first')", strategy),
			}
		}
	}

	// 验证fallback模式
	if fallbackMode, ok := config.EngineConfig["fallback_mode"].(string); ok {
		validModes := map[string]bool{"pay_as_you_go": true, "stop": true, "credit": true}
		if !validModes[fallbackMode] {
			return &ValidationError{
				Field:   "fallback_mode",
				Message: fmt.Sprintf("invalid fallback_mode: %s", fallbackMode),
			}
		}

		// 如果是pay_as_you_go模式，必须提供fallback_rate
		if fallbackMode == "pay_as_you_go" {
			if _, ok := config.EngineConfig["fallback_rate"].(float64); !ok {
				return &ValidationError{
					Field:   "fallback_rate",
					Message: "fallback_rate is required when fallback_mode is pay_as_you_go",
				}
			}
		}
	}

	return nil
}

// FromConfigBoosterPack 从配置创建增值包引擎
func FromConfigBoosterPack(config *BillingConfig, packManager PackManager) (*BoosterPackEngine, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	deductionStrategy := "fifo"
	if s, ok := config.EngineConfig["deduction_strategy"].(string); ok {
		deductionStrategy = s
	}

	fallbackMode := "pay_as_you_go"
	if m, ok := config.EngineConfig["fallback_mode"].(string); ok {
		fallbackMode = m
	}

	fallbackRate := 0.015
	if r, ok := config.EngineConfig["fallback_rate"].(float64); ok {
		fallbackRate = r
	}

	currency := "CNY"
	if c, ok := config.EngineConfig["currency"].(string); ok && c != "" {
		currency = c
	}

	engine := NewBoosterPackEngine(deductionStrategy, fallbackMode, fallbackRate, currency, packManager)

	// 验证配置
	if err := engine.Validate(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return engine, nil
}

// InMemoryPackManager 内存增值包管理器（用于测试）
type InMemoryPackManager struct {
	packs map[string][]*Pack // tenantID -> packs
}

// NewInMemoryPackManager 创建内存增值包管理器
func NewInMemoryPackManager() *InMemoryPackManager {
	return &InMemoryPackManager{
		packs: make(map[string][]*Pack),
	}
}

// GetPacks 获取有效增值包
func (m *InMemoryPackManager) GetPacks(ctx context.Context, tenantID string) ([]*Pack, error) {
	packs := m.packs[tenantID]
	if packs == nil {
		return []*Pack{}, nil
	}

	// 过滤有效的增值包
	validPacks := []*Pack{}
	now := time.Now()
	for _, pack := range packs {
		if pack.Status != "active" {
			continue
		}
		if pack.RemainingAmount <= 0 {
			continue
		}
		if pack.ExpiresAt != nil && now.After(*pack.ExpiresAt) {
			continue
		}
		validPacks = append(validPacks, pack)
	}

	return validPacks, nil
}

// DeductFromPacks 从增值包扣减
func (m *InMemoryPackManager) DeductFromPacks(ctx context.Context, tenantID string, amount int64, strategy string) (int64, int64, error) {
	packs, err := m.GetPacks(ctx, tenantID)
	if err != nil {
		return 0, amount, err
	}

	if len(packs) == 0 {
		return 0, amount, nil // 没有增值包，全部需要fallback
	}

	// 根据策略排序
	m.sortPacks(packs, strategy)

	// 依次扣减
	totalDeducted := int64(0)
	remaining := amount

	for _, pack := range packs {
		if remaining <= 0 {
			break
		}

		deduct := remaining
		if deduct > pack.RemainingAmount {
			deduct = pack.RemainingAmount
		}

		pack.RemainingAmount -= deduct
		totalDeducted += deduct
		remaining -= deduct

		// 更新状态
		if pack.RemainingAmount <= 0 {
			pack.Status = "depleted"
		}
	}

	return totalDeducted, remaining, nil
}

// GetTotalRemaining 获取总剩余量
func (m *InMemoryPackManager) GetTotalRemaining(ctx context.Context, tenantID string) (int64, error) {
	packs, err := m.GetPacks(ctx, tenantID)
	if err != nil {
		return 0, err
	}

	total := int64(0)
	for _, pack := range packs {
		total += pack.RemainingAmount
	}

	return total, nil
}

// AddPack 添加增值包
func (m *InMemoryPackManager) AddPack(ctx context.Context, pack *Pack) error {
	if pack == nil {
		return fmt.Errorf("pack cannot be nil")
	}

	m.packs[pack.TenantID] = append(m.packs[pack.TenantID], pack)
	return nil
}

// ExpirePacks 过期增值包
func (m *InMemoryPackManager) ExpirePacks(ctx context.Context, tenantID string) error {
	packs := m.packs[tenantID]
	now := time.Now()

	for _, pack := range packs {
		if pack.ExpiresAt != nil && now.After(*pack.ExpiresAt) {
			pack.Status = "expired"
		}
	}

	return nil
}

// sortPacks 根据策略排序增值包
func (m *InMemoryPackManager) sortPacks(packs []*Pack, strategy string) {
	switch strategy {
	case "fifo":
		// 按购买时间排序（早的在前）
		sort.Slice(packs, func(i, j int) bool {
			return packs[i].PurchasedAt.Before(packs[j].PurchasedAt)
		})

	case "expire_first":
		// 按过期时间排序（早过期的在前）
		sort.Slice(packs, func(i, j int) bool {
			// 没有过期时间的排在后面
			if packs[i].ExpiresAt == nil {
				return false
			}
			if packs[j].ExpiresAt == nil {
				return true
			}
			return packs[i].ExpiresAt.Before(*packs[j].ExpiresAt)
		})
	}
}
