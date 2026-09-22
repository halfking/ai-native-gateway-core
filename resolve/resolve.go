package resolve

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"golang.org/x/sync/singleflight"
)

type Resolution struct {
	ClientModel    string   `json:"client_model"`
	CanonicalID    *int     `json:"canonical_id"`
	CanonicalName  *string  `json:"canonical_name"`
	RawModels      []string `json:"raw_models"`
	ResolutionPath string   `json:"resolution_path"`
}

type cacheEntry struct {
	resolved   *Resolution
	expiration time.Time
}

type Resolver struct {
	mu      sync.RWMutex
	cache   map[string]cacheEntry
	ttl     time.Duration
	dbPool  *pgxpool.Pool
	sfGroup singleflight.Group
	stopCh  chan struct{}

	// B3 (2026-09-22) request-side auto-seed: when resolveDB fully misses
	// but a provider already reported a same-named raw model, the canonical
	// row + aliases are seeded via discovery.EnsureCanonicalAndAliases so the
	// next lookup resolves instead of pass-throughing forever. ensureAttempts
	// debounces the seed per cache key (ensureDebounceTTL) so a client
	// hammering an unknown model cannot turn into a discovery upsert storm.
	ensureAttempts map[string]time.Time
	// ensureFn/relookupFn/rawLookupFn are test seams; production leaves them
	// nil and the flow falls back to discovery.EnsureCanonicalAndAliases,
	// r.resolveDB, and the provider_models SQL in lookupSameRawDB.
	ensureFn    func(ctx context.Context, db modelcatalog.Querier, rawName, source string) (int, string, error)
	resolveDBFn func(ctx context.Context, clientModel, clientProfile string) (*Resolution, error)
	rawLookupFn func(ctx context.Context, clientModel string) (rawName string, found bool, err error)
}

// ensureDebounceTTL is how long a cache key waits after one auto-seed
// attempt (success or failure) before another may run. Longer than
// negativeTTL so the negative cache expires first and the relookup (not a
// second seed) is what picks up the freshly seeded rows.
const ensureDebounceTTL = 5 * time.Minute

// negativeTTL is how long a "model not found → passthrough" miss is cached.
// Kept short (≤30s, ≤ttl/4) so a model registered right after a miss is
// picked up quickly; see Resolve for why the miss is cached at all.
func (r *Resolver) negativeTTL() time.Duration {
	neg := r.ttl / 4
	if neg > 30*time.Second {
		neg = 30 * time.Second
	}
	if neg < 5*time.Second {
		neg = 5 * time.Second
	}
	return neg
}

func NewResolver(_ string, cacheTTL time.Duration) *Resolver {
	if cacheTTL == 0 {
		cacheTTL = 120 * time.Second
	}
	r := &Resolver{
		cache:          make(map[string]cacheEntry),
		ttl:            cacheTTL,
		stopCh:         make(chan struct{}),
		ensureAttempts: make(map[string]time.Time),
	}
	go r.evictLoop()
	return r
}

func (r *Resolver) SetDB(pool *pgxpool.Pool) {
	r.dbPool = pool
}

func cacheKey(model, profile string) string {
	model = modelname.NormalizeRouteKey(model)
	if profile == "" {
		return strings.ToLower(model)
	}
	return strings.ToLower(model) + "|" + strings.ToLower(profile)
}

func (r *Resolver) Resolve(ctx context.Context, clientModel, clientProfile string) *Resolution {
	key := cacheKey(clientModel, clientProfile)

	r.mu.RLock()
	if entry, ok := r.cache[key]; ok && time.Now().Before(entry.expiration) {
		r.mu.RUnlock()
		return entry.resolved
	}
	r.mu.RUnlock()

	v, err, _ := r.sfGroup.Do(key, func() (any, error) {
		resolved, fetchErr := r.dbLookup(ctx, clientModel, clientProfile)
		if fetchErr != nil {
			slog.Debug("resolve: DB failed, using passthrough",
				"model", clientModel,
				"error", fetchErr,
			)
			// DB errors are NOT negatively cached: the miss is infrastructure,
			// not a real "model not found" answer.
			return passthrough(clientModel), nil
		}
		if resolved == nil {
			// B3 (2026-09-22): before falling back to the negative cache,
			// try to seed the canonical row from a same-named provider raw
			// model. A hit returns a real Resolution (positive-cached); a
			// miss/debounce/error returns nil and the negative cache below
			// applies as before.
			if seeded := r.missWithAutoSeed(ctx, clientModel, clientProfile, key); seeded != nil {
				r.mu.Lock()
				r.cache[key] = cacheEntry{
					resolved:   seeded,
					expiration: time.Now().Add(r.ttl),
				}
				r.mu.Unlock()
				return seeded, nil
			}
			// Negative cache (2026-08-15): an unregistered model hit
			// resolveDB on EVERY request outside the positive TTL window —
			// singleflight only dedups concurrent lookups, not repeats. Cache
			// the passthrough miss for a short window (negativeTTL) so a
			// newly-registered model is still picked up quickly.
			p := passthrough(clientModel)
			r.mu.Lock()
			r.cache[key] = cacheEntry{
				resolved:   p,
				expiration: time.Now().Add(r.negativeTTL()),
			}
			r.mu.Unlock()
			return p, nil
		}

		r.mu.Lock()
		r.cache[key] = cacheEntry{
			resolved:   resolved,
			expiration: time.Now().Add(r.ttl),
		}
		r.mu.Unlock()

		return resolved, nil
	})
	if err != nil {
		return passthrough(clientModel)
	}
	if v == nil {
		return passthrough(clientModel)
	}
	return v.(*Resolution)
}

// missWithAutoSeed runs once per (cacheKey, ensureDebounceTTL) when resolveDB
// fully missed. When a provider-reported raw model with the same name exists
// (case/prefix-insensitive via sameRawVariants), its verbatim raw name seeds
// the standard-model row through discovery.EnsureCanonicalAndAliases — the
// same path the periodic discovery worker uses, so operator-disabled rows
// stay disabled and confident catalog matches are linked instead of
// duplicated. On success the lookup is re-run and the real Resolution is
// returned; any miss, error, or debounce hit returns nil so the caller's
// negative-cache fallback applies unchanged.
func (r *Resolver) missWithAutoSeed(ctx context.Context, clientModel, clientProfile, key string) *Resolution {
	if r.dbPool == nil {
		return nil
	}
	if r.ensureDebounced(key) {
		return nil
	}
	rawName, found := r.lookupSameRaw(ctx, clientModel)
	if !found {
		return nil
	}
	ensureFn := r.ensureFn
	if ensureFn == nil {
		ensureFn = discovery.EnsureCanonicalAndAliases
	}
	if _, _, err := ensureFn(ctx, r.dbPool, rawName, "resolve_autoseed"); err != nil {
		slog.Warn("resolve: auto-seed failed",
			"model", clientModel,
			"raw", rawName,
			"error", err,
		)
		return nil
	}
	slog.Info("resolve: seeded canonical from same-named provider raw",
		"model", clientModel,
		"raw", rawName,
	)
	relookup := r.resolveDBFn
	if relookup == nil {
		relookup = r.resolveDB
	}
	seeded, err := relookup(ctx, clientModel, clientProfile)
	if err != nil || seeded == nil {
		return nil
	}
	return seeded
}

// dbLookup dispatches the canonical/alias read to the injected seam or the
// production resolveDB.
func (r *Resolver) dbLookup(ctx context.Context, clientModel, clientProfile string) (*Resolution, error) {
	if r.resolveDBFn != nil {
		return r.resolveDBFn(ctx, clientModel, clientProfile)
	}
	return r.resolveDB(ctx, clientModel, clientProfile)
}

// ensureDebounced records a seed attempt for key and reports whether one is
// already inside the debounce window.
func (r *Resolver) ensureDebounced(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.ensureAttempts[key]; ok && time.Since(t) < ensureDebounceTTL {
		return true
	}
	r.ensureAttempts[key] = time.Now()
	return false
}

// lookupSameRaw finds the verbatim provider-reported raw model name matching
// the resolve-missed client model, shorter names first. The verbatim casing
// matters: EnsureCanonicalAndAliases seeds aliases from it exactly like the
// discovery worker does for the same row.
func (r *Resolver) lookupSameRaw(ctx context.Context, clientModel string) (string, bool) {
	lookup := r.rawLookupFn
	if lookup == nil {
		lookup = r.lookupSameRawDB
	}
	raw, found, err := lookup(ctx, clientModel)
	if err != nil {
		if err != pgx.ErrNoRows {
			slog.Debug("resolve: same-raw lookup failed",
				"model", clientModel,
				"error", err,
			)
		}
		return "", false
	}
	if !found {
		return "", false
	}
	return raw, true
}

func (r *Resolver) lookupSameRawDB(ctx context.Context, clientModel string) (string, bool, error) {
	variants := sameRawVariants(clientModel)
	if len(variants) == 0 {
		return "", false, nil
	}
	var raw string
	err := r.dbPool.QueryRow(ctx, `
		SELECT raw_model_name
		FROM provider_models
		WHERE lower(raw_model_name) = ANY($1)
		   OR lower(regexp_replace(raw_model_name, '^.*/', '')) = ANY($1)
		ORDER BY length(raw_model_name), raw_model_name
		LIMIT 1
	`, variants).Scan(&raw)
	if err != nil {
		return "", false, err
	}
	return raw, true, nil
}

// sameRawVariants builds the lowercase match keys deciding whether a
// provider-observed raw model name corresponds to a resolve-missed client
// model: the prefix-stripped canonical form (CanonicalizeClientModel), the
// normalized route key (which folds date suffixes), and the full variant
// matrix (which keeps the verbatim date-suffixed form). Together they cover
// case variants, vendor prefixes on either side, and verbatim names.
func sameRawVariants(clientModel string) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(v string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	add(modelname.CanonicalizeClientModel(clientModel))
	add(modelname.NormalizeRouteKey(clientModel))
	for _, v := range modelname.NormalizeRouteKeyAliases(clientModel) {
		add(v)
	}
	return out
}

func (r *Resolver) resolveDB(ctx context.Context, clientModel, clientProfile string) (*Resolution, error) {
	if r.dbPool == nil {
		return nil, nil
	}
	variants := modelname.NormalizeRouteKeyAliases(clientModel)
	if len(variants) == 0 {
		// T02 round fix (2026-09-22): report a true miss (nil) so the
		// caller's miss branch — auto-seed + negative cache — runs. A
		// non-nil passthrough here would be positive-cached for the full
		// TTL and skip the miss handling entirely.
		return nil, nil
	}
	normalized := variants[0]
	// canonical_name is persisted lowercase (exact compare); but
	// model_aliases.raw_name preserves client case — since 2026-09-18 the
	// alias queries match with lower(ma.raw_name) = lower($1), which hits
	// the functional partial index idx_model_aliases_lower_raw_name_status
	// (and drops the old COALESCE(status,'active') — the column is NOT NULL
	// DEFAULT 'active'). Do NOT "optimize" back to raw equality.
	rawLookup := modelname.CanonicalizeClientModel(clientModel)
	profile := strings.TrimSpace(strings.ToLower(clientProfile))

	var canonicalID *int
	var canonicalName *string
	var hitPath string

	// 2026-06-19 audit: walk the cross-form variant matrix so a
	// request like "claude-sonnet-4.6" matches a DB canonical
	// "claude-sonnet-4-6" (and the inverse).  The first matching
	// variant wins; the original normalized form stays preferred.
	for _, v := range variants {
		err := r.dbPool.QueryRow(ctx, `
			SELECT id, canonical_name
			FROM models_canonical
			WHERE canonical_name = $1
			  AND COALESCE(status, 'active') = 'active'
		`, modelname.CanonicalizeClientModel(v)).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			hitPath = "canonical"
			break
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}
	if canonicalID != nil {
		raw, err := r.aliasRawNames(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		all := append([]string{normalized}, raw...)
		return &Resolution{
			ClientModel:    clientModel,
			CanonicalID:    canonicalID,
			CanonicalName:  canonicalName,
			RawModels:      uniqueRawModels(all),
			ResolutionPath: hitPath,
		}, nil
	}

	// Alias phase: a raw_name can legitimately map to MULTIPLE canonicals —
	// discovery seeds the bare family alias (`deepseek-v4`) for every raw
	// whose date suffix / wrapper token it strips (`deepseek-v4-flash-260425`
	// → `deepseek-v4-flash` → `deepseek-v4`), one per provider canonical.
	// LIMIT 1 without ORDER BY was nondeterministic over that set; ORDER BY
	// length(name), name resolves it to the most-base (undated) row — the
	// same "shorter name wins, then alphabetical" tie-break the matcher's
	// betterMatch uses.
	for _, v := range variants {
		err := r.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE lower(ma.raw_name) = lower($1)
			  AND ma.status = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
			ORDER BY length(mc.canonical_name), mc.canonical_name
			LIMIT 1
		`, modelname.CanonicalizeClientModel(v), profile).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			hitPath = "alias"
			break
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}

	if canonicalID != nil {
		raw, err := r.aliasRawNames(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		all := append([]string{normalized}, raw...)
		return &Resolution{
			ClientModel:    clientModel,
			CanonicalID:    canonicalID,
			CanonicalName:  canonicalName,
			RawModels:      uniqueRawModels(all),
			ResolutionPath: hitPath,
		}, nil
	}

	if rawLookup != "" && rawLookup != normalized {
		err := r.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE lower(ma.raw_name) = lower($1)
			  AND ma.status = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
			ORDER BY length(mc.canonical_name), mc.canonical_name
			LIMIT 1
		`, rawLookup, profile).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			raw, err := r.aliasRawNames(ctx, *canonicalID, profile)
			if err != nil {
				return nil, err
			}
			all := append([]string{normalized, rawLookup}, raw...)
			return &Resolution{
				ClientModel:    clientModel,
				CanonicalID:    canonicalID,
				CanonicalName:  canonicalName,
				RawModels:      uniqueRawModels(all),
				ResolutionPath: "raw_fallback",
			}, nil
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}

	// T02 round fix (2026-09-22): the full-miss tail MUST return a true nil
	// miss. It previously returned a non-nil passthrough, which made the
	// caller's `resolved == nil` branch — and therefore BOTH the
	// auto-seed (Wave 3 B3) and the 2026-08-15 negative cache — dead code on
	// every deployment with a database. Verified live on the local instance:
	// a seeded provider_models raw never triggered EnsureCanonicalAndAliases
	// until this returned nil.
	return nil, nil
}

func (r *Resolver) aliasRawNames(ctx context.Context, canonicalID int, profile string) ([]string, error) {
	rows, err := r.dbPool.Query(ctx, `
		SELECT raw_name
		FROM model_aliases
		WHERE canonical_id = $1
		  AND COALESCE(status, 'active') = 'active'
		  AND (
		      client_profiles IS NULL
		      OR cardinality(client_profiles) = 0
		      OR $2 = ANY(client_profiles)
		      OR $2 = ''
		  )
	`, canonicalID, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

func uniqueRawModels(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

func passthrough(model string) *Resolution {
	lowered := modelname.NormalizeRouteKey(model)
	return &Resolution{
		ClientModel:    model,
		CanonicalID:    nil,
		CanonicalName:  nil,
		RawModels:      []string{lowered},
		ResolutionPath: "direct",
	}
}

func (r *Resolver) CachedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache)
}

func (r *Resolver) EvictExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for k, v := range r.cache {
		if now.After(v.expiration) {
			delete(r.cache, k)
		}
	}
	for k, t := range r.ensureAttempts {
		if now.Sub(t) >= ensureDebounceTTL {
			delete(r.ensureAttempts, k)
		}
	}
}

func (r *Resolver) evictLoop() {
	ticker := time.NewTicker(r.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.EvictExpired()
		case <-r.stopCh:
			return
		}
	}
}

func (r *Resolver) Stop() {
	close(r.stopCh)
}
