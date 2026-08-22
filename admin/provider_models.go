package admin

// provider_models.go — extracted from providers.go (2026-06-21 audit §3
// single-file-bloat remediation, fifth cut). Owns the read side of a
// provider's model catalog plus the admin logs endpoint.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/modelcatalog"
)

func (h *Handler) getProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, offerListSQL+`
		WHERE c.provider_id = $1
		ORDER BY mo.raw_model_name
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	offers := make([]modelOfferDTO, 0)
	for rows.Next() {
		o, scanErr := scanModelOfferDTO(rows.Scan)
		if scanErr != nil {
			slog.Warn("getProviderModels scan failed", "error", scanErr)
			continue
		}
		offers = append(offers, o)
	}
	writeJSON(w, http.StatusOK, offers)
}

// clearProviderModels hard-deletes all credential_model_bindings for a
// provider so the list is empty and the operator can re-fetch from the vendor.
// We bypass DELETE on model_offers — that view's trigger only soft-deletes
// (available=false, reason=deleted), which leaves a full disabled list.
func (h *Handler) clearProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var exists int
	if err := h.db.QueryRow(ctx, `
		SELECT 1 FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&exists); err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	deleted, err := modelcatalog.ClearProviderBindings(ctx, h.db, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clear failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message": "ok",
		"deleted": int(deleted),
	})
	// 2026-06-19 audit: clear-all wipes the model rows this endpoint
	// aggregates — invalidate the cache so the next page render
	// re-reads the DB instead of returning a stale list.
	InvalidateAvailableModelsCache()
}

func classifyAvailability(available bool, reason *string) string {
	if reason == nil {
		if available {
			return "auto"
		}
		return "never"
	}
	r := *reason
	if strings.HasPrefix(r, "auto_") {
		return "auto"
	}
	if strings.HasPrefix(r, "manual") {
		return "manual"
	}
	if available {
		return "auto"
	}
	return "never"
}

func (h *Handler) queryProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		Q             *string  `json:"q"`
		Available     *bool    `json:"available"`
		UnavailReason *string  `json:"unavailable_reason"`
		CredentialID  *int     `json:"credential_id"`
		MinSuccess    *float64 `json:"min_success_rate"`
		MaxP95Latency *int     `json:"max_p95_latency"`
		Page          int      `json:"page"`
		PageSize      int      `json:"page_size"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 {
		req.PageSize = 50
	}

	conditions := []string{"c.provider_id = $1"}
	args := []any{providerID}
	argIdx := 2

	if req.Q != nil && *req.Q != "" {
		conditions = append(conditions, fmt.Sprintf("(mo.raw_model_name ILIKE $%d OR mo.standardized_name ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+*req.Q+"%")
		argIdx++
	}
	if req.Available != nil {
		conditions = append(conditions, fmt.Sprintf("mo.available = $%d", argIdx))
		args = append(args, *req.Available)
		argIdx++
	}
	if req.UnavailReason != nil && *req.UnavailReason != "" {
		conditions = append(conditions, fmt.Sprintf("mo.unavailable_reason = $%d", argIdx))
		args = append(args, *req.UnavailReason)
		argIdx++
	}
	if req.CredentialID != nil {
		conditions = append(conditions, fmt.Sprintf("mo.credential_id = $%d", argIdx))
		args = append(args, *req.CredentialID)
		argIdx++
	}
	if req.MinSuccess != nil {
		conditions = append(conditions, fmt.Sprintf("mo.success_rate >= $%d", argIdx))
		args = append(args, *req.MinSuccess)
		argIdx++
	}
	if req.MaxP95Latency != nil {
		conditions = append(conditions, fmt.Sprintf("mo.p95_latency_ms <= $%d", argIdx))
		args = append(args, *req.MaxP95Latency)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var total int
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM model_offers mo JOIN credentials c ON c.id = mo.credential_id WHERE %s`, where)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		slog.Warn("queryProviderModels count failed", "error", err)
		total = 0
	}

	offset := (req.Page - 1) * req.PageSize
	dataSQL := fmt.Sprintf(offerListSQL+`
		WHERE %s
		ORDER BY mo.raw_model_name
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, req.PageSize, offset)

	rows, err := h.db.Query(ctx, dataSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	offers := make([]modelOfferDTO, 0)
	for rows.Next() {
		o, scanErr := scanModelOfferDTO(rows.Scan)
		if scanErr != nil {
			slog.Warn("queryProviderModels scan failed", "error", scanErr)
			continue
		}
		offers = append(offers, o)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     offers,
		"total":     total,
		"page":      req.Page,
		"page_size": req.PageSize,
	})
}

func (h *Handler) providerLogs(w http.ResponseWriter, r *http.Request, providerID int) {
	var filter struct {
		CredentialID *int       `json:"credential_id"`
		Model        *string    `json:"model"`
		FromTS       *time.Time `json:"from_ts"`
		ToTS         *time.Time `json:"to_ts"`
		Success      *bool      `json:"success"`
		ErrorKind    *string    `json:"error_kind"`
		Page         int        `json:"page"`
		PageSize     int        `json:"page_size"`
	}

	if r.Method == http.MethodPost {
		if err := readJSON(r, &filter); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	} else {
		filter.CredentialID = queryIntPtr(r, "credential_id")
		if m := queryString(r, "model"); m != "" {
			filter.Model = &m
		}
		if s := queryString(r, "success"); s != "" {
			b := s == "true" || s == "1"
			filter.Success = &b
		}
		if ek := queryString(r, "error_kind"); ek != "" {
			filter.ErrorKind = &ek
		}
	}

	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 50
	}

	conditions := []string{"rl.provider_id = $1"}
	args := []any{providerID}
	argIdx := 2

	if filter.CredentialID != nil {
		conditions = append(conditions, fmt.Sprintf("rl.credential_id = $%d", argIdx))
		args = append(args, *filter.CredentialID)
		argIdx++
	}
	if filter.Model != nil && *filter.Model != "" {
		conditions = append(conditions, fmt.Sprintf("(rl.client_model ILIKE $%d OR rl.outbound_model ILIKE $%d)", argIdx, argIdx))
		args = append(args, "%"+*filter.Model+"%")
		argIdx++
	}
	if filter.FromTS != nil {
		conditions = append(conditions, fmt.Sprintf("rl.ts >= $%d", argIdx))
		args = append(args, *filter.FromTS)
		argIdx++
	}
	if filter.ToTS != nil {
		conditions = append(conditions, fmt.Sprintf("rl.ts <= $%d", argIdx))
		args = append(args, *filter.ToTS)
		argIdx++
	}
	if filter.Success != nil {
		conditions = append(conditions, fmt.Sprintf("rl.success = $%d", argIdx))
		args = append(args, *filter.Success)
		argIdx++
	}
	if filter.ErrorKind != nil && *filter.ErrorKind != "" {
		conditions = append(conditions, fmt.Sprintf("rl.error_kind = $%d", argIdx))
		args = append(args, *filter.ErrorKind)
		argIdx++
	}

	where := strings.Join(conditions, " AND ")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var total int
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM request_logs rl WHERE %s`, where)
	if err := h.db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		slog.Warn("providerLogs count failed", "error", err)
		total = 0
	}

	offset := (filter.Page - 1) * filter.PageSize
	dataSQL := fmt.Sprintf(`
		SELECT rl.ts, rl.request_id, rl.credential_id,
		       rl.client_model, rl.outbound_model,
		       rl.success, rl.error_kind,
		       rl.prompt_tokens, rl.completion_tokens, rl.total_tokens,
		       rl.cost_usd::float8, rl.latency_ms
		FROM request_logs_with_current_month rl
		WHERE %s
		ORDER BY rl.ts DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)
	args = append(args, filter.PageSize, offset)

	rows, err := h.db.Query(ctx, dataSQL, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type logEntry struct {
		Ts               *time.Time `json:"ts"`
		RequestID        *string    `json:"request_id"`
		CredentialID     *int       `json:"credential_id"`
		ClientModel      *string    `json:"client_model"`
		OutboundModel    *string    `json:"outbound_model"`
		Success          bool       `json:"success"`
		ErrorKind        *string    `json:"error_kind"`
		PromptTokens     *int       `json:"prompt_tokens"`
		CompletionTokens *int       `json:"completion_tokens"`
		TotalTokens      *int       `json:"total_tokens"`
		CostUSD          *float64   `json:"cost_usd"`
		LatencyMs        *int       `json:"latency_ms"`
	}

	items := make([]logEntry, 0)
	for rows.Next() {
		var l logEntry
		if err := rows.Scan(
			&l.Ts, &l.RequestID, &l.CredentialID,
			&l.ClientModel, &l.OutboundModel,
			&l.Success, &l.ErrorKind,
			&l.PromptTokens, &l.CompletionTokens, &l.TotalTokens,
			&l.CostUSD, &l.LatencyMs,
		); err != nil {
			slog.Warn("providerLogs scan failed", "error", err)
			continue
		}
		items = append(items, l)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      filter.Page,
		"page_size": filter.PageSize,
	})
}

func queryIntPtr(r *http.Request, key string) *int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}
