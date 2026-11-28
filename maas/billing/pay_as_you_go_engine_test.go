package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewPayAsYouGoEngine tests engine creation
func TestNewPayAsYouGoEngine(t *testing.T) {
	t.Run("with USD currency", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(2.5, "USD")
		assert.NotNil(t, engine)
		assert.Equal(t, 2.5, engine.CentsPerCredit)
		assert.Equal(t, "USD", engine.Currency)
		assert.Equal(t, ModePayAsYouGo, engine.SupportedMode())
	})

	t.Run("with CNY currency", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(18.0, "CNY")
		assert.Equal(t, 18.0, engine.CentsPerCredit)
		assert.Equal(t, "CNY", engine.Currency)
	})

	t.Run("with empty currency defaults to USD", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(2.5, "")
		assert.Equal(t, "USD", engine.Currency)
	})

	t.Run("convenience constructors", func(t *testing.T) {
		cnyEngine := NewPayAsYouGoCNYEngine(18.0)
		assert.Equal(t, "CNY", cnyEngine.Currency)

		usdEngine := NewPayAsYouGoUSDEngine(2.5)
		assert.Equal(t, "USD", usdEngine.Currency)
	})
}

// TestPayAsYouGoEngine_Calculate tests the core billing formula
//
// These tests align with consumption_detail_test.go to ensure
// the new engine produces identical results to the existing code.
func TestPayAsYouGoEngine_Calculate(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name              string
		centsPerCredit    float64
		currency          string
		creditsCharged    int64
		upstreamCostUSD   float64
		wantTotalAmount   float64
		wantRevenueUSD    float64
		wantMarginUSD     float64
		wantMarginRate    float64
	}{
		{
			name:            "USD billing - direct conversion",
			centsPerCredit:  2.5,
			currency:        "USD",
			creditsCharged:  1000,
			upstreamCostUSD: 15.0,
			wantTotalAmount: 25.0, // 1000 * 2.5 / 100
			wantRevenueUSD:  25.0,
			wantMarginUSD:   10.0, // 25 - 15
			wantMarginRate:  0.4,  // 10 / 25
		},
		{
			name:            "CNY billing - with FX conversion",
			centsPerCredit:  2.5,
			currency:        "CNY",
			creditsCharged:  1000,
			upstreamCostUSD: 2.0,
			wantTotalAmount: 25.0,     // 1000 * 2.5 / 100 = 25 CNY
			wantRevenueUSD:  3.472222, // 25 CNY / 7.2
			wantMarginUSD:   1.472222,
			wantMarginRate:  0.4239,
		},
		{
			name:            "zero credits",
			centsPerCredit:  2.5,
			currency:        "USD",
			creditsCharged:  0,
			upstreamCostUSD: 0,
			wantTotalAmount: 0,
			wantRevenueUSD:  0,
			wantMarginUSD:   0,
			wantMarginRate:  0,
		},
		{
			name:            "negative margin",
			centsPerCredit:  1.0,
			currency:        "USD",
			creditsCharged:  100,
			upstreamCostUSD: 5.0,
			wantTotalAmount: 1.0,
			wantRevenueUSD:  1.0,
			wantMarginUSD:   -4.0,
			wantMarginRate:  -4.0,
		},
		{
			name:            "large credits",
			centsPerCredit:  1.5,
			currency:        "USD",
			creditsCharged:  1000000,
			upstreamCostUSD: 10000.0,
			wantTotalAmount: 15000.0, // 1M * 1.5 / 100
			wantRevenueUSD:  15000.0,
			wantMarginUSD:   5000.0,
			wantMarginRate:  0.333333,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := NewPayAsYouGoEngine(tt.centsPerCredit, tt.currency)

			consumption := &Consumption{
				TenantID:       "test_tenant",
				PeriodStart:    time.Now().AddDate(0, 0, -30),
				PeriodEnd:      time.Now(),
				CreditsCharged: tt.creditsCharged,
				RequestCount:   10,
				ModelBreakdown: []ModelConsumption{
					{
						ModelID:         "model_1",
						ModelName:       "test-model",
						CreditsCharged:  tt.creditsCharged,
						UpstreamCostUSD: tt.upstreamCostUSD,
					},
				},
			}

			result, err := engine.Calculate(ctx, consumption)
			require.NoError(t, err)
			require.NotNil(t, result)

			// Verify total amount
			assert.InDelta(t, tt.wantTotalAmount, result.TotalAmount, 0.0001,
				"total amount mismatch")

			// Verify currency
			assert.Equal(t, tt.currency, result.Currency)

			// Verify metadata
			revenueUSD := result.Metadata["revenue_usd"].(float64)
			assert.InDelta(t, tt.wantRevenueUSD, revenueUSD, 0.0001,
				"revenue_usd mismatch")

			marginUSD := result.Metadata["gross_margin_usd"].(float64)
			assert.InDelta(t, tt.wantMarginUSD, marginUSD, 0.0001,
				"gross_margin_usd mismatch")

			marginRate := result.Metadata["gross_margin_rate"].(float64)
			assert.InDelta(t, tt.wantMarginRate, marginRate, 0.001,
				"gross_margin_rate mismatch")

			// Verify breakdown
			assert.NotEmpty(t, result.Breakdown)
			assert.Equal(t, "usage", result.Breakdown[0].Category)
		})
	}
}

// TestPayAsYouGoEngine_Calculate_MultiModel tests multiple models in breakdown
func TestPayAsYouGoEngine_Calculate_MultiModel(t *testing.T) {
	ctx := context.Background()
	engine := NewPayAsYouGoEngine(2.0, "USD")

	consumption := &Consumption{
		TenantID:       "test_tenant",
		CreditsCharged: 1500,
		RequestCount:   20,
		ModelBreakdown: []ModelConsumption{
			{
				ModelName:       "gpt-4",
				CreditsCharged:  1000,
				UpstreamCostUSD: 15.0,
			},
			{
				ModelName:       "gpt-3.5",
				CreditsCharged:  500,
				UpstreamCostUSD: 2.0,
			},
		},
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// Total: 1500 * 2.0 / 100 = 30.0 USD
	assert.InDelta(t, 30.0, result.TotalAmount, 0.01)

	// Should have 1 usage line + 2 model lines = 3 total
	assert.Len(t, result.Breakdown, 3)

	// Verify model breakdown lines
	modelLines := result.Breakdown[1:]
	assert.Equal(t, "model_usage", modelLines[0].Category)
	assert.Equal(t, "model_usage", modelLines[1].Category)

	// Verify total upstream cost
	totalCost := result.Metadata["upstream_cost_usd"].(float64)
	assert.InDelta(t, 17.0, totalCost, 0.01) // 15 + 2
}

// TestPayAsYouGoEngine_Calculate_CurrencyMixingBug verifies AF-1 fix
//
// This test ensures the engine correctly handles CNY billing without
// treating CNY amounts as USD (which would inflate margins ~7.2x).
func TestPayAsYouGoEngine_Calculate_CurrencyMixingBug(t *testing.T) {
	ctx := context.Background()

	// Scenario: 1000 credits at 180 CNY cents/credit (1.8 CNY/credit)
	// Real bill: 1800 CNY = 250 USD at FX 7.2
	// Upstream cost: 200 USD
	// Real margin: 50 USD (20%)

	engine := NewPayAsYouGoEngine(180.0, "CNY")

	consumption := &Consumption{
		TenantID:       "test_tenant",
		CreditsCharged: 1000,
		ModelBreakdown: []ModelConsumption{
			{
				ModelName:       "test-model",
				CreditsCharged:  1000,
				UpstreamCostUSD: 200.0,
			},
		},
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// Bill amount should be 1800 CNY
	assert.InDelta(t, 1800.0, result.TotalAmount, 0.01)
	assert.Equal(t, "CNY", result.Currency)

	// Revenue in USD should be ~250 USD (1800 / 7.2)
	revenueUSD := result.Metadata["revenue_usd"].(float64)
	assert.InDelta(t, 250.0, revenueUSD, 0.01)

	// Margin should be ~50 USD (not ~1600 USD if bug exists)
	marginUSD := result.Metadata["gross_margin_usd"].(float64)
	assert.InDelta(t, 50.0, marginUSD, 0.01)

	// Margin rate should be ~20% (not ~88%)
	marginRate := result.Metadata["gross_margin_rate"].(float64)
	assert.InDelta(t, 0.2, marginRate, 0.001)
}

// TestPayAsYouGoEngine_Calculate_ErrorCases tests error handling
func TestPayAsYouGoEngine_Calculate_ErrorCases(t *testing.T) {
	ctx := context.Background()

	t.Run("nil consumption", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(2.5, "USD")
		result, err := engine.Calculate(ctx, nil)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "cannot be nil")
	})

	t.Run("zero cents_per_credit", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(0, "USD")
		consumption := &Consumption{
			CreditsCharged: 100,
		}
		result, err := engine.Calculate(ctx, consumption)
		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "must be positive")
	})

	t.Run("negative cents_per_credit", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(-1.5, "USD")
		consumption := &Consumption{
			CreditsCharged: 100,
		}
		result, err := engine.Calculate(ctx, consumption)
		assert.Error(t, err)
		assert.Nil(t, result)
	})
}

// TestPayAsYouGoEngine_Validate tests configuration validation
func TestPayAsYouGoEngine_Validate(t *testing.T) {
	engine := NewPayAsYouGoEngine(2.5, "USD")

	tests := []struct {
		name    string
		config  *BillingConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &BillingConfig{
				Mode: ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": 2.5,
					"currency":         "USD",
				},
				Currency: "USD",
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
				Mode: ModeTokenPlan,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": 2.5,
				},
			},
			wantErr: true,
			errMsg:  "expected pay_as_you_go",
		},
		{
			name: "missing cents_per_credit",
			config: &BillingConfig{
				Mode:       ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{},
			},
			wantErr: true,
			errMsg:  "must be a number",
		},
		{
			name: "zero cents_per_credit",
			config: &BillingConfig{
				Mode: ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": 0.0,
				},
			},
			wantErr: true,
			errMsg:  "must be positive",
		},
		{
			name: "negative cents_per_credit",
			config: &BillingConfig{
				Mode: ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": -1.5,
				},
			},
			wantErr: true,
			errMsg:  "must be positive",
		},
		{
			name: "unsupported currency",
			config: &BillingConfig{
				Mode: ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": 2.5,
					"currency":         "INVALID",
				},
			},
			wantErr: true,
			errMsg:  "unsupported currency",
		},
		{
			name: "supported CNY currency",
			config: &BillingConfig{
				Mode: ModePayAsYouGo,
				ConfigJSON: map[string]interface{}{
					"cents_per_credit": 18.0,
					"currency":         "CNY",
				},
			},
			wantErr: false,
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

// TestPayAsYouGoEngine_FromConfig tests config-based construction
func TestPayAsYouGoEngine_FromConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		config := &BillingConfig{
			Mode: ModePayAsYouGo,
			ConfigJSON: map[string]interface{}{
				"cents_per_credit": 2.5,
				"currency":         "USD",
			},
			Currency: "USD",
		}

		engine, err := FromConfig(config)
		require.NoError(t, err)
		assert.Equal(t, 2.5, engine.CentsPerCredit)
		assert.Equal(t, "USD", engine.Currency)
	})

	t.Run("currency from config_json overrides", func(t *testing.T) {
		config := &BillingConfig{
			Mode: ModePayAsYouGo,
			ConfigJSON: map[string]interface{}{
				"cents_per_credit": 18.0,
				"currency":         "CNY",
			},
			Currency: "USD", // Will be overridden
		}

		engine, err := FromConfig(config)
		require.NoError(t, err)
		assert.Equal(t, "CNY", engine.Currency)
	})

	t.Run("nil config", func(t *testing.T) {
		engine, err := FromConfig(nil)
		assert.Error(t, err)
		assert.Nil(t, engine)
	})

	t.Run("invalid config fails validation", func(t *testing.T) {
		config := &BillingConfig{
			Mode: ModePayAsYouGo,
			ConfigJSON: map[string]interface{}{
				"cents_per_credit": -1.0, // Invalid
			},
		}

		engine, err := FromConfig(config)
		assert.Error(t, err)
		assert.Nil(t, engine)
		assert.Contains(t, err.Error(), "invalid config")
	})
}

// TestPayAsYouGoEngine_Metadata tests metadata completeness
func TestPayAsYouGoEngine_Metadata(t *testing.T) {
	ctx := context.Background()
	engine := NewPayAsYouGoEngine(2.5, "CNY")

	consumption := &Consumption{
		TenantID:       "tenant_123",
		PeriodStart:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		PeriodEnd:      time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC),
		CreditsCharged: 1000,
		RequestCount:   50,
		ModelBreakdown: []ModelConsumption{
			{UpstreamCostUSD: 10.0},
		},
	}

	result, err := engine.Calculate(ctx, consumption)
	require.NoError(t, err)

	// Verify all expected metadata fields exist
	requiredFields := []string{
		"credits_charged",
		"cents_per_credit",
		"revenue_usd",
		"upstream_cost_usd",
		"gross_margin_usd",
		"gross_margin_rate",
		"request_count",
		"period_start",
		"period_end",
		"fx_conversion_applied",
	}

	for _, field := range requiredFields {
		assert.Contains(t, result.Metadata, field, "missing metadata field: %s", field)
	}

	// Verify specific values
	assert.Equal(t, int64(1000), result.Metadata["credits_charged"])
	assert.Equal(t, 2.5, result.Metadata["cents_per_credit"])
	assert.Equal(t, int64(50), result.Metadata["request_count"])
	assert.Equal(t, "2026-11-01", result.Metadata["period_start"])
	assert.Equal(t, "2026-11-30", result.Metadata["period_end"])
	assert.Equal(t, true, result.Metadata["fx_conversion_applied"]) // CNY != USD
}

// TestPayAsYouGoEngine_Name tests engine naming
func TestPayAsYouGoEngine_Name(t *testing.T) {
	t.Run("with custom name", func(t *testing.T) {
		engine := &PayAsYouGoEngine{
			Name_:          "custom_engine_name",
			CentsPerCredit: 2.5,
			Currency:       "USD",
		}
		assert.Equal(t, "custom_engine_name", engine.Name())
	})

	t.Run("without custom name", func(t *testing.T) {
		engine := &PayAsYouGoEngine{
			CentsPerCredit: 2.5,
			Currency:       "USD",
		}
		assert.Equal(t, "pay_as_you_go", engine.Name())
	})

	t.Run("generated name from constructor", func(t *testing.T) {
		engine := NewPayAsYouGoEngine(2.5, "CNY")
		assert.Contains(t, engine.Name(), "pay_as_you_go")
		assert.Contains(t, engine.Name(), "cny")
	})
}
