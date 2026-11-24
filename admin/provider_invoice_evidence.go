package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
	"github.com/kaixuan/llm-gateway-go/settings"
)

func (h *Handler) handleProviderInvoices(w http.ResponseWriter, r *http.Request) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "supplier invoice evidence requires platform access")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), usageAggregateTimeout)
	defer cancel()
	if r.Method == http.MethodPost {
		h.importProviderInvoice(ctx, w, r)
		return
	}
	h.readProviderInvoices(ctx, w, r)
}

func (h *Handler) importProviderInvoice(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	var input reconciliation.InvoiceInput
	if err := readJSONRequired(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid invoice JSON")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	if input.Day != "" && input.Timezone == "" {
		input.Timezone = settings.SystemTimezoneName()
	}
	out, err := reconciliation.ImportInvoice(ctx, h.db, input, platformEvidenceActor(r))
	switch {
	case errors.Is(err, reconciliation.ErrInvalidInvoice):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, reconciliation.ErrInvoiceConflict):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.ErrorContext(ctx, "supplier invoice import failed", "error", err, "provider_id", input.ProviderID)
		writeError(w, http.StatusServiceUnavailable, "invoice import unavailable")
	case out.Created:
		writeJSON(w, http.StatusCreated, out)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func platformEvidenceActor(r *http.Request) string {
	if auth := GetAuthContext(r); auth.UserID > 0 {
		return "user:" + strconv.Itoa(auth.UserID)
	}
	return "admin-key"
}

func (h *Handler) readProviderInvoices(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query()
	limit, offset := 200, 0
	if p.Has("limit") {
		limit, _ = strconv.Atoi(p.Get("limit"))
		if limit < 1 || limit > 2000 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 2000")
			return
		}
	}
	if p.Has("offset") {
		var err error
		offset, err = strconv.Atoi(p.Get("offset"))
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
	}
	format := p.Get("format")
	if raw := p.Get("invoice_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil || p.Has("month") || p.Has("day") || p.Has("timezone") || p.Has("provider_id") {
			writeError(w, http.StatusBadRequest, "use a valid invoice_id without period or provider filters")
			return
		}
		if format != "" && format != "json" && format != "csv" && format != "csv-summary" {
			writeError(w, http.StatusBadRequest, "format must be json, csv, or csv-summary")
			return
		}
		if h.db == nil {
			writeError(w, http.StatusServiceUnavailable, "database not configured")
			return
		}
		report, err := reconciliation.ReadInvoiceReport(ctx, h.db, id, limit, offset)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "invoice not found")
		} else if err != nil {
			slog.ErrorContext(ctx, "supplier invoice report failed", "error", err, "invoice_id", id)
			writeError(w, http.StatusServiceUnavailable, "invoice report unavailable")
		} else if format == "csv" || format == "csv-summary" {
			writeProviderInvoiceCSV(w, id, report, format == "csv-summary")
		} else {
			writeJSON(w, http.StatusOK, report)
		}
		return
	}
	month, day, zone := p.Get("month"), p.Get("day"), p.Get("timezone")
	if p.Has("day") == p.Has("month") || (p.Has("timezone") && !p.Has("day")) {
		writeError(w, http.StatusBadRequest, "provide exactly one day or month")
		return
	}
	var providerID int64
	if p.Has("provider_id") {
		id, err := strconv.ParseInt(p.Get("provider_id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "provider_id must be a positive integer")
			return
		}
		providerID = id
	} else if p.Has("month") {
		writeError(w, http.StatusBadRequest, "provider_id is required for a monthly invoice list")
		return
	}
	if zone == "" {
		if p.Has("day") {
			zone = settings.SystemTimezoneName()
		} else {
			zone = reconciliation.DefaultMonthTimezone
		}
	}
	period, bounds := month, reconciliation.NaturalMonth
	if p.Has("day") {
		period, bounds = day, reconciliation.NaturalDay
	}
	if _, _, err := bounds(period, zone); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if format != "" && format != "json" {
		writeError(w, http.StatusBadRequest, "invoice_id is required for CSV export")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	var items []json.RawMessage
	var err error
	result := map[string]any{"limit": limit, "offset": offset}
	if providerID > 0 {
		result["provider_id"] = providerID
	}
	if day != "" {
		items, err = reconciliation.ListDailyInvoices(ctx, h.db, providerID, day, zone, limit, offset)
		result["day"], result["timezone"] = day, zone
	} else {
		items, err = reconciliation.ListInvoices(ctx, h.db, providerID, month, limit, offset)
		result["month"] = month
	}
	if err != nil {
		slog.ErrorContext(ctx, "supplier invoice list failed", "error", err, "provider_id", providerID)
		writeError(w, http.StatusServiceUnavailable, "invoice list unavailable")
		return
	}
	result["items"] = items
	writeJSON(w, http.StatusOK, result)
}

func writeProviderInvoiceCSV(w http.ResponseWriter, id uuid.UUID, report reconciliation.InvoiceReport, summary bool) {
	name, view := "supplier-invoice-lines", "page"
	var data []byte
	var err error
	if summary {
		name, view = "supplier-invoice-summary", "summary"
		data, err = requestReconciliationSummaryCSV(report.Summary)
	} else {
		data, err = reconciliationRowsCSV([]string{"line_ref", "kind", "provider_request_id", "invoice_amount", "invoice_currency", "model", "description",
			"match_status", "attempt_id", "sent_at", "credential_id", "captured_estimate", "estimated_currency", "captured_reported", "reported_currency", "reported_semantics", "comparable_estimate", "comparable_reported",
			"estimated_minus_invoiced_amount", "reported_minus_invoiced_amount", "bucket_sent_attempts", "bucket_input_tokens", "bucket_output_tokens",
			"bucket_observed_sends", "bucket_observed_input", "bucket_observed_output", "bucket_attempts_without_model", "bucket_unconfirmed_attempts",
			"bucket_sends_without_comparable_estimate", "bucket_sends_without_comparable_report", "estimate_basis",
			"bucket_audio_input_duration_us", "bucket_observed_audio_input_duration_us",
			"bucket_audio_billed_duration_us", "bucket_observed_audio_billed_duration_us",
			"bucket_audio_output_duration_us", "bucket_observed_audio_output_duration_us",
			"bucket_generated_images", "bucket_observed_generated_images",
			"bucket_video_output_duration_us", "bucket_observed_video_output_duration_us"}, report.Lines)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "invoice export unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-`+id.String()+`.csv"`)
	w.Header().Set("X-Invoice-ID", id.String())
	if !report.ReadAt.IsZero() {
		w.Header().Set("X-Report-Read-At", report.ReadAt.Format(time.RFC3339Nano))
	}
	w.Header().Set("X-Report-Export", view)
	w.Header().Set("X-Report-Limit", strconv.Itoa(report.Limit))
	w.Header().Set("X-Report-Offset", strconv.Itoa(report.Offset))
	_, _ = w.Write(data)
}
