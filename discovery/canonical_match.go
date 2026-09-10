// canonical_match.go — links provider-observed raw model names to EXISTING
// standard models (models_canonical) before discovery seeds a new row.
//
// 2026-09-10: provider /v1/models feeds frequently carry names that are not
// verbatim catalog names — "cluade/opus-5" (vendor prefix holds a misspelled
// family), "grok/4.6" (prefix holds the family, base only the version). The
// previous flow derived canonical_name = NormalizeRouteKey(raw) which strips
// the prefix, producing junk standard rows like "opus-5" / "4.6" instead of
// matching "claude-opus-5" / "grok-4.6". We now try modelname matching
// against the active catalog first and only fall back to the legacy
// upsert-what-you-see behaviour when nothing scores above
// modelname.AutoLinkThreshold.
package discovery

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

// canonicalRef is one (id, canonical_name) pair from models_canonical.
type canonicalRef struct {
	id   int
	name string
}

// canonicalListQuerier is the read surface needed to load the catalog.
// *pgxpool.Pool satisfies it directly; modelcatalog.Querier callers are
// probed via type assertion so mocks without Query keep working.
type canonicalListQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// canonicalCatalogTTL bounds how long the Service reuses one catalog load.
// Discovery upserts models one by one inside a run; without the cache each
// upsert would re-scan the whole models_canonical table.
const canonicalCatalogTTL = time.Minute

// loadActiveCanonicalCatalog reads every active standard-model name.
func loadActiveCanonicalCatalog(ctx context.Context, q canonicalListQuerier) ([]canonicalRef, error) {
	rows, err := q.Query(ctx, `
		SELECT id, canonical_name FROM models_canonical
		WHERE status = 'active' AND canonical_name <> ''
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	catalog := make([]canonicalRef, 0, 256)
	for rows.Next() {
		var ref canonicalRef
		if err := rows.Scan(&ref.id, &ref.name); err != nil {
			continue
		}
		catalog = append(catalog, ref)
	}
	return catalog, rows.Err()
}

// matchExistingCanonical returns the confident standard-model match for
// rawName. The caller decides what "confident" means by comparing against
// modelname.AutoLinkThreshold — done here so every call site shares one
// policy.
func matchExistingCanonical(ctx context.Context, q canonicalListQuerier, rawName string) (canonicalRef, bool) {
	if strings.TrimSpace(rawName) == "" {
		return canonicalRef{}, false
	}
	catalog, err := loadActiveCanonicalCatalog(ctx, q)
	if err != nil {
		return canonicalRef{}, false
	}
	return bestCanonicalMatch(rawName, catalog)
}

func bestCanonicalMatch(rawName string, catalog []canonicalRef) (canonicalRef, bool) {
	names := make([]string, len(catalog))
	for i, ref := range catalog {
		names[i] = ref.name
	}
	best := modelname.BestStandardModelMatch(rawName, names)
	if best == nil || best.Score < modelname.AutoLinkThreshold {
		return canonicalRef{}, false
	}
	for _, ref := range catalog {
		if strings.EqualFold(ref.name, best.Name) {
			return ref, true
		}
	}
	return canonicalRef{}, false
}

// matchExistingCanonicalCached is the Service-level wrapper that reuses the
// catalog snapshot for canonicalCatalogTTL across the per-model upsert loop.
func (s *Service) matchExistingCanonicalCached(ctx context.Context, rawName string) (canonicalRef, bool) {
	s.canonMu.Lock()
	expired := time.Since(s.canonCachedAt) > canonicalCatalogTTL || s.canonCatalog == nil
	var catalog []canonicalRef
	if !expired {
		catalog = s.canonCatalog
	}
	s.canonMu.Unlock()

	if expired {
		fresh, err := loadActiveCanonicalCatalog(ctx, s.db)
		if err != nil {
			// Matching is an optimization over the legacy path — a
			// failed read must not fail discovery.
			return canonicalRef{}, false
		}
		s.canonMu.Lock()
		s.canonCatalog = fresh
		s.canonCachedAt = time.Now()
		catalog = fresh
		s.canonMu.Unlock()
	}

	if catalog == nil {
		return canonicalRef{}, false
	}
	return bestCanonicalMatch(rawName, catalog)
}
