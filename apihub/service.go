package apihub

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Store is the persistence interface for the asset hub. The default
// implementation talks to PostgreSQL; tests inject an in-memory store.
//
// All methods receive tenantID explicitly so that the DB implementation can
// enforce RLS via current_setting('app.tenant_id'). The in-memory store
// enforces the same boundary so multi-tenant isolation is testable without
// a database.
type Store interface {
	Upsert(ctx context.Context, a Asset) error
	// UpsertBatch writes many assets in as few statements as possible.
	//
	// 2026-10-05（runbook §10.30）：AssetWatcher 每 60 秒把源表全量灌进来，
	// 逐行 Upsert 让语句数达到 5,562,432 次/天而其中 93.7% 不改任何行
	// （门控只管「写不写」，不管「发不发」）。批量把它降到每 tick 数条。
	//
	// 契约：① 跨租户由实现方自行分组（RLS 逐租户生效）；
	// ② 单行坏数据不得中断整批 —— 实现方需隔离坏行并继续。
	UpsertBatch(ctx context.Context, assets []Asset) error
	Get(ctx context.Context, tenantID string, k Kind, refID int64) (Asset, error)
	List(ctx context.Context, f Filter) ([]Asset, error)
	Link(ctx context.Context, tenantID string, rel Relationship) error
	Neighbors(ctx context.Context, tenantID string, k Kind, refID int64, depth int) ([]Asset, []Relationship, error)
	// MarkHealth (Phase 7) updates the health_state column of one asset.
	MarkHealth(ctx context.Context, tenantID string, k Kind, refID int64, state HealthState) error
	// ListStale (Phase 7) returns assets whose last_seen_at is older than threshold.
	ListStale(ctx context.Context, tenantID string, threshold time.Duration) ([]Asset, error)
	// ListTenants (Phase 7 audit fix) returns all distinct tenant_id values.
	ListTenants(ctx context.Context) ([]string, error)
}

// Service is the application-layer facade over Store. It adds an in-process
// cache (TTL 60s) and validation, and is the only type relay/admin code
// should depend on.
type Service struct {
	store  Store
	cache  *assetCache
	logger *slog.Logger
}

// New creates a Service backed by the given Store. The cache starts empty
// and is populated lazily; call StartRefresh in a long-running process to
// keep it warm via a background goroutine.
func New(store Store, opts ...Option) *Service {
	s := &Service{
		store:  store,
		cache:  newAssetCache(60 * time.Second),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(s *Service) { s.logger = l }
}

// WithCacheTTL overrides the default 60s cache TTL.
func WithCacheTTL(d time.Duration) Option {
	return func(s *Service) { s.cache.ttl = d }
}

// Register validates and upserts an asset. It enforces Kind validity and
// stamps RegisteredAt. The Store is responsible for RLS scoping.
func (s *Service) Register(ctx context.Context, a Asset) error {
	if s.store == nil {
		return errors.New("apihub: store is not configured")
	}
	if !a.Kind.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, a.Kind)
	}
	if a.TenantID == "" {
		return errors.New("apihub: tenant_id is required")
	}
	if a.HealthState == "" {
		a.HealthState = HealthUnknown
	}
	now := time.Now().UTC()
	a.RegisteredAt = now
	a.LastSeenAt = now
	if err := s.store.Upsert(ctx, a); err != nil {
		return err
	}
	s.cache.invalidate(a.Kind, a.RefID, a.TenantID)
	return nil
}

// RegisterBatch registers many assets at once, invalidating the cache for each.
//
// 2026-10-05（runbook §10.30）：语义与逐个 Register 一致 —— 同样补
// RegisteredAt/LastSeenAt、同样逐条失效缓存。差别只在发往 DB 的**语句条数**。
//
// ★ 与 Register 的**唯一语义差异**：Register 对非法 kind/空 tenant **返回错误**；
//
//	RegisterBatch **跳过并记日志**，因为一批里有一行坏的不该让另外 N-1 行失败
//	—— 这正是改动前 watcher「log+continue」的语义，批量化不能把它丢掉。
//	校验放在这一层（而不是只放在 pgStore）是为了让**所有** Store 实现
//	都拿到同一个契约，也让这条契约不需要真库就能测。
//
// ★ 缓存失效必须逐条：assetCache 按 (tenant, kind, ref_id) 存，
//
//	批量写完只清一次会留下 N-1 条陈旧读。
func (s *Service) RegisterBatch(ctx context.Context, assets []Asset) error {
	if s.store == nil {
		return errors.New("apihub: store is not configured")
	}
	if len(assets) == 0 {
		return nil
	}
	now := time.Now().UTC()
	prepared := make([]Asset, 0, len(assets))
	for _, a := range assets {
		if !a.Kind.IsValid() {
			slog.Warn("apihub: RegisterBatch 跳过非法 kind",
				"kind", string(a.Kind), "ref_id", a.RefID, "tenant", a.TenantID)
			continue
		}
		if a.TenantID == "" {
			slog.Warn("apihub: RegisterBatch 跳过空 tenant",
				"kind", string(a.Kind), "ref_id", a.RefID)
			continue
		}
		if a.HealthState == "" {
			a.HealthState = HealthUnknown
		}
		a.RegisteredAt = now
		a.LastSeenAt = now
		prepared = append(prepared, a)
	}
	if len(prepared) == 0 {
		return nil
	}
	// ★ 缓存失效必须**无条件**逐条执行，不能放在「store 成功之后」。
	//
	// 2026-10-05 审计发现的缺陷：pgStore.UpsertBatch 在分片失败时会
	// **退回逐行**并隔离坏行 —— 于是它返回 error 时，库里其实已经写进去
	// 绝大多数行了。若在这里提前 return，prepared 里所有行的缓存
	// 都不失效 ⇒ 读方命中 TTL 内的陈旧缓存（症状：改了名字但列表不变，
	// 默认 60s 后自愈，因而完全静默）。
	//
	// 为什么改动前不存在：逐行 Register 时代每个 Register 成功后
	// **自己**失效那一条；批量化把「写」与「失效」拆到两个循环，
	// 中间夹了一个 return err ⇒ 失败时两者不再配对。
	//
	// 失效放在 err 检查**之前**：宁可多清几条不该清的缓存
	// （代价是一次 cache miss + 一次回表），也不要留下读得到的陈旧值。
	err := s.store.UpsertBatch(ctx, prepared)
	for _, a := range prepared {
		s.cache.invalidate(a.Kind, a.RefID, a.TenantID)
	}
	return err
}

// Get returns a single asset, honoring tenant isolation.
func (s *Service) Get(ctx context.Context, k Kind, refID int64) (Asset, error) {
	tenantID := tenantFromCtx(ctx)
	if a, ok := s.cache.get(tenantID, k, refID); ok {
		return a, nil
	}
	a, err := s.store.Get(ctx, tenantID, k, refID)
	if err != nil {
		return Asset{}, err
	}
	s.cache.put(a)
	return a, nil
}

// List returns assets matching the filter (always scoped to the caller's tenant).
func (s *Service) List(ctx context.Context, f Filter) ([]Asset, error) {
	f.TenantID = tenantFromCtx(ctx)
	if f.Limit == 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	return s.store.List(ctx, f)
}

// Link creates a directed relationship between two assets. Both endpoints
// must exist and belong to the caller's tenant.
func (s *Service) Link(ctx context.Context, rel Relationship) error {
	if !rel.SrcKind.Kind.IsValid() || !rel.DstKind.Kind.IsValid() {
		return fmt.Errorf("%w: src=%q dst=%q", ErrInvalidKind, rel.SrcKind.Kind, rel.DstKind.Kind)
	}
	return s.store.Link(ctx, tenantFromCtx(ctx), rel)
}

// Neighbors performs a breadth-first traversal of the topology graph up to
// the given depth (1 = direct neighbors).
func (s *Service) Neighbors(ctx context.Context, k Kind, refID int64, depth int) ([]Asset, []Relationship, error) {
	if depth < 1 {
		depth = 1
	}
	return s.store.Neighbors(ctx, tenantFromCtx(ctx), k, refID, depth)
}

// MarkHealth (Phase 7) updates the health_state column and invalidates cache.
func (s *Service) MarkHealth(ctx context.Context, k Kind, refID int64, state HealthState) error {
	if !k.IsValid() {
		return fmt.Errorf("%w: %q", ErrInvalidKind, k)
	}
	if state == "" {
		state = HealthUnknown
	}
	tenantID := tenantFromCtx(ctx)
	if err := s.store.MarkHealth(ctx, tenantID, k, refID, state); err != nil {
		return err
	}
	s.cache.invalidate(k, refID, tenantID)
	return nil
}

// ListStale (Phase 7) returns assets older than threshold.
func (s *Service) ListStale(ctx context.Context, threshold time.Duration) ([]Asset, error) {
	return s.store.ListStale(ctx, tenantFromCtx(ctx), threshold)
}

func (s *Service) ListTenants(ctx context.Context) ([]string, error) {
	return s.store.ListTenants(ctx)
}

// StartRefresh launches a background goroutine that periodically refreshes
// the cache. Call once at startup; the goroutine runs until ctx is cancelled.
func (s *Service) StartRefresh(ctx context.Context) {
	go func() {
		t := time.NewTicker(s.cache.ttl)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.cache.sweep()
			}
		}
	}()
}

// --- tenant context plumbing ---

type tenantCtxKey struct{}

// WithTenant returns a new context carrying the tenant id. Handlers MUST use
// this to derive the tenant from the authenticated session — never trust a
// tenant_id in a request body.
func WithTenant(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenantID)
}

// TenantFromContext (2026-06-30 PR-6) is the public counterpart of
// tenantFromCtx — used by test stubs in admin package to assert which
// tenant the handler queried. Returns "" if not set.
func TenantFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// tenantFromCtx extracts the tenant id; returns "default" if absent.
func tenantFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(tenantCtxKey{}).(string); ok && v != "" {
		return v
	}
	return "default"
}

// --- cache ---

type assetCache struct {
	mu  sync.RWMutex
	m   map[string]cacheEntry
	ttl time.Duration
}

type cacheEntry struct {
	asset    Asset
	expireAt time.Time
}

func newAssetCache(ttl time.Duration) *assetCache {
	return &assetCache{m: make(map[string]cacheEntry), ttl: ttl}
}

func cacheKey(tenant string, k Kind, refID int64) string {
	return fmt.Sprintf("%s|%s|%d", tenant, k, refID)
}

func (c *assetCache) get(tenant string, k Kind, refID int64) (Asset, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.m[cacheKey(tenant, k, refID)]
	if !ok || time.Now().After(e.expireAt) {
		return Asset{}, false
	}
	return e.asset, true
}

func (c *assetCache) put(a Asset) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[cacheKey(a.TenantID, a.Kind, a.RefID)] = cacheEntry{asset: a, expireAt: time.Now().Add(c.ttl)}
}

func (c *assetCache) invalidate(k Kind, refID int64, tenant string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, cacheKey(tenant, k, refID))
}

func (c *assetCache) sweep() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for key, e := range c.m {
		if now.After(e.expireAt) {
			delete(c.m, key)
		}
	}
}
