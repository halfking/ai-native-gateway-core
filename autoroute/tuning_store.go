package autoroute

// tuning_store.go — runtime-tunable parameter store for the auto-route system.
//
// TuningStore holds a snapshot of all overridable parameters (keyword lists,
// thresholds, profile weights) loaded from the tuning_params DB table.
//
// Design goals:
//
//  1. Zero hot-path overhead: the Classify() and Score() functions read
//     parameters via an atomic pointer load — no mutex, no allocation.
//  2. Graceful degradation: if the DB is down or the table is empty,
//     the store returns compiled defaults (DefaultKeywords(), etc.).
//  3. Aligned refresh cadence: the background worker refreshes every
//     5 minutes (same as auto_index_refresher), so parameter changes
//     propagate within 5 minutes without a restart.
//  4. Admin instant-apply: the admin API can call Reload() to push
//     changes immediately after approving a tuning proposal.
//
// Lifecycle:
//
//	store := NewTuningStore(pool)
//	store.Reload(ctx)          // load at startup (best-effort)
//	classifier := NewHeuristicClassifier(
//	    DefaultHeuristicThresholds(),
//	    store.Keywords(),       // returns current snapshot
//	)
//	// ... every 5 min, bg worker calls store.Reload(ctx)

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tuningSnapshot is the immutable point-in-time view of all tuning params.
// Once created, it is never mutated — readers grab the whole struct via
// an atomic pointer load.
type tuningSnapshot struct {
	// Keywords holds the current keyword lists for the 3 keyword channels.
	// Falls back to DefaultKeywords() when no DB override exists.
	Keywords KeywordSet

	// LLMConfidenceThreshold overrides the heuristic's confidence threshold
	// for triggering the LLM fallback classifier. Default: 0.7.
	LLMConfidenceThreshold float64

	// LongContextTokens overrides the token-count threshold for TaskLongContext.
	// Default: 50000.
	LongContextTokens int

	// Weights holds the profile weight matrix overrides. Missing profiles
	// fall back to DefaultProfileWeights().
	Weights map[Profile]ProfileWeights

	// LoadedAt is when this snapshot was read from the DB (for staleness
	// logging and admin metrics).
	LoadedAt time.Time

	// SourceCount tracks how many params came from each source
	// (default/manual/feedback) for observability.
	SourceCount map[string]int
}

// TuningStore provides atomic-snapshot access to runtime tuning parameters.
type TuningStore struct {
	pool *pgxpool.Pool

	// snapshot is an atomic.Pointer[tuningSnapshot] for lock-free reads.
	//nil before first Reload(); readers must handle nil by using defaults.
	snapshot atomic.Pointer[tuningSnapshot]
}

// NewTuningStore constructs an empty store. Call Reload() to populate.
// pool may be nil in tests (defaults are always returned).
func NewTuningStore(pool *pgxpool.Pool) *TuningStore {
	return &TuningStore{pool: pool}
}

// current returns the active snapshot, or a default-constructed one if
// Reload() has never succeeded. Never returns nil.
func (s *TuningStore) current() *tuningSnapshot {
	snap := s.snapshot.Load()
	if snap == nil {
		return defaultSnapshot()
	}
	return snap
}

// defaultSnapshot builds a snapshot from compiled Go defaults (no DB read).
func defaultSnapshot() *tuningSnapshot {
	dk := DefaultKeywords()
	return &tuningSnapshot{
		Keywords:               dk,
		LLMConfidenceThreshold: 0.7,
		LongContextTokens:      50_000,
		Weights:                DefaultProfileWeights(),
		LoadedAt:               time.Now(),
		SourceCount:            map[string]int{"default": 1},
	}
}

// Keywords returns the current keyword set (snapshot copy).
// Safe for concurrent use; the returned struct is never mutated.
func (s *TuningStore) Keywords() KeywordSet {
	return s.current().Keywords
}

// LLMConfidenceThreshold returns the current LLM-fallback threshold.
func (s *TuningStore) LLMConfidenceThreshold() float64 {
	return s.current().LLMConfidenceThreshold
}

// LongContextTokens returns the current long-context token threshold.
func (s *TuningStore) LongContextTokens() int {
	return s.current().LongContextTokens
}

// WeightsFor returns the weight matrix for the given profile, falling
// back to DefaultProfileWeights() when no override exists.
func (s *TuningStore) WeightsFor(p Profile) ProfileWeights {
	snap := s.current()
	if w, ok := snap.Weights[p]; ok {
		return w
	}
	return DefaultProfileWeights()[p]
}

// LoadedAt returns the timestamp of the last successful reload.
func (s *TuningStore) LoadedAt() time.Time {
	return s.current().LoadedAt
}

// SourceCount returns a copy of the per-source parameter counts.
func (s *TuningStore) SourceCount() map[string]int {
	src := s.current().SourceCount
	out := make(map[string]int, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// Reload fetches all enabled tuning_params rows from the DB and atomically
// swaps the snapshot. On error, the previous snapshot is retained
// (stale-while-error).
//
// Safe to call concurrently — the atomic swap is the only write.
func (s *TuningStore) Reload(ctx context.Context) error {
	if s.pool == nil {
		// No DB configured → use defaults forever (test mode).
		if s.snapshot.Load() == nil {
			s.snapshot.Store(defaultSnapshot())
		}
		return nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT key, value, source
		FROM tuning_params
		WHERE enabled = TRUE
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	snap := &tuningSnapshot{
		Keywords:               DefaultKeywords(), // start from defaults, override per-key
		LLMConfidenceThreshold: 0.7,
		LongContextTokens:      50_000,
		Weights:                DefaultProfileWeights(),
		LoadedAt:               time.Now(),
		SourceCount:            make(map[string]int),
	}

	for rows.Next() {
		var key, source string
		var value []byte
		if err := rows.Scan(&key, &value, &source); err != nil {
			slog.Warn("tuning_store: scan failed", "error", err)
			continue
		}
		snap.SourceCount[source]++

		if err := applyTuningParam(snap, key, value); err != nil {
			slog.Warn("tuning_store: apply failed",
				"key", key, "error", err)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	s.snapshot.Store(snap)
	slog.Info("tuning_store reloaded",
		"params", len(snap.SourceCount),
		"sources", snap.SourceCount,
	)
	return nil
}

// applyTuningParam dispatches a single key→value into the snapshot.
func applyTuningParam(snap *tuningSnapshot, key string, value json.RawMessage) error {
	switch key {
	case "keywords.reasoning":
		var kw []string
		if err := json.Unmarshal(value, &kw); err != nil {
			return err
		}
		snap.Keywords.Reasoning = kw
	case "keywords.code":
		var kw []string
		if err := json.Unmarshal(value, &kw); err != nil {
			return err
		}
		snap.Keywords.Code = kw
	case "keywords.creative":
		var kw []string
		if err := json.Unmarshal(value, &kw); err != nil {
			return err
		}
		snap.Keywords.Creative = kw
	case "thresholds.llm_confidence":
		var v float64
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		snap.LLMConfidenceThreshold = v
	case "thresholds.long_context_tokens":
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return err
		}
		snap.LongContextTokens = v
	case "weights.smart":
		w, err := unmarshalWeights(value)
		if err != nil {
			return err
		}
		snap.Weights[ProfileSmart] = w
	case "weights.speed_first":
		w, err := unmarshalWeights(value)
		if err != nil {
			return err
		}
		snap.Weights[ProfileSpeedFirst] = w
	case "weights.cost_first":
		w, err := unmarshalWeights(value)
		if err != nil {
			return err
		}
		snap.Weights[ProfileCostFirst] = w
	default:
		slog.Debug("tuning_store: unknown key, ignoring", "key", key)
	}
	return nil
}

// unmarshalWeights parses a JSON object into ProfileWeights.
func unmarshalWeights(value json.RawMessage) (ProfileWeights, error) {
	var raw struct {
		Price      float64 `json:"price"`
		Speed      float64 `json:"speed"`
		Stability  float64 `json:"stability"`
		Match      float64 `json:"match"`
		Pressure   float64 `json:"pressure"`
		ContextFit float64 `json:"context_fit"`
	}
	if err := json.Unmarshal(value, &raw); err != nil {
		return ProfileWeights{}, err
	}
	return ProfileWeights{
		Price:      raw.Price,
		Speed:      raw.Speed,
		Stability:  raw.Stability,
		Match:      raw.Match,
		Pressure:   raw.Pressure,
		ContextFit: raw.ContextFit,
	}, nil
}

// SetForKeywords allows tests and admin API to inject a keyword set
// without touching the DB. Not for production hot-path use.
//
// Deep-copies the Weights map so the new snapshot shares no mutable state
// with the previous one — even though snapshots are documented as
// immutable, the deep copy is a defensive guard against future mutations.
func (s *TuningStore) SetForKeywords(kw KeywordSet) {
	snap := s.current()

	// Deep-copy the Weights map to prevent shared mutable state.
	wCopy := make(map[Profile]ProfileWeights, len(snap.Weights))
	for k, v := range snap.Weights {
		wCopy[k] = v
	}

	newSnap := &tuningSnapshot{
		Keywords:               kw,
		LLMConfidenceThreshold: snap.LLMConfidenceThreshold,
		LongContextTokens:      snap.LongContextTokens,
		Weights:                wCopy,
		LoadedAt:               time.Now(),
		SourceCount:            map[string]int{"manual": 1},
	}
	s.snapshot.Store(newSnap)
}
