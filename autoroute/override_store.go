package autoroute

// override_store.go — P7.6: load and apply admin-authored routing
// overrides.
//
// An override is a (task_type, profile, mode, model_chosen) row
// from the routing_overrides table. The store loads all active
// overrides into an atomic pointer snapshot (same pattern as
// TuningStore) and exposes two methods:
//
//   FilterBanned(candidates, taskType, profile) — removes any
//     candidate that has a 'ban' override for this (task, profile).
//     Pre-scoring filter: banned models never appear in the
//     candidate list at all.
//
//   PromotePins(candidates, taskType, profile) — promotes any
//     candidate that has a 'pin' override to the top of the list
//     (in pin order). Post-scoring sort: pinned models win
//     regardless of composite score.
//
// Together, these let admins:
//   - Blacklist a model that P7.2's correlation dashboard shows
//     is failing on a specific task type
//   - Pin a model that they trust for a specific task type
//
// Atomic.Pointer keeps the hot path zero-overhead. Reloads happen
// on a 1-min ticker (via the same bg worker pattern as
// TuningStoreRefresher).

import (
	"context"
	"sort"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OverrideMode identifies whether a row pins or bans a model.
type OverrideMode string

const (
	OverridePin OverrideMode = "pin"
	OverrideBan OverrideMode = "ban"
)

// Override is one row from routing_overrides.
type Override struct {
	ID          int64
	TaskType    string
	Profile     string
	Mode        OverrideMode
	ModelChosen string // may be empty for a generic pin
	Reason      string
	CreatedBy   string
	ExpiresAt   *time.Time
}

// overrideSnapshot is the immutable point-in-time view of all
// active overrides. Indexed by (task_type, profile) for O(1)
// lookup on the hot path.
type overrideSnapshot struct {
	byTaskProfile map[string][]Override
	LoadedAt      time.Time
}

// OverrideStore loads and exposes active routing overrides.
type OverrideStore struct {
	pool     *pgxpool.Pool
	snapshot atomic.Pointer[overrideSnapshot]
}

// NewOverrideStore constructs an empty store. Call Reload before
// first use to populate the snapshot.
func NewOverrideStore(pool *pgxpool.Pool) *OverrideStore {
	return &OverrideStore{pool: pool}
}

// current returns the active snapshot, or an empty one if Reload
// has never succeeded. Never returns nil.
func (s *OverrideStore) current() *overrideSnapshot {
	snap := s.snapshot.Load()
	if snap == nil {
		return &overrideSnapshot{byTaskProfile: map[string][]Override{}}
	}
	return snap
}

// Reload fetches all active overrides from the DB and atomically
// swaps the snapshot. Safe to call concurrently.
func (s *OverrideStore) Reload(ctx context.Context) error {
	if s == nil || s.pool == nil {
		if s != nil {
			s.snapshot.Store(&overrideSnapshot{byTaskProfile: map[string][]Override{}})
		}
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, task_type, profile, mode, model_chosen, reason, created_by, expires_at
		FROM routing_overrides
		WHERE expires_at IS NULL OR expires_at > NOW()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &overrideSnapshot{
		byTaskProfile: make(map[string][]Override),
		LoadedAt:      time.Now(),
	}
	for rows.Next() {
		var o Override
		var mode string
		var modelChosen *string
		var createdBy *string
		var expiresAt *time.Time
		if err := rows.Scan(&o.ID, &o.TaskType, &o.Profile, &mode,
			&modelChosen, &o.Reason, &createdBy, &expiresAt); err != nil {
			continue
		}
		o.Mode = OverrideMode(mode)
		if modelChosen != nil {
			o.ModelChosen = *modelChosen
		}
		if createdBy != nil {
			o.CreatedBy = *createdBy
		}
		o.ExpiresAt = expiresAt
		key := overrideKey(o.TaskType, o.Profile)
		snap.byTaskProfile[key] = append(snap.byTaskProfile[key], o)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.snapshot.Store(snap)
	return nil
}

func overrideKey(taskType, profile string) string {
	return taskType + "|" + profile
}

// GetBans returns the set of model_chosen strings that are banned
// for the given (taskType, profile).
func (s *OverrideStore) GetBans(taskType, profile string) map[string]bool {
	snap := s.current()
	bans := make(map[string]bool, 4)
	for _, o := range snap.byTaskProfile[overrideKey(taskType, profile)] {
		if o.Mode == OverrideBan && o.ModelChosen != "" {
			bans[o.ModelChosen] = true
		}
	}
	return bans
}

// GetPins returns the pinned model names in priority order.
func (s *OverrideStore) GetPins(taskType, profile string) []string {
	snap := s.current()
	pins := make([]string, 0)
	for _, o := range snap.byTaskProfile[overrideKey(taskType, profile)] {
		if o.Mode == OverridePin && o.ModelChosen != "" {
			pins = append(pins, o.ModelChosen)
		}
	}
	return pins
}

// FilterBanned removes any candidate whose CanonicalName is in the
// ban set for (taskType, profile).
func (s *OverrideStore) FilterBanned(candidates []ScoredCandidate, taskType, profile string) []ScoredCandidate {
	bans := s.GetBans(taskType, profile)
	if len(bans) == 0 {
		return candidates
	}
	out := make([]ScoredCandidate, 0, len(candidates))
	for _, c := range candidates {
		if bans[c.Candidate.CanonicalName] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// PromotePins re-sorts the candidates so that pinned models appear
// at the top (in pin order), with the original composite order
// preserved within each group.
func (s *OverrideStore) PromotePins(candidates []ScoredCandidate, taskType, profile string) []ScoredCandidate {
	pins := s.GetPins(taskType, profile)
	if len(pins) == 0 {
		return candidates
	}
	pinOrder := make(map[string]int, len(pins))
	for i, name := range pins {
		pinOrder[name] = i
	}
	pinned := make([]ScoredCandidate, 0, len(pins))
	unpinned := make([]ScoredCandidate, 0, len(candidates)-len(pins))
	for _, c := range candidates {
		if _, isPinned := pinOrder[c.Candidate.CanonicalName]; isPinned {
			pinned = append(pinned, c)
		} else {
			unpinned = append(unpinned, c)
		}
	}
	sort.SliceStable(pinned, func(i, j int) bool {
		return pinOrder[pinned[i].Candidate.CanonicalName] <
			pinOrder[pinned[j].Candidate.CanonicalName]
	})
	out := make([]ScoredCandidate, 0, len(candidates))
	out = append(out, pinned...)
	out = append(out, unpinned...)
	return out
}

// LoadedAt returns the timestamp of the last successful reload.
func (s *OverrideStore) LoadedAt() time.Time {
	return s.current().LoadedAt
}
