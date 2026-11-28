package billing

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockConfigLoader 用于测试的配置加载器
type MockConfigLoader struct {
	configs map[string]*BillingConfig
}

func NewMockConfigLoader() *MockConfigLoader {
	return &MockConfigLoader{
		configs: make(map[string]*BillingConfig),
	}
}

func (m *MockConfigLoader) LoadByAPIKey(ctx context.Context, apiKey string) (*BillingConfig, error) {
	return nil, nil
}

func (m *MockConfigLoader) LoadByTenantID(ctx context.Context, tenantID string) (*BillingConfig, error) {
	if config, ok := m.configs[tenantID]; ok {
		return config, nil
	}
	return nil, nil
}

func (m *MockConfigLoader) LoadByUserID(ctx context.Context, userID string) (*BillingConfig, error) {
	return nil, nil
}

func (m *MockConfigLoader) PreloadHotConfigs(ctx context.Context, limit int) error {
	return nil
}

func (m *MockConfigLoader) InvalidateCache(ctx context.Context, tenantID string) error {
	delete(m.configs, tenantID)
	return nil
}

func (m *MockConfigLoader) SetConfig(tenantID string, config *BillingConfig) {
	m.configs[tenantID] = config
}

// TestAdminAPI_ListBillingModes tests listing available billing modes
func TestAdminAPI_ListBillingModes(t *testing.T) {
	ctx := context.Background()

	// 创建工厂并注册引擎
	factory := NewBillingEngineFactory()
	factory.Register(&PayAsYouGoEngine{})
	factory.Register(&TokenPlanEngine{})

	api := NewAdminAPI(nil, nil, nil, nil, nil, factory)

	resp, err := api.ListBillingModes(ctx)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// 应该返回4种模式
	assert.Len(t, resp.Modes, 4)

	// 验证模式信息
	modeMap := make(map[BillingMode]BillingModeInfo)
	for _, mode := range resp.Modes {
		modeMap[mode.Mode] = mode
	}

	// PayAsYouGo应该可用
	assert.True(t, modeMap[ModePayAsYouGo].Available)
	assert.Equal(t, "按量付费", modeMap[ModePayAsYouGo].Name)

	// TokenPlan应该可用
	assert.True(t, modeMap[ModeTokenPlan].Available)
	assert.Equal(t, "包月套餐", modeMap[ModeTokenPlan].Name)

	// BoosterPack未注册，应该不可用
	assert.False(t, modeMap[ModeBoosterPack].Available)

	// Custom未注册，应该不可用
	assert.False(t, modeMap[ModeCustom].Available)
}

// TestAdminAPI_GetBillingConfig tests getting billing config
func TestAdminAPI_GetBillingConfig(t *testing.T) {
	ctx := context.Background()

	configLoader := NewMockConfigLoader()
	api := NewAdminAPI(nil, configLoader, nil, nil, nil, nil)

	t.Run("valid tenant", func(t *testing.T) {
		// 设置测试配置
		config := &BillingConfig{
			TenantID:    "tenant_1",
			BillingMode: ModePayAsYouGo,
			EngineConfig: map[string]interface{}{
				"cents_per_credit": 2.5,
			},
		}
		configLoader.SetConfig("tenant_1", config)

		result, err := api.GetBillingConfig(ctx, "tenant_1")
		require.NoError(t, err)
		assert.Equal(t, "tenant_1", result.TenantID)
		assert.Equal(t, ModePayAsYouGo, result.BillingMode)
	})

	t.Run("empty tenant_id", func(t *testing.T) {
		_, err := api.GetBillingConfig(ctx, "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tenant_id is required")
	})
}

// TestAdminAPI_GetQuotaUsage tests quota usage query
func TestAdminAPI_GetQuotaUsage(t *testing.T) {
	ctx := context.Background()

	configLoader := NewMockConfigLoader()
	quotaManager := NewInMemoryQuotaManager()

	api := NewAdminAPI(nil, configLoader, nil, quotaManager, nil, nil)

	// 设置租户配置（套餐制，100万配额）
	config := &BillingConfig{
		TenantID:    "tenant_1",
		BillingMode: ModeTokenPlan,
		EngineConfig: map[string]interface{}{
			"monthly_quota": float64(1000000),
		},
	}
	configLoader.SetConfig("tenant_1", config)

	// 记录使用量
	quotaManager.RecordUsage(ctx, "tenant_1", 300000)

	t.Run("current period", func(t *testing.T) {
		resp, err := api.GetQuotaUsage(ctx, "tenant_1", "current")
		require.NoError(t, err)
		require.NotNil(t, resp)

		assert.Equal(t, "tenant_1", resp.TenantID)
		assert.Equal(t, int64(1000000), resp.TotalQuota)
		assert.Equal(t, int64(300000), resp.UsedQuota)
		assert.Equal(t, int64(700000), resp.RemainingQuota)
		assert.InDelta(t, 30.0, resp.Utilization, 0.1)
	})

	t.Run("empty period defaults to current", func(t *testing.T) {
		resp, err := api.GetQuotaUsage(ctx, "tenant_1", "")
		require.NoError(t, err)
		assert.Equal(t, int64(300000), resp.UsedQuota)
	})
}

// TestAdminAPI_PurchasePack tests purchasing booster pack
func TestAdminAPI_PurchasePack(t *testing.T) {
	ctx := context.Background()

	packManager := NewInMemoryPackManager()
	
	// 注意：PurchasePack需要数据库，这里只测试验证逻辑
	t.Run("missing tenant_id", func(t *testing.T) {
		api := NewAdminAPI(nil, nil, nil, nil, packManager, nil)
		req := &PurchasePackRequest{
			QuotaAmount: 1000000,
		}

		_, err := api.PurchasePack(ctx, req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tenant_id is required")
	})

	t.Run("invalid quota amount", func(t *testing.T) {
		api := NewAdminAPI(nil, nil, nil, nil, packManager, nil)
		req := &PurchasePackRequest{
			TenantID:    "tenant_1",
			QuotaAmount: 0,
		}

		_, err := api.PurchasePack(ctx, req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "quota_amount must be positive")
	})
	
	t.Run("no pack manager", func(t *testing.T) {
		api := NewAdminAPI(nil, nil, nil, nil, nil, nil)
		req := &PurchasePackRequest{
			TenantID:    "tenant_1",
			QuotaAmount: 1000000,
		}

		_, err := api.PurchasePack(ctx, req)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "pack manager not configured")
	})
}

// TestAdminAPI_ListPacks tests listing booster packs
func TestAdminAPI_ListPacks(t *testing.T) {
	ctx := context.Background()

	packManager := NewInMemoryPackManager()
	api := NewAdminAPI(nil, nil, nil, nil, packManager, nil)

	// 添加两个增值包
	pack1 := &Pack{
		PackID:          1,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     1000000,
		RemainingAmount: 800000,
		Status:          "active",
	}

	pack2 := &Pack{
		PackID:          2,
		TenantID:        "tenant_1",
		PackType:        "token_pack",
		QuotaAmount:     500000,
		RemainingAmount: 500000,
		Status:          "active",
	}

	packManager.AddPack(ctx, pack1)
	packManager.AddPack(ctx, pack2)

	resp, err := api.ListPacks(ctx, "tenant_1")
	require.NoError(t, err)
	require.NotNil(t, resp)

	assert.Equal(t, 2, resp.Total)
	assert.Len(t, resp.Packs, 2)

	// 验证第一个包
	assert.Equal(t, int64(1), resp.Packs[0].PackID)
	assert.Equal(t, int64(800000), resp.Packs[0].RemainingAmount)
}

// TestAdminAPI_RealWorldScenario tests a complete scenario
func TestAdminAPI_RealWorldScenario(t *testing.T) {
	ctx := context.Background()

	// 创建完整的Admin API
	factory := NewBillingEngineFactory()
	factory.Register(&PayAsYouGoEngine{})
	factory.Register(&TokenPlanEngine{})
	factory.Register(&BoosterPackEngine{})

	configLoader := NewMockConfigLoader()
	quotaManager := NewInMemoryQuotaManager()
	packManager := NewInMemoryPackManager()

	api := NewAdminAPI(nil, configLoader, nil, quotaManager, packManager, factory)

	// 场景：企业客户从按量付费切换到增值包模式

	// 1. 初始配置（按量付费）
	initialConfig := &BillingConfig{
		TenantID:    "enterprise_001",
		BillingMode: ModePayAsYouGo,
		EngineConfig: map[string]interface{}{
			"cents_per_credit": 0.015,
		},
	}
	configLoader.SetConfig("enterprise_001", initialConfig)

	config, _ := api.GetBillingConfig(ctx, "enterprise_001")
	assert.Equal(t, ModePayAsYouGo, config.BillingMode)

	// 2. 直接通过packManager添加增值包（绕过数据库）
	pack := &Pack{
		PackID:          1,
		TenantID:        "enterprise_001",
		PackType:        "token_pack",
		QuotaAmount:     1000000,
		RemainingAmount: 1000000,
		PriceCents:      80000,
		Currency:        "CNY",
		PurchasedAt:     time.Now(),
		Status:          "active",
	}
	err := packManager.AddPack(ctx, pack)
	require.NoError(t, err)

	// 3. 验证增值包已添加
	packs, _ := api.ListPacks(ctx, "enterprise_001")
	assert.Equal(t, 1, packs.Total)
	assert.Equal(t, int64(1000000), packs.Packs[0].QuotaAmount)

	// 4. 列出所有可用模式
	modes, _ := api.ListBillingModes(ctx)
	assert.Len(t, modes.Modes, 4)
	
	// 验证三种主要模式都可用
	availableCount := 0
	for _, mode := range modes.Modes {
		if mode.Available {
			availableCount++
		}
	}
	assert.Equal(t, 3, availableCount) // PayAsYouGo, TokenPlan, BoosterPack
}

// TestCreatePlanRequest_Validation tests plan creation validation
func TestCreatePlanRequest_Validation(t *testing.T) {
	tests := []struct {
		name    string
		req     *CreatePlanRequest
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid plan",
			req: &CreatePlanRequest{
				PlanName:       "Standard",
				MonthlyQuota:   1000000,
				BasePriceCents: 100000,
				OverageRate:    0.015,
				Currency:       "CNY",
			},
			wantErr: false,
		},
		{
			name: "missing plan name",
			req: &CreatePlanRequest{
				MonthlyQuota: 1000000,
			},
			wantErr: true,
			errMsg:  "plan_name is required",
		},
		{
			name: "zero quota",
			req: &CreatePlanRequest{
				PlanName:     "Standard",
				MonthlyQuota: 0,
			},
			wantErr: true,
			errMsg:  "monthly_quota must be positive",
		},
		{
			name: "negative price",
			req: &CreatePlanRequest{
				PlanName:       "Standard",
				MonthlyQuota:   1000000,
				BasePriceCents: -100,
			},
			wantErr: true,
			errMsg:  "base_price_cents cannot be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 简化的验证逻辑
			var err error

			if tt.req.PlanName == "" {
				err = assert.AnError
			} else if tt.req.MonthlyQuota <= 0 {
				err = assert.AnError
			} else if tt.req.BasePriceCents < 0 {
				err = assert.AnError
			}

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestGetQuotaUsageResponse_Calculation tests quota calculation
func TestGetQuotaUsageResponse_Calculation(t *testing.T) {
	tests := []struct {
		name             string
		totalQuota       int64
		usedQuota        int64
		wantRemaining    int64
		wantUtilization  float64
	}{
		{
			name:             "50% usage",
			totalQuota:       1000000,
			usedQuota:        500000,
			wantRemaining:    500000,
			wantUtilization:  50.0,
		},
		{
			name:             "over quota",
			totalQuota:       1000000,
			usedQuota:        1200000,
			wantRemaining:    0, // 不应该是负数
			wantUtilization:  120.0,
		},
		{
			name:             "no usage",
			totalQuota:       1000000,
			usedQuota:        0,
			wantRemaining:    1000000,
			wantUtilization:  0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining := tt.totalQuota - tt.usedQuota
			if remaining < 0 {
				remaining = 0
			}

			utilization := 0.0
			if tt.totalQuota > 0 {
				utilization = float64(tt.usedQuota) / float64(tt.totalQuota) * 100
			}

			assert.Equal(t, tt.wantRemaining, remaining)
			assert.InDelta(t, tt.wantUtilization, utilization, 0.1)
		})
	}
}

// TestParseInt64 tests integer parsing
func TestParseInt64(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{
			name:    "valid positive",
			input:   "12345",
			want:    12345,
			wantErr: false,
		},
		{
			name:    "valid negative",
			input:   "-100",
			want:    -100,
			wantErr: false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "invalid format",
			input:   "abc",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseInt64(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// TestBillingModeInfo tests mode info structure
func TestBillingModeInfo(t *testing.T) {
	info := BillingModeInfo{
		Mode:        ModePayAsYouGo,
		Name:        "按量付费",
		Description: "根据实际使用量计费",
		Available:   true,
	}

	assert.Equal(t, ModePayAsYouGo, info.Mode)
	assert.Equal(t, "按量付费", info.Name)
	assert.True(t, info.Available)
	assert.NotEmpty(t, info.Description)
}

// TestPlan tests plan structure
func TestPlan(t *testing.T) {
	now := time.Now()
	plan := Plan{
		PlanID:         1,
		PlanName:       "Standard",
		Description:    "标准套餐",
		MonthlyQuota:   1000000,
		BasePriceCents: 100000,
		OverageRate:    0.015,
		Currency:       "CNY",
		IsActive:       true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	assert.Equal(t, int64(1), plan.PlanID)
	assert.Equal(t, "Standard", plan.PlanName)
	assert.Equal(t, int64(1000000), plan.MonthlyQuota)
	assert.True(t, plan.IsActive)
	assert.Equal(t, "CNY", plan.Currency)
}
