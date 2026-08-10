package reasoncap

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGSource is the tier-1 DBSource backed by models_canonical.reasoning_caps.
//
// Schema created by sql/migrations/startup/478_model_reasoning_caps.sql.
// The JSONB payload deserialises directly into capsJSON below.
type PGSource struct {
	db *pgxpool.Pool

	// timeout bounds the lookup so a slow DB never stalls the request path.
	// Reasoning capability is an optimisation, not a correctness requirement:
	// on timeout we return (nil, err) and Resolve falls through to tier 2.
	timeout time.Duration
}

// NewPGSource constructs a Postgres-backed reasoning capability source.
func NewPGSource(db *pgxpool.Pool) *PGSource {
	return &PGSource{db: db, timeout: 300 * time.Millisecond}
}

// capsJSON mirrors Caps for JSONB (un)marshalling. Source is not stored.
type capsJSON struct {
	Supported    bool     `json:"supported"`
	Dialect      string   `json:"dialect,omitempty"`
	Efforts      []string `json:"efforts,omitempty"`
	BudgetMin    int      `json:"budget_min,omitempty"`
	BudgetMax    int      `json:"budget_max,omitempty"`
	CanDisable   bool     `json:"can_disable,omitempty"`
	Adaptive     bool     `json:"adaptive,omitempty"`
	HistoryField string   `json:"history_field,omitempty"`
}

// LookupReasoningCaps implements DBSource.
//
// Matching strategy: exact canonical_name first, then the model_aliases table
// so a provider-specific name (e.g. "claude-sonnet-4-20250514") resolves to the
// canonical row that carries the operator override.
//
// Returns (nil, nil) when no row has a reasoning_caps override — the caller
// (Resolve) then falls through to the name-pattern table.
func (s *PGSource) LookupReasoningCaps(ctx context.Context, canonicalModel string) (*Caps, error) {
	if s == nil || s.db == nil || canonicalModel == "" {
		return nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	const q = `
		SELECT mc.reasoning_caps
		FROM models_canonical mc
		WHERE mc.reasoning_caps IS NOT NULL
		  AND (
		    mc.canonical_name = $1
		    OR EXISTS (
		      SELECT 1 FROM model_aliases ma
		      WHERE ma.canonical_id = mc.id AND ma.alias = $1
		    )
		  )
		LIMIT 1`

	var raw []byte
	err := s.db.QueryRow(ctx, q, canonicalModel).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil // no override — fall through to tier 2
		}
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}

	var cj capsJSON
	if err := json.Unmarshal(raw, &cj); err != nil {
		return nil, err
	}

	return &Caps{
		Supported:    cj.Supported,
		Dialect:      Dialect(cj.Dialect),
		Efforts:      cj.Efforts,
		BudgetMin:    cj.BudgetMin,
		BudgetMax:    cj.BudgetMax,
		CanDisable:   cj.CanDisable,
		Adaptive:     cj.Adaptive,
		HistoryField: cj.HistoryField,
	}, nil
}
