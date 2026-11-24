package credit

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/bucket/core"
)

// CreditBucketProvider 信用桶提供者
// 实现基于 PostgreSQL 的小时级信用消耗统计
type CreditBucketProvider struct {
	pool    *pgxpool.Pool
	cache   core.BucketCache
	enabled bool
}

// NewCreditBucketProvider 创建信用桶提供者
func NewCreditBucketProvider(pool *pgxpool.Pool, cache core.BucketCache) *CreditBucketProvider {
	return &CreditBucketProvider{
		pool:    pool,
		cache:   cache,
		enabled: pool != nil,
	}
}

// Name 返回提供者名称
func (p *CreditBucketProvider) Name() string {
	return "credit-bucket-provider"
}

// Type 返回桶类型
func (p *CreditBucketProvider) Type() core.BucketType {
	return core.BucketTypeCredit
}

// GetState 获取桶状态
func (p *CreditBucketProvider) GetState(ctx context.Context, key core.BucketKey) (*core.BucketState, error) {
	if !p.enabled {
		return nil, &core.BucketError{
			Type:    core.ErrTypeNotFound,
			Message: "credit bucket provider not enabled",
		}
	}

	// 1. 尝试从缓存获取
	if p.cache != nil {
		if state, ok := p.cache.Get(key); ok {
			return state, nil
		}
	}

	// 2. 从数据库查询
	// 计算时间窗口内的总消耗
	windowDays := int(key.Window.Size.Hours() / 24)
	if windowDays < 1 {
		windowDays = 1
	}
	if windowDays > 90 {
		windowDays = 90
	}

	credits, err := p.queryConsumedCredits(ctx, key.TenantID, windowDays)
	if err != nil {
		return nil, &core.BucketError{
			Type:    core.ErrTypeStorage,
			Message: "failed to query consumed credits",
			Cause:   err,
		}
	}

	// 3. 构建状态
	state := &core.BucketState{
		Capacity:  0, // 信用桶没有固定容量，由配额系统控制
		Used:      credits,
		Reserved:  0,
		Available: 0, // 需要从配额系统获取
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"window_days": windowDays,
			"tenant_id":   key.TenantID,
		},
	}

	// 4. 更新缓存
	if p.cache != nil {
		_ = p.cache.Set(key, state, 5*time.Minute)
	}

	return state, nil
}

// UpdateState 更新桶状态
// 注意：信用桶的更新主要通过 ChargeRequest 实时写入，这里主要用于手动调整
func (p *CreditBucketProvider) UpdateState(ctx context.Context, key core.BucketKey, delta core.BucketDelta) (*core.BucketState, error) {
	if !p.enabled {
		return nil, &core.BucketError{
			Type:    core.ErrTypeNotFound,
			Message: "credit bucket provider not enabled",
		}
	}

	// 信用桶的状态主要由请求日志自动更新
	// 这里返回当前状态，不直接修改
	return p.GetState(ctx, key)
}

// Query 执行桶查询操作
func (p *CreditBucketProvider) Query(ctx context.Context, query core.BucketQuery) (*core.BucketResult, error) {
	if !p.enabled {
		return nil, &core.BucketError{
			Type:    core.ErrTypeNotFound,
			Message: "credit bucket provider not enabled",
		}
	}

	switch query.Action {
	case "backfill":
		return p.backfillCreditBuckets(ctx, query)
	case "query_window":
		return p.queryWindowCredits(ctx, query)
	default:
		return nil, &core.BucketError{
			Type:    core.ErrTypeInvalidInput,
			Message: fmt.Sprintf("unsupported action: %s", query.Action),
		}
	}
}

// Reset 重置桶状态
func (p *CreditBucketProvider) Reset(ctx context.Context, key core.BucketKey) error {
	if !p.enabled {
		return &core.BucketError{
			Type:    core.ErrTypeNotFound,
			Message: "credit bucket provider not enabled",
		}
	}

	// 清除缓存
	if p.cache != nil {
		_ = p.cache.Delete(key)
	}

	// 注意：不直接删除数据库中的桶数据，因为这是历史统计数据
	return nil
}

// GetMetrics 获取监控指标
func (p *CreditBucketProvider) GetMetrics(ctx context.Context) (*core.BucketMetrics, error) {
	// TODO: 实现指标收集
	return &core.BucketMetrics{
		TotalRequests:   0,
		SuccessRequests: 0,
		FailedRequests:  0,
	}, nil
}

// queryConsumedCredits 查询指定租户在时间窗口内的信用消耗
// 这是 QueryConsumedCreditsWindow 的内部实现
func (p *CreditBucketProvider) queryConsumedCredits(ctx context.Context, tenantID string, days int) (int64, error) {
	if tenantID == "" {
		return 0, fmt.Errorf("tenant_id required")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var credits int64
	err := p.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(credits), 0)::bigint
		  FROM maas_credit_consumption_buckets
		 WHERE tenant_id = $1
		   AND bucket_start >= now() - ($2::int * INTERVAL '1 day')
	`, tenantID, days).Scan(&credits)

	if err != nil {
		return 0, fmt.Errorf("query consumed credits: %w", err)
	}

	return credits, nil
}

// backfillCreditBuckets 执行信用桶回填操作
func (p *CreditBucketProvider) backfillCreditBuckets(ctx context.Context, query core.BucketQuery) (*core.BucketResult, error) {
	lookbackDays := 90 // 默认 90 天
	if days, ok := query.Params["lookback_days"].(int); ok && days > 0 {
		lookbackDays = days
	}
	if lookbackDays < 1 {
		lookbackDays = 1
	}
	if lookbackDays > 90 {
		lookbackDays = 90
	}

	rows, err := BackfillPostedConsumption(ctx, p.pool, lookbackDays)
	if err != nil {
		return nil, err
	}

	return &core.BucketResult{
		RowsAffected: rows,
		Metadata: map[string]interface{}{
			"lookback_days": lookbackDays,
		},
	}, nil
}

// queryWindowCredits 查询时间窗口内的信用消耗
func (p *CreditBucketProvider) queryWindowCredits(ctx context.Context, query core.BucketQuery) (*core.BucketResult, error) {
	tenantID := query.TenantID
	if tenantID == "" {
		return nil, &core.BucketError{
			Type:    core.ErrTypeInvalidInput,
			Message: "tenant_id required for query_window action",
		}
	}

	windowDays := 30 // 默认 30 天
	if days, ok := query.Params["days"].(int); ok && days > 0 {
		windowDays = days
	}
	if windowDays < 1 {
		windowDays = 1
	}
	if windowDays > 90 {
		windowDays = 90
	}

	credits, err := p.queryConsumedCredits(ctx, tenantID, windowDays)
	if err != nil {
		return nil, err
	}

	state := core.BucketState{
		Used:      credits,
		UpdatedAt: time.Now(),
		Metadata: map[string]interface{}{
			"tenant_id":   tenantID,
			"window_days": windowDays,
		},
	}

	return &core.BucketResult{
		RowsAffected: 1,
		Data:         []core.BucketState{state},
		Metadata: map[string]interface{}{
			"tenant_id":   tenantID,
			"window_days": windowDays,
			"credits":     credits,
		},
	}, nil
}
