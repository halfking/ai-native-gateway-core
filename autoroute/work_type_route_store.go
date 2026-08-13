package autoroute

// work_type_route_store.go — bridges admin-configured task→model preferences
// (work_type_model_route table) into the V2 runtime routing path.
//
// Problem solved: the admin panel writes task→model mappings to
// work_type_model_route, but DecideV2 never read that table — only
// OverrideStore (routing_overrides pin/ban). This store closes the gap
// by loading work_type_model_route rows and applying a composite-score
// boost to candidates whose CanonicalName matches a configured route.
//
// Boost logic:
//   - primary tier  → composite *= 1.30  (+30 %)
//   - secondary tier→ composite *= 1.15  (+15 %)
//   - fallback tier → no boost
//
// After boost the scored list is re-sorted by composite DESC so that
// admin-preferred models float to the top while still respecting
// channel-quality scoring for health/availability safety.
//
// Concurrency: same atomic.Pointer snapshot pattern as OverrideStore.
// Reload is called on a 1-min ticker (bg worker).

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WorkTypeRoute mirrors one enabled row of work_type_model_route.
type WorkTypeRoute struct {
	WorkTypeKey      string
	L1TaskType       string
	CanonicalName    string
	Weight           float64
	MinScore         float64
	TaskQualityScore float64
	Tier             string // "primary" | "secondary" | "fallback"
}

// wtRouteSnapshot is the immutable point-in-time view indexed by L1 task type.
type wtRouteSnapshot struct {
	byTaskType map[string][]WorkTypeRoute
	LoadedAt   time.Time
	Version    uint64
}

// WorkTypeRouteStore loads and exposes work_type_model_route rows.
type WorkTypeRouteStore struct {
	pool     *pgxpool.Pool
	snapshot atomic.Pointer[wtRouteSnapshot]
	version  atomic.Uint64
}

// NewWorkTypeRouteStore constructs an empty store. Call Reload before first use.
func NewWorkTypeRouteStore(pool *pgxpool.Pool) *WorkTypeRouteStore {
	return &WorkTypeRouteStore{pool: pool}
}

// current returns the active snapshot (never nil).
func (s *WorkTypeRouteStore) current() *wtRouteSnapshot {
	snap := s.snapshot.Load()
	if snap == nil {
		return &wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{}}
	}
	return snap
}

// Reload fetches enabled routes and their L1 task types from the DB and
// atomically swaps the snapshot. A failed reload leaves the last good snapshot
// active so a transient database error cannot disable routing preferences.
func (s *WorkTypeRouteStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil {
		if s != nil {
			s.snapshot.Store(&wtRouteSnapshot{byTaskType: map[string][]WorkTypeRoute{}})
		}
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.work_type_key, c.l1_task_type, r.canonical_name,
		       COALESCE(r.weight, 1.0), COALESCE(r.min_score, 0),
		       COALESCE(r.task_quality_score, 0),
		       COALESCE(r.tier, 'secondary')
		FROM work_type_model_route r
		JOIN work_type_config c ON c.key = r.work_type_key
		WHERE r.enabled = TRUE AND c.enabled = TRUE
		ORDER BY c.l1_task_type,
		         CASE r.tier WHEN 'primary' THEN 0 WHEN 'secondary' THEN 1 WHEN 'fallback' THEN 2 ELSE 3 END,
		         r.weight DESC, r.work_type_key
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &wtRouteSnapshot{
		byTaskType: make(map[string][]WorkTypeRoute),
		LoadedAt:   time.Now(),
	}
	for rows.Next() {
		var r WorkTypeRoute
		if err := rows.Scan(&r.WorkTypeKey, &r.L1TaskType, &r.CanonicalName,
			&r.Weight, &r.MinScore, &r.TaskQualityScore, &r.Tier); err != nil {
			return err
		}
		snap.byTaskType[r.L1TaskType] = append(snap.byTaskType[r.L1TaskType], r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	snap.Version = s.version.Add(1)
	s.snapshot.Store(snap)
	return nil
}

// tierBoost returns the multiplier for a given tier.
func tierBoost(tier string) float64 {
	switch tier {
	case "primary":
		return 1.30
	case "secondary":
		return 1.15
	default:
		return 1.0 // fallback or unknown → no boost
	}
}

// HasRoutes reports whether the current snapshot contains an enabled route for
// the L1 task type. Decider uses it to retain a full candidate window before
// applying a configured preference.
func (s *WorkTypeRouteStore) HasRoutes(taskType string) bool {
	return s != nil && len(s.current().byTaskType[taskType]) > 0
}

// Version returns the monotonically increasing version of the active snapshot.
// A zero value means no successful reload has completed.
func (s *WorkTypeRouteStore) Version() uint64 {
	if s == nil {
		return 0
	}
	return s.current().Version
}

// routeBoost returns a bounded preference multiplier. Tier supplies the
// primary policy signal; weight and an operator quality override refine it
// without allowing admin data to defeat health/availability scoring.
func routeBoost(r WorkTypeRoute) float64 {
	base := tierBoost(r.Tier)
	weight := r.Weight
	if weight <= 0 {
		weight = 1
	}
	if weight > 1 {
		weight = 1
	}
	quality := r.TaskQualityScore
	if quality < 0 {
		quality = 0
	}
	if quality > 100 {
		quality = 100
	}
	if quality > 0 {
		base *= 0.90 + 0.20*(quality/100)
	}
	return 1 + (base-1)*weight
}

func normalizeCanonicalName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ApplyBoost modifies scored candidates in-place. It is idempotent for a
// candidate slice: the same route marker is never multiplied twice.
func (s *WorkTypeRouteStore) ApplyBoost(scored []ScoredCandidate, taskType string) []ScoredCandidate {
	if s == nil || len(scored) == 0 {
		return scored
	}
	routes := s.current().byTaskType[taskType]
	if len(routes) == 0 {
		return scored
	}

	// Build canonical → boost lookup. A model can be configured by multiple
	// work types mapping to the same L1 task; use the strongest effective route.
	boosts := make(map[string]float64, len(routes))
	minScores := make(map[string]float64, len(routes))
	for _, r := range routes {
		name := normalizeCanonicalName(r.CanonicalName)
		if name == "" {
			continue
		}
		b := routeBoost(r)
		if prev, ok := boosts[name]; !ok || b > prev {
			boosts[name] = b
			minScores[name] = r.MinScore
		}
	}

	changed := false
	for i := range scored {
		if scored[i].Breakdown.RouteBoostApplied {
			continue
		}
		name := normalizeCanonicalName(scored[i].Candidate.CanonicalName)
		if mult, ok := boosts[name]; ok && mult > 1.0 &&
			(minScores[name] <= 0 || scored[i].Breakdown.Composite >= minScores[name]) {
			scored[i].Breakdown.Composite *= mult
			scored[i].Breakdown.RouteBoostApplied = true
			changed = true
		}
	}

	if changed {
		sort.SliceStable(scored, func(i, j int) bool {
			return scored[i].Breakdown.Composite > scored[j].Breakdown.Composite
		})
	}
	return scored
}

// LoadedAt returns the timestamp of the last successful reload. The zero value
// means no reload has completed yet.
func (s *WorkTypeRouteStore) LoadedAt() time.Time {
	if s == nil {
		return time.Time{}
	}
	return s.current().LoadedAt
}
