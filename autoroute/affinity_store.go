package autoroute

// affinity_store.go — DB-backed snapshot of the learned task→model affinity.
//
// Mirrors the TuningStore / OverrideStore / DefaultRoutingStore pattern already
// used in this package:
//
//   - the whole table is small (task_types × profiles × models), so it is held
//     in memory and swapped atomically; readers never touch the DB
//   - lock-free reads via atomic.Pointer, because Lookup runs per candidate
//     per request inside scoring
//   - a nil pool or a failed Reload degrades to "no opinion" (neutral 50)
//     rather than an error: affinity is an optimisation, never a gate
//
// Tenant resolution (design doc open question #1, resolved here): a tenant row
// is only trusted once it has AffinityTenantMinSamples of its own evidence,
// otherwise the platform row is used. A tenant with three requests should
// inherit platform-wide knowledge, not overrule it.

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AffinityTenantMinSamples is the evidence a tenant-scoped row needs before it
// overrides the platform-scoped row for the same (task, profile, model).
const AffinityTenantMinSamples = 30

// affinityKey identifies one learned row within a scope.
type affinityKey struct {
	taskType string
	profile  string
	tenantID string
	canonID  int64
}

// affinitySnapshot is an immutable point-in-time view of task_model_affinity.
type affinitySnapshot struct {
	// byKey holds every row, platform and tenant alike.
	byKey map[affinityKey]AffinityRecord

	// ranked holds rows ordered by affinity DESC per (task, profile, tenant),
	// for the admin "best models for this task" read path.
	ranked map[string][]AffinityRecord

	LoadedAt time.Time
	RowCount int
}

// AffinityStore provides atomic-snapshot access to learned affinity.
type AffinityStore struct {
	pool *pgxpool.Pool
	mode AffinityMode

	// exploreRatio is the share of traffic that bypasses affinity.
	exploreRatio float64

	// snapshot is nil until the first successful Reload; readers must treat
	// nil as "no opinion".
	snapshot atomic.Pointer[affinitySnapshot]
}

// NewAffinityStore constructs a store. pool may be nil (tests, or a deployment
// with no DB), in which case every lookup returns neutral.
func NewAffinityStore(pool *pgxpool.Pool, mode AffinityMode, exploreRatio float64) *AffinityStore {
	if exploreRatio < 0 || exploreRatio > 1 {
		exploreRatio = AffinityDefaultExploreRatio
	}
	return &AffinityStore{pool: pool, mode: mode, exploreRatio: exploreRatio}
}

// Mode reports the configured mode. Off/shadow callers must not let affinity
// change the outcome.
func (s *AffinityStore) Mode() AffinityMode {
	if s == nil {
		return AffinityOff
	}
	return s.mode
}

// ExploreRatio reports the configured explore share.
func (s *AffinityStore) ExploreRatio() float64 {
	if s == nil {
		return 0
	}
	return s.exploreRatio
}

// Applies reports whether affinity should actually influence selection for this
// request: mode must be `on` and the request must not be in the explore bucket.
//
// Shadow mode returns false here but callers still record the computed score,
// which is what makes the shadow period useful.
func (s *AffinityStore) Applies(requestID string) bool {
	if s == nil || s.mode != AffinityOn {
		return false
	}
	return !ShouldExplore(requestID, s.exploreRatio)
}

// ShouldExplore reports whether the request hashes into the explore bucket,
// given the store's configured ratio. Independent of mode — useful for the
// MEDIUM-6 Explore flag, which must be recorded even when affinity does not
// apply (otherwise P3 has no observable explore arm).
func (s *AffinityStore) ShouldExplore(requestID string) bool {
	if s == nil {
		return false
	}
	return ShouldExplore(requestID, s.exploreRatio)
}

// current returns the live snapshot, or an empty one when Reload has never
// succeeded. Never returns nil.
func (s *AffinityStore) current() *affinitySnapshot {
	if s == nil {
		return emptyAffinitySnapshot()
	}
	snap := s.snapshot.Load()
	if snap == nil {
		return emptyAffinitySnapshot()
	}
	return snap
}

func emptyAffinitySnapshot() *affinitySnapshot {
	return &affinitySnapshot{
		byKey:    map[affinityKey]AffinityRecord{},
		ranked:   map[string][]AffinityRecord{},
		LoadedAt: time.Time{},
	}
}

func rankedKey(taskType, profile, tenantID string) string {
	return taskType + "\x00" + profile + "\x00" + tenantID
}

// Lookup returns the effective affinity in [10,90] for one candidate, and
// whether a usable learned row backed it.
//
// Resolution order:
//  1. tenant row with >= AffinityTenantMinSamples samples
//  2. platform row
//  3. neutral 50, found=false
//
// The returned value already has the min-sample floor, staleness decay and
// deviation clamp applied, so callers can use it directly.
func (s *AffinityStore) Lookup(taskType TaskType, profile Profile, tenantID string, canonicalID int64) (float64, bool) {
	if s == nil || s.mode == AffinityOff || canonicalID <= 0 {
		return AffinityNeutral, false
	}
	snap := s.current()
	if len(snap.byKey) == 0 {
		return AffinityNeutral, false
	}
	now := time.Now()

	if tenantID != "" {
		if rec, ok := snap.byKey[affinityKey{string(taskType), string(profile), tenantID, canonicalID}]; ok {
			if rec.SampleCount >= AffinityTenantMinSamples {
				return rec.EffectiveAffinity(now), true
			}
		}
	}

	if rec, ok := snap.byKey[affinityKey{string(taskType), string(profile), "", canonicalID}]; ok {
		if rec.SampleCount >= AffinityMinSamples {
			return rec.EffectiveAffinity(now), true
		}
	}

	return AffinityNeutral, false
}

// Ranking returns the learned rows for a (task, profile, tenant) scope, ordered
// by stored affinity descending. Read-only; the caller must not mutate.
func (s *AffinityStore) Ranking(taskType TaskType, profile Profile, tenantID string) []AffinityRecord {
	snap := s.current()
	return snap.ranked[rankedKey(string(taskType), string(profile), tenantID)]
}

// Stats reports snapshot metadata for admin/observability.
func (s *AffinityStore) Stats() (rowCount int, loadedAt time.Time) {
	snap := s.current()
	return snap.RowCount, snap.LoadedAt
}

// Reload replaces the snapshot from task_model_affinity. Best-effort: on error
// the previous snapshot is retained (stale data beats no data, and decay
// bounds how wrong stale data can get).
func (s *AffinityStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil || s.mode == AffinityOff {
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT task_type, profile, canonical_id, canonical_model, tenant_id,
		       sample_count, success_count,
		       COALESCE(success_rate, 0), COALESCE(avg_reward, 0),
		       COALESCE(ema_reward, 0), COALESCE(avg_latency_ms, 0),
		       COALESCE(avg_cost_usd, 0), COALESCE(avg_health, 0),
		       affinity, COALESCE(rank, 0), confidence,
		       last_sampled_at, updated_at
		FROM task_model_affinity
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &affinitySnapshot{
		byKey:    make(map[affinityKey]AffinityRecord),
		ranked:   make(map[string][]AffinityRecord),
		LoadedAt: time.Now(),
	}

	for rows.Next() {
		var (
			rec           AffinityRecord
			taskType      string
			profile       string
			lastSampledAt *time.Time
		)
		if err := rows.Scan(
			&taskType, &profile, &rec.CanonicalID, &rec.CanonicalModel, &rec.TenantID,
			&rec.SampleCount, &rec.SuccessCount,
			&rec.SuccessRate, &rec.AvgReward,
			&rec.EMAReward, &rec.AvgLatencyMs,
			&rec.AvgCostUSD, &rec.AvgHealth,
			&rec.Affinity, &rec.Rank, &rec.Confidence,
			&lastSampledAt, &rec.UpdatedAt,
		); err != nil {
			// Skip the malformed row rather than losing the whole snapshot.
			continue
		}
		rec.TaskType = TaskType(taskType)
		rec.Profile = Profile(profile)
		if lastSampledAt != nil {
			rec.LastSampledAt = *lastSampledAt
		}

		snap.byKey[affinityKey{taskType, profile, rec.TenantID, rec.CanonicalID}] = rec
		rk := rankedKey(taskType, profile, rec.TenantID)
		snap.ranked[rk] = append(snap.ranked[rk], rec)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for k := range snap.ranked {
		sortByAffinityDesc(snap.ranked[k])
	}
	snap.RowCount = len(snap.byKey)

	s.snapshot.Store(snap)
	slog.Debug("affinity snapshot reloaded", "rows", snap.RowCount, "mode", string(s.mode))
	return nil
}

// sortByAffinityDesc orders rows strongest-first, breaking ties by confidence
// then sample count so a well-evidenced row outranks a barely-measured one at
// equal affinity. Insertion sort: these slices hold a handful of models.
func sortByAffinityDesc(rows []AffinityRecord) {
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0 && affinityLess(rows[j-1], rows[j]); j-- {
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
}

// affinityLess reports whether a should sort after b (a is weaker than b).
func affinityLess(a, b AffinityRecord) bool {
	if a.Affinity != b.Affinity {
		return a.Affinity < b.Affinity
	}
	if a.Confidence != b.Confidence {
		return a.Confidence < b.Confidence
	}
	return a.SampleCount < b.SampleCount
}
