package dashboardapi

// session_overview_turns_align.go — cross-check SUM(session_summaries.request_count)
// against business turn rows in the same days window so the stats tab can
// surface degraded instead of silent drift.
//
// Universe note (honest boundary):
//   · summaries = session-level snapshots (first_request_at window)
//   · turns     = request rows in session_turns_hot ∪ session_turns
//     (ts window, probe markers excluded; never probe_hot)
// They are designed to track, not to be bitwise identical. Large relative
// gaps mean the KPI cannot be trusted as a turns proxy.

import (
	"context"
	"fmt"
	"math"

	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/probekind"
)

// TurnsAlignmentThresholdPct is the relative gap that flips degraded.
// Absolute floor avoids false positives on tiny windows.
const (
	TurnsAlignmentThresholdPct = 0.10
	TurnsAlignmentAbsFloor     = 100
)

// TurnsAlignment is the wire payload for KPI ↔ turns consistency.
type TurnsAlignment struct {
	SummaryRequests int64   `json:"summary_requests"`
	TurnsCount      int64   `json:"turns_count"`
	AbsDelta        int64   `json:"abs_delta"`
	Degraded        bool    `json:"degraded"`
	ThresholdPct    float64 `json:"threshold_pct"`
	Message         string  `json:"message,omitempty"`
}

func businessTurnsFilterSQL(alias string) string {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	return fmt.Sprintf(`COALESCE(%sorigin_stage, '') NOT IN (%s)
		  AND COALESCE(%stask_type, '') <> 'probe_triggered'
		  AND COALESCE(%srequest_id, '') NOT LIKE 'probe-%%'`,
		prefix, probekind.SQLStageList(), prefix, prefix)
}

func evaluateTurnsAlignment(summaryRequests, turnsCount int64) TurnsAlignment {
	delta := summaryRequests - turnsCount
	if delta < 0 {
		delta = -delta
	}
	base := summaryRequests
	if turnsCount > base {
		base = turnsCount
	}
	thresh := int64(TurnsAlignmentAbsFloor)
	if base > 0 {
		rel := int64(math.Ceil(float64(base) * TurnsAlignmentThresholdPct))
		if rel > thresh {
			thresh = rel
		}
	}
	out := TurnsAlignment{
		SummaryRequests: summaryRequests,
		TurnsCount:      turnsCount,
		AbsDelta:        delta,
		ThresholdPct:    TurnsAlignmentThresholdPct,
		Degraded:        delta > thresh,
	}
	if out.Degraded {
		out.Message = "session summary request_count and business turns are not aligned"
	}
	return out
}

func (h *SessionOverviewHandler) queryBusinessTurnsCount(ctx context.Context, params QueryParams) (int64, error) {
	where := []string{
		fmt.Sprintf("rl.ts >= NOW() - INTERVAL '%d days'", params.Days),
		businessTurnsFilterSQL("rl"),
	}
	args := []interface{}{}
	argIdx := 1
	if params.TenantID != "" {
		where = append(where, fmt.Sprintf("rl.tenant_id = $%d", argIdx))
		args = append(args, params.TenantID)
		argIdx++
	}
	_ = argIdx
	query := fmt.Sprintf(`
		SELECT COUNT(*)::bigint
		  FROM %s rl
		 WHERE %s
	`, db.SessionFamilyTurnsSourceSQL(), joinStrings(where, " AND "))
	var n int64
	if err := h.db.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
