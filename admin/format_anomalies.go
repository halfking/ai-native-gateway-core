package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type FormatAnomalyRecord struct {
	ID              int64          `json:"id"`
	DetectedAt      time.Time      `json:"detected_at"`
	RequestID       string         `json:"request_id"`
	ProviderID      *int           `json:"provider_id,omitempty"`
	ProviderCode    *string        `json:"provider_code,omitempty"`
	ClientModel     *string        `json:"client_model,omitempty"`
	OutboundModel   *string        `json:"outbound_model,omitempty"`
	AnomalyType     string         `json:"anomaly_type"`
	Severity        string         `json:"severity"`
	UsageSource     *string        `json:"usage_source,omitempty"`
	ExpectedTokens  *int           `json:"expected_tokens,omitempty"`
	ActualTokens    *int           `json:"actual_tokens,omitempty"`
	ContentSize     *int           `json:"content_size_bytes,omitempty"`
	Structure       map[string]any `json:"response_structure,omitempty"`
	ResponseSample  *string        `json:"response_sample,omitempty"`
	Resolved        bool           `json:"resolved"`
	ResolvedAt      *time.Time     `json:"resolved_at,omitempty"`
	ResolutionNotes *string        `json:"resolution_notes,omitempty"`
	TenantID        *string        `json:"tenant_id,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type FormatAnomalySummary struct {
	Hour            time.Time `json:"hour"`
	ProviderCode    *string   `json:"provider_code,omitempty"`
	ClientModel     *string   `json:"client_model,omitempty"`
	AnomalyType     string    `json:"anomaly_type"`
	Severity        string    `json:"severity"`
	AnomalyCount    int       `json:"anomaly_count"`
	AffectedReqs    int       `json:"affected_requests"`
	AvgContentSize  *float64  `json:"avg_content_size,omitempty"`
	AvgExpTokens    *float64  `json:"avg_expected_tokens,omitempty"`
	AvgActualTokens *float64  `json:"avg_actual_tokens,omitempty"`
	ResolvedCount   int       `json:"resolved_count"`
}

func (h *Handler) handleFormatAnomalies(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	limit := queryInt(r, "limit", 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	providerCode := strings.TrimSpace(queryString(r, "provider"))
	clientModel := strings.TrimSpace(queryString(r, "model"))
	anomalyType := strings.TrimSpace(queryString(r, "anomaly_type"))
	unresolvedOnly := queryBool(r, "unresolved_only")

	where := []string{"1=1"}
	args := []any{}
	argPos := 1
	if providerCode != "" {
		where = append(where, "COALESCE(rfa.provider_code, p.code) = $"+strconv.Itoa(argPos))
		args = append(args, providerCode)
		argPos++
	}
	if clientModel != "" {
		where = append(where, "rfa.client_model = $"+strconv.Itoa(argPos))
		args = append(args, clientModel)
		argPos++
	}
	if anomalyType != "" {
		where = append(where, "rfa.anomaly_type = $"+strconv.Itoa(argPos))
		args = append(args, anomalyType)
		argPos++
	}
	if unresolvedOnly {
		where = append(where, "NOT rfa.resolved")
	}
	whereSQL := strings.Join(where, " AND ")

	countQuery := `
		SELECT COUNT(*)
		FROM response_format_anomalies rfa
		LEFT JOIN providers p ON p.id = rfa.provider_id
		WHERE ` + whereSQL
	var total int
	if err := h.db.QueryRow(r.Context(), countQuery, args...).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "count query failed")
		return
	}

	listQuery := `
		SELECT
			rfa.id,
			rfa.detected_at,
			rfa.request_id,
			rfa.provider_id,
			COALESCE(rfa.provider_code, p.code) AS provider_code,
			rfa.client_model,
			rfa.outbound_model,
			rfa.anomaly_type,
			rfa.severity,
			rfa.usage_source,
			rfa.expected_tokens,
			rfa.actual_tokens,
			rfa.content_size_bytes,
			rfa.response_structure,
			rfa.response_sample,
			rfa.resolved,
			rfa.resolved_at,
			rfa.resolution_notes,
			rfa.tenant_id,
			rfa.created_at
		FROM response_format_anomalies rfa
		LEFT JOIN providers p ON p.id = rfa.provider_id
		WHERE ` + whereSQL + `
		ORDER BY rfa.detected_at DESC
		LIMIT $` + strconv.Itoa(argPos) + ` OFFSET $` + strconv.Itoa(argPos+1)
	args = append(args, limit, offset)

	rows, err := h.db.Query(r.Context(), listQuery, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list query failed")
		return
	}
	defer rows.Close()

	items := make([]FormatAnomalyRecord, 0, limit)
	for rows.Next() {
		var item FormatAnomalyRecord
		var structureRaw []byte
		if err := rows.Scan(
			&item.ID,
			&item.DetectedAt,
			&item.RequestID,
			&item.ProviderID,
			&item.ProviderCode,
			&item.ClientModel,
			&item.OutboundModel,
			&item.AnomalyType,
			&item.Severity,
			&item.UsageSource,
			&item.ExpectedTokens,
			&item.ActualTokens,
			&item.ContentSize,
			&structureRaw,
			&item.ResponseSample,
			&item.Resolved,
			&item.ResolvedAt,
			&item.ResolutionNotes,
			&item.TenantID,
			&item.CreatedAt,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if len(structureRaw) > 0 {
			_ = json.Unmarshal(structureRaw, &item.Structure)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "rows failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"anomalies": items,
		"count":     total,
		"limit":     limit,
		"offset":    offset,
	})
}

func (h *Handler) handleFormatAnomalySummary(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	hours := queryInt(r, "hours", 24)
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT
			DATE_TRUNC('hour', detected_at) AS hour,
			provider_code,
			client_model,
			anomaly_type,
			severity,
			COUNT(*) AS anomaly_count,
			COUNT(DISTINCT request_id) AS affected_requests,
			AVG(content_size_bytes) AS avg_content_size,
			AVG(expected_tokens) AS avg_expected_tokens,
			AVG(actual_tokens) AS avg_actual_tokens,
			COUNT(*) FILTER (WHERE resolved) AS resolved_count
		FROM response_format_anomalies
		WHERE detected_at > NOW() - ($1::int * INTERVAL '1 hour')
		GROUP BY 1, 2, 3, 4, 5
		ORDER BY hour DESC, anomaly_count DESC
		LIMIT 200
	`, hours)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "summary query failed")
		return
	}
	defer rows.Close()

	summaries := make([]FormatAnomalySummary, 0)
	for rows.Next() {
		var item FormatAnomalySummary
		if err := rows.Scan(
			&item.Hour,
			&item.ProviderCode,
			&item.ClientModel,
			&item.AnomalyType,
			&item.Severity,
			&item.AnomalyCount,
			&item.AffectedReqs,
			&item.AvgContentSize,
			&item.AvgExpTokens,
			&item.AvgActualTokens,
			&item.ResolvedCount,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "summary scan failed")
			return
		}
		summaries = append(summaries, item)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "summary rows failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summaries": summaries,
		"count":     len(summaries),
		"hours":     hours,
	})
}

func (h *Handler) handleFormatAnomalySubrouter(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/format-anomalies/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[1] != "resolve" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid anomaly id")
		return
	}
	var body struct {
		ResolutionNotes string `json:"resolution_notes"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	ct, err := h.db.Exec(r.Context(), `
		UPDATE response_format_anomalies
		SET resolved = TRUE,
		    resolved_at = NOW(),
		    resolution_notes = $2
		WHERE id = $1
	`, id, body.ResolutionNotes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "anomaly not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "anomaly marked as resolved",
	})
}
