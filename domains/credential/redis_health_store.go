package credential

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	redissafe "github.com/kaixuan/llm-gateway-go/internal/redis"
	"github.com/redis/go-redis/v9"
)

// RedisHealthStore Redis-backed health state store with memory cache fallback.
//
// 架构：混合模式（P1优化方案B）
//   - 读：优先内存（零延迟）
//   - 写：内存+Redis双写（异步，不阻塞）
//   - 启动：从Redis恢复
//   - Redis故障：降级到纯内存模式
type RedisHealthStore struct {
	client *redis.Client
	memory *InMemoryStore // fallback cache
	logger *slog.Logger

	// 配置
	keyPrefix string
	ttl       time.Duration
	enabled   bool

	// nextConsistencyCheck bounds background Redis verification triggered by
	// reads. The memory cache is authoritative between writes and startup
	// recovery, so verifying every read only creates unbounded goroutine and
	// connection-pool pressure on the hot path.
	nextConsistencyCheck atomic.Int64
}

const (
	defaultKeyPrefix         = "llmgw:health:cred:"
	defaultHealthTTL         = 10 * time.Minute
	maxAsyncWriteBuffer      = 1000 // 异步写入队列最大长度
	consistencyCheckInterval = time.Minute
)

// HealthState Redis中存储的健康状态
type HealthState struct {
	Status           string    `json:"status"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	LastHealthCheck  time.Time `json:"last_health_check"`
}

// Lua script: 原子更新健康状态
var updateHealthStateScript = redis.NewScript(`
	local key = KEYS[1]
	local status = ARGV[1]
	local fails = tonumber(ARGV[2])
	local timestamp = ARGV[3]
	local ttl = tonumber(ARGV[4])
	
	redis.call('HSET', key, 
		'status', status,
		'consecutive_fails', fails,
		'last_health_check', timestamp
	)
	redis.call('EXPIRE', key, ttl)
	
	return 1
`)

// Lua script: 原子增加失败计数并更新状态
var incrementFailsScript = redis.NewScript(`
	local key = KEYS[1]
	local fail_threshold = tonumber(ARGV[1])
	local timestamp = ARGV[2]
	local ttl = tonumber(ARGV[3])
	
	local current_fails = tonumber(redis.call('HGET', key, 'consecutive_fails') or '0')
	local new_fails = current_fails + 1
	
	local new_status = 'active'
	if new_fails >= fail_threshold then
		new_status = 'unhealthy'
	elseif new_fails > 0 then
		new_status = 'degraded'
	end
	
	redis.call('HSET', key,
		'status', new_status,
		'consecutive_fails', new_fails,
		'last_health_check', timestamp
	)
	redis.call('EXPIRE', key, ttl)
	
	return {new_status, new_fails}
`)

// Lua script: 原子重置为成功（需连续2次）
var markSuccessScript = redis.NewScript(`
	local key = KEYS[1]
	local success_threshold = tonumber(ARGV[1])
	local timestamp = ARGV[2]
	local ttl = tonumber(ARGV[3])
	
	local current_fails = tonumber(redis.call('HGET', key, 'consecutive_fails') or '0')
	local new_fails = math.max(0, current_fails - 1)
	
	local new_status = 'active'
	if new_fails >= 3 then
		new_status = 'unhealthy'
	elseif new_fails > 0 then
		new_status = 'degraded'
	end
	
	redis.call('HSET', key,
		'status', new_status,
		'consecutive_fails', new_fails,
		'last_health_check', timestamp
	)
	redis.call('EXPIRE', key, ttl)
	
	return {new_status, new_fails}
`)

// NewRedisHealthStore 创建Redis健康状态存储
func NewRedisHealthStore(client *redis.Client, logger *slog.Logger) *RedisHealthStore {
	if logger == nil {
		logger = slog.Default()
	}

	enabled := client != nil

	return &RedisHealthStore{
		client:    client,
		memory:    NewInMemoryStore(),
		logger:    logger,
		keyPrefix: defaultKeyPrefix,
		ttl:       defaultHealthTTL,
		enabled:   enabled,
	}
}

// Enabled 返回Redis是否可用
func (s *RedisHealthStore) Enabled() bool {
	return s.enabled && s.client != nil
}

// key 生成Redis key
func (s *RedisHealthStore) key(id string) string {
	return s.keyPrefix + id
}

// Get retrieves a credential from the authoritative in-memory cache.
func (s *RedisHealthStore) Get(id string) (*Credential, bool, error) {
	// Always read from memory on the request path.
	cred, ok, err := s.memory.Get(id)
	if err != nil || !ok {
		return nil, false, err
	}

	s.scheduleConsistencyCheck(id, cred)

	return cred, true, nil
}

func (s *RedisHealthStore) scheduleConsistencyCheck(id string, cred *Credential) {
	if !s.Enabled() {
		return
	}
	now := time.Now().UnixNano()
	next := s.nextConsistencyCheck.Load()
	if now < next || !s.nextConsistencyCheck.CompareAndSwap(next, now+consistencyCheckInterval.Nanoseconds()) {
		return
	}
	go s.verifyConsistency(id, cred)
}

// Save 保存credential（内存+Redis双写）。
//
// Redis 写入是**同步**的（而非 fire-and-forget 的 goroutine）。Save 是
// 低频路径（credential 注册 / reload），不在请求热路径上，一次 Redis
// 往返的开销可以接受。改成同步是为了消除一个真实的覆盖竞态：
//
//	旧实现 `go s.asyncSaveToRedis(cred)` 写入的是调用时刻的快照（通常
//	consecutive_fails=0）。若该 goroutine 滞后执行，晚于随后的
//	MarkFailure/MarkSuccess（它们用 Lua 原子 HSET 把 fails 推进到
//	1/2/3），滞后 goroutine 的无条件 HSET 会把 Redis 状态**倒退**回
//	0，覆盖掉原子操作的结果。重启后 LoadFromRedis 读到的就是被覆盖
//	的错误状态。
//
//	同步写入后，Save 返回即代表内存与 Redis 一致，没有任何滞后 goroutine
//	能再覆盖后续的 MarkFailure/MarkSuccess。Redis 失败时仍降级为纯内存
//	（与原行为一致），不影响可用性。
func (s *RedisHealthStore) Save(cred *Credential) error {
	// 1. 内存写入（同步，必须成功）
	if err := s.memory.Save(cred); err != nil {
		return err
	}

	// 2. Redis 写入（同步；失败降级到纯内存，不影响主流程）
	if s.Enabled() {
		s.saveToRedis(context.Background(), cred)
	}

	return nil
}

// saveToRedis synchronously writes the credential's health state to Redis.
// Failures are logged but not returned: Redis is a secondary store and the
// memory copy is already authoritative after step 1 of Save.
func (s *RedisHealthStore) saveToRedis(ctx context.Context, cred *Credential) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, err := updateHealthStateScript.Run(
		ctx, s.client,
		[]string{s.key(cred.ID)},
		string(cred.Status),
		cred.ConsecutiveFails,
		cred.LastHealthCheck.UTC().Format(time.RFC3339),
		int(s.ttl.Seconds()),
	).Result()

	if err != nil {
		s.logger.Warn("save to redis failed",
			"credential_id", cred.ID, "error", err)
	}
}

// MarkFailure 原子标记失败（内存+Redis）
func (s *RedisHealthStore) MarkFailure(id string, failThreshold int) error {
	ctx := context.Background()

	// 1. Redis原子操作（如果可用）
	if s.Enabled() {
		result, err := incrementFailsScript.Run(
			ctx, s.client,
			[]string{s.key(id)},
			failThreshold,
			time.Now().UTC().Format(time.RFC3339),
			int(s.ttl.Seconds()),
		).Result()

		if err == nil {
			// Redis成功，同步到内存
			resultSlice := result.([]interface{})
			newStatus := Status(resultSlice[0].(string))
			newFails := int(resultSlice[1].(int64))

			cred, ok, _ := s.memory.Get(id)
			if ok {
				cred.Status = newStatus
				cred.ConsecutiveFails = newFails
				cred.LastHealthCheck = time.Now().UTC()
				_ = s.memory.Save(cred)
			}
			return nil
		}

		// Redis失败，降级到内存
		s.logger.Warn("redis incrementFails failed, fallback to memory",
			"error", err, "credential_id", id)
	}

	// 2. 内存操作（fallback）
	cred, ok, err := s.memory.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("credential %s not found", id)
	}

	cred.ConsecutiveFails++
	cred.LastHealthCheck = time.Now().UTC()

	if cred.ConsecutiveFails >= failThreshold {
		cred.Status = StatusUnhealthy
	} else if cred.ConsecutiveFails > 0 {
		cred.Status = StatusDegraded
	}

	return s.memory.Save(cred)
}

// MarkSuccess 原子标记成功（内存+Redis）
func (s *RedisHealthStore) MarkSuccess(id string, successThreshold int) error {
	ctx := context.Background()

	// 1. Redis原子操作（如果可用）
	if s.Enabled() {
		result, err := markSuccessScript.Run(
			ctx, s.client,
			[]string{s.key(id)},
			successThreshold,
			time.Now().UTC().Format(time.RFC3339),
			int(s.ttl.Seconds()),
		).Result()

		if err == nil {
			// Redis成功，同步到内存
			resultSlice := result.([]interface{})
			newStatus := Status(resultSlice[0].(string))
			newFails := int(resultSlice[1].(int64))

			cred, ok, _ := s.memory.Get(id)
			if ok {
				cred.Status = newStatus
				cred.ConsecutiveFails = newFails
				cred.LastHealthCheck = time.Now().UTC()
				_ = s.memory.Save(cred)
			}
			return nil
		}

		// Redis失败，降级到内存
		s.logger.Warn("redis markSuccess failed, fallback to memory",
			"error", err, "credential_id", id)
	}

	// 2. 内存操作（fallback）
	cred, ok, err := s.memory.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("credential %s not found", id)
	}

	cred.ConsecutiveFails = maxInt(0, cred.ConsecutiveFails-1)
	cred.LastHealthCheck = time.Now().UTC()

	if cred.ConsecutiveFails >= 3 {
		cred.Status = StatusUnhealthy
	} else if cred.ConsecutiveFails > 0 {
		cred.Status = StatusDegraded
	} else {
		cred.Status = StatusActive
	}

	return s.memory.Save(cred)
}

// LoadFromRedis 从Redis恢复状态（启动时调用）
func (s *RedisHealthStore) LoadFromRedis(ctx context.Context) (int, error) {
	if !s.Enabled() {
		return 0, fmt.Errorf("redis not enabled")
	}

	// 扫描所有健康状态key
	pattern := s.keyPrefix + "*"
	iter := s.client.Scan(ctx, 0, pattern, 100).Iterator()

	loaded := 0
	for iter.Next(ctx) {
		key := iter.Val()
		id := key[len(s.keyPrefix):]

		// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
		data, err := redissafe.SafeHGetAll(ctx, s.client, key)
		if err != nil {
			// Do not log the full Redis key: it contains the credential ID.
			s.logger.Warn("failed to load health state from redis",
				"operation", "hgetall", "error", err)
			continue
		}

		// 解析状态
		state := HealthState{
			Status:           data["status"],
			ConsecutiveFails: 0,
		}

		if fails, err := strconv.Atoi(data["consecutive_fails"]); err == nil {
			state.ConsecutiveFails = fails
		}

		if ts, err := time.Parse(time.RFC3339, data["last_health_check"]); err == nil {
			state.LastHealthCheck = ts
		}

		// 写入内存（需要完整的Credential对象，这里只更新健康状态字段）
		cred, ok, _ := s.memory.Get(id)
		if ok {
			cred.Status = Status(state.Status)
			cred.ConsecutiveFails = state.ConsecutiveFails
			cred.LastHealthCheck = state.LastHealthCheck
			_ = s.memory.Save(cred)
			loaded++
		}
	}

	if err := iter.Err(); err != nil {
		return loaded, err
	}

	s.logger.Info("loaded health states from redis",
		"count", loaded)

	return loaded, nil
}

// verifyConsistency 异步验证内存与Redis一致性
func (s *RedisHealthStore) verifyConsistency(id string, memCred *Credential) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// P1-14 fix (2026-08-28): Use SafeHGetAll to prevent WRONGTYPE errors
	data, err := redissafe.SafeHGetAll(ctx, s.client, s.key(id))
	if err != nil {
		return // 静默失败，不影响读取
	}

	// 检查一致性
	redisStatus := data["status"]
	redisFails, _ := strconv.Atoi(data["consecutive_fails"])

	if string(memCred.Status) != redisStatus || memCred.ConsecutiveFails != redisFails {
		s.logger.Warn("health state inconsistency detected",
			"credential_id", id,
			"memory_status", memCred.Status,
			"redis_status", redisStatus,
			"memory_fails", memCred.ConsecutiveFails,
			"redis_fails", redisFails)
	}
}

// Delete 删除credential
func (s *RedisHealthStore) Delete(id string) error {
	// 1. 内存删除
	if err := s.memory.Delete(id); err != nil {
		return err
	}

	// 2. Redis删除（异步）
	if s.Enabled() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			_ = s.client.Del(ctx, s.key(id)).Err()
		}()
	}

	return nil
}

// List 列出所有credentials
func (s *RedisHealthStore) List(tenantID string) ([]*Credential, error) {
	return s.memory.List(tenantID)
}

// Count 返回总数
func (s *RedisHealthStore) Count() int {
	return s.memory.Count()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
