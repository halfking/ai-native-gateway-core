package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/reconciliation"
)

func (h *Handler) handleSupplierContractRates(w http.ResponseWriter, r *http.Request) {
	if !IsSuperAdminOrLegacy(r) {
		writeError(w, http.StatusForbidden, "supplier contract rates require platform access")
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
	if r.Method == http.MethodGet {
		h.listSupplierContractRates(ctx, w, r)
		return
	}
	var input reconciliation.SupplierRateInput
	if err := readJSONRequired(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid supplier rate JSON")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	out, err := reconciliation.ImportSupplierRate(ctx, h.db, input, platformEvidenceActor(r))
	switch {
	case errors.Is(err, reconciliation.ErrInvalidSupplierRate):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, reconciliation.ErrSupplierRateConflict):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.ErrorContext(ctx, "supplier contract rate import failed", "provider_id", input.ProviderID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "supplier rate import unavailable")
	case out.Created:
		writeJSON(w, http.StatusCreated, out)
	default:
		writeJSON(w, http.StatusOK, out)
	}
}

func (h *Handler) listSupplierContractRates(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query()
	provider, providerErr := strconv.ParseInt(p.Get("provider_id"), 10, 64)
	credential, credentialErr := strconv.ParseInt(p.Get("credential_id"), 10, 64)
	model := p.Get("model")
	limit, offset := 100, 0
	if p.Has("limit") {
		limit, _ = strconv.Atoi(p.Get("limit"))
	}
	if p.Has("offset") {
		var err error
		offset, err = strconv.Atoi(p.Get("offset"))
		if err != nil {
			offset = -1
		}
	}
	if providerErr != nil || credentialErr != nil || provider <= 0 || credential <= 0 || model == "" || limit < 1 || limit > 500 || offset < 0 {
		writeError(w, http.StatusBadRequest, "positive provider_id, credential_id, model and valid paging are required")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	items, err := reconciliation.ListSupplierRates(ctx, h.db, provider, credential, model, limit, offset)
	if errors.Is(err, reconciliation.ErrInvalidSupplierRate) {
		writeError(w, http.StatusBadRequest, err.Error())
	} else if err != nil {
		slog.ErrorContext(ctx, "supplier contract rate list failed", "provider_id", provider, "error", err)
		writeError(w, http.StatusServiceUnavailable, "supplier rate list unavailable")
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	}
}
