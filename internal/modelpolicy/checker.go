// Package modelpolicy is the tenant-scoped model denylist used by
// the relay hot path.  Default behaviour (table empty) is "all
// models allowed for all tenants".  super_admin can configure a
// per-tenant denylist via /api/admin/tenants/{code}/model-policies.
//
// Design summary (see docs/llm-gateway-go/2026-06-21-tenant-model-policy.md §2):
//
//   - Read path is cache-only (no DB on hot path).  Cache TTL is 60s,
//     reload is lazy on miss + background ticker (60s) for safety.
//
//   - Per-tenant invalidation is invoked from the admin API write
//     path so the next request sees the change immediately.
//
//   - singleflight prevents thundering-herd reloads when many
//     requests find the same tenant expired at once.
//
//   - RLS bypass: the reload SQL is wrapped in SET LOCAL row_security
//     = off because the Checker runs as a system-level reader with no
//     app.current_tenant GUC set.  Without bypass, the SELECT returns
//     zero rows (RLS USING clause fails) and the cache stays empty,
//     which is the dangerous "always allow" state.
//
//   - Fail-open on DB errors: a governance DB outage must not
//     become an availability outage.  We log and return false.
//
// The package is intentionally tiny: a single file with no
// transitive dependencies beyond pgxpool + singleflight.
package modelpolicy

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

// DefaultTTL is the per-tenant cache lifetime.  60s is a balance
// between staleness (admin creates a policy, users see it within
// 60s absent explicit invalidation) and DB pressure.
const DefaultTTL = 60 * time.Second

// Checker is a process-wide singleton.  Construct once in main, pass
// to relay.ChatHandler and admin.Handler.
type Checker struct {
	dbPool *pgxpool.Pool

	// cache is tenant_id -> set of denied canonical_name.
	// Mutated under mu; reads use RLock.
	mu      sync.RWMutex
	cache   map[string]map[string]bool
	expires map[string]time.Time

	// sfGroup collapses concurrent reloads of the same tenant.
	sfGroup singleflight.Group

	// ttl is the per-tenant cache lifetime.  Defaults to DefaultTTL
	// but tunable for tests.
	ttl time.Duration

	// stopCh signals the background reload loop to exit.
	stopCh chan struct{}

	// started marks whether Run() has been called; protects against
	// double-start.
	startedMu sync.Mutex
	started   bool
}

// New constructs a Checker.  dbPool may be nil (used in unit
// tests); in that case IsForbidden always returns false (fail-open).
func New(dbPool *pgxpool.Pool) *Checker {
	return &Checker{
		dbPool:  dbPool,
		cache:   make(map[string]map[string]bool),
		expires: make(map[string]time.Time),
		ttl:     DefaultTTL,
		stopCh:  make(chan struct{}),
	}
}

// SetTTL overrides the default cache lifetime.  Test-only.
func (c *Checker) SetTTL(ttl time.Duration) {
	if ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
}

// Enabled reports whether the Checker can load fresh policies from
// the DB.  When false, IsForbidden still works for tenants already
// in the cache (from a previous load) but cannot refresh expired
// entries — it returns the cached value until TTL expires.
func (c *Checker) Enabled() bool {
	return c != nil && c.dbPool != nil
}

// IsForbidden returns true if (tenantID, canonicalName) is in the
// active denylist.  Returns false (allow) on any error or if the
// tenant has no entry — fail-open by design.
//
// canonicalName must be the canonical model name from
// models_canonical.canonical_name (lowercase, no vendor prefix,
// post-NormalizeRouteKeyAliases resolution).  The Checker matches
// by exact equality on the lowercased value.
//
// We do NOT call NormalizeRouteKeyAliases here because the resolver
// (resolve.Resolver) already produces the canonical form.  Doing
// it twice would also mean the cache key is ambiguous ("did the
// client ask for glm-5-1 or glm-5.1?") which makes debugging hard.
//
// Fail-open contract: any error or nil state returns false
// (allow).  This prevents the governance DB from becoming an
// availability dependency.  The cache may still return forbidden
// if the entry is fresh and the tenant was previously denied.
func (c *Checker) IsForbidden(ctx context.Context, tenantID, canonicalName string) bool {
	if c == nil {
		return false
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if canonicalName == "" {
		return false
	}
	canonicalName = strings.ToLower(canonicalName)

	// Fast path: cache hit and not expired.
	c.mu.RLock()
	exp, hasExp := c.expires[tenantID]
	denied, hasDenied := c.cache[tenantID]
	hit := hasExp && time.Now().Before(exp) && hasDenied
	c.mu.RUnlock()

	if !hit {
		// Cache miss or expired: lazy reload via singleflight.
		// Skip if no DB pool is configured (fail-open, but
		// preserve any pre-existing cache entry).
		if c.dbPool == nil {
			return false
		}
		// Errors are swallowed (logged) because we fail-open.
		_, err, _ := c.sfGroup.Do("reload:"+tenantID, func() (interface{}, error) {
			reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return nil, c.reloadTenant(reloadCtx, tenantID)
		})
		if err != nil {
			slog.Warn("model_policy reload failed (fail-open)",
				"tenant", tenantID, "error", err)
			return false
		}
		c.mu.RLock()
		denied, hasDenied = c.cache[tenantID]
		c.mu.RUnlock()
		if !hasDenied {
			return false
		}
	}

	return denied[canonicalName]
}

// Invalidate clears the cache entry for one tenant.  Called from
// the admin API write path so the next request sees the change
// without waiting for TTL.  Safe to call for unknown tenants
// (no-op).
func (c *Checker) Invalidate(tenantID string) {
	if c == nil || tenantID == "" {
		return
	}
	c.mu.Lock()
	delete(c.cache, tenantID)
	delete(c.expires, tenantID)
	c.mu.Unlock()
}

// InvalidateAll clears the entire cache.  Used by tests and as an
// emergency knob.
func (c *Checker) InvalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.cache = make(map[string]map[string]bool)
	c.expires = make(map[string]time.Time)
	c.mu.Unlock()
}

// ReloadAll reloads every tenant's denylist.  Used at startup
// (pre-warm) and by the background ticker as a safety net.
func (c *Checker) ReloadAll(ctx context.Context) error {
	if c == nil || !c.Enabled() {
		return nil
	}
	rows, err := c.dbPool.Query(ctx, `
		SELECT tenant_id, canonical_name
		FROM tenant_model_policies_active
	`)
	if err != nil {
		return fmt.Errorf("query active policies: %w", err)
	}
	defer rows.Close()

	fresh := make(map[string]map[string]bool)
	for rows.Next() {
		var tenant, model string
		if err := rows.Scan(&tenant, &model); err != nil {
			return fmt.Errorf("scan: %w", err)
		}
		if fresh[tenant] == nil {
			fresh[tenant] = make(map[string]bool)
		}
		fresh[tenant][strings.ToLower(model)] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows.Err: %w", err)
	}

	c.mu.Lock()
	now := time.Now()
	for tenant, denials := range fresh {
		c.cache[tenant] = denials
		c.expires[tenant] = now.Add(c.ttl)
	}
	// Drop tenants that no longer have any denials (empty map
	// would still cost a cache lookup; reclaim the memory).
	for tenant := range c.cache {
		if _, ok := fresh[tenant]; !ok {
			delete(c.cache, tenant)
			delete(c.expires, tenant)
		}
	}
	c.mu.Unlock()

	slog.Info("model_policy cache reloaded",
		"tenants_with_policies", len(fresh),
		"total_denials", countTotal(fresh))
	return nil
}

// reloadTenant fetches the denylist for a single tenant and updates
// the cache.  Bypasses RLS via SET LOCAL row_security = off because
// the Checker runs without an app.current_tenant GUC.
//
// Why not use a separate role: production uses one DB user per
// service; creating model_policy_reader role requires touching
// secrets, k8s config, and deploy scripts.  SET LOCAL is simpler
// and stays in the same transaction so it auto-rollbacks.
func (c *Checker) reloadTenant(ctx context.Context, tenantID string) error {
	tx, err := c.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // no-op after Commit
	}()

	if _, err := tx.Exec(ctx, "SET LOCAL row_security = off"); err != nil {
		return fmt.Errorf("disable RLS: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT canonical_name
		FROM tenant_model_policies_active
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	denials := make(map[string]bool)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		denials[strings.ToLower(model)] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("rows.Err: %w", err)
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	c.mu.Lock()
	if len(denials) == 0 {
		// No denials — drop the entry so we never look it up
		// again until the next reload.
		delete(c.cache, tenantID)
		delete(c.expires, tenantID)
	} else {
		c.cache[tenantID] = denials
		c.expires[tenantID] = time.Now().Add(c.ttl)
	}
	c.mu.Unlock()

	return nil
}

// Run starts a background loop that reloads every TTL/2 so a
// forgotten tenant (one whose Invalidate nobody calls) still gets
// refreshed.  Call once from main; cancel via the returned func or
// by calling Stop().
func (c *Checker) Run(ctx context.Context) {
	c.startedMu.Lock()
	if c.started {
		c.startedMu.Unlock()
		return
	}
	c.started = true
	c.startedMu.Unlock()

	interval := c.ttl / 2
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.stopCh:
				return
			case <-ticker.C:
				reloadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				if err := c.ReloadAll(reloadCtx); err != nil {
					slog.Warn("model_policy background reload failed",
						"error", err)
				}
				cancel()
			}
		}
	}()
}

// Stop signals the background loop to exit.  Idempotent.
func (c *Checker) Stop() {
	if c == nil {
		return
	}
	select {
	case <-c.stopCh:
		// already closed
	default:
		close(c.stopCh)
	}
}

// Stats returns cache state for /api/admin/model-policy/stats
// observability (count of cached tenants, total denials, oldest
// expiration).  Used by future tests; harmless to expose.
type Stats struct {
	Tenants      int   `json:"tenants"`
	TotalDenials int   `json:"total_denials"`
	OldestExpiry int64 `json:"oldest_expiry_unix"`
}

func (c *Checker) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var oldest int64
	for _, t := range c.expires {
		ts := t.Unix()
		if oldest == 0 || ts < oldest {
			oldest = ts
		}
	}
	return Stats{
		Tenants:      len(c.cache),
		TotalDenials: countTotal(c.cache),
		OldestExpiry: oldest,
	}
}

func countTotal(m map[string]map[string]bool) int {
	total := 0
	for _, inner := range m {
		total += len(inner)
	}
	return total
}