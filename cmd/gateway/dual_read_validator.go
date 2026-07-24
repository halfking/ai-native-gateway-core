package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DualReadDiff summarizes the divergence between V1 (public.request_logs)
// and V2 (gateway.session_turns) for one session during the cut-over
// observation window. Non-zero TokenDiff / CostDiff means V1 and V2
// disagree, which must be reconciled before flipping the primary read.
type DualReadDiff struct {
	SessionID string
	Compared  int       // number of V1 rows seen for the session
	TokenDiff int64     // V1 total tokens - V2 total tokens
	CostDiff  float64   // V1 total cost_usd - V2 total cost_usd
	SampleAt  time.Time
}

// DualReadValidator compares V1 and V2 read paths for a session.
//
// It is safe to construct without a DB pool (Compare returns a "nil pool"
// error); this lets the admin handler wire it up unconditionally.
type DualReadValidator struct {
	db *pgxpool.Pool
}

// NewDualReadValidator returns a validator backed by the given pool.
func NewDualReadValidator(db *pgxpool.Pool) *DualReadValidator {
	return &DualReadValidator{db: db}
}

// Compare runs the V1 vs V2 diff for (tenant, session).
// lastN bounds how many V1 rows we look at; 0 / negative defaults to 10.
func (v *DualReadValidator) Compare(ctx context.Context, tenant, session string, lastN int) (DualReadDiff, error) {
	d := DualReadDiff{SessionID: session, SampleAt: time.Now()}
	if v.db == nil {
		return d, fmt.Errorf("dual-read: nil pool")
	}
	if lastN <= 0 {
		lastN = 10
	}

	var count int
	if err := v.db.QueryRow(ctx, `
		SELECT count(*) FROM public.request_logs
		WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
	`, tenant, session).Scan(&count); err != nil {
		return d, fmt.Errorf("count v1: %w", err)
	}
	d.Compared = count

	err := v.db.QueryRow(ctx, `
		WITH v1 AS (
			SELECT COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
			       COALESCE(cost_usd, 0) AS cost
			FROM public.request_logs
			WHERE tenant_id::TEXT=$1 AND gw_session_id=$2
		),
		v2 AS (
			SELECT COALESCE(prompt_tokens + completion_tokens, 0) AS total_tokens,
			       COALESCE(cost_usd, 0) AS cost
			FROM gateway.session_turns
			WHERE tenant_id=$1 AND session_id=$2
		)
		SELECT
			COALESCE((SELECT sum(total_tokens) FROM v1) - (SELECT sum(total_tokens) FROM v2), 0),
			COALESCE((SELECT sum(cost) FROM v1) - (SELECT sum(cost) FROM v2), 0)
	`, tenant, session).Scan(&d.TokenDiff, &d.CostDiff)
	if err != nil {
		return d, fmt.Errorf("diff query: %w", err)
	}
	return d, nil
}