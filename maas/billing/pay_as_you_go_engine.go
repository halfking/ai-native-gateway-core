package billing

import (
	"context"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// PayAsYouGoEngine 实现按量付费计费模式。
//
// 计费公式：
//   credits_charged × cents_per_credit / 100 → revenue (bill currency)
//   revenue (bill currency) → revenue_usd (via FX conversion)
//   revenue_usd - upstream_cost_usd → gross_margin_usd
//
// 特性：
//   - 完全按实际使用量计费，无固定费用
//   - 支持多币种计费（USD, CNY, EUR等）
//   - 自动汇率转换（用于毛利计算）
//   - 包含AF-1修复：正确处理非USD货币
//
// 配置参数：
//   - CentsPerCredit: 每信用点的价格（以分为单位）
//   - Currency: 账单货币（如 "CNY", "USD"）
type PayAsYouGoEngine struct {
	// CentsPerCredit 是每个信用点的价格（以分为单位）
	// 例如：2.5 表示每个信用点收费 2.5 分（0.025 元）
	CentsPerCredit float64

	// Currency 是账单货币代码（ISO 4217）
	// 例如："CNY", "USD", "EUR"
	Currency string

	// Name_表示引擎实例的名称（可选，用于调试）
	Name_ string
}

// NewPayAsYouGoEngine 创建一个新的按量付费引擎
//
// 参数：
//   centsPerCredit: 每信用点的价格（分）
//   currency: 账单货币代码
//
// 示例：
//   engine := NewPayAsYouGoEngine(2.5, "CNY")
func NewPayAsYouGoEngine(centsPerCredit float64, currency string) *PayAsYouGoEngine {
	if currency == "" {
		currency = "USD" // 默认USD
	}

	return &PayAsYouGoEngine{
		CentsPerCredit: centsPerCredit,
		Currency:       currency,
		Name_:          fmt.Sprintf("pay_as_you_go_%s_%.2f", strings.ToLower(currency), centsPerCredit),
	}
}

// Name 返回引擎标识符
func (e *PayAsYouGoEngine) Name() string {
	if e.Name_ != "" {
		return e.Name_
	}
	return "pay_as_you_go"
}

// SupportedMode 返回支持的计费模式
func (e *PayAsYouGoEngine) SupportedMode() BillingMode {
	return ModePayAsYouGo
}

// Calculate 计算按量付费账单
//
// 实现核心计费公式（与 consumption_detail.go:128-146 对齐）：
//
// 1. 计算账单金额（账单货币）：
//    revenueBillMajor = credits_charged × cents_per_credit / 100
//
// 2. 转换为USD（用于毛利计算）：
//    - 如果是USD：直接使用
//    - 如果是其他货币：通过 ConvertToUSD 转换
//    - CNY特殊处理：失败时回退到 CNYToUSDFX
//
// 3. 计算毛利：
//    gross_margin_usd = revenue_usd - upstream_cost_usd
//    margin_rate = gross_margin_usd / revenue_usd
func (e *PayAsYouGoEngine) Calculate(ctx context.Context, consumption *Consumption) (*BillingResult, error) {
	if consumption == nil {
		return nil, fmt.Errorf("consumption cannot be nil")
	}

	// 验证配置
	if e.CentsPerCredit <= 0 {
		return nil, fmt.Errorf("cents_per_credit must be positive, got: %.4f", e.CentsPerCredit)
	}

	// Step 1: 计算账单金额（账单货币单位）
	revenueBillMajor := float64(consumption.CreditsCharged) * e.CentsPerCredit / 100.0

	// Step 2: 转换为USD（用于统一的毛利计算）
	revenueUSD := revenueBillMajor

	if e.Currency != "" && !strings.EqualFold(e.Currency, "USD") {
		// 尝试使用通用汇率转换服务
		usdRevenue, err := settings.ConvertToUSD(revenueBillMajor, e.Currency)
		if err == nil {
			revenueUSD = usdRevenue
		} else if strings.EqualFold(e.Currency, "CNY") {
			// CNY 回退策略（向后兼容）
			revenueUSD = revenueBillMajor / settings.CNYToUSDFX
		} else {
			// 其他货币转换失败：记录但继续（使用原值）
			// 在生产环境中，这会导致毛利计算不准确
			// TODO: 考虑是否应该返回错误
			revenueUSD = revenueBillMajor
		}
	}

	// Step 3: 聚合上游成本（从模型明细中汇总）
	var totalUpstreamCostUSD float64
	for _, model := range consumption.ModelBreakdown {
		totalUpstreamCostUSD += model.UpstreamCostUSD
	}

	// Step 4: 计算毛利
	grossMarginUSD := revenueUSD - totalUpstreamCostUSD
	marginRate := 0.0
	if revenueUSD != 0 {
		marginRate = grossMarginUSD / revenueUSD
	}

	// 构建账单明细
	breakdown := []LineItem{
		{
			Description: fmt.Sprintf("API Usage (%d credits)", consumption.CreditsCharged),
			Quantity:    float64(consumption.CreditsCharged),
			UnitPrice:   e.CentsPerCredit / 100.0, // 转换为主货币单位
			Amount:      revenueBillMajor,
			Category:    "usage",
		},
	}

	// 如果有模型明细，添加到breakdown
	if len(consumption.ModelBreakdown) > 0 {
		for _, model := range consumption.ModelBreakdown {
			modelRevenue := float64(model.CreditsCharged) * e.CentsPerCredit / 100.0
			breakdown = append(breakdown, LineItem{
				Description: fmt.Sprintf("Model: %s (%d credits)", model.ModelName, model.CreditsCharged),
				Quantity:    float64(model.CreditsCharged),
				UnitPrice:   e.CentsPerCredit / 100.0,
				Amount:      modelRevenue,
				Category:    "model_usage",
			})
		}
	}

	// 构建结果
	result := &BillingResult{
		TotalAmount: revenueBillMajor,
		Currency:    e.Currency,
		Breakdown:   breakdown,
		EngineName:  e.Name(),
		BillingMode: ModePayAsYouGo,
		Metadata: map[string]interface{}{
			"credits_charged":       consumption.CreditsCharged,
			"cents_per_credit":      e.CentsPerCredit,
			"revenue_usd":           revenueUSD,
			"upstream_cost_usd":     totalUpstreamCostUSD,
			"gross_margin_usd":      grossMarginUSD,
			"gross_margin_rate":     marginRate,
			"request_count":         consumption.RequestCount,
			"period_start":          consumption.PeriodStart.Format("2006-01-02"),
			"period_end":            consumption.PeriodEnd.Format("2006-01-02"),
			"fx_conversion_applied": e.Currency != "USD",
		},
	}

	return result, nil
}

// Validate 验证计费配置
//
// 检查：
//   - CentsPerCredit 必须大于0
//   - Currency 必须是支持的货币代码
func (e *PayAsYouGoEngine) Validate(config *BillingConfig) error {
	if config == nil {
		return &ValidationError{Field: "config", Message: "config cannot be nil"}
	}

	if config.BillingMode != ModePayAsYouGo {
		return &ValidationError{
			Field:   "billing_mode",
			Message: fmt.Sprintf("expected pay_as_you_go, got %s", config.BillingMode),
		}
	}

	// 验证 CentsPerCredit
	centsPerCredit, ok := config.EngineConfig["cents_per_credit"].(float64)
	if !ok {
		return &ValidationError{
			Field:   "cents_per_credit",
			Message: "cents_per_credit must be a number",
		}
	}

	if centsPerCredit <= 0 {
		return &ValidationError{
			Field:   "cents_per_credit",
			Message: fmt.Sprintf("cents_per_credit must be positive, got %.4f", centsPerCredit),
		}
	}

	// 验证 Currency（如果提供）
	if currency, ok := config.EngineConfig["currency"].(string); ok && currency != "" {
		// 检查是否是支持的货币
		supportedCurrencies := settings.SupportedCurrencies()
		found := false
		for _, c := range supportedCurrencies {
			if strings.EqualFold(c, currency) {
				found = true
				break
			}
		}
		if !found {
			return &ValidationError{
				Field:   "currency",
				Message: fmt.Sprintf("unsupported currency: %s", currency),
			}
		}
	}

	return nil
}

// 便捷构造函数

// NewPayAsYouGoCNYEngine 创建人民币按量付费引擎
func NewPayAsYouGoCNYEngine(centsPerCredit float64) *PayAsYouGoEngine {
	return NewPayAsYouGoEngine(centsPerCredit, "CNY")
}

// NewPayAsYouGoUSDEngine 创建美元按量付费引擎
func NewPayAsYouGoUSDEngine(centsPerCredit float64) *PayAsYouGoEngine {
	return NewPayAsYouGoEngine(centsPerCredit, "USD")
}

// FromConfig 从通用配置创建按量付费引擎
//
// 配置格式：
//   {
//     "billing_mode": "pay_as_you_go",
//     "engine_config": {
//       "cents_per_credit": 2.5,
//       "currency": "CNY"
//     }
//   }
func FromConfig(config *BillingConfig) (*PayAsYouGoEngine, error) {
	if config == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	centsPerCredit, ok := config.EngineConfig["cents_per_credit"].(float64)
	if !ok {
		return nil, fmt.Errorf("cents_per_credit must be a number")
	}

	currency := "USD" // 默认
	if c, ok := config.EngineConfig["currency"].(string); ok && c != "" {
		currency = c
	}

	engine := NewPayAsYouGoEngine(centsPerCredit, currency)

	// 验证配置
	if err := engine.Validate(config); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return engine, nil
}
