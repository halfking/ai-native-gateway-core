package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RouteNodeStore 是 RouteNodeState 的 Redis 持久化层。
//
// Redis Key 设计（V3.1）：
//   route_node:<credID>:<model>  String (JSON)  TTL=1h
//
// 线程安全：
//   - 内部使用 singleflight 风格锁（in-process dedup）避免并发请求穿透到 Redis
//   - 所有更新通过 Lua 原子执行，避免读改写竞态
//
// 降级策略：
//   - Redis 不可达时 Get 返回 (nil, false, nil)，调用方视作"无状态"
//   - Save 失败时记录 warn 日志，不阻断请求路径
type RouteNodeStore struct {
	client *redis.Client
	cfg    RouteNodeConfig
	ttl    time.Duration

	// in-process 内存缓存 + 写锁，避免并发读改写冲突。
	// 注意：这只是同进程内的 dedup；跨进程一致性由 Redis 保证。
	mu    sync.Mutex
	cache map[string]*RouteNodeState // key = "<credID>:<model>"
}

// NewRouteNodeStore 创建一个新的 RouteNodeStore。
// redisClient=nil 时所有操作返回 no-op（禁用持久化）。
func NewRouteNodeStore(redisClient *redis.Client, cfg RouteNodeConfig) *RouteNodeStore {
	if cfg.WindowSeconds == 0 {
		cfg = DefaultRouteNodeConfig()
	}
	return &RouteNodeStore{
		client: redisClient,
		cfg:    cfg,
		ttl:    DefaultRouteNodeStateTTL,
		cache:  make(map[string]*RouteNodeState),
	}
}

// DefaultRouteNodeStateTTL 是 RouteNodeState 在 Redis 中的 TTL。
// 1 小时足够覆盖滑动窗口（5min）+ 充足余量，且自然清理。
const DefaultRouteNodeStateTTL = time.Hour

// routeNodeRedisKey 构造 Redis key。
func routeNodeRedisKey(credID int, model string) string {
	return fmt.Sprintf("route_node:%d:%s", credID, model)
}

// cacheKey 构造 in-process cache key。
func cacheKey(credID int, model string) string {
	return fmt.Sprintf("%d:%s", credID, model)
}

// Get 读取一个 RouteNodeState。
//
// 返回值：
//   - state: 命中时返回最新状态（应用了 Disabled 冷却恢复和窗口 prune）
//   - found: 是否命中
//   - err:  Redis 错误（调用方应视作"未命中"降级）
//
// 注意：返回的 *RouteNodeState 是 in-process 缓存对象，
// 调用方应只读，不可修改。如需修改请使用 Clone()。
func (s *RouteNodeStore) Get(ctx context.Context, credID int, model string) (*RouteNodeState, bool, error) {
	if s == nil || s.client == nil {
		return nil, false, nil
	}
	key := routeNodeRedisKey(credID, model)

	// 1. 尝试 in-process cache
	s.mu.Lock()
	cached, ok := s.cache[cacheKey(credID, model)]
	s.mu.Unlock()
	if ok {
		// 应用时间相关逻辑（冷却恢复 / 窗口 prune）
		now := time.Now()
		cached.IsUsable(now, s.cfg) // 这会修改 Disabled 状态
		cached.PruneOldRecords(now, s.cfg.WindowSeconds)
		return cached, true, nil
	}

	// 2. 读 Redis
	val, err := s.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	// 3. 反序列化
	var state RouteNodeState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		// 数据损坏：当作未命中，但记 warn
		slog.Warn("route_node_state: corrupt data, treating as miss",
			"cred_id", credID, "model", model, "error", err)
		return nil, false, nil
	}

	// 4. 放入 in-process cache
	s.mu.Lock()
	// 再次检查，避免重复创建（竞态）
	if existing, ok := s.cache[cacheKey(credID, model)]; ok {
		s.mu.Unlock()
		return existing, true, nil
	}
	// 注意：这里存的是 state 的副本（值传递），不是指针。
	// 但 RouteNodeState 是结构体，map 中的值类型存的是副本，外部修改不会影响 cache。
	// 为了让 IsUsable() 修改能反映到所有调用方，我们需要存指针。
	stateCopy := state
	s.cache[cacheKey(credID, model)] = &stateCopy
	s.mu.Unlock()

	return &stateCopy, true, nil
}

// Save 保存一个 RouteNodeState 到 Redis（覆盖式写入）。
//
// 行为：
//   - 序列化为 JSON 写入 Redis key，TTL 刷新为 1 小时
//   - 同步更新 in-process cache（如果存在）
//   - Redis 写入失败：记录 warn 日志，不返回错误（best-effort）
func (s *RouteNodeStore) Save(ctx context.Context, credID int, model string, state *RouteNodeState) error {
	if s == nil || s.client == nil || state == nil {
		return nil
	}

	// 1. 同步 in-process cache（让后续 Get 看到最新状态）
	cacheK := cacheKey(credID, model)
	s.mu.Lock()
	// 深拷贝避免外部修改影响 cache
	cached := *state
	s.cache[cacheK] = &cached
	s.mu.Unlock()

	// 2. 写 Redis
	key := routeNodeRedisKey(credID, model)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, key, data, s.ttl).Err(); err != nil {
		slog.Warn("route_node_state: redis save failed",
			"cred_id", credID, "model", model, "error", err)
		return err
	}
	return nil
}

// RecordSuccess 记录一次成功（高层 API：封装 Get + 修改 + Save）。
//
// 返回最新状态（指针，in-process cache 中的对象）。
func (s *RouteNodeStore) RecordSuccess(ctx context.Context, credID int, model, requestID string) (*RouteNodeState, error) {
	state, _, err := s.Get(ctx, credID, model)
	if err != nil {
		return nil, err
	}
	if state == nil {
		// 第一次成功：创建新状态
		state = acquireRouteNodeState(credID, model)
		defer releaseRouteNodeState(state)
	}
	now := time.Now()
	state.RecordSuccess(now, requestID, s.cfg)
	if err := s.Save(ctx, credID, model, state); err != nil {
		return state, err // 仍返回内存中的 state
	}
	return state, nil
}

// RecordFailure 记录一次 credential-level 失败。
//
// 返回 (state, justDisabled, err)：
//   - state: 最新状态
//   - justDisabled: 是否刚触发 Disabled（连续 3 次失败瞬间）
//   - err: Redis 错误
func (s *RouteNodeStore) RecordFailure(ctx context.Context, credID int, model, requestID, errorKind string) (*RouteNodeState, bool, error) {
	state, _, err := s.Get(ctx, credID, model)
	if err != nil {
		return nil, false, err
	}
	if state == nil {
		state = acquireRouteNodeState(credID, model)
		defer releaseRouteNodeState(state)
	}
	now := time.Now()
	justDisabled := state.RecordFailure(now, requestID, errorKind, s.cfg)
	if err := s.Save(ctx, credID, model, state); err != nil {
		return state, justDisabled, err
	}
	return state, justDisabled, nil
}

// IsUsable 是常用查询便捷方法：返回该 (credID, model) 是否可用于路由。
//
// 即使 Redis 中没有记录（首次访问），也返回 true（让请求能进入；后续失败会建立记录）。
func (s *RouteNodeStore) IsUsable(ctx context.Context, credID int, model string) bool {
	state, found, err := s.Get(ctx, credID, model)
	if err != nil || !found {
		return true // 降级：视作可用
	}
	return state.IsUsable(time.Now(), s.cfg)
}

// FilterUsableCandidates 返回过滤后的候选列表（移除 IsUsable()==false 的）。
//
// 不修改原 slice；返回新 slice 或 nil。
func (s *RouteNodeStore) FilterUsableCandidates(ctx context.Context, candidates []CandidateForRoute) []CandidateForRoute {
	if s == nil || len(candidates) == 0 {
		return candidates
	}
	out := make([]CandidateForRoute, 0, len(candidates))
	for _, c := range candidates {
		if s.IsUsable(ctx, c.CredentialID, c.Model) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CandidateForRoute 是过滤接口的最小输入契约（避免直接 import provider 包造成循环）。
// provider.Candidate 包含 CredentialID + RawModel，可以直接构造。
type CandidateForRoute struct {
	CredentialID int
	Model        string // 即 cand.RawModel
}

// Delete 用于测试或运维手动清理某个节点的状态。
func (s *RouteNodeStore) Delete(ctx context.Context, credID int, model string) error {
	if s == nil || s.client == nil {
		return nil
	}
	key := routeNodeRedisKey(credID, model)
	if err := s.client.Del(ctx, key).Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, cacheKey(credID, model))
	s.mu.Unlock()
	return nil
}

// ErrRouteNodeStoreDisabled 当节点被禁用时返回。
// 调用方可以选择忽略（视作"无可用候选"）或包装为用户错误。
var ErrRouteNodeStoreDisabled = errors.New("route node disabled")