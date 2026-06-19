package resolve

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
}

func NewResolver(_ string, cacheTTL time.Duration) *Resolver {
	if cacheTTL == 0 {
		cacheTTL = 120 * time.Second
	}
	r := &Resolver{
		cache:  make(map[string]cacheEntry),
		ttl:    cacheTTL,
		stopCh: make(chan struct{}),
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
		resolved, fetchErr := r.resolveDB(ctx, clientModel, clientProfile)
		if fetchErr != nil {
			slog.Debug("resolve: DB failed, using passthrough",
				"model", clientModel,
				"error", fetchErr,
			)
			return passthrough(clientModel), nil
		}
		if resolved == nil {
			return passthrough(clientModel), nil
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

func (r *Resolver) resolveDB(ctx context.Context, clientModel, clientProfile string) (*Resolution, error) {
	if r.dbPool == nil {
		return nil, nil
	}
	variants := modelname.NormalizeRouteKeyAliases(clientModel)
	if len(variants) == 0 {
		return passthrough(clientModel), nil
	}
	normalized := variants[0]
	rawLookup := strings.TrimSpace(strings.ToLower(clientModel))
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
			WHERE lower(canonical_name) = lower($1)
			  AND COALESCE(status, 'active') = 'active'
		`, v).Scan(&canonicalID, &canonicalName)
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
			RawModels:      lowerUnique(all),
			ResolutionPath: hitPath,
		}, nil
	}

	for _, v := range variants {
		err := r.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE lower(ma.raw_name) = lower($1)
			  AND COALESCE(ma.status, 'active') = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
			LIMIT 1
		`, v, profile).Scan(&canonicalID, &canonicalName)
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
			RawModels:      lowerUnique(all),
			ResolutionPath: hitPath,
		}, nil
	}

	if rawLookup != "" && rawLookup != normalized {
		err := r.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE lower(ma.raw_name) = lower($1)
			  AND COALESCE(ma.status, 'active') = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
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
				RawModels:      lowerUnique(all),
				ResolutionPath: "raw_fallback",
			}, nil
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}

	return passthrough(clientModel), nil
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

func lowerUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
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
