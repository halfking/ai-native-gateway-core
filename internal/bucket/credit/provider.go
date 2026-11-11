package credit

import (
	"context"
	"fmt"
	"log/slog"
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

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// R189: 水位短路优化
	// 检查桶表是否已覆盖 lookback 窗口
	var oldest, newest *time.Time
	wmErr := p.pool.QueryRow(ctx, `
		SELECT MIN(bucket_start), MAX(bucket_start)
		  FROM maas_credit_consumption_buckets
	`).Scan(&oldest, &newest)
	
	if wmErr == nil && bucketsCoverLookback(time.Now(), oldest, newest, lookbackDays) {
		slog.Info("credit: backfill skipped — bucket table already covers lookback window",
			"lookback_days", lookbackDays, "oldest_bucket", oldest, "newest_bucket", newest)
		return &core.BucketResult{
			RowsAffected: 0,
			Metadata: map[string]interface{}{
				"skipped":       true,
				"reason":        "already_covered",
				"oldest_bucket": oldest,
				"newest_bucket": newest,
			},
		}, nil
	}

	// 执行回填
	logsTable, alias := requestLogsSource(lookbackDays)
	res, err := p.pool.Exec(ctx, `
		INSERT INTO maas_credit_consumption_buckets
			(tenant_id, bucket_start, credits, request_count, updated_at)
		SELECT
			`+alias+`.tenant_id,
			date_trunc('hour', `+alias+`.ts) AS bucket_start,
			COALESCE(SUM(`+requestLogCreditsSQL(alias, false)+`), 0)::bigint AS credits,
			COUNT(*)::int AS request_count,
			now() AS updated_at
		FROM `+logsTable+`
		WHERE `+alias+`.ts >= now() - ($1::int * INTERVAL '1 day')
		  AND `+alias+`.tenant_id NOT IN ('', 'default')
		  AND (`+alias+`.credits_charged IS NOT NULL
		    OR COALESCE(`+alias+`.prompt_tokens, 0)
		     + COALESCE(`+alias+`.completion_tokens, 0)
		     + COALESCE(`+alias+`.cache_read_tokens, 0)
		     + COALESCE(`+alias+`.cache_write_tokens, 0) > 0)
		GROUP BY `+alias+`.tenant_id, date_trunc('hour', `+alias+`.ts)
		ON CONFLICT (tenant_id, bucket_start) DO UPDATE
			SET credits       = EXCLUDED.credits,
			    request_count = EXCLUDED.request_count,
			    updated_at    = EXCLUDED.updated_at
	`, lookbackDays)

	if err != nil {
		slog.Warn("credit: backfill failed", "lookback_days", lookbackDays, "error", err)
		return nil, fmt.Errorf("backfill credit buckets: %w", err)
	}

	rows := res.RowsAffected()
	slog.Info("credit: backfill completed", "lookback_days", lookbackDays, "rows_affected", rows)

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

// Helper functions

// bucketsCoverLookback 检查桶表水位是否已覆盖 lookback 窗口
func bucketsCoverLookback(now time.Time, oldest, newest *time.Time, lookbackDays int) bool {
	if oldest == nil || newest == nil {
		return false
	}
	windowStart := now.Add(-time.Duration(lookbackDays) * 24 * time.Hour)
	headOK := !oldest.After(windowStart.Add(24 * time.Hour))
	tailOK := !newest.Before(now.Add(-2 * time.Hour))
	return headOK && tailOK
}

// requestLogsSource 返回请求日志表名和别名
func requestLogsSource(lookbackDays int) (table, alias string) {
	if lookbackDays <= 7 {
		return "_request_logs", "rl"
	}
	return "_request_logs_partitions", "rlp"
}

// requestLogCreditsSQL 返回计算信用的 SQL 表达式
func requestLogCreditsSQL(alias string, _ bool) string {
	return fmt.Sprintf(`COALESCE(%s.credits_charged, 
		(COALESCE(%s.prompt_tokens, 0) 
		 + COALESCE(%s.completion_tokens, 0)
		 + COALESCE(%s.cache_read_tokens, 0)
		 + COALESCE(%s.cache_write_tokens, 0)))`,
		alias, alias, alias, alias, alias)
}
