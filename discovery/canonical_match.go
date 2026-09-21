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
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
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
// Ordered by id so case-variant duplicates resolve deterministically.
func loadActiveCanonicalCatalog(ctx context.Context, q canonicalListQuerier) ([]canonicalRef, error) {
	rows, err := q.Query(ctx, `
		SELECT id, canonical_name FROM models_canonical
		WHERE status = 'active' AND canonical_name <> ''
		ORDER BY id
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
			// failed read must not fail discovery, but it silently
			// reverts ingestion to the seed-new-canonical behaviour, so
			// make the degradation visible.
			slog.Warn("standard-model catalog load failed; discovery will seed new canonical rows without matching",
				"error", err)
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

// maintainMatchedCanonical re-applies the models_canonical maintenance that
// the INSERT ... ON CONFLICT branch performs (family:<id> tag backfill,
// split-family normalization, sticky-modality upgrade) to a row reached via
// the matched-standard path, which returns before that branch runs.
//
// 2026-09-11: the matched fast path used to skip all of it, so a row linked
// from a provider feed never got the family tag the /models family-chip
// filter needs, never normalized a legacy split family ("claude" →
// "anthropic-claude"), and — worst — kept a stale modality='text' forever
// when inference had moved on. Because loadCandidatesByModalityDB only
// admits modality IN ('vision','multimodal') for an image request, such a
// row 503s every image call (the 2026-08-09 sticky-modality bug, reintroduced
// for matched rows by the fast path).
//
// One guarded UPDATE mirrors the three ON CONFLICT CASE branches exactly;
// the WHERE admits only rows that still need a change, so healthy rows cost
// a single no-op index hit instead of a write on every discovery pass:
//   - family unchanged but the family:<id> tag is missing → append it;
//   - family is a known split token → normalize to the canonical form and
//     swap the family:<old> tag for family:<new>;
//   - modality is still the column default 'text' and inference now claims
//     something richer → adopt it (upgrade-only, never a downgrade, never
//     over a super_admin override which always sets a non-'text' value).
//
// An admin-edited family that differs from the computed one (i.e. NOT a
// known split token) is left alone, exactly like the ON CONFLICT ELSE
// branch — only the modality clause can still admit such a row.
func maintainMatchedCanonical(ctx context.Context, db modelcatalog.Querier, canonicalID int, canonicalName, rawName string) error {
	if db == nil {
		return nil
	}
	family := InferFamily(canonicalName)
	inferredModality := modelname.InferModality(rawName)
	_, err := db.Exec(ctx, `
		UPDATE models_canonical
		SET family = CASE
				WHEN family = $2
				THEN family
				WHEN family = ANY($3::text[])
				THEN $2
				ELSE family
			END,
			tags = CASE
				WHEN family = $2
				THEN (
					SELECT array_agg(DISTINCT t)
					FROM unnest(tags || ARRAY['family:' || $2]) AS t
				)
				WHEN family = ANY($3::text[])
				THEN (
					SELECT array_agg(DISTINCT t)
					FROM unnest(
						array_remove(
							array_remove(tags, 'family:' || family),
							'family:' || $2
						) || ARRAY['family:' || $2]
					) AS t
				)
				ELSE tags
			END,
			modality = CASE
				WHEN modality = 'text' AND $4 <> 'text'
				THEN $4
				ELSE modality
			END
		WHERE id = $1
		  AND (
			(family = $2 AND NOT tags @> ARRAY['family:' || $2])
			OR family = ANY($3::text[])
			OR (modality = 'text' AND $4 <> 'text')
		  )
	`, canonicalID, family, splitFamilyIDs, inferredModality)
	return err
}
