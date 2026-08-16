package autoroute

// work_type_route_store.go — bridges admin-configured task→model preferences
// (work_type_model_route table) into the V2 runtime routing path.
//
// Problem solved: the admin panel writes task→model mappings to
// work_type_model_route, but DecideV2 never read that table — only
// OverrideStore (routing_overrides pin/ban). This store closes the gap
// by loading work_type_model_route rows and applying strict configured tier
// eligibility after candidates have passed health and availability scoring.
// Tier semantics:
//   - primary tier is considered before secondary and fallback
//   - secondary is considered only if no primary candidate meets min_score
//   - fallback is considered only if neither higher tier has an eligible candidate
//
// Candidates within an eligible tier retain their existing composite ordering,
// preserving cost, pressure, health, and channel-quality selection.
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

// wtRouteSnapshot is the immutable point-in-time view indexed by both the
// legacy L1 task type and the exact enabled work type key.
type wtRouteSnapshot struct {
	byTaskType    map[string][]WorkTypeRoute
	byWorkTypeKey map[string][]WorkTypeRoute
	workTypeL1    map[string]string
	LoadedAt      time.Time
	Version       uint64
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
		return &wtRouteSnapshot{
			byTaskType:    map[string][]WorkTypeRoute{},
			byWorkTypeKey: map[string][]WorkTypeRoute{},
			workTypeL1:    map[string]string{},
		}
	}
	return snap
}

// Reload fetches enabled routes and their L1 task types from the DB and
// atomically swaps the snapshot. A failed reload leaves the last good snapshot
// active so a transient database error cannot disable routing preferences.
func (s *WorkTypeRouteStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil {
		if s != nil {
			s.snapshot.Store(&wtRouteSnapshot{
				byTaskType:    map[string][]WorkTypeRoute{},
				byWorkTypeKey: map[string][]WorkTypeRoute{},
				workTypeL1:    map[string]string{},
			})
		}
		return nil
	}

	configRows, err := s.pool.Query(ctx, `
		SELECT key, l1_task_type
		FROM work_type_config
		WHERE enabled = TRUE
	`)
	if err != nil {
		return err
	}
	defer configRows.Close()

	snap := &wtRouteSnapshot{
		byTaskType:    make(map[string][]WorkTypeRoute),
		byWorkTypeKey: make(map[string][]WorkTypeRoute),
		workTypeL1:    make(map[string]string),
		LoadedAt:      time.Now(),
	}
	for configRows.Next() {
		var key, l1TaskType string
		if err := configRows.Scan(&key, &l1TaskType); err != nil {
			return err
		}
		snap.workTypeL1[key] = l1TaskType
	}
	if err := configRows.Err(); err != nil {
		return err
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

	for rows.Next() {
		var r WorkTypeRoute
		if err := rows.Scan(&r.WorkTypeKey, &r.L1TaskType, &r.CanonicalName,
			&r.Weight, &r.MinScore, &r.TaskQualityScore, &r.Tier); err != nil {
			return err
		}
		snap.workTypeL1[r.WorkTypeKey] = r.L1TaskType
		snap.byWorkTypeKey[r.WorkTypeKey] = append(snap.byWorkTypeKey[r.WorkTypeKey], r)
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

// ResolveWorkType validates an enabled work type key and returns its L1 task type.
func (s *WorkTypeRouteStore) ResolveWorkType(key string) (string, bool) {
	if s == nil {
		return "", false
	}
	key = strings.TrimSpace(key)
	l1, ok := s.current().workTypeL1[key]
	if !ok || !isSupportedTaskType(l1) {
		return "", false
	}
	return l1, true
}

// RoutesForWorkType returns the exact configured routes for an enabled key.
func (s *WorkTypeRouteStore) RoutesForWorkType(key string) ([]WorkTypeRoute, bool) {
	if s == nil {
		return nil, false
	}
	key = strings.TrimSpace(key)
	if _, ok := s.ResolveWorkType(key); !ok {
		return nil, false
	}
	return s.current().byWorkTypeKey[key], true
}

func isSupportedTaskType(task string) bool {
	for _, supported := range AllTaskTypes {
		if string(supported) == task {
			return true
		}
	}
	return false
}

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

// ApplyTierPolicy applies strict configured tier semantics to scored candidates.
// Candidates in the highest configured tier that are available and meet their
// min_score remain eligible; lower tiers are considered only when that tier has
// no eligible candidate. If no configured candidate is eligible, the original
// score-ranked set is returned so stale configuration cannot make routing fail.
func (s *WorkTypeRouteStore) ApplyTierPolicy(scored []ScoredCandidate, taskType string) []ScoredCandidate {
	return s.applyTierPolicy(scored, taskType, nil)
}

// ApplyTierPolicyWithPins keeps explicit pin candidates available for the
// override layer while applying tier policy to the remaining candidates.
func (s *WorkTypeRouteStore) ApplyTierPolicyWithPins(scored []ScoredCandidate, taskType string, pinned []string) []ScoredCandidate {
	return s.applyTierPolicy(scored, taskType, pinned)
}

// ApplyTierPolicyWithWorkType uses the exact work type routes when workType is
// valid; otherwise it preserves the legacy L1 aggregate behavior.
func (s *WorkTypeRouteStore) ApplyTierPolicyWithWorkType(scored []ScoredCandidate, taskType, workType string, pinned []string) []ScoredCandidate {
	return s.applyTierPolicyWithRoutes(scored, s.routesForPolicy(taskType, workType), pinned)
}

func (s *WorkTypeRouteStore) routesForPolicy(taskType, workType string) []WorkTypeRoute {
	if s == nil {
		return nil
	}
	if routes, ok := s.RoutesForWorkType(workType); ok && len(routes) > 0 {
		return routes
	}
	return s.current().byTaskType[taskType]
}

func (s *WorkTypeRouteStore) applyTierPolicy(scored []ScoredCandidate, taskType string, pinned []string) []ScoredCandidate {
	return s.applyTierPolicyWithRoutes(scored, s.routesForPolicy(taskType, ""), pinned)
}

func (s *WorkTypeRouteStore) applyTierPolicyWithRoutes(scored []ScoredCandidate, routes []WorkTypeRoute, pinned []string) []ScoredCandidate {
	if len(routes) == 0 {
		return scored
	}

	pinnedModels := make(map[string]struct{}, len(pinned))
	for _, name := range pinned {
		pinnedModels[normalizeCanonicalName(name)] = struct{}{}
	}

	eligibleTierByModel := eligibleTierByModel(scored, routes, pinnedModels)
	activeRank := activeTierRank(eligibleTierByModel)
	if activeRank == 4 {
		return scored
	}

	filtered := make([]ScoredCandidate, 0, len(scored))
	for i := range scored {
		name := normalizeCanonicalName(scored[i].Candidate.CanonicalName)
		if _, isPinned := pinnedModels[name]; isPinned {
			filtered = append(filtered, scored[i])
			continue
		}
		rank, ok := eligibleTierByModel[name]
		if !ok || rank != activeRank {
			continue
		}
		scored[i].Breakdown.RouteTier = routeTierName(activeRank)
		filtered = append(filtered, scored[i])
	}
	return filtered
}

// TierFailoverModels returns an ordered model-level recovery plan for the
// configured L1 tier pool. It begins at the active eligible tier and includes
// each lower tier in turn, retaining the scorer's order within a tier. It does
// not invent a plan when no configured candidate is eligible, so stale admin
// configuration cannot constrain the normal fallback behavior.
func (s *WorkTypeRouteStore) TierFailoverModels(scored []ScoredCandidate, taskType string) []string {
	return s.tierFailoverModelsWithRoutes(scored, s.routesForPolicy(taskType, ""))
}

// TierFailoverModelsWithWorkType uses exact work type routes when available.
func (s *WorkTypeRouteStore) TierFailoverModelsWithWorkType(scored []ScoredCandidate, taskType, workType string) []string {
	return s.tierFailoverModelsWithRoutes(scored, s.routesForPolicy(taskType, workType))
}

func (s *WorkTypeRouteStore) tierFailoverModelsWithRoutes(scored []ScoredCandidate, routes []WorkTypeRoute) []string {
	if s == nil || len(scored) == 0 {
		return nil
	}
	if len(routes) == 0 {
		return nil
	}

	eligibleTierByModel := eligibleTierByModel(scored, routes, nil)
	activeRank := activeTierRank(eligibleTierByModel)
	if activeRank == 4 {
		return nil
	}

	models := make([]string, 0, len(eligibleTierByModel))
	seen := make(map[string]struct{}, len(eligibleTierByModel))
	for rank := activeRank; rank <= 2; rank++ {
		for _, candidate := range scored {
			name := normalizeCanonicalName(candidate.Candidate.CanonicalName)
			candidateRank, eligible := eligibleTierByModel[name]
			if !eligible || candidateRank != rank {
				continue
			}
			if _, duplicate := seen[name]; duplicate {
				continue
			}
			seen[name] = struct{}{}
			models = append(models, candidate.Candidate.CanonicalName)
		}
	}
	return models
}

func eligibleTierByModel(scored []ScoredCandidate, routes []WorkTypeRoute, excluded map[string]struct{}) map[string]int {
	eligible := make(map[string]int, len(scored))
	for i := range scored {
		name := normalizeCanonicalName(scored[i].Candidate.CanonicalName)
		if _, skip := excluded[name]; skip {
			continue
		}
		for _, route := range routes {
			if normalizeCanonicalName(route.CanonicalName) != name ||
				(route.MinScore > 0 && scored[i].Breakdown.Composite < route.MinScore) {
				continue
			}
			rank := routeTierRank(route.Tier)
			if rank > 2 {
				continue
			}
			if previous, ok := eligible[name]; !ok || rank < previous {
				eligible[name] = rank
			}
		}
	}
	return eligible
}

func activeTierRank(eligible map[string]int) int {
	activeRank := 4
	for _, rank := range eligible {
		if rank < activeRank {
			activeRank = rank
		}
	}
	return activeRank
}

func routeTierRank(tier string) int {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "primary":
		return 0
	case "secondary":
		return 1
	case "fallback":
		return 2
	default:
		return 3
	}
}

func routeTierName(rank int) string {
	return []string{"primary", "secondary", "fallback"}[rank]
}

// ApplyBoost modifies scored candidates in-place. It is idempotent for a
// candidate slice: the same route marker is never multiplied twice. V2
// runtime selection applies ApplyTierPolicy separately before this legacy
// preference multiplier.
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
