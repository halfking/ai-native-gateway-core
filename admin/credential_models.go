package admin

// credential_models.go — per-credential model list / clear / create / refresh.
// Shared DTO + SQL live in credential_models_dto.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

func (h *Handler) assertCredentialBelongs(ctx context.Context, providerID, credentialID int) error {
	var n int
	err := h.db.QueryRow(ctx, `
		SELECT 1 FROM credentials WHERE id = $1 AND provider_id = $2
	`, credentialID, providerID).Scan(&n)
	if err != nil {
		return fmt.Errorf("credential not found")
	}
	return nil
}

func (h *Handler) handleCredentialModels(w http.ResponseWriter, r *http.Request, providerID, credentialID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := h.assertCredentialBelongs(ctx, providerID, credentialID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.listCredentialModels(w, ctx, credentialID)
	case http.MethodPost:
		h.createCredentialModel(w, r, ctx, credentialID)
	case http.MethodDelete:
		h.clearCredentialModels(w, r, ctx, credentialID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) listCredentialModels(w http.ResponseWriter, ctx context.Context, credentialID int) {
	rows, err := h.db.Query(ctx, offerListSQL+`
		WHERE mo.credential_id = $1
		ORDER BY mo.raw_model_name
	`, credentialID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	offers := make([]modelOfferDTO, 0)
	for rows.Next() {
		o, scanErr := scanModelOfferDTO(rows.Scan)
		if scanErr != nil {
			slog.Warn("listCredentialModels scan failed", "error", scanErr)
			continue
		}
		offers = append(offers, o)
	}
	writeJSON(w, http.StatusOK, offers)
}

func (h *Handler) clearCredentialModels(w http.ResponseWriter, r *http.Request, ctx context.Context, credentialID int) {
	includeProtected := queryString(r, "include_protected") == "1" ||
		strings.EqualFold(queryString(r, "include_protected"), "true")

	deleted, err := modelcatalog.ClearCredentialBindings(ctx, h.db, credentialID, includeProtected)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "clear failed: "+err.Error())
		return
	}
	var protectedKept int
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM credential_model_bindings
		WHERE credential_id = $1 AND admin_protected = TRUE
	`, credentialID).Scan(&protectedKept)
	InvalidateAvailableModelsCache()
	writeJSON(w, http.StatusOK, map[string]any{
		"message":           "ok",
		"deleted":           int(deleted),
		"include_protected": includeProtected,
		"protected_kept":    protectedKept,
	})
}

func (h *Handler) createCredentialModel(w http.ResponseWriter, r *http.Request, ctx context.Context, credentialID int) {
	var req createCredentialModelReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	raw := strings.TrimSpace(req.RawModelName)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "raw_model_name required")
		return
	}

	stdName := modelname.NormalizeRouteKey(raw)
	if req.StandardizedName != nil && strings.TrimSpace(*req.StandardizedName) != "" {
		stdName = modelname.NormalizeRouteKey(strings.TrimSpace(*req.StandardizedName))
	}

	canonicalID := req.CanonicalID
	if canonicalID == nil {
		id, _, err := discovery.EnsureCanonicalAndAliases(ctx, h.refreshDB(), stdName, "manual")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "ensure canonical failed: "+err.Error())
			return
		}
		canonicalID = &id
	} else {
		var exists int
		if err := h.db.QueryRow(ctx, `SELECT 1 FROM models_canonical WHERE id = $1`, *canonicalID).Scan(&exists); err != nil {
			writeError(w, http.StatusBadRequest, "canonical model not found")
			return
		}
	}

	if err := h.patchCanonicalCaps(ctx, *canonicalID, req); err != nil {
		writeError(w, http.StatusInternalServerError, "update canonical caps failed: "+err.Error())
		return
	}

	available := true
	if req.Available != nil {
		available = *req.Available
	}
	var outbound *string
	if req.OutboundModelName != nil {
		v := strings.TrimSpace(*req.OutboundModelName)
		if v != "" {
			outbound = &v
		}
	}

	bindingID, err := modelcatalog.InsertManualCredentialModel(ctx, h.refreshDB(), modelcatalog.ManualInsertParams{
		CredentialID:      credentialID,
		RawName:           raw,
		CanonicalRawName:  modelname.CanonicalizeClientModel(raw),
		StandardizedName:  stdName,
		CanonicalID:       canonicalID,
		OutboundModelName: outbound,
		Available:         available,
		ContextWindow:     req.ContextWindow,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}

	InvalidateAvailableModelsCache()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":             bindingID,
		"credential_id":  credentialID,
		"raw_model_name": raw,
		"canonical_id":   canonicalID,
	})
}

// createProviderOffer handles POST /api/providers/{id}/models/ with
// credential_id in the body — forwards to the credential-scoped create path.
func (h *Handler) createProviderOffer(w http.ResponseWriter, r *http.Request, providerID int) {
	var peek struct {
		CredentialID *int `json:"credential_id"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := json.Unmarshal(body, &peek); err != nil || peek.CredentialID == nil || *peek.CredentialID <= 0 {
		writeError(w, http.StatusBadRequest, "credential_id required")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := h.assertCredentialBelongs(ctx, providerID, *peek.CredentialID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	h.createCredentialModel(w, r, ctx, *peek.CredentialID)
}

func (h *Handler) refreshCredentialModels(w http.ResponseWriter, r *http.Request, providerID, credentialID int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	if err := h.assertCredentialBelongs(ctx, providerID, credentialID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	target, err := h.loadCredentialRowLite(ctx, providerID, credentialID)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	var protectedBefore int
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM credential_model_bindings
		WHERE credential_id = $1 AND admin_protected = TRUE
	`, credentialID).Scan(&protectedBefore)

	upserted, failed, uerr := h.discoverAndUpsertForCredential(ctx, target)

	var protectedAfter int
	_ = h.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM credential_model_bindings
		WHERE credential_id = $1 AND admin_protected = TRUE
	`, credentialID).Scan(&protectedAfter)

	InvalidateAvailableModelsCache()

	status := http.StatusOK
	msg := "ok"
	if uerr != nil && upserted == 0 {
		status = http.StatusBadGateway
		msg = uerr.Error()
	} else if uerr != nil {
		msg = "partial: " + uerr.Error()
	}

	writeJSON(w, status, map[string]any{
		"message":            msg,
		"models_upserted":    upserted,
		"models_failed":      failed,
		"skipped_protected":  protectedBefore,
		"protected_bindings": protectedAfter,
		"credential_id":      credentialID,
		"provider_id":        providerID,
	})
}

