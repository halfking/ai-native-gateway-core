package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// model_iq.go — admin HTTP handlers for the model IQ system.
//
// Routes (registered in handler.go RegisterRoutes, superAdmin-guarded):
//
//	GET  /api/admin/model-iq/catalog           — per-canonical-model standard IQ + node-average
//	GET  /api/admin/model-iq/node-latest?provider_id=&canonical_id= — per-node latest IQ
//	GET  /api/admin/model-iq/history?credential_id=&raw_model_name=&limit=
//	POST /api/admin/model-iq/trigger           — run on-demand IQ test on one node
//
// The first three read the DB directly; trigger delegates to modelQualityBackend
// (the bg.ModelQualityWorker injected via SetModelQualityBackend). When the
// backend is nil the trigger endpoint returns 503 but the read endpoints keep
// working.

// nodeIQLatestRow is one row of GET /node-latest, joining the cache table to
// provider/credential/model labels for display.
type nodeIQLatestRow struct {
	CredentialID    int64   `json:"credential_id"`
	CredentialLabel string  `json:"credential_label"`
	ProviderID      int64   `json:"provider_id"`
	ProviderName    string  `json:"provider_name"`
	RawModelName    string  `json:"raw_model_name"`
	CanonicalName   string  `json:"canonical_name"`
	OverallScore    float64 `json:"overall_score"`
	Grade           string  `json:"grade"`
	AvgScore        float64 `json:"avg_score"`
	MinScore        float64 `json:"min_score"`
	MaxScore        float64 `json:"max_score"`
	SampleCount     int     `json:"sample_count"`
	TestedAt        *string `json:"tested_at"`
}

// handleModelIQNodeLatest returns the node_iq_latest rows, optionally filtered
// by provider_id or canonical_id. Used by the provider model list to render the
// "节点智商" column and by the catalog view.
func (h *Handler) handleModelIQNodeLatest(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	where := "WHERE 1=1"
	args := []any{}
	if v := q.Get("provider_id"); v != "" {
		pid, err := strconv.ParseInt(v, 10, 64)
		if err != nil || pid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid provider_id")
			return
		}
		args = append(args, pid)
		where += fmt.Sprintf(" AND n.credential_id IN (SELECT id FROM credentials WHERE provider_id = $%d)", len(args))
	}
	if v := q.Get("canonical_id"); v != "" {
		cid, err := strconv.ParseInt(v, 10, 64)
		if err != nil || cid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid canonical_id")
			return
		}
		args = append(args, cid)
		// join via provider_models to filter by canonical
		where += fmt.Sprintf(" AND n.raw_model_name IN (SELECT raw_model_name FROM provider_models WHERE canonical_id = $%d)", len(args))
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT n.credential_id,
		       COALESCE(c.label,''),
		       COALESCE(c.provider_id,0),
		       COALESCE(p.display_name, p.code,''),
		       n.raw_model_name,
		       COALESCE(mc.canonical_name,''),
		       COALESCE(n.overall_score::float8,0),
		       COALESCE(n.grade,''),
		       COALESCE(n.avg_score::float8,0),
		       COALESCE(n.min_score::float8,0),
		       COALESCE(n.max_score::float8,0),
		       COALESCE(n.sample_count,0),
		       to_char(n.tested_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM node_iq_latest n
		LEFT JOIN credentials c ON c.id = n.credential_id
		LEFT JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_models pm
		       ON pm.provider_id = c.provider_id
		      AND lower(pm.raw_model_name) = lower(n.raw_model_name)
		LEFT JOIN models_canonical mc ON mc.id = pm.canonical_id
		`+where+`
		ORDER BY n.overall_score DESC NULLS LAST, n.credential_id`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query: %v", err))
		return
	}
	defer rows.Close()
	out := []nodeIQLatestRow{}
	for rows.Next() {
		var row nodeIQLatestRow
		var testedAt string
		if err := rows.Scan(&row.CredentialID, &row.CredentialLabel, &row.ProviderID,
			&row.ProviderName, &row.RawModelName, &row.CanonicalName,
			&row.OverallScore, &row.Grade, &row.AvgScore, &row.MinScore, &row.MaxScore,
			&row.SampleCount, &testedAt); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan: %v", err))
			return
		}
		if testedAt != "" {
			row.TestedAt = &testedAt
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// iqHistoryPoint is one row of GET /history.
type iqHistoryPoint struct {
	TestedAt     time.Time `json:"tested_at"`
	OverallScore float64   `json:"overall_score"`
	Grade        string    `json:"grade"`
	Accuracy     float64   `json:"accuracy"`
	Stability    float64   `json:"stability"`
	LatencyP95   int       `json:"latency_p95"`
	ProbeKind    string    `json:"probe_kind"`
	TriggerKind  string    `json:"trigger_kind"`
	Status       string    `json:"status"`
}

// handleModelIQHistory returns the time-series of IQ test runs for a node
// (credential_id + raw_model_name). Powers the drawer chart in the provider
// model list.
func (h *Handler) handleModelIQHistory(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	credID, err := strconv.ParseInt(q.Get("credential_id"), 10, 64)
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	rawModel := q.Get("raw_model_name")
	if rawModel == "" {
		writeError(w, http.StatusBadRequest, "raw_model_name required")
		return
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT COALESCE(tested_at, created_at), overall_score::float8, COALESCE(grade,''),
		       accuracy::float8, COALESCE(stability::float8,0), COALESCE(latency_p95,0),
		       probe_kind, trigger_kind, status
		FROM model_iq_runs
		WHERE credential_id = $1 AND lower(raw_model_name) = lower($2)
		ORDER BY tested_at DESC LIMIT $3`, credID, rawModel, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query: %v", err))
		return
	}
	defer rows.Close()
	out := []iqHistoryPoint{}
	for rows.Next() {
		var p iqHistoryPoint
		if err := rows.Scan(&p.TestedAt, &p.OverallScore, &p.Grade, &p.Accuracy,
			&p.Stability, &p.LatencyP95, &p.ProbeKind, &p.TriggerKind, &p.Status); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan: %v", err))
			return
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, out)
}

// catalogIQRow is one row of GET /catalog.
type catalogIQRow struct {
	CanonicalID   int64    `json:"canonical_id"`
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	Family        string   `json:"family"`
	StandardIQ    *float64 `json:"standard_iq"`
	StandardIQSrc string   `json:"standard_iq_source"`
	NodeAvgIQ     *float64 `json:"node_avg_iq"`
	NodeCount     int      `json:"node_count"`
	MaxNodeIQ     *float64 `json:"max_node_iq"`
	MinNodeIQ     *float64 `json:"min_node_iq"`
}

// handleModelIQCatalog returns each canonical model with its standard IQ plus
// the average/max/min node IQ measured across all nodes serving it. Used by the
// global model catalog page (standard IQ column + node-average column).
func (h *Handler) handleModelIQCatalog(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT mc.id, mc.canonical_name, COALESCE(mc.display_name,''), COALESCE(mc.family,''),
		       mc.standard_iq::float8, COALESCE(mc.standard_iq_source,''),
		       agg.node_avg::float8, agg.node_cnt, agg.node_max::float8, agg.node_min::float8
		FROM models_canonical mc
		LEFT JOIN LATERAL (
		    SELECT avg(n.overall_score) AS node_avg,
		           count(*)             AS node_cnt,
		           max(n.overall_score) AS node_max,
		           min(n.overall_score) AS node_min
		      FROM node_iq_latest n
		      JOIN credentials c ON c.id = n.credential_id
		      JOIN provider_models pm
		        ON pm.provider_id = c.provider_id
		       AND lower(pm.raw_model_name) = lower(n.raw_model_name)
		     WHERE pm.canonical_id = mc.id
		) agg ON true
		WHERE mc.status IN ('active','disabled','deprecated')
		  AND (mc.standard_iq IS NOT NULL OR agg.node_cnt > 0)
		ORDER BY COALESCE(mc.standard_iq, agg.node_avg) DESC NULLS LAST`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("query: %v", err))
		return
	}
	defer rows.Close()
	out := []catalogIQRow{}
	for rows.Next() {
		var row catalogIQRow
		var stdIQ, nodeAvg, nodeMax, nodeMin *float64
		if err := rows.Scan(&row.CanonicalID, &row.CanonicalName, &row.DisplayName, &row.Family,
			&stdIQ, &row.StandardIQSrc, &nodeAvg, &row.NodeCount, &nodeMax, &nodeMin); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("scan: %v", err))
			return
		}
		row.StandardIQ = stdIQ
		row.NodeAvgIQ = nodeAvg
		row.MaxNodeIQ = nodeMax
		row.MinNodeIQ = nodeMin
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// triggerReq is the body of POST /api/admin/model-iq/trigger.
type triggerReq struct {
	CredentialID int    `json:"credential_id"`
	RawModelName string `json:"raw_model_name"`
}

// handleModelIQTrigger runs a synchronous IQ test on one node via the
// model-quality worker and returns the resulting score. Costs real tokens;
// callers are expected to gate frequency on the client side.
func (h *Handler) handleModelIQTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if h.modelQualityBackend == nil {
		writeError(w, http.StatusServiceUnavailable, "model-quality worker not configured")
		return
	}
	var req triggerReq
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.CredentialID <= 0 || req.RawModelName == "" {
		writeError(w, http.StatusBadRequest, "credential_id and raw_model_name required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	score, err := h.modelQualityBackend.TestSingleNode(ctx, req.CredentialID, req.RawModelName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("test failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, score)
}
