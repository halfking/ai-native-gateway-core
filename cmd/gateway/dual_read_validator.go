package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DualReadDiff summarizes the divergence between V1 (public.request_logs)
// and V2 (public.session_turns) for one session during the cut-over
// observation window. Non-zero TokenDiff / CostDiff means V1 and V2
// disagree, which must be reconciled before flipping the primary read.
type DualReadDiff struct {
	SessionID string
	Compared  int     // number of V1 rows seen for the session
	TokenDiff int64   // V1 total tokens - V2 total tokens
	CostDiff  float64 // V1 total cost_usd - V2 total cost_usd
	SampleAt  time.Time
}

// DualReadDetail is the storage-plan-v2 S2 row-level reconciliation
// (plan §4 S2: dual_read_validator 对账扩展「turns vs request_logs 等值」;
// §8-C/D 的端点化形态). Compare is the aggregate view; CompareDetail adds
// the per-request_id set difference and per-field drift samples that make
// a "7 天零漂移" gate auditable:
//   - OnlyInV1: request_logs rows with no session_turns counterpart
//     (mirror loss / no-session traffic not yet covered by D4 synthetics);
//   - OnlyInV2: turns rows with no request_logs counterpart;
//   - drifted matched rows per field (tokens / cost / success /
//     credits_charged), each with up to SampleLimit request_ids.
//
// Both sides read the BASE tables (hot ∪ parent), not the compatibility
// views: post-710 request_logs_with_current_month already contains turns
// rows, so view-vs-turns would self-compare and hide V1-side drift; the
// turns view predates 707 and lacks credits_charged.
type DualReadDetail struct {
	SessionID string    `json:"session_id"`
	Tenant    string    `json:"tenant"`
	SampleAt  time.Time `json:"sampled_at"`

	V1Rows int `json:"v1_rows"`
	V2Rows int `json:"v2_rows"`

	OnlyInV1Count int      `json:"only_in_v1_count"`
	OnlyInV2Count int      `json:"only_in_v2_count"`
	OnlyInV1      []string `json:"only_in_v1,omitempty"`
	OnlyInV2      []string `json:"only_in_v2,omitempty"`

	TokenDriftCount   int               `json:"token_drift_count"`
	CostDriftCount    int               `json:"cost_drift_count"`
	SuccessDriftCount int               `json:"success_drift_count"`
	CreditsDriftCount int               `json:"credits_drift_count"`
	DriftSamples      []DualDriftSample `json:"drift_samples,omitempty"`

	// 计费等值合计（plan §8-D）：停写 gate 的硬指标。
	V1CreditsTotal int64 `json:"v1_credits_total"`
	V2CreditsTotal int64 `json:"v2_credits_total"`

	// ZeroDrift 为真 = 双侧行集相等且无字段漂移（§8-C/D 同判）。
	ZeroDrift bool `json:"zero_drift"`
}

// DualDriftSample is one drifted request with the offending fields.
type DualDriftSample struct {
	RequestID string   `json:"request_id"`
	TokenV1   *int64   `json:"token_v1,omitempty"`
	TokenV2   *int64   `json:"token_v2,omitempty"`
	CostV1    *float64 `json:"cost_v1,omitempty"`
	CostV2    *float64 `json:"cost_v2,omitempty"`
	SuccessV1 *bool    `json:"success_v1,omitempty"`
	SuccessV2 *bool    `json:"success_v2,omitempty"`
	CreditsV1 *int64   `json:"credits_v1,omitempty"`
	CreditsV2 *int64   `json:"credits_v2,omitempty"`
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

// RegisterRoutes wires the reconciliation endpoint:
//
//	GET /api/admin/sessions/{id}/dual-read?limit=10
//
// limit caps the per-side missing/drift sample arrays (1..50, default 10).
func (v *DualReadValidator) RegisterRoutes(mux *http.ServeMux, adminMw func(http.HandlerFunc) http.HandlerFunc) {
	mux.Handle("/api/admin/sessions/{id}/dual-read", adminMw(v.handleDualRead))
}

func (v *DualReadValidator) handleDualRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := r.PathValue("id")
	if sessionID == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}
	limit := 10
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &limit); err != nil || limit < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		if limit > 50 {
			limit = 50
		}
	}
	tenant := r.URL.Query().Get("tenant")

	detail, err := v.CompareDetail(r.Context(), tenant, sessionID, limit)
	if err != nil {
		http.Error(w, "dual-read compare failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(detail)
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
			FROM public.session_turns_with_current_month
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

// CompareDetail runs the row-level V1 vs V2 reconciliation for one session.
// tenant may be empty (super-admin cross-tenant sweep); session is the
// server-side session key (= gw_session_id for mirrored traffic, 'sys:…'
// for D4 synthetics).
func (v *DualReadValidator) CompareDetail(ctx context.Context, tenant, session string, sampleLimit int) (DualReadDetail, error) {
	d := DualReadDetail{
		SessionID: session,
		Tenant:    tenant,
		SampleAt:  time.Now(),
	}
	if v.db == nil {
		return d, fmt.Errorf("dual-read: nil pool")
	}
	if sampleLimit <= 0 {
		sampleLimit = 10
	}
	if sampleLimit > 50 {
		sampleLimit = 50
	}

	// Base tables both sides. v1: request_logs_hot holds the pre-promote
	// window, request_logs the promoted partitions. v2: same family shape.
	// token 表达式双侧同构（NULLIF(COALESCE(p,0)+COALESCE(c,0),0)）以便
	// 等值比较而非口径比较。
	const v1CTE = `
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.request_logs_hot
		WHERE gw_session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
		UNION ALL
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.request_logs
		WHERE gw_session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
	`
	const v2CTE = `
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.session_turns_hot
		WHERE session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
		UNION ALL
		SELECT request_id,
		       NULLIF(COALESCE(prompt_tokens, 0) + COALESCE(completion_tokens, 0), 0) AS tokens,
		       cost_usd, success, credits_charged
		FROM public.session_turns
		WHERE session_id = $2 AND ($1 = '' OR tenant_id::TEXT = $1)
	`

	// Row counts + credit totals (plan §8-D).
	err := v.db.QueryRow(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`)
		SELECT
			(SELECT count(*) FROM v1), (SELECT count(*) FROM v2),
			COALESCE((SELECT sum(credits_charged) FROM v1), 0),
			COALESCE((SELECT sum(credits_charged) FROM v2), 0)
	`, tenant, session).Scan(&d.V1Rows, &d.V2Rows, &d.V1CreditsTotal, &d.V2CreditsTotal)
	if err != nil {
		return d, fmt.Errorf("counts query: %w", err)
	}

	// Set difference both ways (plan §8-C).
	if err := v.db.QueryRow(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`)
		SELECT
		  count(*) FILTER (WHERE side = 'v1'),
		  count(*) FILTER (WHERE side = 'v2')
		FROM (
		  (
		    SELECT request_id, 'v1' AS side FROM v1 WHERE request_id IS NOT NULL
		    EXCEPT
		    SELECT request_id, 'v1' FROM v2 WHERE request_id IS NOT NULL
		  )
		  UNION ALL
		  (
		    SELECT request_id, 'v2' AS side FROM v2 WHERE request_id IS NOT NULL
		    EXCEPT
		    SELECT request_id, 'v2' FROM v1 WHERE request_id IS NOT NULL
		  )
		) d
	`, tenant, session).Scan(&d.OnlyInV1Count, &d.OnlyInV2Count); err != nil {
		return d, fmt.Errorf("set-diff query: %w", err)
	}

	// Matched-row field drift + samples (single scan).
	rows, err := v.db.Query(ctx, `
		WITH v1 AS (`+v1CTE+`), v2 AS (`+v2CTE+`),
		m AS (
		  SELECT COALESCE(v1.request_id, v2.request_id) AS request_id,
		         v1.tokens AS tok1, v2.tokens AS tok2,
		         v1.cost_usd AS cost1, v2.cost_usd AS cost2,
		         v1.success AS succ1, v2.success AS succ2,
		         v1.credits_charged AS cred1, v2.credits_charged AS cred2
		  FROM v1 FULL OUTER JOIN v2 ON v1.request_id = v2.request_id
		  WHERE v1.request_id IS NOT NULL AND v2.request_id IS NOT NULL
		)
		SELECT request_id, tok1, tok2, cost1, cost2, succ1, succ2, cred1, cred2
		FROM m
		-- 计费/用量数值按 NULL↔0 归一化比较：v1 失败行 cost/credits 为 NULL
		-- 而 turns 落 0（占位零值）不是计费事实漂移；真实数值差异仍然报告。
		WHERE COALESCE(tok1, 0)  IS DISTINCT FROM COALESCE(tok2, 0)
		   OR COALESCE(cost1, 0) IS DISTINCT FROM COALESCE(cost2, 0)
		   OR COALESCE(succ1, false) IS DISTINCT FROM COALESCE(succ2, false)
		   OR COALESCE(cred1, 0) IS DISTINCT FROM COALESCE(cred2, 0)
		LIMIT $3
	`, tenant, session, sampleLimit)
	if err != nil {
		return d, fmt.Errorf("drift query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s DualDriftSample
		if err := rows.Scan(&s.RequestID, &s.TokenV1, &s.TokenV2, &s.CostV1, &s.CostV2,
			&s.SuccessV1, &s.SuccessV2, &s.CreditsV1, &s.CreditsV2); err != nil {
			return d, fmt.Errorf("scan drift sample: %w", err)
		}
		d.DriftSamples = append(d.DriftSamples, s)
	}
	if err := rows.Err(); err != nil {
		return d, fmt.Errorf("drift samples: %w", err)
	}
	for _, s := range d.DriftSamples {
		if normI(s.TokenV1) != normI(s.TokenV2) {
			d.TokenDriftCount++
		}
		if normF(s.CostV1) != normF(s.CostV2) {
			d.CostDriftCount++
		}
		if normB(s.SuccessV1) != normB(s.SuccessV2) {
			d.SuccessDriftCount++
		}
		if normI(s.CreditsV1) != normI(s.CreditsV2) {
			d.CreditsDriftCount++
		}
	}
	// Drift counts are capped by sampleLimit; when the cap is hit, report the
	// capped value conservatively (the zero-drift gate treats >0 as fail).
	d.ZeroDrift = d.OnlyInV1Count == 0 && d.OnlyInV2Count == 0 && len(d.DriftSamples) == 0
	return d, nil
}

// NULL↔0 / NULL↔false normalizers mirroring the drift WHERE clause.
func normI(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func normF(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func normB(v *bool) bool {
	if v == nil {
		return false
	}
	return *v
}
