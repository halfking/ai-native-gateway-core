package admin

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type modelRoutingDiagnosticNode struct {
	CredentialID       int      `json:"credential_id"`
	ProviderID         int      `json:"provider_id"`
	ProviderName       string   `json:"provider_name"`
	CredentialLabel    string   `json:"credential_label"`
	RawModelName       string   `json:"raw_model_name"`
	Routable           bool     `json:"routable"`
	BlockReason        *string  `json:"block_reason,omitempty"`
	ManualPriority     int      `json:"manual_priority"`
	RecentSamples      int      `json:"recent_samples"`
	RecentSuccessRate  *float64 `json:"recent_success_rate,omitempty"`
	Requests24h        int      `json:"requests_24h"`
	SuccessRate24h     *float64 `json:"success_rate_24h,omitempty"`
	P95LatencyMS       *int     `json:"p95_latency_ms,omitempty"`
	P95FirstChunkMS    *int     `json:"p95_first_chunk_ms,omitempty"`
	TimeoutCount       int      `json:"timeout_count"`
	EmptyResponseCount int      `json:"empty_response_count"`
	QuotaFailureCount  int      `json:"quota_failure_count"`
	DirectOK           *bool    `json:"direct_ok,omitempty"`
	GatewayOK          *bool    `json:"gateway_ok,omitempty"`
	ProbeErrorCode     *string  `json:"probe_error_code,omitempty"`
	ProbeConflict      bool     `json:"probe_conflict"`
	LatestProbeAt      *string  `json:"latest_probe_at,omitempty"`
}

// handleModelRoutingDiagnostic exposes per-node routing and quality evidence.
// It is intentionally read-only: operators can distinguish a supplier outage
// from a stale manual/probe state before taking a recovery action.
//
// GET /api/admin/diagnostics/model-routing?model=gpt-5.6-terra
func (h *Handler) handleModelRoutingDiagnostic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeError(w, http.StatusBadRequest, "model query parameter required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	rows, err := h.db.Query(ctx, `
		WITH target AS (
			SELECT cmb.id AS binding_id, cmb.credential_id, pm.provider_id,
				p.display_name AS provider_name, c.label AS credential_label,
				pm.raw_model_name, v.is_routable, v.unavailable_reason,
				COALESCE(cmb.manual_priority, 99) AS manual_priority,
				rsr.rate AS recent_success_rate, rsr.samples AS recent_samples
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			JOIN providers p ON p.id = pm.provider_id
			JOIN credentials c ON c.id = cmb.credential_id
			JOIN v_routable_credential_models v ON v.binding_id = cmb.id
			CROSS JOIN LATERAL recent_success_rate(cmb.credential_id, pm.raw_model_name, 50) rsr
			WHERE lower(pm.raw_model_name) = lower($1)
		), request_stats AS (
			SELECT credential_id,
				COUNT(*)::int AS requests_24h,
				AVG(success::int)::float8 AS success_rate_24h,
				PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms)::int AS p95_latency_ms,
				PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY stream_first_chunk_ms)::int AS p95_first_chunk_ms,
				COUNT(*) FILTER (WHERE error_kind IN ('timeout', 'stream_timeout', 'network'))::int AS timeout_count,
				COUNT(*) FILTER (WHERE error_kind = 'empty_response')::int AS empty_response_count,
				COUNT(*) FILTER (WHERE error_kind IN ('quota', 'quota_balance', 'quota_periodic', 'quota_permanent'))::int AS quota_failure_count
			FROM request_logs
			WHERE lower(raw_model_name) = lower($1) AND ts > now() - interval '24 hours'
			GROUP BY credential_id
		)
		SELECT t.credential_id, t.provider_id, t.provider_name, t.credential_label,
			t.raw_model_name, t.is_routable, t.unavailable_reason, t.manual_priority,
			t.recent_samples, t.recent_success_rate, COALESCE(rs.requests_24h, 0),
			rs.success_rate_24h, rs.p95_latency_ms, rs.p95_first_chunk_ms,
			COALESCE(rs.timeout_count, 0), COALESCE(rs.empty_response_count, 0),
			COALESCE(rs.quota_failure_count, 0), npr.direct_ok, npr.gateway_ok,
			COALESCE(npr.direct_err_code, npr.gateway_err_code), npr.started_at
		FROM target t
		LEFT JOIN request_stats rs ON rs.credential_id = t.credential_id
		LEFT JOIN LATERAL (
			SELECT direct_ok, gateway_ok, direct_err_code, gateway_err_code, started_at
			FROM node_probe_runs
			WHERE credential_id = t.credential_id AND lower(raw_model_name) = lower(t.raw_model_name)
			ORDER BY started_at DESC LIMIT 1
		) npr ON TRUE
		ORDER BY t.manual_priority, t.credential_id`, model)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("model routing diagnostic failed: %v", err))
		return
	}
	defer rows.Close()

	nodes := make([]modelRoutingDiagnosticNode, 0)
	for rows.Next() {
		var node modelRoutingDiagnosticNode
		var latestProbeAt *time.Time
		if err := rows.Scan(
			&node.CredentialID, &node.ProviderID, &node.ProviderName, &node.CredentialLabel,
			&node.RawModelName, &node.Routable, &node.BlockReason, &node.ManualPriority,
			&node.RecentSamples, &node.RecentSuccessRate, &node.Requests24h,
			&node.SuccessRate24h, &node.P95LatencyMS, &node.P95FirstChunkMS,
			&node.TimeoutCount, &node.EmptyResponseCount, &node.QuotaFailureCount,
			&node.DirectOK, &node.GatewayOK, &node.ProbeErrorCode, &latestProbeAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("model routing diagnostic scan failed: %v", err))
			return
		}
		node.ProbeConflict = node.DirectOK != nil && *node.DirectOK && node.GatewayOK != nil && !*node.GatewayOK
		if latestProbeAt != nil {
			value := latestProbeAt.UTC().Format(time.RFC3339)
			node.LatestProbeAt = &value
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("model routing diagnostic rows failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": model, "nodes": nodes})
}
