package credit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

// ServiceAdapter 适配器，保持与原有 maas.Service 的兼容性
type ServiceAdapter struct {
	provider *CreditBucketProvider
	manager  *core.BucketManager
}

// NewServiceAdapter 创建服务适配器
func NewServiceAdapter(pool *pgxpool.Pool, manager *core.BucketManager) *ServiceAdapter {
	cache := core.NewMemoryCache(10000)
	provider := NewCreditBucketProvider(pool, cache)

	// 注册到管理器
	if manager != nil {
		_ = manager.RegisterProvider(provider)
	}

	return &ServiceAdapter{
		provider: provider,
		manager:  manager,
	}
}

// BackfillCreditConsumptionBuckets 回填信用消耗桶
// 保持与原有 maas.Service.BackfillCreditConsumptionBuckets 的兼容性
func (a *ServiceAdapter) BackfillCreditConsumptionBuckets(ctx context.Context, lookbackDays int) (int64, error) {
	query := core.BucketQuery{
		Type:   core.BucketTypeCredit,
		Action: "backfill",
		Params: map[string]interface{}{
			"lookback_days": lookbackDays,
		},
	}

	result, err := a.provider.Query(ctx, query)
	if err != nil {
		return 0, err
	}

	return result.RowsAffected, nil
}

// QueryConsumedCreditsWindow 查询时间窗口内的信用消耗
// 保持与原有 maas.Service.QueryConsumedCreditsWindow 的兼容性
func (a *ServiceAdapter) QueryConsumedCreditsWindow(ctx context.Context, tenantID string, days int) (int64, error) {
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}

	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}

	// 方式1：直接调用 provider 的内部方法
	credits, err := a.provider.queryConsumedCredits(ctx, tenantID, days)
	if err != nil {
		return 0, err
	}

	return credits, nil
}

// QueryConsumedCreditsWindowV2 查询时间窗口内的信用消耗（新版本）
// 返回完整的 BucketResult，包含更多元数据
func (a *ServiceAdapter) QueryConsumedCreditsWindowV2(ctx context.Context, tenantID string, days int) (*core.BucketResult, error) {
	query := core.BucketQuery{
		Type:     core.BucketTypeCredit,
		TenantID: tenantID,
		Action:   "query_window",
		Params: map[string]interface{}{
			"days": days,
		},
	}

	return a.provider.Query(ctx, query)
}

// GetState 获取桶状态（新接口）
func (a *ServiceAdapter) GetState(ctx context.Context, tenantID string, windowDays int) (*core.BucketState, error) {
	key := core.BucketKey{
		Type:     core.BucketTypeCredit,
		TenantID: tenantID,
		Window:   core.NewTimeWindow(core.CurrentHourWindow().Start, time.Duration(windowDays)*24*time.Hour),
	}

	if a.manager != nil {
		return a.manager.GetState(ctx, key)
	}

	return a.provider.GetState(ctx, key)
}

// GetProvider 获取底层的 provider（用于高级用法）
func (a *ServiceAdapter) GetProvider() *CreditBucketProvider {
	return a.provider
}

// GetManager 获取管理器（用于高级用法）
func (a *ServiceAdapter) GetManager() *core.BucketManager {
	return a.manager
}

// Enabled 返回服务是否已启用
func (a *ServiceAdapter) Enabled() bool {
	return a.provider.enabled
}
