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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"
)

// DefaultTTL is the per-tenant cache lifetime.  60s is a balance
// between staleness (admin creates a policy, users see it within
// 60s absent explicit invalidation) and DB pressure.
const DefaultTTL = 60 * time.Second

// Policy mode constants (2026-11-27 三级配额制轮).
//   - ModeDeny: blacklist semantics — model present in the set ⇒ forbidden
//     (the pre-1004 behaviour; the column default keeps every legacy row
//     and every legacy behaviour byte-for-byte identical).
//   - ModeAllow: whitelist semantics — model ABSENT from the set ⇒
//     forbidden.  An empty whitelist means "no restriction configured"
//     (fail-open) so that creating a policy row with mode=allow but no
//     entries does not lock the tenant out of every model.
const (
	ModeDeny  = "deny"
	ModeAllow = "allow"
)

// subjectPolicy is the cached policy for one subject (tenant / org / user):
// the mode plus the lowercase canonical-name set.  A nil or empty set with
// mode=deny means "no denials configured" (allow everything); with
// mode=allow it means "no whitelist configured" (allow everything —
// empty whitelist is not "deny everything", it is "not engaged").
type subjectPolicy struct {
	mode   string
	models map[string]bool
}

// verdict evaluates one canonical name against this policy.
func (p *subjectPolicy) verdict(canonicalName string) bool {
	if p == nil || len(p.models) == 0 {
		// No policy rows for this subject ⇒ not engaged ⇒ allow.
		return false
	}
	if p.mode == ModeAllow {
		// Whitelist: absent from the list ⇒ forbidden.
		return !p.models[canonicalName]
	}
	// Blacklist (default/legacy semantics): present ⇒ forbidden.
	return p.models[canonicalName]
}

// Checker is a process-wide singleton.  Construct once in main, pass
// to relay.ChatHandler and admin.Handler.
type Checker struct {
	dbPool *pgxpool.Pool

	// cache is subject_key -> policy.  subject_key is "t:<tenant>",
	// "o:<orgID>" or "u:<userID>" (scope prefix avoids key collisions
	// between the three namespaces; org/user IDs are ints but stored
	// as their decimal text form).
	// Mutated under mu; reads use RLock.
	mu      sync.RWMutex
	cache   map[string]*subjectPolicy
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

	// ownerCache maps ownerUser (users.username) → (userID, orgID).
	// Populated lazily on the hot path; TTL matches the policy TTL.
	// A missing users row caches (0,0) so repeated lookups for a
	// platform/external owner do not hammer the DB.  Nil (no DB)
	// keeps resolution disabled and the org/user checks fail-open.
	ownerMu    sync.RWMutex
	ownerCache map[string]ownerRef
	ownerExp   map[string]time.Time
}

// ownerRef is the resolved identity for one ownerUser string.
// userID==0 ⇒ user unknown (external/platform owner); orgID==0 ⇒
// user exists but belongs to no organization.
type ownerRef struct {
	userID int64
	orgID  int64
}

// ResolveOwner maps an ownerUser (users.username) to (userID, orgID)
// with caching.  Returns (0,0,ok=false) when the Checker has no DB or
// the user does not exist.  TTL = policy TTL.
func (c *Checker) ResolveOwner(ctx context.Context, ownerUser string) (int64, int64, bool) {
	if c == nil || c.dbPool == nil || ownerUser == "" {
		return 0, 0, false
	}
	c.ownerMu.RLock()
	ref, has := c.ownerCache[ownerUser]
	exp, hasExp := c.ownerExp[ownerUser]
	c.ownerMu.RUnlock()
	fresh := has && hasExp && time.Now().Before(exp)
	if !fresh {
		// Reload one row.  Fail-open on error: (0,0) means both
		// scope checks skip this request.
		rowErr := func() error {
			qctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var userID, orgID int64
			err := c.dbPool.QueryRow(qctx, `
				SELECT id, COALESCE(org_id, 0)
				FROM users WHERE username = $1 AND enabled
				LIMIT 1
			`, ownerUser).Scan(&userID, &orgID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					// Cache the negative so repeat external owners
					// don't re-query until TTL.
					c.setOwnerRef(ownerUser, ownerRef{})
					return nil
				}
				return err
			}
			c.setOwnerRef(ownerUser, ownerRef{userID: userID, orgID: orgID})
			return nil
		}()
		if rowErr != nil {
			slog.Debug("model_policy owner resolution failed (fail-open)",
				"owner_user", ownerUser, "error", rowErr)
			return 0, 0, false
		}
		c.ownerMu.RLock()
		ref, has = c.ownerCache[ownerUser]
		c.ownerMu.RUnlock()
		if !has {
			return 0, 0, false
		}
	}
	return ref.userID, ref.orgID, true
}

func (c *Checker) setOwnerRef(ownerUser string, ref ownerRef) {
	c.ownerMu.Lock()
	if c.ownerCache == nil {
		c.ownerCache = make(map[string]ownerRef)
		c.ownerExp = make(map[string]time.Time)
	}
	c.ownerCache[ownerUser] = ref
	c.ownerExp[ownerUser] = time.Now().Add(c.ttl)
	c.ownerMu.Unlock()
}

// New constructs a Checker.  dbPool may be nil (used in unit
// tests); in that case IsForbidden always returns false (fail-open).
func New(dbPool *pgxpool.Pool) *Checker {
	return &Checker{
		dbPool:  dbPool,
		cache:   make(map[string]*subjectPolicy),
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

// IsForbidden returns true if (tenantID, canonicalName) violates the
// tenant's active policy — mode=deny (legacy blacklist) hits a listed
// model; mode=allow (whitelist) misses an unlisted model.  Returns
// false (allow) on any error or if the tenant has no policy —
// fail-open by design.
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
	return c.isForbiddenForSubject(ctx, "t:"+tenantID, "tenant", tenantID, canonicalName)
}

// IsForbiddenForOrg returns true if the model violates the org's
// active policy (same deny/allow semantics as the tenant scope,
// 2026-11-27 三级配额制轮).  orgID <= 0 (user/org not resolved or
// not configured) ⇒ allow.  Fail-open identical to the tenant path.
func (c *Checker) IsForbiddenForOrg(ctx context.Context, orgID int64, canonicalName string) bool {
	if c == nil || orgID <= 0 || canonicalName == "" {
		return false
	}
	return c.isForbiddenForSubject(ctx, "o:"+strconv.FormatInt(orgID, 10), "org", orgID, canonicalName)
}

// IsForbiddenForUser returns true if the model violates the user's
// active policy (same deny/allow semantics, 2026-11-27).
func (c *Checker) IsForbiddenForUser(ctx context.Context, userID int64, canonicalName string) bool {
	if c == nil || userID <= 0 || canonicalName == "" {
		return false
	}
	return c.isForbiddenForSubject(ctx, "u:"+strconv.FormatInt(userID, 10), "user", userID, canonicalName)
}

// isForbiddenForSubject is the shared hot path for the three scopes.
// scopeKind is for logging only; subjectKey is the cache key.
func (c *Checker) isForbiddenForSubject(ctx context.Context, subjectKey, scopeKind string, subjectID interface{}, canonicalName string) bool {
	if canonicalName == "" {
		return false
	}
	canonicalName = strings.ToLower(canonicalName)

	// Fast path: cache hit and not expired.
	c.mu.RLock()
	exp, hasExp := c.expires[subjectKey]
	pol, hasPol := c.cache[subjectKey]
	hit := hasExp && time.Now().Before(exp) && hasPol
	c.mu.RUnlock()

	if !hit {
		// Cache miss or expired: lazy reload via singleflight.
		// Skip if no DB pool is configured (fail-open, but
		// preserve any pre-existing cache entry).
		if c.dbPool == nil {
			return false
		}
		// Errors are swallowed (logged) because we fail-open.
		_, err, _ := c.sfGroup.Do("reload:"+subjectKey, func() (interface{}, error) {
			reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return nil, c.reloadSubject(reloadCtx, subjectKey, scopeKind)
		})
		if err != nil {
			slog.Warn("model_policy reload failed (fail-open)",
				"scope", scopeKind, "subject", subjectID, "error", err)
			return false
		}
		c.mu.RLock()
		pol, hasPol = c.cache[subjectKey]
		c.mu.RUnlock()
		if !hasPol {
			return false
		}
	}

	return pol.verdict(canonicalName)
}

// Invalidate clears the cache entry for one subject key.  Tenant
// scope callers pass the bare tenant id (legacy shape, prefixed
// here); org/user callers use InvalidateOrg/InvalidateUser or the
// generic InvalidateSubject.  Safe to call for unknown subjects
// (no-op).
func (c *Checker) Invalidate(tenantID string) {
	if c == nil || tenantID == "" {
		return
	}
	c.InvalidateSubject("t:" + tenantID)
}

// InvalidateOrg clears the cached policy for one org.  Called from
// the admin write path.
func (c *Checker) InvalidateOrg(orgID int64) {
	if c == nil || orgID <= 0 {
		return
	}
	c.InvalidateSubject("o:" + strconv.FormatInt(orgID, 10))
}

// InvalidateUser clears the cached policy for one user.
func (c *Checker) InvalidateUser(userID int64) {
	if c == nil || userID <= 0 {
		return
	}
	c.InvalidateSubject("u:" + strconv.FormatInt(userID, 10))
}

// InvalidateSubject clears one raw cache key (scope-prefixed).
func (c *Checker) InvalidateSubject(subjectKey string) {
	if c == nil || subjectKey == "" {
		return
	}
	c.mu.Lock()
	delete(c.cache, subjectKey)
	delete(c.expires, subjectKey)
	c.mu.Unlock()
}

// InvalidateAll clears the entire cache.  Used by tests and as an
// emergency knob.
func (c *Checker) InvalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.cache = make(map[string]*subjectPolicy)
	c.expires = make(map[string]time.Time)
	c.mu.Unlock()
}

// ReloadAll reloads every tenant's denylist.  Used at startup
// (pre-warm) and by the background ticker as a safety net.
//
// RLS Phase 2 适配 (R41 P1-2): 跨租户拉取必须显式事务 + super_admin 旁路
// GUC（迁移 725 新增的 tenant_model_policies_super_admin_bypass policy），
// 因为 NOSUPERUSER owner 在 FORCE+ENABLE 形态下不能用 SET LOCAL row_security
// 旁路（PG 在 SELECT 阶段抛 ERROR，R41 staging 矩阵 B/F 实证）。事务在
// superuser 角色（llm_gateway SUPERUSER+BYPASSRLS）期间仍命中 BYPASSRLS
// 旁路、与旧直连语义等价；NOSUPERUSER owner 角色下 725 bypass policy 触
// 发、跨租户可见。
func (c *Checker) ReloadAll(ctx context.Context) error {
	if c == nil || !c.Enabled() {
		return nil
	}
	tx, err := c.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // no-op after Commit
	}()
	// 725 super_admin_bypass policy 旁路分支
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_role', 'super_admin', true)`); err != nil {
		return fmt.Errorf("set super_admin role: %w", err)
	}

	// 2026-11-27 三级配额制轮：三条策略面一次拉全（tenant/org/user）。
	//   tenant 沿用 active 视图（RLS 旁路 GUC 形态不变）；org/user 表无
	//   RLS（admin-only 写入面，读路径是网关系统读者），直接读 active 视图。
	fresh := make(map[string]*subjectPolicy)

	rows, err := tx.Query(ctx, `
		SELECT tenant_id, canonical_name, mode
		FROM tenant_model_policies_active
	`)
	if err != nil {
		return fmt.Errorf("query active policies: %w", err)
	}
	fresh = collectPolicies(rows, fresh, "t:", func(scan func(dest ...any) error) (string, string, string, error) {
		var tenant, model, mode string
		if err := scan(&tenant, &model, &mode); err != nil {
			return "", "", "", err
		}
		return tenant, model, mode, nil
	})
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows.Err: %w", err)
	}
	rows.Close()

	orgRows, err := tx.Query(ctx, `
		SELECT org_id, canonical_name, mode
		FROM org_model_policies_active
	`)
	if err != nil {
		return fmt.Errorf("query org policies: %w", err)
	}
	fresh = collectPolicies(orgRows, fresh, "o:", func(scan func(dest ...any) error) (string, string, string, error) {
		var orgID int64
		var model, mode string
		if err := scan(&orgID, &model, &mode); err != nil {
			return "", "", "", err
		}
		return strconv.FormatInt(orgID, 10), model, mode, nil
	})
	if err := orgRows.Err(); err != nil {
		return fmt.Errorf("org rows.Err: %w", err)
	}
	orgRows.Close()

	userRows, err := tx.Query(ctx, `
		SELECT user_id, canonical_name, mode
		FROM user_model_policies_active
	`)
	if err != nil {
		return fmt.Errorf("query user policies: %w", err)
	}
	fresh = collectPolicies(userRows, fresh, "u:", func(scan func(dest ...any) error) (string, string, string, error) {
		var userID int64
		var model, mode string
		if err := scan(&userID, &model, &mode); err != nil {
			return "", "", "", err
		}
		return strconv.FormatInt(userID, 10), model, mode, nil
	})
	if err := userRows.Err(); err != nil {
		return fmt.Errorf("user rows.Err: %w", err)
	}
	userRows.Close()

	c.mu.Lock()
	now := time.Now()
	for subject, pol := range fresh {
		c.cache[subject] = pol
		c.expires[subject] = now.Add(c.ttl)
	}
	// Drop subjects that no longer have any policy rows (empty map
	// would still cost a cache lookup; reclaim the memory).
	for subject := range c.cache {
		if _, ok := fresh[subject]; !ok {
			delete(c.cache, subject)
			delete(c.expires, subject)
		}
	}
	c.mu.Unlock()

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	slog.Info("model_policy cache reloaded",
		"subjects_with_policies", len(fresh),
		"total_rows", countTotal(c.cache))
	return nil
}

// collectPolicies folds one policy row stream into the fresh map.
// keyFn maps a scanned row to (subject-suffix, canonical_name, mode).
// mode falls back to "deny" for NULL/empty (pre-1004 rows all default
// to deny at the DB level; the fallback is defence in depth).
func collectPolicies(rows pgx.Rows, fresh map[string]*subjectPolicy, prefix string, keyFn func(scan func(dest ...any) error) (string, string, string, error)) map[string]*subjectPolicy {
	for rows.Next() {
		suffix, model, mode, err := keyFn(rows.Scan)
		if err != nil {
			// Let the caller's rows.Err() surface the error; stop folding.
			return fresh
		}
		key := prefix + suffix
		if fresh[key] == nil {
			if mode == "" {
				mode = ModeDeny
			}
			fresh[key] = &subjectPolicy{mode: mode, models: make(map[string]bool)}
		}
		// A subject's mode comes from every row (single mode per subject
		// enforced by the UI/admin surface); last row wins, all rows
		// share the same value in practice.
		if mode != "" {
			fresh[key].mode = mode
		}
		fresh[key].models[strings.ToLower(model)] = true
	}
	return fresh
}

// reloadSubject fetches one subject's policy (scope-dispatched on the
// subjectKey prefix) and updates the cache.  Sets `app.current_tenant`
// in the local tx for the tenant scope so tenant_model_policies's
// `tenant_isolation_tmp` policy lets the row through (canonical
// vocabulary form); org/user tables have no RLS so no GUC is needed.
//
// RLS Phase 2 适配 (R41 P1-2): 原 `SET LOCAL row_security=off` 在
// NOSUPERUSER owner + FORCE+ENABLE 形态下被 PG 在 SELECT 阶段抛 ERROR
// （staging 矩阵 B/F 实证）。改用 tenant GUC 走标准 tenant_id 隔离分支——
// 比 bypass 路径更窄、更安全（不能跨租户），且与同包其它读路径（denylist
// cache refresh）一致。
func (c *Checker) reloadSubject(ctx context.Context, subjectKey, scopeKind string) error {
	// Dispatch on the scope prefix to (table, subject column, subject value).
	var table, idCol, idVal string
	switch {
	case strings.HasPrefix(subjectKey, "t:"):
		table, idCol, idVal = "tenant_model_policies_active", "tenant_id", strings.TrimPrefix(subjectKey, "t:")
	case strings.HasPrefix(subjectKey, "o:"):
		table, idCol, idVal = "org_model_policies_active", "org_id", strings.TrimPrefix(subjectKey, "o:")
	case strings.HasPrefix(subjectKey, "u:"):
		table, idCol, idVal = "user_model_policies_active", "user_id", strings.TrimPrefix(subjectKey, "u:")
	default:
		return fmt.Errorf("unknown subject key: %s", subjectKey)
	}

	tx, err := c.dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx) // no-op after Commit
	}()

	// Tenant scope rows are RLS-protected (Pattern A): set the tenant
	// GUC so the isolation policy lets them through.  org/user tables
	// carry no RLS; setting the GUC is harmless but pointless, skip it.
	if scopeKind == "tenant" {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, idVal); err != nil {
			return fmt.Errorf("set tenant guc: %w", err)
		}
	}

	query := fmt.Sprintf(`
		SELECT canonical_name, mode
		FROM %s
		WHERE %s = $1
	`, table, idCol)
	rows, err := tx.Query(ctx, query, idVal)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	pol := &subjectPolicy{mode: ModeDeny, models: make(map[string]bool)}
	for rows.Next() {
		var model, mode string
		if err := rows.Scan(&model, &mode); err != nil {
			rows.Close()
			return fmt.Errorf("scan: %w", err)
		}
		if mode != "" {
			pol.mode = mode
		}
		pol.models[strings.ToLower(model)] = true
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
	if len(pol.models) == 0 {
		// No policy rows — drop the entry so we never look it up
		// again until the next reload.
		delete(c.cache, subjectKey)
		delete(c.expires, subjectKey)
	} else {
		c.cache[subjectKey] = pol
		c.expires[subjectKey] = time.Now().Add(c.ttl)
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
// observability (count of cached subjects, total policy rows, oldest
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

func countTotal(m map[string]*subjectPolicy) int {
	total := 0
	for _, inner := range m {
		total += len(inner.models)
	}
	return total
}
