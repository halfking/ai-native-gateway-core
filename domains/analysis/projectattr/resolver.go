package projectattr

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// ProjectResolver 按 tenant 缓存 project_dim 快照，构造 Attributor。
//
// 为什么需要 Resolver：
//
//   - Attributor 构造时持有 []Project（值拷贝，不可变），要拿最新数据
//     必须重新构造。每条会话关闭都查一次 PG 既不必要（项目变更罕见），
//     也会让 DB 抖动直接污染推断命中率。
//   - tenant 维度不能跨租户共用缓存：不同租户看到的项目集不同。
//     多租户部署上线时共享同一份快照会导致"租户 A 的项目归属到租户 B
//     的会话上"这种严重错误。
//
// 设计要点：
//
//   - 每条会话关闭构造新 Attributor（attributor 是 immutable 快照），
//     但 Attributor 内部的 projects 切片来自缓存（短 TTL，最多几秒
//     抖动），所以开销可忽略。
//   - singleflight：同一 tenant 的并发 miss 只查一次 DB，避免会话关
//     闭风暴把 PG 打爆。
//   - 负缓存：DB 错误时返回 (nil, err)；上层决定是否告警。
//   - 显式失效：同步器在 upsert 完成后调 Invalidate(tenantID)，下一
//     次会话关闭立即看到新项目。
//
// 默认关闭：resolver 永远不主动加载；只有 SessionCloseHook 装配时
// 才会构造并使用。CloseHook 已存在 "store 或 attributor 为 nil 时静默
// 禁用" 的保护。
type ProjectResolver struct {
	store     ProjectLoader
	ttl       time.Duration
	clock     func() time.Time
	invalFunc func(string) // optional post-invalidation hook (e.g. metrics)

	mu     sync.RWMutex
	cache  map[string]resolverEntry
	flight map[string]*resolverCall

	hits          atomic.Int64
	misses        atomic.Int64
	invalidations atomic.Int64
}

// ProjectLoader 是 ProjectResolver 需要的最小数据访问能力。
// 与 PGStore.LoadProjects 同构，便于测试用 fake 替代。
type ProjectLoader interface {
	LoadProjects(ctx context.Context, tenantID string) ([]Project, error)
}

// ProjectResolverOption 配置 resolver。
type ProjectResolverOption func(*ProjectResolver)

// WithResolverTTL 自定义缓存 TTL。默认 5 秒。允许 0 表示"每次都查
// DB"，主要用于测试。
func WithResolverTTL(d time.Duration) ProjectResolverOption {
	return func(r *ProjectResolver) { r.ttl = d }
}

// WithResolverClock 注入时钟，便于测试控制时间。
func WithResolverClock(now func() time.Time) ProjectResolverOption {
	return func(r *ProjectResolver) { r.clock = now }
}

// WithInvalidationHook 注册缓存失效钩子；同步器调用 Invalidate 时同步触发。
// 用于指标统计、追踪、调试日志等场景。nil 时 no-op。
func WithInvalidationHook(f func(string)) ProjectResolverOption {
	return func(r *ProjectResolver) { r.invalFunc = f }
}

// DefaultResolverTTL 是默认缓存 TTL。短到能让"误禁用→恢复"几乎实时生效，
// 长到足以吸收同一会话关闭风暴下的 DB 抖动。5s 是经验值，参考域名
// 缓存（DNS 5–30 分钟）和配置中心缓存（10s–60s）。
const DefaultResolverTTL = 5 * time.Second

// resolverEntry 是缓存的一行：projects 切片 + 加载时间 + 上次错误。
// err 单独记录（而不是把 nil projects 当成错误），是因为空项目集是合法
// 的——ACC 还没同步过来或该 tenant 没有任何项目。
type resolverEntry struct {
	projects []Project
	loadedAt time.Time
	err      error
}

// resolverCall 是一次并发 miss 合并调用：所有 waiters 通过 read cache
// 等待唯一 winner 完成。sync.Once 仅用于"已标记 done"的占位语义。
type resolverCall struct {
	done chan struct{}
	val  []Project
	err  error
}

// NewProjectResolver 构造 resolver。store 为 nil 时返回 nil，便于上层按需禁用。
func NewProjectResolver(store ProjectLoader, opts ...ProjectResolverOption) *ProjectResolver {
	if store == nil {
		return nil
	}
	r := &ProjectResolver{
		store:  store,
		ttl:    DefaultResolverTTL,
		clock:  time.Now,
		cache:  map[string]resolverEntry{},
		flight: map[string]*resolverCall{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Projects 返回当前 tenant 的项目快照（缓存命中直接返回）。这是给
// SessionCloseHook 用的低层接口；attributor 由 BuildAttributor 包装。
//
// miss 行为：DB 错误时返回 (nil, err)——调用方决定是否记录日志；Attributor
// 在 nil projects 下退化为"无规则匹配"，但仍会尝试 inherit 层。
func (r *ProjectResolver) Projects(ctx context.Context, tenantID string) ([]Project, error) {
	if r == nil {
		return nil, nil
	}

	r.mu.RLock()
	if entry, ok := r.cache[tenantID]; ok {
		fresh := r.ttl == 0 || r.clock().Sub(entry.loadedAt) < r.ttl
		if fresh {
			r.mu.RUnlock()
			r.hits.Add(1)
			return entry.projects, entry.err
		}
	}
	r.mu.RUnlock()

	// 进入 miss / singleflight 路径。
	projects, err := r.loadWithFlight(ctx, tenantID)
	if err != nil {
		r.misses.Add(1)
		return projects, err
	}
	r.misses.Add(1)
	return projects, err
}

func (r *ProjectResolver) loadWithFlight(ctx context.Context, tenantID string) ([]Project, error) {
	r.mu.Lock()
	if call, exists := r.flight[tenantID]; exists {
		r.mu.Unlock()
		// 等待唯一 winner 完成。
		select {
		case <-call.done:
			return call.val, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	call := &resolverCall{done: make(chan struct{})}
	r.flight[tenantID] = call
	r.mu.Unlock()

	// 唯一 winner 执行加载。
	projects, err := r.store.LoadProjects(ctx, tenantID)
	r.mu.Lock()
	call.val, call.err = projects, err
	close(call.done)
	delete(r.flight, tenantID)
	r.cache[tenantID] = resolverEntry{
		projects: projects,
		loadedAt: r.clock(),
		err:      err,
	}
	r.mu.Unlock()
	return projects, err
}

// BuildAttributor 为给定 tenant 构造 Attributor；只注入 WithInherit（24h
// 窗口），不注入 WithLLM（计划要求：本轮默认不启用模型兜底层）。
//
// 防御：r==nil 时返回 nil，让 SessionCloseHook 的 nil-guard 静默禁用。
// Attributor 拿到 nil/空 projects 切片也安全——matchRules 直接没有命中
// 候选，回退到 inherit 层。
func (r *ProjectResolver) BuildAttributor(ctx context.Context, tenantID string) (*Attributor, error) {
	if r == nil {
		return nil, nil
	}
	projects, _ := r.Projects(ctx, tenantID) // err 由 caller 决定是否告警
	if projects == nil {
		projects = []Project{}
	}
	inherit := r.inheritLookup()
	return New(projects, WithInherit(inherit)), nil
}

// inheritLookup 解析 inherit 查询能力。ProjectLoader 不一定实现 Querier；
// 只有当 store 同时是 Querier 时，inherit 查询才挂得上。InheritFromHistory
// 在 Querier==nil 时静默返回空（见 store_test.go 中的 nil-guard 行为）。
func (r *ProjectResolver) inheritLookup() InheritLookup {
	if r == nil || r.store == nil {
		return nil
	}
	q, _ := r.store.(Querier)
	return InheritFromHistory(q, 24*time.Hour)
}

// Invalidate 清除指定 tenant 的缓存。下次 BuildAttributor 会触发一次
// DB 加载。worker 在每次 syncProjectsFromACC 完成后调用一次，让新项目
// 几乎实时可见。
//
// 多次调用是幂等的。
func (r *ProjectResolver) Invalidate(tenantID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.cache, tenantID)
	r.mu.Unlock()
	r.invalidations.Add(1)
	if r.invalFunc != nil {
		r.invalFunc(tenantID)
	}
}

// Stats 返回缓存命中/未命中计数（供运维面板评估 TTL 选择是否合理）。
func (r *ProjectResolver) Stats() ResolverStats {
	if r == nil {
		return ResolverStats{}
	}
	return ResolverStats{
		CacheHits:     r.hits.Load(),
		CacheMisses:   r.misses.Load(),
		Invalidations: r.invalidations.Load(),
	}
}

// ResolverStats 是 ResolverStats 缓存命中率指标。
type ResolverStats struct {
	CacheHits     int64
	CacheMisses   int64
	Invalidations int64
}
