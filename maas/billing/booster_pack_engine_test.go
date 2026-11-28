package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewBoosterPackEngine tests engine creation
func TestNewBoosterPackEngine(t *testing.T) {
	packManager := NewInMemoryPackManager()

	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", packManager)

	assert.NotNil(t, engine)
	assert.Equal(t, "fifo", engine.DeductionStrategy)
	assert.Equal(t, "pay_as_you_go", engine.FallbackMode)
	assert.Equal(t, 0.02, engine.FallbackRate)
	assert.Equal(t, "CNY", engine.Currency)
	assert.Equal(t, ModeBoosterPack, engine.SupportedMode())
	assert.Equal(t, "booster_pack", engine.Name())
}

// TestBoosterPackEngine_Calculate_WithinPack tests usage within pack balance
func TestBoosterPackEngine_Calculate_WithinPack(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	// 添加一个100万tokens的增值包
	pack := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     1000000,
		RemainingAmount: 1000000,
		PriceCents:      80000, // 800元
		Currency:        "CNY",
		PurchasedAt:     time.Now().AddDate(0, 0, -1),
		Status:          "active",
	}
	packManager.AddPack(ctx, pack)

	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		PeriodStart:    time.Now().AddDate(0, 0, -30),
		PeriodEnd:      time.Now(),
		CreditsCharged: 500000, // 使用50万，增值包内
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 应该没有费用（全部从增值包扣减）
	assert.Equal(t, 0.0, result.TotalAmount)
	assert.Equal(t, "CNY", result.Currency)
	assert.Equal(t, ModeBoosterPack, result.BillingMode)

	// 验证元数据
	assert.Equal(t, int64(500000), result.Metadata["usage_amount"])
	assert.Equal(t, int64(500000), result.Metadata["deducted_from_packs"])
	assert.Equal(t, int64(0), result.Metadata["fallback_amount"])

	// 验证明细
	assert.Len(t, result.Breakdown, 1)
	assert.Equal(t, "pack_deduction", result.Breakdown[0].Category)

	// 验证剩余量
	remaining, _ := packManager.GetTotalRemaining(ctx, "tenant_1")
	assert.Equal(t, int64(500000), remaining)
}

// TestBoosterPackEngine_Calculate_ExceedPack tests usage exceeding pack balance
func TestBoosterPackEngine_Calculate_ExceedPack(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	// 添加一个50万tokens的增值包
	pack := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     500000,
		RemainingAmount: 500000,
		PriceCents:      40000,
		Currency:        "CNY",
		PurchasedAt:     time.Now().AddDate(0, 0, -1),
		Status:          "active",
	}
	packManager.AddPack(ctx, pack)

	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		PeriodStart:    time.Now().AddDate(0, 0, -30),
		PeriodEnd:      time.Now(),
		CreditsCharged: 800000, // 使用80万，超出30万
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 应该收费超出部分：30万 × 0.02 / 100 = 60元
	assert.InDelta(t, 60.0, result.TotalAmount, 0.01)

	// 验证元数据
	assert.Equal(t, int64(500000), result.Metadata["deducted_from_packs"])
	assert.Equal(t, int64(300000), result.Metadata["fallback_amount"])
	assert.InDelta(t, 60.0, result.Metadata["fallback_fee"], 0.01)

	// 验证明细
	assert.Len(t, result.Breakdown, 2)
	assert.Equal(t, "pack_deduction", result.Breakdown[0].Category)
	assert.Equal(t, "fallback_payg", result.Breakdown[1].Category)

	// 增值包应该耗尽
	remaining, _ := packManager.GetTotalRemaining(ctx, "tenant_1")
	assert.Equal(t, int64(0), remaining)
}

// TestBoosterPackEngine_Calculate_NoPack tests usage without any pack
func TestBoosterPackEngine_Calculate_NoPack(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		PeriodStart:    time.Now().AddDate(0, 0, -30),
		PeriodEnd:      time.Now(),
		CreditsCharged: 100000,
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 全部按量付费：10万 × 0.02 / 100 = 20元
	assert.InDelta(t, 20.0, result.TotalAmount, 0.01)

	// 验证元数据
	assert.Equal(t, int64(0), result.Metadata["deducted_from_packs"])
	assert.Equal(t, int64(100000), result.Metadata["fallback_amount"])
}

// TestBoosterPackEngine_FallbackMode_Stop tests stop mode
func TestBoosterPackEngine_FallbackMode_Stop(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	// 只有10万tokens的增值包
	pack := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     time.Now(),
		Status:          "active",
	}
	packManager.AddPack(ctx, pack)

	engine := NewBoosterPackEngine("fifo", "stop", 0.0, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		CreditsCharged: 200000, // 超出10万
	}

	_, err := engine.Calculate(ctx, consumption)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "insufficient pack balance")
}

// TestBoosterPackEngine_FallbackMode_Credit tests credit mode
func TestBoosterPackEngine_FallbackMode_Credit(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	pack := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     time.Now(),
		Status:          "active",
	}
	packManager.AddPack(ctx, pack)

	engine := NewBoosterPackEngine("fifo", "credit", 0.0, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		CreditsCharged: 200000, // 超出10万
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 赊账模式：不收费
	assert.Equal(t, 0.0, result.TotalAmount)

	// 验证明细
	assert.Len(t, result.Breakdown, 2)
	assert.Equal(t, "pack_deduction", result.Breakdown[0].Category)
	assert.Equal(t, "credit_overdraft", result.Breakdown[1].Category)
}

// TestBoosterPackEngine_DeductionStrategy_FIFO tests FIFO strategy
func TestBoosterPackEngine_DeductionStrategy_FIFO(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	// 添加两个增值包（先后购买）
	pack1 := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     time.Now().AddDate(0, 0, -10), // 10天前购买
		Status:          "active",
	}

	pack2 := &Pack{
		PackID:          2,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     time.Now().AddDate(0, 0, -5), // 5天前购买
		Status:          "active",
	}

	packManager.AddPack(ctx, pack1)
	packManager.AddPack(ctx, pack2)

	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		CreditsCharged: 150000, // 使用15万
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 应该先扣pack1（10万），再扣pack2（5万）
	assert.Equal(t, int64(150000), result.Metadata["deducted_from_packs"])
	assert.Equal(t, int64(0), result.Metadata["fallback_amount"])

	// 验证pack1已耗尽（通过直接访问内存）
	assert.Equal(t, int64(0), pack1.RemainingAmount)
	assert.Equal(t, "depleted", pack1.Status)
	
	// pack2剩余5万
	assert.Equal(t, int64(50000), pack2.RemainingAmount)
	assert.Equal(t, "active", pack2.Status)

	// GetPacks只返回active且有余额的pack
	activePacks, _ := packManager.GetPacks(ctx, "tenant_1")
	assert.Len(t, activePacks, 1)
	assert.Equal(t, int64(2), activePacks[0].PackID)
}

// TestBoosterPackEngine_DeductionStrategy_ExpireFirst tests expire_first strategy
func TestBoosterPackEngine_DeductionStrategy_ExpireFirst(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	now := time.Now()
	expire1 := now.AddDate(0, 0, 30) // 30天后过期
	expire2 := now.AddDate(0, 0, 10) // 10天后过期（先过期）

	pack1 := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     now.AddDate(0, 0, -20),
		ExpiresAt:       &expire1,
		Status:          "active",
	}

	pack2 := &Pack{
		PackID:          2,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     100000,
		RemainingAmount: 100000,
		PriceCents:      10000,
		Currency:        "CNY",
		PurchasedAt:     now.AddDate(0, 0, -10),
		ExpiresAt:       &expire2, // 先过期
		Status:          "active",
	}

	packManager.AddPack(ctx, pack1)
	packManager.AddPack(ctx, pack2)

	engine := NewBoosterPackEngine("expire_first", "pay_as_you_go", 0.02, "CNY", packManager)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		CreditsCharged: 150000,
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 应该先扣pack2（先过期的），再扣pack1
	assert.Equal(t, int64(150000), result.Metadata["deducted_from_packs"])

	// 验证pack2已耗尽（先过期的被优先使用）
	assert.Equal(t, int64(0), pack2.RemainingAmount)
	assert.Equal(t, "depleted", pack2.Status)
	
	// pack1剩余5万
	assert.Equal(t, int64(50000), pack1.RemainingAmount)
	assert.Equal(t, "active", pack1.Status)
	
	// GetPacks只返回active且有余额的pack
	activePacks, _ := packManager.GetPacks(ctx, "tenant_1")
	assert.Len(t, activePacks, 1)
	assert.Equal(t, int64(1), activePacks[0].PackID) // pack1还有余额
}

// TestBoosterPackEngine_Validate tests configuration validation
func TestBoosterPackEngine_Validate(t *testing.T) {
	engine := NewBoosterPackEngine("fifo", "pay_as_you_go", 0.02, "CNY", nil)

	tests := []struct {
		name    string
		config  *BillingConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &BillingConfig{
				BillingMode: ModeBoosterPack,
				EngineConfig: map[string]interface{}{
					"deduction_strategy": "fifo",
					"fallback_mode":      "pay_as_you_go",
					"fallback_rate":      0.02,
				},
			},
			wantErr: false,
		},
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "cannot be nil",
		},
		{
			name: "wrong mode",
			config: &BillingConfig{
				BillingMode: ModePayAsYouGo,
				EngineConfig: map[string]interface{}{
					"deduction_strategy": "fifo",
				},
			},
			wantErr: true,
			errMsg:  "expected booster_pack",
		},
		{
			name: "invalid deduction strategy",
			config: &BillingConfig{
				BillingMode: ModeBoosterPack,
				EngineConfig: map[string]interface{}{
					"deduction_strategy": "invalid",
				},
			},
			wantErr: true,
			errMsg:  "invalid strategy",
		},
		{
			name: "invalid fallback mode",
			config: &BillingConfig{
				BillingMode: ModeBoosterPack,
				EngineConfig: map[string]interface{}{
					"fallback_mode": "invalid",
				},
			},
			wantErr: true,
			errMsg:  "invalid fallback_mode",
		},
		{
			name: "pay_as_you_go without fallback_rate",
			config: &BillingConfig{
				BillingMode: ModeBoosterPack,
				EngineConfig: map[string]interface{}{
					"fallback_mode": "pay_as_you_go",
					// 缺少fallback_rate
				},
			},
			wantErr: true,
			errMsg:  "fallback_rate is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := engine.Validate(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestFromConfigBoosterPack tests config-based construction
func TestFromConfigBoosterPack(t *testing.T) {
	packManager := NewInMemoryPackManager()

	t.Run("valid config", func(t *testing.T) {
		config := &BillingConfig{
			BillingMode: ModeBoosterPack,
			EngineConfig: map[string]interface{}{
				"deduction_strategy": "expire_first",
				"fallback_mode":      "stop",
				"currency":           "USD",
			},
		}

		engine, err := FromConfigBoosterPack(config, packManager)
		require.NoError(t, err)
		assert.Equal(t, "expire_first", engine.DeductionStrategy)
		assert.Equal(t, "stop", engine.FallbackMode)
		assert.Equal(t, "USD", engine.Currency)
	})

	t.Run("default values", func(t *testing.T) {
		config := &BillingConfig{
			BillingMode:  ModeBoosterPack,
			EngineConfig: map[string]interface{}{},
		}

		engine, err := FromConfigBoosterPack(config, packManager)
		require.NoError(t, err)
		assert.Equal(t, "fifo", engine.DeductionStrategy)
		assert.Equal(t, "pay_as_you_go", engine.FallbackMode)
		assert.Equal(t, "CNY", engine.Currency)
	})

	t.Run("nil config", func(t *testing.T) {
		engine, err := FromConfigBoosterPack(nil, packManager)
		assert.Error(t, err)
		assert.Nil(t, engine)
	})
}

// TestBoosterPackEngine_RealWorldScenario tests a realistic scenario
func TestBoosterPackEngine_RealWorldScenario(t *testing.T) {
	ctx := context.Background()
	packManager := NewInMemoryPackManager()

	// 场景：客户购买了2个增值包
	// Pack1: 100万tokens，800元，30天前购买，60天后过期
	// Pack2: 50万tokens，380元，10天前购买，90天后过期

	now := time.Now()
	expire1 := now.AddDate(0, 0, 60)
	expire2 := now.AddDate(0, 0, 90)

	pack1 := &Pack{
		PackID:          1,
		TenantID:        "enterprise_001",
		PackType:        "token_pack",
		QuotaAmount:     1000000,
		RemainingAmount: 1000000,
		PriceCents:      80000,
		Currency:        "CNY",
		PurchasedAt:     now.AddDate(0, 0, -30),
		ExpiresAt:       &expire1,
		Status:          "active",
	}

	pack2 := &Pack{
		PackID:          2,
		TenantID:        "enterprise_001",
		PackType:        "token_pack",
		QuotaAmount:     500000,
		RemainingAmount: 500000,
		PriceCents:      38000,
		Currency:        "CNY",
		PurchasedAt:     now.AddDate(0, 0, -10),
		ExpiresAt:       &expire2,
		Status:          "active",
	}

	packManager.AddPack(ctx, pack1)
	packManager.AddPack(ctx, pack2)

	// 使用expire_first策略（优先消耗快过期的）
	engine := NewBoosterPackEngine("expire_first", "pay_as_you_go", 0.015, "CNY", packManager)

	// 本月使用了120万tokens
	consumption := &Consumption{
		TenantID:       "enterprise_001",
		PeriodStart:    now.AddDate(0, 0, -30),
		PeriodEnd:      now,
		CreditsCharged: 1200000,
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 预期：
	// 1. 先扣pack1（60天后过期）：100万
	// 2. 再扣pack2（90天后过期）：20万（剩余30万）
	// 3. 增值包共抵扣120万，无需付费
	assert.Equal(t, 0.0, result.TotalAmount)
	assert.Equal(t, int64(1200000), result.Metadata["deducted_from_packs"])
	assert.Equal(t, int64(0), result.Metadata["fallback_amount"])

	// 验证剩余
	totalRemaining, _ := packManager.GetTotalRemaining(ctx, "enterprise_001")
	assert.Equal(t, int64(300000), totalRemaining) // pack2剩余30万
}

// Helper function
func ptrTime(t time.Time) *time.Time {
	return &t
}
