package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ModelIntegrityRecord is the JSON shape returned by the events
// endpoint and accepted by the resolve endpoint. Mirrors the
// model_integrity_events table.
type ModelIntegrityRecord struct {
	ID              int64      `json:"id"`
	DetectedAt      time.Time  `json:"detected_at"`
	RequestID       *string    `json:"request_id,omitempty"`
	ProviderID      *int       `json:"provider_id,omitempty"`
	ProviderCode    *string    `json:"provider_code,omitempty"`
	CredentialID    *int       `json:"credential_id,omitempty"`
	ClientModel     *string    `json:"client_model,omitempty"`
	OutboundModel   *string    `json:"outbound_model,omitempty"`
	RawModel        *string    `json:"raw_model_name,omitempty"`
	AnomalyType     string     `json:"anomaly_type"`
	Severity        string     `json:"severity"`
	ExpectedValue   *string    `json:"expected_value,omitempty"`
	ActualValue     *string    `json:"actual_value,omitempty"`
	Sample          *string    `json:"sample,omitempty"`
	Context         any        `json:"context,omitempty"`
	Resolved        bool       `json:"resolved"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	ResolutionNotes *string    `json:"resolution_notes,omitempty"`
	TenantID        *string    `json:"tenant_id,omitempty"`
}

// ModelIntegritySummary is the rollup by hour × (provider, model,
// anomaly_type, severity). Mirrors the SQL used by
// /api/admin/format-anomaly-summary so the operator can switch
// between the two dashboards with the same mental model.
type ModelIntegritySummary struct {
	Hour          time.Time `json:"hour"`
	ProviderCode  *string   `json:"provider_code,omitempty"`
	ClientModel   *string   `json:"client_model,omitempty"`
	AnomalyType   string    `json:"anomaly_type"`
	Severity      string    `json:"severity"`
	AnomalyCount  int       `json:"anomaly_count"`
	AffectedReqs  int       `json:"affected_requests"`
	ResolvedCount int       `json:"resolved_count"`
}

// handleModelIntegrity is the /api/admin/model-integrity dispatcher.
// It owns the prefix and routes to summary, events, fingerprint-drift,
// and the events/{id}/resolve subrouter.
func (h *Handler) handleModelIntegrity(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/admin/model-integrity")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[0] {
	case "summary":
		h.handleModelIntegritySummary(w, r)
	case "events":
		if len(parts) == 1 {
			h.handleModelIntegrityList(w, r)
			return
		}
		if len(parts) == 3 && parts[2] == "resolve" {
			h.handleModelIntegrityResolve(w, r, parts[1])
			return
		}
		http.NotFound(w, r)
	case "fingerprint-drift":
		h.handleModelIntegrityFingerprintDrift(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleModelIntegrityList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	limit := queryInt(r, "limit", 100)
	if limit <= 0 {
		limit = 100
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
	severity := strings.TrimSpace(queryString(r, "severity"))
	unresolvedOnly := queryBool(r, "unresolved_only")

	where := []string{"1=1"}
	args := []any{}
	argPos := 1
	if providerCode != "" {
		where = append(where, "mie.provider_code = $"+strconv.Itoa(argPos))
		args = append(args, providerCode)
		argPos++
	}
	if clientModel != "" {
		where = append(where, "mie.client_model = $"+strconv.Itoa(argPos))
		args = append(args, clientModel)
		argPos++
	}
	if anomalyType != "" {
		where = append(where, "mie.anomaly_type = $"+strconv.Itoa(argPos))
		args = append(args, anomalyType)
		argPos++
	}
	if severity != "" {
		where = append(where, "mie.severity = $"+strconv.Itoa(argPos))
		args = append(args, severity)
		argPos++
	}
	if unresolvedOnly {
		where = append(where, "NOT mie.resolved")
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	countQuery := `SELECT COUNT(*) FROM model_integrity_events mie WHERE ` + whereSQL
	if err := withAllTenantReadOnlyTx(r.Context(), h.db, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), countQuery, args...).Scan(&total)
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "count query failed")
		return
	}

	listQuery := `
		SELECT id, ts, request_id, provider_id, provider_code, credential_id,
		       client_model, outbound_model, raw_model_name,
		       anomaly_type, severity, expected_value, actual_value,
		       sample, context, resolved, resolved_at, resolution_notes, tenant_id
		FROM model_integrity_events mie
		WHERE ` + whereSQL + `
		ORDER BY ts DESC
		LIMIT $` + strconv.Itoa(argPos) + ` OFFSET $` + strconv.Itoa(argPos+1)
	args = append(args, limit, offset)

	items := make([]ModelIntegrityRecord, 0, limit)
	if err := withAllTenantReadOnlyTx(r.Context(), h.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), listQuery, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ModelIntegrityRecord
			var ctxRaw []byte
			if err := rows.Scan(
				&item.ID,
				&item.DetectedAt,
				&item.RequestID,
				&item.ProviderID,
				&item.ProviderCode,
				&item.CredentialID,
				&item.ClientModel,
				&item.OutboundModel,
				&item.RawModel,
				&item.AnomalyType,
				&item.Severity,
				&item.ExpectedValue,
				&item.ActualValue,
				&item.Sample,
				&ctxRaw,
				&item.Resolved,
				&item.ResolvedAt,
				&item.ResolutionNotes,
				&item.TenantID,
			); err != nil {
				return err
			}
			if len(ctxRaw) > 0 {
				_ = json.Unmarshal(ctxRaw, &item.Context)
			}
			items = append(items, item)
		}
		return rows.Err()
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "list query failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": items,
		"count":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *Handler) handleModelIntegritySummary(w http.ResponseWriter, r *http.Request) {
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
	summaries := make([]ModelIntegritySummary, 0)
	if err := withAllTenantReadOnlyTx(r.Context(), h.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT
				DATE_TRUNC('hour', ts) AS hour,
				provider_code,
				client_model,
				anomaly_type,
				severity,
				COUNT(*) AS anomaly_count,
				COUNT(DISTINCT request_id) AS affected_requests,
				COUNT(*) FILTER (WHERE resolved) AS resolved_count
			FROM model_integrity_events
			WHERE ts > NOW() - ($1::int * INTERVAL '1 hour')
			GROUP BY 1, 2, 3, 4, 5
			ORDER BY hour DESC, anomaly_count DESC
			LIMIT 200
		`, hours)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ModelIntegritySummary
			if err := rows.Scan(
				&item.Hour,
				&item.ProviderCode,
				&item.ClientModel,
				&item.AnomalyType,
				&item.Severity,
				&item.AnomalyCount,
				&item.AffectedReqs,
				&item.ResolvedCount,
			); err != nil {
				return err
			}
			summaries = append(summaries, item)
		}
		return rows.Err()
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "summary query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"summaries": summaries,
		"count":     len(summaries),
		"hours":     hours,
	})
}

func (h *Handler) handleModelIntegrityResolve(w http.ResponseWriter, r *http.Request, idStr string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid integrity id")
		return
	}
	var body struct {
		ResolutionNotes string `json:"resolution_notes"`
	}
	_ = readJSON(r, &body)
	var ct pgconn.CommandTag
	if err := withAllTenantTx(r.Context(), h.db, func(tx pgx.Tx) error {
		var execErr error
		ct, execErr = tx.Exec(r.Context(), `
			UPDATE model_integrity_events
			SET resolved = TRUE,
			    resolved_at = NOW(),
			    resolution_notes = $2
			WHERE id = $1
		`, id, body.ResolutionNotes)
		return execErr
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed")
		return
	}
	if ct.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "integrity event not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "integrity event marked as resolved",
	})
}

// handleModelIntegrityFingerprintDrift (2026-07-28) returns the
// last 7 days of (credential, model) fingerprint drift events.
// This is the dedicated dashboard surface for the
// bg/integrity_fingerprint_drift worker.
func (h *Handler) handleModelIntegrityFingerprintDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	days := queryInt(r, "days", 7)
	if days <= 0 || days > 30 {
		days = 7
	}
	limit := queryInt(r, "limit", 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	items := make([]ModelIntegrityRecord, 0)
	if err := withAllTenantReadOnlyTx(r.Context(), h.db, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, ts, request_id, provider_id, provider_code, credential_id,
			       client_model, outbound_model, raw_model_name,
			       anomaly_type, severity, expected_value, actual_value,
			       sample, context, resolved, resolved_at, resolution_notes, tenant_id
			FROM model_integrity_events
			WHERE anomaly_type = 'fingerprint_drift'
			  AND ts > NOW() - ($1::int * INTERVAL '1 day')
			ORDER BY ts DESC
			LIMIT $2
		`, days, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ModelIntegrityRecord
			var ctxRaw []byte
			if err := rows.Scan(
				&item.ID,
				&item.DetectedAt,
				&item.RequestID,
				&item.ProviderID,
				&item.ProviderCode,
				&item.CredentialID,
				&item.ClientModel,
				&item.OutboundModel,
				&item.RawModel,
				&item.AnomalyType,
				&item.Severity,
				&item.ExpectedValue,
				&item.ActualValue,
				&item.Sample,
				&ctxRaw,
				&item.Resolved,
				&item.ResolvedAt,
				&item.ResolutionNotes,
				&item.TenantID,
			); err != nil {
				return err
			}
			if len(ctxRaw) > 0 {
				_ = json.Unmarshal(ctxRaw, &item.Context)
			}
			items = append(items, item)
		}
		return rows.Err()
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "drift query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events": items,
		"count":  len(items),
		"days":   days,
	})
}
