package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DatabaseConfigLoader 从数据库加载计费配置
//
// 实现特性：
// - 自动集成多级缓存
// - 支持API Key、租户ID、用户ID多种查询
// - 预加载热门配置
// - 配置失效管理
type DatabaseConfigLoader struct {
	pool  *pgxpool.Pool
	cache BillingConfigCache
}

// NewDatabaseConfigLoader 创建数据库配置加载器
func NewDatabaseConfigLoader(pool *pgxpool.Pool, cache BillingConfigCache) *DatabaseConfigLoader {
	return &DatabaseConfigLoader{
		pool:  pool,
		cache: cache,
	}
}

// LoadByAPIKey 通过API Key加载计费配置
//
// 查询流程：
// 1. 尝试从缓存获取
// 2. 未命中则查询数据库：api_keys → api_key_billing_map → billing_configs
// 3. 加载成功后回填缓存
func (l *DatabaseConfigLoader) LoadByAPIKey(ctx context.Context, apiKey string) (*BillingConfig, error) {
	// 生成缓存键
	cacheKey := CacheKey{}.ForAPIKey(apiKey)

	// 1. 尝试从缓存获取
	if l.cache != nil {
		config, err := l.cache.Get(ctx, cacheKey)
		if err == nil && config != nil {
			return config, nil
		}
		// 缓存未命中，继续从数据库加载
	}

	// 2. 从数据库查询
	config, err := l.loadFromDBByAPIKey(ctx, apiKey)
	if err != nil {
		return nil, fmt.Errorf("load from database: %w", err)
	}

	if config == nil {
		return nil, fmt.Errorf("config not found for api_key")
	}

	// 3. 回填缓存
	config.LoadedAt = time.Now()
	if l.cache != nil {
		l.cache.Set(ctx, cacheKey, config)
	}

	return config, nil
}

// LoadByTenantID 通过租户ID加载计费配置
func (l *DatabaseConfigLoader) LoadByTenantID(ctx context.Context, tenantID string) (*BillingConfig, error) {
	cacheKey := CacheKey{}.ForTenant(tenantID)

	// 尝试从缓存获取
	if l.cache != nil {
		config, err := l.cache.Get(ctx, cacheKey)
		if err == nil && config != nil {
			return config, nil
		}
	}

	// 从数据库加载
	config, err := l.loadFromDBByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load from database: %w", err)
	}

	if config == nil {
		return nil, fmt.Errorf("config not found for tenant: %s", tenantID)
	}

	// 回填缓存
	config.LoadedAt = time.Now()
	if l.cache != nil {
		l.cache.Set(ctx, cacheKey, config)
	}

	return config, nil
}

// LoadByUserID 通过用户ID加载计费配置
func (l *DatabaseConfigLoader) LoadByUserID(ctx context.Context, userID string) (*BillingConfig, error) {
	// 注意：需要先获取租户ID，简化实现假设user_id格式包含租户信息
	// 实际实现需要查询 users 表获取 tenant_id
	
	// 从数据库加载（简化查询）
	query := `
		SELECT bc.id, bc.tenant_id, bc.user_id, bc.billing_mode, bc.plan_id,
		       bc.engine_config, bc.discount_rules, bc.quota_limits,
		       bc.cache_priority, bc.cache_ttl_seconds, bc.preload_hint,
		       bc.created_at, bc.updated_at
		FROM billing_configs bc
		WHERE bc.user_id = $1
		LIMIT 1
	`

	var config BillingConfig
	var planID *int64
	var engineConfigJSON, discountRulesJSON, quotaLimitsJSON []byte
	var cacheTTLSeconds int

	err := l.pool.QueryRow(ctx, query, userID).Scan(
		&config.ID,
		&config.TenantID,
		&config.UserID,
		&config.BillingMode,
		&planID,
		&engineConfigJSON,
		&discountRulesJSON,
		&quotaLimitsJSON,
		&config.CacheHint.Priority,
		&cacheTTLSeconds,
		&config.CacheHint.PreloadHint,
		&config.CreatedAt,
		&config.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("query user config: %w", err)
	}

	config.PlanID = planID
	config.CacheHint.TTL = time.Duration(cacheTTLSeconds) * time.Second

	// 解析JSON字段
	if err := json.Unmarshal(engineConfigJSON, &config.EngineConfig); err != nil {
		return nil, fmt.Errorf("parse engine_config: %w", err)
	}

	if len(discountRulesJSON) > 0 {
		if err := json.Unmarshal(discountRulesJSON, &config.DiscountRules); err != nil {
			return nil, fmt.Errorf("parse discount_rules: %w", err)
		}
	}

	if len(quotaLimitsJSON) > 0 {
		config.QuotaLimits = &QuotaLimits{}
		if err := json.Unmarshal(quotaLimitsJSON, config.QuotaLimits); err != nil {
			return nil, fmt.Errorf("parse quota_limits: %w", err)
		}
	}

	config.LoadedAt = time.Now()
	return &config, nil
}

// PreloadHotConfigs 预加载热门配置
//
// 基于 request_stats_hourly 表识别热门API Key，提前加载到缓存。
// 策略：最近1小时请求数 > 阈值的前N个
func (l *DatabaseConfigLoader) PreloadHotConfigs(ctx context.Context, limit int) error {
	if l.cache == nil {
		return fmt.Errorf("cache not configured")
	}

	// 查询热门API Keys
	query := `
		SELECT akbm.api_key_hash
		FROM api_key_billing_map akbm
		WHERE akbm.request_count_1h > 100  -- 阈值：1小时内>100次请求
		ORDER BY akbm.request_count_1h DESC
		LIMIT $1
	`

	rows, err := l.pool.Query(ctx, query, limit)
	if err != nil {
		return fmt.Errorf("query hot api keys: %w", err)
	}
	defer rows.Close()

	preloadCount := 0
	for rows.Next() {
		var apiKeyHash string
		if err := rows.Scan(&apiKeyHash); err != nil {
			continue
		}

		// 加载配置（会自动缓存）
		_, err := l.LoadByAPIKey(ctx, apiKeyHash)
		if err != nil {
			// 记录错误但继续下一个
			continue
		}

		preloadCount++
	}

	return nil
}

// InvalidateCache 失效指定租户的缓存
func (l *DatabaseConfigLoader) InvalidateCache(ctx context.Context, tenantID string) error {
	if l.cache == nil {
		return nil
	}

	return l.cache.InvalidateTenant(ctx, tenantID)
}

// 内部方法：从数据库加载配置

func (l *DatabaseConfigLoader) loadFromDBByAPIKey(ctx context.Context, apiKey string) (*BillingConfig, error) {
	// 通过API Key查询配置
	// 路径：api_keys → api_key_billing_map → billing_configs
	query := `
		SELECT bc.id, bc.tenant_id, bc.user_id, bc.billing_mode, bc.plan_id,
		       bc.engine_config, bc.discount_rules, bc.quota_limits,
		       bc.cache_priority, bc.cache_ttl_seconds, bc.preload_hint,
		       bc.created_at, bc.updated_at
		FROM billing_configs bc
		INNER JOIN api_key_billing_map akbm ON akbm.billing_config_id = bc.id
		INNER JOIN api_keys ak ON ak.id = akbm.api_key_id
		WHERE ak.key_hash = $1
		LIMIT 1
	`

	return l.scanConfig(ctx, query, apiKey)
}

func (l *DatabaseConfigLoader) loadFromDBByTenant(ctx context.Context, tenantID string) (*BillingConfig, error) {
	query := `
		SELECT id, tenant_id, user_id, billing_mode, plan_id,
		       engine_config, discount_rules, quota_limits,
		       cache_priority, cache_ttl_seconds, preload_hint,
		       created_at, updated_at
		FROM billing_configs
		WHERE tenant_id = $1 AND user_id IS NULL
		LIMIT 1
	`

	return l.scanConfig(ctx, query, tenantID)
}

func (l *DatabaseConfigLoader) scanConfig(ctx context.Context, query string, args ...interface{}) (*BillingConfig, error) {
	var config BillingConfig
	var planID *int64
	var engineConfigJSON, discountRulesJSON, quotaLimitsJSON []byte
	var cacheTTLSeconds int

	err := l.pool.QueryRow(ctx, query, args...).Scan(
		&config.ID,
		&config.TenantID,
		&config.UserID,
		&config.BillingMode,
		&planID,
		&engineConfigJSON,
		&discountRulesJSON,
		&quotaLimitsJSON,
		&config.CacheHint.Priority,
		&cacheTTLSeconds,
		&config.CacheHint.PreloadHint,
		&config.CreatedAt,
		&config.UpdatedAt,
	)

	if err != nil {
		return nil, err
	}

	config.PlanID = planID
	config.CacheHint.TTL = time.Duration(cacheTTLSeconds) * time.Second

	// 解析JSON字段
	if err := json.Unmarshal(engineConfigJSON, &config.EngineConfig); err != nil {
		return nil, fmt.Errorf("parse engine_config: %w", err)
	}

	if len(discountRulesJSON) > 0 {
		if err := json.Unmarshal(discountRulesJSON, &config.DiscountRules); err != nil {
			return nil, fmt.Errorf("parse discount_rules: %w", err)
		}
	}

	if len(quotaLimitsJSON) > 0 {
		config.QuotaLimits = &QuotaLimits{}
		if err := json.Unmarshal(quotaLimitsJSON, config.QuotaLimits); err != nil {
			return nil, fmt.Errorf("parse quota_limits: %w", err)
		}
	}

	return &config, nil
}

// UpdateRequestStats 更新API Key的请求统计
//
// 应该在每次请求处理后调用，用于识别热数据。
func (l *DatabaseConfigLoader) UpdateRequestStats(ctx context.Context, apiKeyID int64) error {
	query := `
		INSERT INTO api_key_billing_map (api_key_id, billing_config_id, request_count_1h, last_used_at)
		VALUES ($1, (SELECT billing_config_id FROM api_key_billing_map WHERE api_key_id = $1), 1, NOW())
		ON CONFLICT (api_key_id) 
		DO UPDATE SET 
			request_count_1h = api_key_billing_map.request_count_1h + 1,
			last_used_at = NOW()
	`

	_, err := l.pool.Exec(ctx, query, apiKeyID)
	return err
}

// ResetHourlyStats 重置小时统计
//
// 应该通过定时任务每小时调用一次，重置计数器。
func (l *DatabaseConfigLoader) ResetHourlyStats(ctx context.Context) error {
	query := `
		UPDATE api_key_billing_map 
		SET request_count_1h = 0
		WHERE last_used_at < NOW() - INTERVAL '1 hour'
	`

	_, err := l.pool.Exec(ctx, query)
	return err
}
