package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisBillingCache 实现基于Redis的L2缓存
//
// 特性：
// - JSON序列化存储
// - TTL自动过期
// - 连接池复用
// - 错误容忍（Redis故障不影响业务）
type RedisBillingCache struct {
	client *redis.Client
	prefix string // 键前缀，用于命名空间隔离
}

// NewRedisBillingCache 创建Redis缓存实例
//
// client: Redis客户端连接
// prefix: 键前缀，建议使用 "billing:" 或类似命名空间
func NewRedisBillingCache(client *redis.Client, prefix string) *RedisBillingCache {
	if prefix == "" {
		prefix = "billing:"
	}

	return &RedisBillingCache{
		client: client,
		prefix: prefix,
	}
}

// Get 从Redis获取配置
func (r *RedisBillingCache) Get(ctx context.Context, key string) (*BillingConfig, error) {
	fullKey := r.prefix + key

	data, err := r.client.Get(ctx, fullKey).Bytes()
	if err == redis.Nil {
		// 键不存在
		return nil, fmt.Errorf("not found")
	}
	if err != nil {
		// Redis错误
		return nil, fmt.Errorf("redis get: %w", err)
	}

	// 反序列化
	var config BillingConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}

	return &config, nil
}

// Set 将配置存储到Redis
func (r *RedisBillingCache) Set(ctx context.Context, key string, config *BillingConfig, ttl time.Duration) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	fullKey := r.prefix + key

	// 序列化
	data, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// 存储到Redis
	if err := r.client.Set(ctx, fullKey, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis set: %w", err)
	}

	return nil
}

// Delete 从Redis删除配置
func (r *RedisBillingCache) Delete(ctx context.Context, key string) error {
	fullKey := r.prefix + key

	if err := r.client.Del(ctx, fullKey).Err(); err != nil {
		return fmt.Errorf("redis del: %w", err)
	}

	return nil
}

// Close 关闭Redis连接
//
// 注意：如果Redis客户端是共享的，不要在这里关闭。
// 此方法主要用于测试场景。
func (r *RedisBillingCache) Close() error {
	// 不关闭共享的client，由外部管理
	return nil
}

// DeleteByPattern 按模式删除键（用于批量失效）
//
// 警告：KEYS命令在生产环境中可能阻塞Redis，建议使用SCAN替代。
// 此方法仅用于开发/测试环境。
func (r *RedisBillingCache) DeleteByPattern(ctx context.Context, pattern string) error {
	fullPattern := r.prefix + pattern

	// 使用SCAN而不是KEYS（生产安全）
	iter := r.client.Scan(ctx, 0, fullPattern, 100).Iterator()
	
	keys := make([]string, 0, 100)
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}

	if err := iter.Err(); err != nil {
		return fmt.Errorf("scan keys: %w", err)
	}

	// 批量删除
	if len(keys) > 0 {
		if err := r.client.Del(ctx, keys...).Err(); err != nil {
			return fmt.Errorf("batch delete: %w", err)
		}
	}

	return nil
}

// Stats 获取缓存统计
//
// 返回Redis的基本统计信息（键数量、内存使用等）
func (r *RedisBillingCache) Stats(ctx context.Context) (map[string]interface{}, error) {
	info, err := r.client.Info(ctx, "stats", "memory").Result()
	if err != nil {
		return nil, fmt.Errorf("redis info: %w", err)
	}

	// 获取键数量
	dbSize, err := r.client.DBSize(ctx).Result()
	if err != nil {
		return nil, fmt.Errorf("redis dbsize: %w", err)
	}

	stats := map[string]interface{}{
		"db_size":   dbSize,
		"info_raw":  info,
		"connected": true,
	}

	return stats, nil
}

// Ping 检查Redis连接是否正常
func (r *RedisBillingCache) Ping(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// ResilientRedisCache 带容错机制的Redis缓存包装器
//
// 特性：
// - Redis故障时自动降级（返回未找到，而不是错误）
// - 记录错误但不阻塞业务
// - 适合生产环境使用
type ResilientRedisCache struct {
	inner         *RedisBillingCache
	errorCallback func(operation string, err error) // 错误回调，用于日志记录
}

// NewResilientRedisCache 创建容错Redis缓存
func NewResilientRedisCache(client *redis.Client, prefix string, errorCallback func(string, error)) *ResilientRedisCache {
	return &ResilientRedisCache{
		inner:         NewRedisBillingCache(client, prefix),
		errorCallback: errorCallback,
	}
}

// Get 获取配置（容错版本）
func (r *ResilientRedisCache) Get(ctx context.Context, key string) (*BillingConfig, error) {
	config, err := r.inner.Get(ctx, key)
	if err != nil {
		// 记录错误但不返回，让调用者降级到L3
		if r.errorCallback != nil {
			r.errorCallback("get", err)
		}
		return nil, fmt.Errorf("not found") // 统一返回未找到
	}
	return config, nil
}

// Set 存储配置（容错版本）
func (r *ResilientRedisCache) Set(ctx context.Context, key string, config *BillingConfig, ttl time.Duration) error {
	err := r.inner.Set(ctx, key, config, ttl)
	if err != nil {
		// 记录错误但不返回，允许业务继续
		if r.errorCallback != nil {
			r.errorCallback("set", err)
		}
		return nil // 不返回错误，避免阻塞
	}
	return nil
}

// Delete 删除配置（容错版本）
func (r *ResilientRedisCache) Delete(ctx context.Context, key string) error {
	err := r.inner.Delete(ctx, key)
	if err != nil && r.errorCallback != nil {
		r.errorCallback("delete", err)
	}
	return nil // 不返回错误
}

// Close 关闭连接
func (r *ResilientRedisCache) Close() error {
	return r.inner.Close()
}

// RedisConfigOptions Redis配置选项
type RedisConfigOptions struct {
	Addr         string        // Redis地址，如 "localhost:6379"
	Password     string        // 密码
	DB           int           // 数据库编号
	PoolSize     int           // 连接池大小
	MinIdleConns int           // 最小空闲连接数
	MaxRetries   int           // 最大重试次数
	DialTimeout  time.Duration // 连接超时
	ReadTimeout  time.Duration // 读超时
	WriteTimeout time.Duration // 写超时
}

// NewRedisClientFromOptions 从配置创建Redis客户端
func NewRedisClientFromOptions(opts RedisConfigOptions) *redis.Client {
	// 设置默认值
	if opts.PoolSize == 0 {
		opts.PoolSize = 10
	}
	if opts.MinIdleConns == 0 {
		opts.MinIdleConns = 2
	}
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 5 * time.Second
	}
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 3 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 3 * time.Second
	}

	return redis.NewClient(&redis.Options{
		Addr:         opts.Addr,
		Password:     opts.Password,
		DB:           opts.DB,
		PoolSize:     opts.PoolSize,
		MinIdleConns: opts.MinIdleConns,
		MaxRetries:   opts.MaxRetries,
		DialTimeout:  opts.DialTimeout,
		ReadTimeout:  opts.ReadTimeout,
		WriteTimeout: opts.WriteTimeout,
	})
}

// BatchGetConfigs 批量获取配置（性能优化）
//
// 使用Redis Pipeline减少网络往返
func (r *RedisBillingCache) BatchGetConfigs(ctx context.Context, keys []string) (map[string]*BillingConfig, error) {
	if len(keys) == 0 {
		return make(map[string]*BillingConfig), nil
	}

	// 构建完整键
	fullKeys := make([]string, len(keys))
	for i, key := range keys {
		fullKeys[i] = r.prefix + key
	}

	// 使用Pipeline批量获取
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(fullKeys))
	for i, fullKey := range fullKeys {
		cmds[i] = pipe.Get(ctx, fullKey)
	}

	_, _ = pipe.Exec(ctx)
	// Pipeline错误处理：即使部分失败也继续处理成功的

	// 解析结果
	results := make(map[string]*BillingConfig)
	for i, cmd := range cmds {
		data, err := cmd.Bytes()
		if err != nil {
			continue // 跳过失败的
		}

		var config BillingConfig
		if err := json.Unmarshal(data, &config); err != nil {
			continue // 跳过解析失败的
		}

		results[keys[i]] = &config
	}

	return results, nil
}

// BatchSetConfigs 批量设置配置
func (r *RedisBillingCache) BatchSetConfigs(ctx context.Context, configs map[string]*BillingConfig, ttl time.Duration) error {
	if len(configs) == 0 {
		return nil
	}

	pipe := r.client.Pipeline()
	for key, config := range configs {
		data, err := json.Marshal(config)
		if err != nil {
			continue // 跳过序列化失败的
		}

		fullKey := r.prefix + key
		pipe.Set(ctx, fullKey, data, ttl)
	}

	_, err := pipe.Exec(ctx)
	return err
}
