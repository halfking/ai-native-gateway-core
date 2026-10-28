package admin

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// RecordingUnavailableCode is the structured error code for consent=off
// replay / summary / forensics surfaces (never a silent empty body).
const RecordingUnavailableCode = "recording_unavailable"

// handleTenantRecordingConsent serves:
//
//	GET  /api/admin/tenants/{code}/recording-consent
//	PUT  /api/admin/tenants/{code}/recording-consent
//	POST /api/admin/tenants/{code}/recording-consent/clear
//
// Wired from handleTenants (same mux prefix as other tenant sub-resources).
func (h *Handler) handleTenantRecordingConsent(w http.ResponseWriter, r *http.Request, code, sub string) {
	tid := code
	rest := strings.TrimPrefix(sub, "recording-consent")
	rest = strings.Trim(rest, "/")
	switch {
	case r.Method == http.MethodGet && rest == "":
		h.handleRecordingConsentGet(w, tid)
	case r.Method == http.MethodPut && rest == "":
		h.handleRecordingConsentPut(w, r, tid)
	case r.Method == http.MethodPost && rest == "clear":
		h.handleRecordingConsentClear(w, r, tid)
	default:
		writeError(w, http.StatusNotFound, "unknown recording-consent endpoint")
	}
}

func (h *Handler) handleRecordingConsentGet(w http.ResponseWriter, tid string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":     tid,
		"consent":       settings.RecordingConsentEnabled(tid),
		"generation":    settings.RecordingConsentGeneration(tid),
		"retention_h":   settings.RecordingBodyRetentionHours(tid),
		"store_max_mib": settings.RecordingBodyStoreMaxMiB(tid),
	})
}

type recordingConsentPutBody struct {
	Consent *bool `json:"consent"`
	Clear   bool  `json:"clear"`
}

func (h *Handler) handleRecordingConsentPut(w http.ResponseWriter, r *http.Request, tid string) {
	var body recordingConsentPutBody
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Consent == nil {
		writeError(w, http.StatusBadRequest, "consent bool required")
		return
	}
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	wasOn := settings.RecordingConsentEnabled(tid)
	if _, err := store.SetTenant(tid, settings.KeySessionRecordingConsent, *body.Consent); err != nil {
		writeInternalErr(w, "save consent failed", err)
		return
	}
	gen := settings.RecordingConsentGeneration(tid)
	if (wasOn && !*body.Consent) || body.Clear {
		var err error
		gen, err = settings.BumpRecordingConsentGeneration(store, tid)
		if err != nil {
			writeInternalErr(w, "bump generation failed", err)
			return
		}
	}
	cleared := 0
	if body.Clear || (wasOn && !*body.Consent) {
		var err error
		cleared, err = h.clearSessionBodies(r.Context(), tid)
		if err != nil {
			writeInternalErr(w, "clear bodies failed", err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":       tid,
		"consent":         *body.Consent,
		"generation":      gen,
		"bodies_cleared":  cleared,
		"usage_preserved": true,
	})
}

func (h *Handler) handleRecordingConsentClear(w http.ResponseWriter, r *http.Request, tid string) {
	store, ok := h.dbSettingsStore()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	if _, err := store.SetTenant(tid, settings.KeySessionRecordingConsent, false); err != nil {
		writeInternalErr(w, "disable consent failed", err)
		return
	}
	gen, err := settings.BumpRecordingConsentGeneration(store, tid)
	if err != nil {
		writeInternalErr(w, "bump generation failed", err)
		return
	}
	cleared, err := h.clearSessionBodies(r.Context(), tid)
	if err != nil {
		writeInternalErr(w, "clear bodies failed", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":       tid,
		"consent":         false,
		"generation":      gen,
		"bodies_cleared":  cleared,
		"usage_preserved": true,
	})
}

// clearSessionBodies deletes body rows for a tenant. Usage/turns untouched.
func (h *Handler) clearSessionBodies(ctx context.Context, tenantID string) (int, error) {
	if h.db == nil {
		return 0, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	total := 0
	for _, sql := range []string{
		`DELETE FROM public.session_bodies_hot WHERE tenant_id = $1`,
		`DELETE FROM public.session_bodies WHERE tenant_id = $1`,
	} {
		tag, err := h.db.Exec(ctx, sql, tenantID)
		if err != nil {
			if strings.Contains(err.Error(), "does not exist") {
				continue
			}
			return total, err
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

func writeRecordingUnavailable(w http.ResponseWriter) {
	writeErrorWithCode(w, http.StatusConflict, RecordingUnavailableCode,
		"session body recording is off for this tenant; enable session_recording_consent to capture bodies")
}

func requireRecordingConsent(w http.ResponseWriter, tenantID string) bool {
	if settings.RecordingConsentEnabled(tenantID) {
		return true
	}
	writeRecordingUnavailable(w)
	return false
}

func isRecordingConsentSubResource(sub string) bool {
	return sub == "recording-consent" || strings.HasPrefix(sub, "recording-consent/")
}
