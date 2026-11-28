package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewTokenPlanEngine tests engine creation
func TestNewTokenPlanEngine(t *testing.T) {
	quotaManager := NewInMemoryQuotaManager()

	engine := NewTokenPlanEngine(
		1,              // planID
		"Standard",     // planName
		1000000,        // monthlyQuota
		100000,         // basePrice (1000元，100000分)
		1.5,            // overageRate (0.015元/token)
		"CNY",          // currency
		quotaManager,   // quotaManager
	)

	assert.NotNil(t, engine)
	assert.Equal(t, int64(1), engine.PlanID)
	assert.Equal(t, "Standard", engine.PlanName)
	assert.Equal(t, int64(1000000), engine.MonthlyQuota)
	assert.Equal(t, int64(100000), engine.BasePrice)
	assert.Equal(t, 1.5, engine.OverageRate)
	assert.Equal(t, "CNY", engine.Currency)
	assert.Equal(t, ModeTokenPlan, engine.SupportedMode())
}

// TestTokenPlanEngine_Calculate tests billing calculation
func TestTokenPlanEngine_Calculate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name             string
		monthlyQuota     int64
		basePrice        int64
		overageRate      float64
		usedAmount       int64
		wantTotalAmount  float64
		wantBaseFee      float64
		wantOverageFee   float64
		wantOverageAmount int64
	}{
		{
			name:             "within quota - no overage",
			monthlyQuota:     1000000,
			basePrice:        100000, // 1000元
			overageRate:      0.015,  // 0.015分/token
			usedAmount:       800000,
			wantTotalAmount:  1000.0,
			wantBaseFee:      1000.0,
			wantOverageFee:   0,
			wantOverageAmount: 0,
		},
		{
			name:             "exact quota - no overage",
			monthlyQuota:     1000000,
			basePrice:        100000,
			overageRate:      0.015,
			usedAmount:       1000000,
			wantTotalAmount:  1000.0,
			wantBaseFee:      1000.0,
			wantOverageFee:   0,
			wantOverageAmount: 0,
		},
		{
			name:             "over quota - with overage",
			monthlyQuota:     1000000,
			basePrice:        100000,
			overageRate:      0.015, // 0.015分/token
			usedAmount:       1200000,
			wantTotalAmount:  1030.0, // 1000 + 200000*0.015/100 = 1000 + 30
			wantBaseFee:      1000.0,
			wantOverageFee:   30.0,
			wantOverageAmount: 200000,
		},
		{
			name:             "heavy overage",
			monthlyQuota:     1000000,
			basePrice:        100000,
			overageRate:      0.02,
			usedAmount:       2000000, // 超额100%
			wantTotalAmount:  1200.0,  // 1000 + 1000000*0.02/100 = 1000 + 200
			wantBaseFee:      1000.0,
			wantOverageFee:   200.0,
			wantOverageAmount: 1000000,
		},
		{
			name:             "zero usage",
			monthlyQuota:     1000000,
			basePrice:        100000,
			overageRate:      0.015,
			usedAmount:       0,
			wantTotalAmount:  1000.0, // 仍需支付基础月费
			wantBaseFee:      1000.0,
			wantOverageFee:   0,
			wantOverageAmount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			quotaManager := NewInMemoryQuotaManager()
			engine := NewTokenPlanEngine(1, "Test Plan", tt.monthlyQuota, tt.basePrice, tt.overageRate, "CNY", quotaManager)

			// 记录使用量
			period := TimePeriod{
				Start: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
				End:   time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
			}
			quotaManager.RecordUsage(ctx, "tenant_1", tt.usedAmount)

			consumption := &Consumption{
				TenantID:       "tenant_1",
				PeriodStart:    period.Start,
				PeriodEnd:      period.End,
				CreditsCharged: tt.usedAmount,
			}

			result, err := engine.Calculate(ctx, consumption)
			require.NoError(t, err)
			require.NotNil(t, result)

			// 验证总金额
			assert.InDelta(t, tt.wantTotalAmount, result.TotalAmount, 0.01, "total amount mismatch")

			// 验证货币
			assert.Equal(t, "CNY", result.Currency)

			// 验证模式
			assert.Equal(t, ModeTokenPlan, result.BillingMode)

			// 验证元数据
			assert.Equal(t, int64(1), result.Metadata["plan_id"])
			assert.Equal(t, tt.usedAmount, result.Metadata["total_used"])
			assert.Equal(t, tt.wantOverageAmount, result.Metadata["overage_amount"])
			assert.InDelta(t, tt.wantBaseFee, result.Metadata["base_fee"], 0.01)
			assert.InDelta(t, tt.wantOverageFee, result.Metadata["overage_fee"], 0.01)

			// 验证明细
			assert.GreaterOrEqual(t, len(result.Breakdown), 2, "should have at least base_fee and quota_usage")
			assert.Equal(t, "base_fee", result.Breakdown[0].Category)
			
			if tt.wantOverageAmount > 0 {
				assert.Equal(t, 3, len(result.Breakdown), "should have overage line item")
				assert.Equal(t, "overage", result.Breakdown[2].Category)
			}
		})
	}
}

// TestTokenPlanEngine_CalculateWithoutQuotaManager tests fallback to consumption data
func TestTokenPlanEngine_CalculateWithoutQuotaManager(t *testing.T) {
	ctx := context.Background()

	// 不提供QuotaManager，应该降级到使用Consumption中的数据
	engine := NewTokenPlanEngine(1, "Test", 1000000, 100000, 0.015, "CNY", nil)

	consumption := &Consumption{
		TenantID:       "tenant_1",
		PeriodStart:    time.Now().AddDate(0, 0, -30),
		PeriodEnd:      time.Now(),
		CreditsCharged: 1200000, // 超额20%
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 应该正确计算超额
	// 1000 + 200000*0.015/100 = 1000 + 30 = 1030
	assert.InDelta(t, 1030.0, result.TotalAmount, 0.01)
	assert.Equal(t, int64(200000), result.Metadata["overage_amount"])
}

// TestTokenPlanEngine_Validate tests configuration validation
func TestTokenPlanEngine_Validate(t *testing.T) {
	engine := NewTokenPlanEngine(1, "Test", 1000000, 100000, 0.015, "CNY", nil)

	tests := []struct {
		name    string
		config  *BillingConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(1000000),
					"base_price":    float64(100000),
					"overage_rate":  0.015,
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
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(1000000),
					"base_price":    float64(100000),
					"overage_rate":  0.015,
				},
			},
			wantErr: true,
			errMsg:  "expected token_plan",
		},
		{
			name: "missing plan_id",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(1000000),
					"base_price":    float64(100000),
					"overage_rate":  0.015,
				},
			},
			wantErr: true,
			errMsg:  "plan_id is required",
		},
		{
			name: "missing monthly_quota",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"base_price":   float64(100000),
					"overage_rate": 0.015,
				},
			},
			wantErr: true,
			errMsg:  "monthly_quota is required",
		},
		{
			name: "zero monthly_quota",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(0),
					"base_price":    float64(100000),
					"overage_rate":  0.015,
				},
			},
			wantErr: true,
			errMsg:  "must be positive",
		},
		{
			name: "negative base_price",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(1000000),
					"base_price":    float64(-100),
					"overage_rate":  0.015,
				},
			},
			wantErr: true,
			errMsg:  "cannot be negative",
		},
		{
			name: "negative overage_rate",
			config: &BillingConfig{
				BillingMode: ModeTokenPlan,
				PlanID:      ptrInt64(1),
				EngineConfig: map[string]interface{}{
					"monthly_quota": float64(1000000),
					"base_price":    float64(100000),
					"overage_rate":  -0.015,
				},
			},
			wantErr: true,
			errMsg:  "cannot be negative",
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

// TestFromConfigTokenPlan tests config-based construction
func TestFromConfigTokenPlan(t *testing.T) {
	quotaManager := NewInMemoryQuotaManager()

	t.Run("valid config", func(t *testing.T) {
		config := &BillingConfig{
			BillingMode: ModeTokenPlan,
			PlanID:      ptrInt64(5),
			EngineConfig: map[string]interface{}{
				"plan_name":     "Premium",
				"monthly_quota": float64(5000000),
				"base_price":    float64(500000),
				"overage_rate":  0.012,
				"currency":      "CNY",
			},
		}

		engine, err := FromConfigTokenPlan(config, quotaManager)
		require.NoError(t, err)
		assert.Equal(t, int64(5), engine.PlanID)
		assert.Equal(t, "Premium", engine.PlanName)
		assert.Equal(t, int64(5000000), engine.MonthlyQuota)
		assert.Equal(t, int64(500000), engine.BasePrice)
		assert.Equal(t, 0.012, engine.OverageRate)
		assert.Equal(t, "CNY", engine.Currency)
	})

	t.Run("default values", func(t *testing.T) {
		config := &BillingConfig{
			BillingMode: ModeTokenPlan,
			PlanID:      ptrInt64(1),
			EngineConfig: map[string]interface{}{
				"monthly_quota": float64(1000000),
				"base_price":    float64(100000),
				"overage_rate":  0.015,
			},
		}

		engine, err := FromConfigTokenPlan(config, quotaManager)
		require.NoError(t, err)
		assert.Equal(t, "Standard Plan", engine.PlanName) // 默认名称
		assert.Equal(t, "CNY", engine.Currency)           // 默认货币
	})

	t.Run("nil config", func(t *testing.T) {
		engine, err := FromConfigTokenPlan(nil, quotaManager)
		assert.Error(t, err)
		assert.Nil(t, engine)
	})

	t.Run("invalid config", func(t *testing.T) {
		config := &BillingConfig{
			BillingMode: ModeTokenPlan,
			PlanID:      ptrInt64(1),
			EngineConfig: map[string]interface{}{
				"monthly_quota": float64(-1000), // 无效
				"base_price":    float64(100000),
				"overage_rate":  0.015,
			},
		}

		engine, err := FromConfigTokenPlan(config, quotaManager)
		assert.Error(t, err)
		assert.Nil(t, engine)
	})
}

// TestTokenPlanEngine_EstimateMonthlyBill tests bill estimation
func TestTokenPlanEngine_EstimateMonthlyBill(t *testing.T) {
	engine := NewTokenPlanEngine(1, "Standard", 1000000, 100000, 0.015, "CNY", nil)

	tests := []struct {
		name            string
		estimatedUsage  int64
		wantTotalAmount float64
		wantOverage     int64
	}{
		{
			name:            "within quota",
			estimatedUsage:  800000,
			wantTotalAmount: 1000.0,
			wantOverage:     0,
		},
		{
			name:            "slight overage",
			estimatedUsage:  1100000,
			wantTotalAmount: 1015.0, // 1000 + 100000*0.015/100 = 1000 + 15
			wantOverage:     100000,
		},
		{
			name:            "heavy overage",
			estimatedUsage:  2000000,
			wantTotalAmount: 1150.0, // 1000 + 1000000*0.015/100 = 1000 + 150
			wantOverage:     1000000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := engine.EstimateMonthlyBill(tt.estimatedUsage)

			assert.InDelta(t, tt.wantTotalAmount, result.TotalAmount, 0.01)
			assert.Equal(t, tt.estimatedUsage, result.Metadata["estimated_usage"])
			assert.Equal(t, tt.wantOverage, result.Metadata["estimated_overage"])
			assert.True(t, result.Metadata["is_estimate"].(bool))
		})
	}
}

// TestTokenPlanEngine_Name tests engine naming
func TestTokenPlanEngine_Name(t *testing.T) {
	engine := NewTokenPlanEngine(123, "Test", 1000000, 100000, 1.5, "CNY", nil)
	assert.Equal(t, "token_plan_123", engine.Name())
}

// Helper function
func ptrInt64(v int64) *int64 {
	return &v
}

// TestTokenPlanEngine_RealWorldScenario tests a realistic scenario
func TestTokenPlanEngine_RealWorldScenario(t *testing.T) {
	ctx := context.Background()
	quotaManager := NewInMemoryQuotaManager()

	// 场景：企业客户，500万tokens/月套餐，月费5000元
	engine := NewTokenPlanEngine(
		10,              // planID
		"Enterprise",    // planName
		5000000,         // 500万quota
		500000,          // 5000元月费（500000分）
		0.012,           // 超额费率0.012分/token
		"CNY",
		quotaManager,
	)

	// 使用当前月份作为周期（配合InMemoryQuotaManager的实现）
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	
	period := TimePeriod{
		Start: start,
		End:   end,
	}

	// 模拟一个月的使用
	tenantID := "enterprise_client_001"

	// 记录每天的使用（假设每天用20万tokens）
	dailyUsage := int64(200000)
	for day := 1; day <= 30; day++ {
		quotaManager.RecordUsage(ctx, tenantID, dailyUsage)
	}

	// 月底计费
	consumption := &Consumption{
		TenantID:       tenantID,
		PeriodStart:    period.Start,
		PeriodEnd:      period.End,
		CreditsCharged: dailyUsage * 30, // 600万tokens
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// 验证计费结果
	// 基础月费：5000元
	// 超额：100万tokens (600万 - 500万)
	// 超额费：100万 × 0.012 / 100 = 120元
	// 总计：5120元
	assert.InDelta(t, 5120.0, result.TotalAmount, 0.01)
	assert.Equal(t, int64(1000000), result.Metadata["overage_amount"])

	// 验证明细（至少有base_fee和quota_usage）
	assert.GreaterOrEqual(t, len(result.Breakdown), 2)
	assert.Equal(t, "base_fee", result.Breakdown[0].Category)
	assert.Equal(t, "quota_usage", result.Breakdown[1].Category)
	
	// 如果有超额，应该有第3项
	if len(result.Breakdown) >= 3 {
		assert.Equal(t, "overage", result.Breakdown[2].Category)
	}

	// 验证配额利用率
	utilization := result.Metadata["quota_utilization"].(float64)
	assert.InDelta(t, 120.0, utilization, 0.1) // 120%利用率
}
