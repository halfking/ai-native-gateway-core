package billing

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisQuotaManager 基于Redis的配额管理器
//
// 特性：
// - 实时配额跟踪
// - 高性能（Redis原子操作）
// - 分布式安全（多实例环境）
// - 自动过期（周期结束后清理）
//
// Redis数据结构：
//   Key: "quota:{tenant_id}:{period}" (如 "quota:tenant_123:2026-11")
//   Value: 已使用量（整数）
//   TTL: 自动过期（周期结束后7天）
type RedisQuotaManager struct {
	client *redis.Client
	prefix string
}

// NewRedisQuotaManager 创建Redis配额管理器
func NewRedisQuotaManager(client *redis.Client) *RedisQuotaManager {
	return &RedisQuotaManager{
		client: client,
		prefix: "quota:",
	}
}

// GetUsage 获取当前周期的使用量
func (m *RedisQuotaManager) GetUsage(ctx context.Context, tenantID string, period TimePeriod) (*QuotaUsage, error) {
	key := m.buildKey(tenantID, period)

	// 从Redis获取使用量
	usedStr, err := m.client.Get(ctx, key).Result()
	if err == redis.Nil {
		// 键不存在，说明还没有使用
		return &QuotaUsage{
			TenantID:       tenantID,
			PeriodStart:    period.Start,
			PeriodEnd:      period.End,
			UsedQuota:      0,
			RemainingQuota: 0, // 需要外部设置总配额
			OverageAmount:  0,
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("redis get: %w", err)
	}

	used, err := strconv.ParseInt(usedStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse usage: %w", err)
	}

	return &QuotaUsage{
		TenantID:    tenantID,
		PeriodStart: period.Start,
		PeriodEnd:   period.End,
		UsedQuota:   used,
	}, nil
}

// RecordUsage 记录使用量（增量）
//
// 使用Redis INCRBY保证原子性和线程安全。
func (m *RedisQuotaManager) RecordUsage(ctx context.Context, tenantID string, amount int64) error {
	if amount <= 0 {
		return fmt.Errorf("amount must be positive, got: %d", amount)
	}

	// 使用当前时间确定周期
	now := time.Now()
	period := m.getCurrentPeriod(now)
	key := m.buildKey(tenantID, period)

	// 原子增加
	if err := m.client.IncrBy(ctx, key, amount).Err(); err != nil {
		return fmt.Errorf("redis incrby: %w", err)
	}

	// 设置TTL（如果是新键）
	// 周期结束后保留7天，用于账单生成和对账
	ttl := period.End.Sub(now) + 7*24*time.Hour
	m.client.Expire(ctx, key, ttl)

	return nil
}

// ResetQuota 重置配额（新周期开始时）
func (m *RedisQuotaManager) ResetQuota(ctx context.Context, tenantID string, period TimePeriod) error {
	key := m.buildKey(tenantID, period)

	// 删除旧周期的数据
	if err := m.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}

	return nil
}

// GetRemainingQuota 获取剩余配额
//
// 注意：需要传入总配额，因为配额信息存储在数据库中。
func (m *RedisQuotaManager) GetRemainingQuota(ctx context.Context, tenantID string, period TimePeriod) (int64, error) {
	usage, err := m.GetUsage(ctx, tenantID, period)
	if err != nil {
		return 0, err
	}

	// 注意：这里无法知道总配额，调用者需要自行计算
	// remaining = totalQuota - usage.UsedQuota
	return -usage.UsedQuota, nil // 返回负的已使用量，让调用者计算
}

// CheckQuotaExceeded 检查是否超额
//
// 返回：是否超额，超额量
func (m *RedisQuotaManager) CheckQuotaExceeded(ctx context.Context, tenantID string, period TimePeriod, totalQuota int64) (bool, int64, error) {
	usage, err := m.GetUsage(ctx, tenantID, period)
	if err != nil {
		return false, 0, err
	}

	if usage.UsedQuota > totalQuota {
		overage := usage.UsedQuota - totalQuota
		return true, overage, nil
	}

	return false, 0, nil
}

// 内部辅助方法

func (m *RedisQuotaManager) buildKey(tenantID string, period TimePeriod) string {
	// Key格式: quota:{tenant_id}:{period}
	// period格式: YYYY-MM (月度)
	periodStr := period.Start.Format("2006-01")
	return fmt.Sprintf("%s%s:%s", m.prefix, tenantID, periodStr)
}

func (m *RedisQuotaManager) getCurrentPeriod(now time.Time) TimePeriod {
	// 计算当前月度周期
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0).Add(-time.Second) // 月末最后一秒

	return TimePeriod{
		Start: start,
		End:   end,
	}
}

// BatchGetUsage 批量获取多个租户的使用量
//
// 性能优化：使用Pipeline减少网络往返。
func (m *RedisQuotaManager) BatchGetUsage(ctx context.Context, tenantIDs []string, period TimePeriod) (map[string]*QuotaUsage, error) {
	if len(tenantIDs) == 0 {
		return make(map[string]*QuotaUsage), nil
	}

	// 构建所有键
	keys := make([]string, len(tenantIDs))
	for i, tenantID := range tenantIDs {
		keys[i] = m.buildKey(tenantID, period)
	}

	// 使用Pipeline批量获取
	pipe := m.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(keys))
	for i, key := range keys {
		cmds[i] = pipe.Get(ctx, key)
	}

	_, _ = pipe.Exec(ctx)

	// 解析结果
	results := make(map[string]*QuotaUsage)
	for i, cmd := range cmds {
		tenantID := tenantIDs[i]
		
		usedStr, err := cmd.Result()
		if err == redis.Nil {
			// 未使用
			results[tenantID] = &QuotaUsage{
				TenantID:    tenantID,
				PeriodStart: period.Start,
				PeriodEnd:   period.End,
				UsedQuota:   0,
			}
			continue
		}
		if err != nil {
			continue // 跳过错误的
		}

		used, err := strconv.ParseInt(usedStr, 10, 64)
		if err != nil {
			continue
		}

		results[tenantID] = &QuotaUsage{
			TenantID:    tenantID,
			PeriodStart: period.Start,
			PeriodEnd:   period.End,
			UsedQuota:   used,
		}
	}

	return results, nil
}

// InMemoryQuotaManager 内存配额管理器（用于测试）
//
// 警告：
// - 仅用于单实例测试环境
// - 重启后数据丢失
// - 不支持分布式
type InMemoryQuotaManager struct {
	usage map[string]int64 // key: tenant_id:period
}

// NewInMemoryQuotaManager 创建内存配额管理器
func NewInMemoryQuotaManager() *InMemoryQuotaManager {
	return &InMemoryQuotaManager{
		usage: make(map[string]int64),
	}
}

// GetUsage 获取使用量
func (m *InMemoryQuotaManager) GetUsage(ctx context.Context, tenantID string, period TimePeriod) (*QuotaUsage, error) {
	key := m.buildKey(tenantID, period)
	used := m.usage[key]

	return &QuotaUsage{
		TenantID:    tenantID,
		PeriodStart: period.Start,
		PeriodEnd:   period.End,
		UsedQuota:   used,
	}, nil
}

// RecordUsage 记录使用量
func (m *InMemoryQuotaManager) RecordUsage(ctx context.Context, tenantID string, amount int64) error {
	now := time.Now()
	period := m.getCurrentPeriod(now)
	key := m.buildKey(tenantID, period)

	m.usage[key] += amount
	return nil
}

// ResetQuota 重置配额
func (m *InMemoryQuotaManager) ResetQuota(ctx context.Context, tenantID string, period TimePeriod) error {
	key := m.buildKey(tenantID, period)
	delete(m.usage, key)
	return nil
}

// GetRemainingQuota 获取剩余配额
func (m *InMemoryQuotaManager) GetRemainingQuota(ctx context.Context, tenantID string, period TimePeriod) (int64, error) {
	usage, err := m.GetUsage(ctx, tenantID, period)
	if err != nil {
		return 0, err
	}
	return -usage.UsedQuota, nil
}

func (m *InMemoryQuotaManager) buildKey(tenantID string, period TimePeriod) string {
	periodStr := period.Start.Format("2006-01")
	return fmt.Sprintf("%s:%s", tenantID, periodStr)
}

func (m *InMemoryQuotaManager) getCurrentPeriod(now time.Time) TimePeriod {
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	end := start.AddDate(0, 1, 0).Add(-time.Second)
	return TimePeriod{Start: start, End: end}
}

// NopQuotaManager 空配额管理器（用于不需要配额跟踪的场景）
type NopQuotaManager struct{}

func (*NopQuotaManager) GetUsage(ctx context.Context, tenantID string, period TimePeriod) (*QuotaUsage, error) {
	return &QuotaUsage{
		TenantID:    tenantID,
		PeriodStart: period.Start,
		PeriodEnd:   period.End,
		UsedQuota:   0,
	}, nil
}

func (*NopQuotaManager) RecordUsage(ctx context.Context, tenantID string, amount int64) error {
	return nil
}

func (*NopQuotaManager) ResetQuota(ctx context.Context, tenantID string, period TimePeriod) error {
	return nil
}

func (*NopQuotaManager) GetRemainingQuota(ctx context.Context, tenantID string, period TimePeriod) (int64, error) {
	return 0, nil
}
