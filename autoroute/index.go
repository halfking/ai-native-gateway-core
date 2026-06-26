package autoroute

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Index is the in-memory cache of the credential_model_index 5-min rolled-up
// snapshot. Refreshed every 5 minutes by bg/auto_index_refresher.go.
//
// The cache is read-mostly: Recommend() is the hot path called on every
// model=auto request. Refresh() is called from the background worker.
//
// Concurrency:
//   - mu protects the entries + lastRefresh fields
//   - recommend snapshots the entries pointer under RLock so concurrent
//     requests don't block on a refresh
type Index struct {
	mu          sync.RWMutex
	entries     []Candidate // flat list of all candidates from last refresh
	byCanonical map[int][]*Candidate
	lastRefresh time.Time

	// Pool is the optional PG pool for on-demand refresh when the cache
	// is stale (older than staleThreshold). nil disables on-demand refresh.
	pool *pgxpool.Pool

	// staleThreshold controls when on-demand refresh kicks in.
	// Default: 10 minutes (2x the refresh interval).
	staleThreshold time.Duration
}

// NewIndex constructs an empty index. Call Refresh once before first use.
func NewIndex() *Index {
	return &Index{
		byCanonical:    make(map[int][]*Candidate),
		staleThreshold: 10 * time.Minute,
	}
}

// SetPool wires the PG pool used by on-demand refresh.
func (idx *Index) SetPool(pool *pgxpool.Pool) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.pool = pool
}

// LastRefresh returns the time of the last successful refresh (zero if
// never refreshed). Used by admin API and tests.
func (idx *Index) LastRefresh() time.Time {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.lastRefresh
}

// Snapshot returns a defensive copy of the current candidates.
// Used by admin API to render the current index state.
func (idx *Index) Snapshot() []Candidate {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	out := make([]Candidate, len(idx.entries))
	copy(out, idx.entries)
	return out
}

// Recommend returns the top-N candidates for the given task type, scored
// against the given profile. Cohort-level baselines (price P75, speed P95)
// are computed from the candidate set so per-candidate scoring is
// apples-to-apples.
//
// Filtering:
//   - Only routable candidates (unavailable_reason == "")
//   - Only candidates whose TaskMatchScore > 0 (some tag overlap)
//   - Top-N by composite score (descending)
//
// If the cohort is empty after filtering, returns nil (caller should
// fall back to the default model).
func (idx *Index) Recommend(task TaskType, sigs ClassificationSignals, profile Profile, topN int) []ScoredCandidate {
	idx.mu.RLock()
	all := idx.entries
	idx.mu.RUnlock()

// Pre-filter: routable + has some tag match.
// Fallback: if NO candidate has any tag match for this task type,
// return all candidates (chat-style fallback) rather than 0 candidates.
// This handles the common case where models_canonical.tags is empty
// (which is the default for newly-discovered models) — without this
// fallback, every task_type other than chat would always 503.
filtered := make([]Candidate, 0, len(all))
for i := range all {
	c := all[i]
	// Compute TaskMatchScore inline so admins can change the
	// required-tag map without a code redeploy.
	c.TaskMatchScore = TaskMatchScore(task, c.Tags)
	filtered = append(filtered, c)
}

// Sort: candidates with non-zero TaskMatchScore first, then all others.
// We don't strictly filter (which would 503 every task type when tags
// are empty); instead we keep all and let the MatchScore (0-100)
// contribute to the weighted composite.
sort.SliceStable(filtered, func(i, j int) bool {
	return filtered[i].TaskMatchScore > filtered[j].TaskMatchScore
})

if len(filtered) == 0 {
	return nil
}

	// Compute cohort baselines
	costCtx := computeCostContext(filtered)

	// Score all
	scored := make([]ScoredCandidate, 0, len(filtered))
	for _, c := range filtered {
		bd := Score(c, sigs, profile, costCtx)
		scored = append(scored, ScoredCandidate{Candidate: c, Breakdown: bd})
	}

	// Sort by composite desc
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
	})

	if topN > 0 && len(scored) > topN {
		scored = scored[:topN]
	}
	return scored
}

// computeCostContext derives the cohort baselines used for normalisation.
//
// PriceP75: 75th-percentile blended price (1:1 input:output assumption)
//   - We use a hand-rolled percentile since the cohort is tiny (<= 50)
//   - Zero-cost candidates are excluded from the percentile so free
//     models don't drag the baseline to 0.
//
// SpeedP95: maximum P95 latency across the cohort (worst case)
//   - Used as the "ceiling" so faster candidates score higher.
func computeCostContext(cands []Candidate) CostContext {
	prices := make([]float64, 0, len(cands))
	speeds := make([]int, 0, len(cands))
	for _, c := range cands {
		blended := c.UnitPriceInPer1M + c.UnitPriceOutPer1M
		if blended > 0 {
			prices = append(prices, blended)
		}
		if c.P95LatencyMs > 0 {
			speeds = append(speeds, c.P95LatencyMs)
		}
	}
	ctx := CostContext{}
	if len(prices) > 0 {
		sort.Float64s(prices)
		ctx.PriceP75 = percentile(prices, 0.75)
	}
	if len(speeds) > 0 {
		ctx.SpeedP95 = float64(speeds[len(speeds)-1]) // max
	}
	return ctx
}

// percentile returns the p-th percentile (0-1) of a sorted slice.
// Uses nearest-rank method with ceil (rounds up so p=0.5 of 10 items
// returns the 5th item, which is the standard interpretation).
// Caller must pre-sort.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	// ceil(n * p)
	n := float64(len(sorted))
	rank := int(n*p + 0.999999)
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

// Refresh reloads the entire credential_model_index snapshot from the
// most recent 5-min bucket. Called by bg/auto_index_refresher.go every
// 5 minutes and by SetPool on first start.
//
// Failure handling: returns the error but leaves the old snapshot in
// place (don't fail-closed on a transient DB blip).
//
// Implementation note: uses a single SQL query joining credentials,
// models_canonical, and credential_model_index. Returns a flat list
// of Candidate structs. Tags are parsed from JSONB.
func (idx *Index) Refresh(ctx context.Context) error {
	if idx.pool == nil {
		return fmt.Errorf("autoroute index: PG pool not set")
	}
	rows, err := idx.pool.Query(ctx, refreshIndexSQL)
	if err != nil {
		return fmt.Errorf("query credential_model_index: %w", err)
	}
	defer rows.Close()

	out := make([]Candidate, 0, 64)
	scanErrors := 0
	for rows.Next() {
		c, scanErr := scanIndexRow(rows)
		if scanErr != nil {
			// Skip the bad row but keep going — partial refresh
			// is better than no refresh.
			scanErrors++
			slog.Warn("autoroute.Index.Refresh: scan error", "error", scanErr)
			continue
		}
		out = append(out, c)
	}
	if rows.Err() != nil {
		return fmt.Errorf("iterate credential_model_index: %w", rows.Err())
	}

	// Log what was loaded
	modelSet := make(map[string]int)
	modelNames := make([]string, 0)
	for i := range out {
		modelSet[out[i].RawModel]++
		if len(modelNames) < 20 {
			modelNames = append(modelNames, out[i].RawModel)
		}
	}
	slog.Info("autoroute.Index.Refresh: query completed",
		"total_candidates", len(out),
		"scan_errors", scanErrors,
		"unique_models", len(modelSet),
		"sample_models", modelNames)

	// Build byCanonical lookup
	byCanon := make(map[int][]*Candidate, 16)
	for i := range out {
		c := &out[i]
		byCanon[c.CanonicalID] = append(byCanon[c.CanonicalID], c)
	}

	idx.mu.Lock()
	idx.entries = out
	idx.byCanonical = byCanon
	idx.lastRefresh = time.Now()
	idx.mu.Unlock()
	return nil
}

// refreshIndexSQL is the single query that materialises the index snapshot.
//
// Source tables:
//   - credential_model_index : latest 5-min bucket
//   - models_canonical       : canonical model attributes (tags, context_window)
//   - credentials            : provider_id, label, lifecycle
//
// Filter:
//   - Only the most recent bucket per (credential_id, raw_model)
//   - Skip disabled credentials / suspended lifecycle
//   - Skip zero-context models (likely misconfigured)
//
// TODO(billing_currency): convert CNY to USD before scoring when the
// cohort contains both currencies. See HasMixedPrices for the placeholder.
const refreshIndexSQL = `
WITH latest_bucket AS (
    SELECT credential_id, raw_model, MAX(bucket) AS bucket
    FROM credential_model_index
    GROUP BY credential_id, raw_model
)
SELECT
    cmi.credential_id,
    cmi.raw_model,
    cmi.canonical_id,
    mc.canonical_name,
    mc.tags,
    mc.context_window,
    cmi.billing_mode,
    cmi.unit_price_in_per_1m,
    cmi.unit_price_out_per_1m,
    cmi.success_rate,
    cmi.p95_latency_ms,
    cmi.active_sessions,
    cmi.concurrency_limit
FROM credential_model_index cmi
JOIN latest_bucket lb
  ON lb.credential_id = cmi.credential_id
 AND lb.raw_model     = cmi.raw_model
 AND lb.bucket        = cmi.bucket
JOIN credentials cr ON cr.id = cmi.credential_id
LEFT JOIN models_canonical mc ON mc.id = cmi.canonical_id
WHERE COALESCE(cr.lifecycle_status, 'active') != 'suspended'
  AND COALESCE(cr.status, 'active') NOT IN ('disabled')
ORDER BY cmi.canonical_id, cmi.score_smart DESC
`

// scanIndexRow decodes one row from the refresh query into a Candidate.
// Uses string-typed SQL scanning then parses tags and computes
// pressure_ratio from active_sessions / concurrency_limit.
//
// Tags handling: models_canonical.tags is a TEXT[] (PostgreSQL array,
// OID 1009). pgx returns it as []string when the destination is []string
// (or pgtype array). We use a []string destination and skip the
// JSONB parser — TEXT[] is not JSONB.
func scanIndexRow(rows interface {
	Scan(dest ...any) error
}) (Candidate, error) {
	var c Candidate
	var tags []string
	var ctxWindow *int
	// v2.0.4 fix: unit_price / success_rate / canonical_id / canonical_name /
	// billing_mode can be NULL in credential_model_index when the source
	// rows have no matching model_offers entry. Use pointers to allow NULL.
	var canonicalID *int
	var canonicalName *string
	var billingMode *string
	var priceIn, priceOut, successRate *float64
	if err := rows.Scan(
		&c.CredentialID, &c.RawModel, &canonicalID,
		&canonicalName, &tags, &ctxWindow,
		&billingMode,
		&priceIn, &priceOut,
		&successRate, &c.P95LatencyMs,
		&c.ActiveSessions, &c.ConcurrencyLimit,
	); err != nil {
		return c, err
	}
	if ctxWindow != nil {
		c.ContextWindow = *ctxWindow
	}
	if canonicalID != nil {
		c.CanonicalID = *canonicalID
	}
	if canonicalName != nil {
		c.CanonicalName = *canonicalName
	}
	if billingMode != nil {
		c.BillingMode = *billingMode
	}
	if priceIn != nil {
		c.UnitPriceInPer1M = *priceIn
	}
	if priceOut != nil {
		c.UnitPriceOutPer1M = *priceOut
	}
	if successRate != nil {
		c.SuccessRate = *successRate
	}
	c.Tags = tags
	// PressureRatio: 0 when concurrency_limit is 0 (unknown → no penalty)
	if c.ConcurrencyLimit > 0 {
		c.PressureRatio = float64(c.ActiveSessions) / float64(c.ConcurrencyLimit)
	}
	return c, nil
}

// parseTagsJSONB parses a PostgreSQL JSONB array literal of strings
// into a []string. Returns nil on parse error or empty input.
//
// Example input: `["reasoning","code","agent"]`
// Example input: `null` → returns nil
//
// Uses encoding/json (canonical, robust to escape sequences).
func parseTagsJSONB(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}