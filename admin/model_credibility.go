package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/modelcredibility"
)

// model_credibility.go — admin HTTP handlers for the model credibility
// (anti-downgrade / anti-swap) system.
//
// Routes (registered in handler.go RegisterRoutes, superAdmin-guarded):
//
//	GET  /api/admin/model-credibility/list?provider_id=&credential_id=&canonical_id=&verdict=&limit=
//	GET  /api/admin/model-credibility/catalog
//	GET  /api/admin/model-credibility/detail?credential_id=&raw_model_name=
//	GET  /api/admin/model-credibility/history?credential_id=&raw_model_name=&limit=
//	POST /api/admin/model-credibility/trigger   {credential_id, raw_model_name}
//	POST /api/admin/model-credibility/scan      {}
//
// The read endpoints query the DB directly and therefore keep working even
// when the worker is not running; only trigger/scan need the worker.
//
// ★ The four sub-scores are exposed separately on purpose: an operator must
// be able to see WHICH measurement produced the verdict, otherwise a single
// opaque number is indistinguishable from a guess.

func (h *Handler) credibilityStore() *modelcredibility.Store {
	return modelcredibility.NewStore(h.db)
}

// handleModelCredibilityList returns the current verdict rows.
func (h *Handler) handleModelCredibilityList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	f := modelcredibility.VerdictFilter{Limit: 200}
	if v := q.Get("provider_id"); v != "" {
		pid, err := strconv.ParseInt(v, 10, 64)
		if err != nil || pid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid provider_id")
			return
		}
		f.ProviderID = pid
	}
	if v := q.Get("credential_id"); v != "" {
		cid, err := strconv.ParseInt(v, 10, 64)
		if err != nil || cid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid credential_id")
			return
		}
		f.CredentialID = cid
	}
	if v := q.Get("canonical_id"); v != "" {
		cid, err := strconv.ParseInt(v, 10, 64)
		if err != nil || cid <= 0 {
			writeError(w, http.StatusBadRequest, "invalid canonical_id")
			return
		}
		f.CanonicalID = cid
	}
	if v := q.Get("verdict"); v != "" {
		if !modelcredibility.ValidVerdict(v) {
			writeError(w, http.StatusBadRequest,
				"verdict must be one of verified/suspected/diluted/unknown")
			return
		}
		f.Verdict = v
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		f.Limit = n
	}
	rows, err := h.credibilityStore().ListVerdicts(r.Context(), f)
	if err != nil {
		writeInternalErr(w, "query", err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// credibilityCatalogRow is one row of GET /catalog.
type credibilityCatalogRow struct {
	CanonicalID      int64   `json:"canonical_id"`
	CanonicalName    string  `json:"canonical_name"`
	StandardIQ       float64 `json:"standard_iq"`
	NodeCount        int     `json:"node_count"`
	VerifiedCount    int     `json:"verified_count"`
	SuspectedCount   int     `json:"suspected_count"`
	DilutedCount     int     `json:"diluted_count"`
	UnknownCount     int     `json:"unknown_count"`
	MinCredibility   *float64 `json:"min_credibility"`
	AvgCredibility   *float64 `json:"avg_credibility"`
	ProviderCount    int     `json:"provider_count"`
}

// handleModelCredibilityCatalog returns a per-canonical-model rollup: how many
// nodes were judged verified / suspected / diluted / unknown, plus the
// worst and average credibility score.
//
// ★ The counts per verdict matter more than the average: a model served by
// 5 providers where 1 is diluted must NOT look average-healthy.
func (h *Handler) handleModelCredibilityCatalog(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	rows, err := h.db.Query(r.Context(), `
		SELECT COALESCE(c.canonical_id, 0),
		       COALESCE(c.canonical_name, c.raw_model_name, ''),
		       COALESCE(mc.standard_iq, 0)::float8,
		       count(*),
		       count(*) FILTER (WHERE c.verdict = 'verified'),
		       count(*) FILTER (WHERE c.verdict = 'suspected'),
		       count(*) FILTER (WHERE c.verdict = 'diluted'),
		       count(*) FILTER (WHERE c.verdict = 'unknown'),
		       min(c.credibility_score)::float8,
		       avg(c.credibility_score)::float8,
		       count(DISTINCT c.provider_id)
		FROM provider_model_credibility c
		LEFT JOIN models_canonical mc ON mc.id = c.canonical_id
		WHERE c.tenant_id = 'default'
		GROUP BY c.canonical_id, c.canonical_name, c.raw_model_name, mc.standard_iq
		ORDER BY count(*) FILTER (WHERE c.verdict IN ('diluted','suspected')) DESC,
		         COALESCE(min(c.credibility_score), 0) ASC`)
	if err != nil {
		writeInternalErr(w, "query", err)
		return
	}
	defer rows.Close()
	out := []credibilityCatalogRow{}
	for rows.Next() {
		var row credibilityCatalogRow
		if err := rows.Scan(&row.CanonicalID, &row.CanonicalName, &row.StandardIQ,
			&row.NodeCount, &row.VerifiedCount, &row.SuspectedCount,
			&row.DilutedCount, &row.UnknownCount,
			&row.MinCredibility, &row.AvgCredibility, &row.ProviderCount); err != nil {
			writeInternalErr(w, "scan", err)
			return
		}
		out = append(out, row)
	}
	if writeAggRowsErr(w, "modelCredibility.catalog", rows.Err()) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleModelCredibilityDetail returns the current verdict of one node plus
// the evidence blob.
func (h *Handler) handleModelCredibilityDetail(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	credentialID, err := strconv.ParseInt(q.Get("credential_id"), 10, 64)
	if err != nil || credentialID <= 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	rawModel := q.Get("raw_model_name")
	if rawModel == "" {
		writeError(w, http.StatusBadRequest, "raw_model_name required")
		return
	}
		var (
			credibility, identity, capability, consistency, reference *float64
			verdict, status, reportedModel, fingerprint               string
			measuredDims                                              int
			evidence, anomalies                                       []byte
			measuredAt                                                *string
		)
		err = h.db.QueryRow(r.Context(), `
			SELECT credibility_score::float8,
			       identity_score::float8,
			       capability_score::float8,
			       consistency_score::float8,
			       reference_score::float8,
		       COALESCE(verdict,''), COALESCE(status,''),
		       COALESCE(reported_model,''), COALESCE(system_fingerprint,''),
		       COALESCE(measured_dimensions,0),
		       COALESCE(evidence,'{}'::jsonb), COALESCE(anomalies,'[]'::jsonb),
		       to_char(measured_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM provider_model_credibility
		WHERE tenant_id = 'default'
		  AND credential_id = $1
		  AND lower(raw_model_name) = lower($2)`,
		credentialID, rawModel).
		Scan(&credibility, &identity, &capability, &consistency, &reference,
			&verdict, &status, &reportedModel, &fingerprint,
			&measuredDims, &evidence, &anomalies, &measuredAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "no credibility verdict for this node")
		return
	}
	out := map[string]any{
		"credential_id":     credentialID,
		"raw_model_name":   rawModel,
		"credibility_score": credibility,
		"identity_score":    identity,
		"capability_score":  capability,
		"consistency_score": consistency,
		"reference_score":   reference,
		"verdict":           verdict,
		"status":            status,
		"reported_model":    reportedModel,
		"system_fingerprint": fingerprint,
		"measured_dimensions": measuredDims,
		"evidence":          jsonRaw(evidence),
		"anomalies":         jsonRaw(anomalies),
	}
	if measuredAt != nil {
		out["measured_at"] = *measuredAt
	}
	writeJSON(w, http.StatusOK, out)
}

// handleModelCredibilityHistory returns the verdict time-series of one node.
func (h *Handler) handleModelCredibilityHistory(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	q := r.URL.Query()
	credentialID, err := strconv.ParseInt(q.Get("credential_id"), 10, 64)
	if err != nil || credentialID <= 0 {
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
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	runs, err := h.credibilityStore().ListRuns(r.Context(), credentialID, rawModel, limit)
	if err != nil {
		writeInternalErr(w, "query", err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

// modelCredibilityTriggerReq is the body of POST /trigger.
type modelCredibilityTriggerReq struct {
	CredentialID int64  `json:"credential_id"`
	RawModelName string `json:"raw_model_name"`
}

// handleModelCredibilityTrigger runs a synchronous credibility scan on one
// node. Costs one small upstream request.
func (h *Handler) handleModelCredibilityTrigger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if h.modelCredibilityBackend == nil {
		writeError(w, http.StatusServiceUnavailable, "model-credibility worker not configured")
		return
	}
	var req modelCredibilityTriggerReq
	if err := readJSONRequired(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid body: %v", err))
		return
	}
	if req.CredentialID <= 0 || req.RawModelName == "" {
		writeError(w, http.StatusBadRequest, "credential_id and raw_model_name required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	res, err := h.modelCredibilityBackend.ScanNode(ctx, req.CredentialID, req.RawModelName)
	if err != nil {
		writeInternalErr(w, "scan failed", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleModelCredibilityScan runs a full scan over all active nodes.
//
// ★ Synchronous and serial on purpose: the scan issues one real upstream
// request per node, and a burst of parallel probes is both expensive and
// likely to trip upstream rate limits (which would poison the results).
func (h *Handler) handleModelCredibilityScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	if h.modelCredibilityBackend == nil {
		writeError(w, http.StatusServiceUnavailable, "model-credibility worker not configured")
		return
	}
	// A full scan may take a while (one request per node, serial).
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	scanned, err := h.modelCredibilityBackend.ScanAll(ctx)
	if err != nil {
		writeInternalErr(w, "scan failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scanned": scanned})
}

// jsonRaw returns the raw JSON bytes as a decoded value so the response
// carries real JSON objects/arrays rather than escaped strings.
func jsonRaw(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
