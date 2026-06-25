package admin

// provider_credential.go — extracted from providers.go (2026-06-21 audit §3
// single-file-bloat remediation, third cut after provider_probe.go and
// provider_diagnose.go). The credential CRUD cluster handles the lifecycle
// of the api_key rows attached to each provider:
//
//   POST   /api/providers/{id}/credentials               addCredential
//   GET    /api/providers/{id}/credentials               listCredentials
//   PATCH  /api/providers/{id}/credentials/{cid}         updateCredential
//   DELETE /api/providers/{id}/credentials/{cid}         deleteCredential
//   POST   /api/providers/{id}/credentials/{cid}/reveal  revealCredential
//
// parseTags is a small helper used by listCredentials to turn a
// sql.NullString of pipe-separated tags into a []string slice. It moved
// with the cluster because nothing else uses it.
//
// Self-contained: only stdlib + same-package helpers (writeJSON / writeError /
// h.db / h.decryptCredStr / h.parseCredentialRequest). No internal/* deps.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
)

func (h *Handler) addCredential(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		Label            *string `json:"label"`
		APIKey           string  `json:"api_key"`
		ConcurrencyLimit *int    `json:"concurrency_limit"`
		FpSlotLimit      *int    `json:"fp_slot_limit"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key required")
		return
	}

	encrypted, err := h.encryptCred([]byte(req.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	label := "default"
	if req.Label != nil && *req.Label != "" {
		label = *req.Label
	}
	concurrencyLimit := 10
	if req.ConcurrencyLimit != nil {
		concurrencyLimit = *req.ConcurrencyLimit
	}
	fpSlotLimit := 20 // 2026-06-24: 5 → 20, matches DefaultDefaultLimit
	if req.FpSlotLimit != nil {
		fpSlotLimit = *req.FpSlotLimit
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id int
	err = h.db.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, secret_ciphertext, status, concurrency_limit, fp_slot_limit, balance_usd)
		VALUES ($1, $2, $3, 'active', $4, $5, 1000.0)
		RETURNING id
	`, providerID, label, encrypted, concurrencyLimit, fpSlotLimit).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}

	// ── Auto probe: fire-and-forget health check after credential creation ──
	// Runs asynchronously in a goroutine so the API returns immediately.
	// The UI can poll the credential's health_status to see the result.
	go func(pid, cid int) {
		taskID, taskErr := insertBackgroundTask(context.Background(), h.db, "health_check", &pid, &cid,
			map[string]any{"provider_id": pid, "credential_id": cid, "source": "auto_on_create"})
		if taskErr != nil {
			slog.Warn("auto-probe: task insert failed", "provider_id", pid, "credential_id", cid, "error", taskErr)
			return
		}
		h.runHealthCheck(pid, cid, taskID)
	}(providerID, id)

	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.trust_level,'standard'), c.concurrency_limit,
		       COALESCE(c.fp_slot_limit, 20) AS fp_slot_limit,  -- 2026-06-24: 5→20
		       c.balance_usd::float8,
		       COALESCE(c.circuit_state,'closed'),
		       c.circuit_opened_at,
		       COALESCE(c.consecutive_failures, 0),
		       c.cooling_until,
		       COALESCE(c.lifecycle_status,'active'),
		       COALESCE(c.availability_state,'ready'),
		       c.availability_recover_at,
		       COALESCE(c.quota_state,'ok'),
		       c.quota_recover_at,
		       c.state_reason_code,
		       c.state_reason_detail,
		       c.state_updated_at,
		       COALESCE(c.health_status,'unknown'),
		       c.health_checked_at,
		       c.health_source,
		       c.health_warning_code,
		       c.health_error,
		       c.health_latency_ms,
		       c.health_probe_model,
		       c.api_models_ok,
		       c.api_models_last_checked_at,
		       c.api_models_error,
		       c.effective_at,
		       c.expires_at,
		       c.tags::text,
		       COALESCE(c.notes,''),
		       c.secret_ciphertext,
		       COALESCE(c.manual_disabled, false),
		       c.created_at,
		       c.updated_at
		FROM credentials c
		WHERE c.provider_id = $1
		ORDER BY c.id
	`, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type cred struct {
		ID                     int        `json:"id"`
		ProviderID             int        `json:"provider_id"`
		Label                  string     `json:"label"`
		Status                 string     `json:"status"`
		TrustLevel             string     `json:"trust_level"`
		ConcurrencyLimit       *int       `json:"concurrency_limit"`
		BalanceUSD             *float64   `json:"balance_usd"`
		CircuitState           string     `json:"circuit_state"`
		CircuitOpenedAt        *time.Time `json:"circuit_opened_at"`
		ConsecutiveFailures    int        `json:"consecutive_failures"`
		CoolingUntil           *time.Time `json:"cooling_until"`
		LifecycleStatus        string     `json:"lifecycle_status"`
		AvailabilityState      string     `json:"availability_state"`
		AvailabilityRecoverAt  *time.Time `json:"availability_recover_at"`
		QuotaState             string     `json:"quota_state"`
		QuotaRecoverAt         *time.Time `json:"quota_recover_at"`
		StateReasonCode        *string    `json:"state_reason_code"`
		StateReasonDetail      *string    `json:"state_reason_detail"`
		StateUpdatedAt         *time.Time `json:"state_updated_at"`
		HealthStatus           string     `json:"health_status"`
		HealthCheckedAt        *time.Time `json:"health_checked_at"`
		HealthSource           *string    `json:"health_source"`
		HealthWarningCode      *string    `json:"health_warning_code"`
		HealthError            *string    `json:"health_error"`
		HealthLatencyMs        *int       `json:"health_latency_ms"`
		HealthProbeModel       *string    `json:"health_probe_model"`
		ApiModelsOk            *bool      `json:"api_models_ok"`
		ApiModelsLastCheckedAt *time.Time `json:"api_models_last_checked_at"`
		ApiModelsError         *string    `json:"api_models_error"`
		EffectiveAt            *time.Time `json:"effective_at"`
		ExpiresAt              *time.Time `json:"expires_at"`
		Tags                   []string   `json:"tags"`
		Notes                  string     `json:"notes"`
		KeyMasked              *string    `json:"key_masked"`
		KeyMaskError           *string    `json:"key_mask_error"`
		FpSlotLimit            *int       `json:"fp_slot_limit"`
		FpSlotsUsed            *int       `json:"fp_slots_used"`
		FpSlotsFree            *int       `json:"fp_slots_free"`
		EffectiveFpSlotLimit   *int       `json:"effective_fp_slot_limit"`
		ManualDisabled         bool       `json:"manual_disabled"`
		CreatedAt              *time.Time `json:"created_at"`
		UpdatedAt              *time.Time `json:"updated_at"`
	}

	var creds []cred
	for rows.Next() {
		var c cred
		var tagsStr sql.NullString
		var balanceUSD sql.NullFloat64
		var ciphertext []byte

		if err := rows.Scan(
			&c.ID, &c.ProviderID, &c.Label, &c.Status,
			&c.TrustLevel, &c.ConcurrencyLimit,
			&c.FpSlotLimit,
			&balanceUSD,
			&c.CircuitState,
			&c.CircuitOpenedAt,
			&c.ConsecutiveFailures,
			&c.CoolingUntil,
			&c.LifecycleStatus,
			&c.AvailabilityState,
			&c.AvailabilityRecoverAt,
			&c.QuotaState,
			&c.QuotaRecoverAt,
			&c.StateReasonCode,
			&c.StateReasonDetail,
			&c.StateUpdatedAt,
			&c.HealthStatus,
			&c.HealthCheckedAt,
			&c.HealthSource,
			&c.HealthWarningCode,
			&c.HealthError,
			&c.HealthLatencyMs,
			&c.HealthProbeModel,
			&c.ApiModelsOk,
			&c.ApiModelsLastCheckedAt,
			&c.ApiModelsError,
			&c.EffectiveAt,
			&c.ExpiresAt,
			&tagsStr,
			&c.Notes,
			&ciphertext,
			&c.ManualDisabled,
			&c.CreatedAt,
			&c.UpdatedAt,
		); err != nil {
			slog.Warn("listCredentials scan failed", "error", err)
			continue
		}

		if balanceUSD.Valid {
			c.BalanceUSD = &balanceUSD.Float64
		}

		c.Tags = parseTags(tagsStr)
		if len(ciphertext) > 0 {
			if plaintext, decErr := h.decryptCredStr(string(ciphertext)); decErr != nil {
				errCode := "decrypt_failed"
				c.KeyMaskError = &errCode
			} else {
				masked := maskAPIKey(plaintext)
				c.KeyMasked = &masked
			}
		}
		if h.fpSlots != nil {
			c.FpSlotLimit, c.FpSlotsUsed, c.FpSlotsFree = h.fpSlots.Stats(ctx, c.ID, c.FpSlotLimit)
			c.EffectiveFpSlotLimit = credentialfpslot.EffectiveFpSlotLimit(c.FpSlotLimit, h.fpSlotsDefaultLimit())
		}
		creds = append(creds, c)
	}
	if creds == nil {
		creds = []cred{}
	}
	writeJSON(w, http.StatusOK, creds)
}

func parseTags(ns sql.NullString) []string {
	if !ns.Valid || ns.String == "" {
		return []string{}
	}
	s := strings.TrimSpace(ns.String)
	if s == "" {
		return []string{}
	}
	if s[0] == '[' {
		var arr []string
		if err := json.Unmarshal([]byte(s), &arr); err == nil {
			return arr
		}
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req struct {
		Label            *string  `json:"label"`
		Status           *string  `json:"status"`
		ConcurrencyLimit *int     `json:"concurrency_limit"`
		FpSlotLimit      *int     `json:"fp_slot_limit"`
		EffectiveAt      *string  `json:"effective_at"`
		ExpiresAt        *string  `json:"expires_at"`
		Tags             []string `json:"tags"`
		Notes            *string  `json:"notes"`
		BalanceUSD       *float64 `json:"balance_usd"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if req.Label != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET label = $1 WHERE id = $2 AND provider_id = $3`, *req.Label, credID, providerID)
	}
	if req.Status != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET status = $1 WHERE id = $2 AND provider_id = $3`, *req.Status, credID, providerID)
	}
	if req.ConcurrencyLimit != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET concurrency_limit = $1 WHERE id = $2 AND provider_id = $3`, *req.ConcurrencyLimit, credID, providerID)
	}
	if req.FpSlotLimit != nil {
		newLimit := *req.FpSlotLimit
		// Constraint: 1 <= fp_slot_limit <= concurrency_limit (or 100 if unlimited)
		var currentConcurrency sql.NullInt32
		err := h.db.QueryRow(ctx, `SELECT concurrency_limit FROM credentials WHERE id = $1 AND provider_id = $2`, credID, providerID).Scan(&currentConcurrency)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to fetch credential: "+err.Error())
			return
		}
		// System max from settings_kv
		var sysMax sql.NullInt32
		_ = h.db.QueryRow(ctx, `SELECT (value #>> '{}')::int4 FROM settings_kv WHERE key = 'llmgw_fp_slot_max_per_credential'`).Scan(&sysMax)
		maxAllowed := 100
		if sysMax.Valid {
			maxAllowed = int(sysMax.Int32)
		}
		if currentConcurrency.Valid && newLimit > int(currentConcurrency.Int32) {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("fp_slot_limit (%d) cannot exceed concurrency_limit (%d)", newLimit, currentConcurrency.Int32))
			return
		}
		if newLimit > maxAllowed {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("fp_slot_limit (%d) exceeds system max (%d)", newLimit, maxAllowed))
			return
		}
		if newLimit < 1 {
			writeError(w, http.StatusBadRequest, "fp_slot_limit must be >= 1")
			return
		}
		// Fetch old value for audit
		var oldLimit sql.NullInt32
		_ = h.db.QueryRow(ctx, `SELECT fp_slot_limit FROM credentials WHERE id = $1`, credID).Scan(&oldLimit)
		//nolint:errcheck // best-effort exec, non-critical
		_, err = h.db.Exec(ctx, `UPDATE credentials SET fp_slot_limit = $1 WHERE id = $2 AND provider_id = $3`, newLimit, credID, providerID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
			return
		}
		// Audit log (best-effort, no PII)
		actor := "admin"
		if v := r.Header.Get("X-Admin-User"); v != "" {
			actor = v
		}
		oldVal := "null"
		if oldLimit.Valid {
			oldVal = strconv.Itoa(int(oldLimit.Int32))
		}
		//nolint:errcheck // audit log is best-effort
		h.db.Exec(ctx, `
			INSERT INTO settings_history (key, old_value, new_value, changed_by, source)
			VALUES ($1, $2, $3, $4, 'api')`,
			fmt.Sprintf("credential:%d:fp_slot_limit", credID),
			oldVal,
			strconv.Itoa(newLimit),
			actor)
	}
	if req.FpSlotLimit != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET fp_slot_limit = $1 WHERE id = $2 AND provider_id = $3`, *req.FpSlotLimit, credID, providerID)
	}
	if req.EffectiveAt != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET effective_at = $1 WHERE id = $2 AND provider_id = $3`, *req.EffectiveAt, credID, providerID)
	}
	if req.ExpiresAt != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET expires_at = $1 WHERE id = $2 AND provider_id = $3`, *req.ExpiresAt, credID, providerID)
	}
	if req.Tags != nil {
		tagsStr := strings.Join(req.Tags, ",")
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET tags = $1 WHERE id = $2 AND provider_id = $3`, tagsStr, credID, providerID)
	}
	if req.Notes != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET notes = $1 WHERE id = $2 AND provider_id = $3`, *req.Notes, credID, providerID)
	}
	if req.BalanceUSD != nil {
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `UPDATE credentials SET balance_usd = $1 WHERE id = $2 AND provider_id = $3`, *req.BalanceUSD, credID, providerID)
	}
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]string{"message": "updated"})
}

func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, err := h.db.Exec(ctx, `UPDATE credentials SET status = 'disabled' WHERE id = $1 AND provider_id = $2`, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]string{"message": "revoked"})
}

// resetCredentialFpSlots clears all fingerprint slots for a credential.
// POST /api/providers/{provider_id}/credentials/{cred_id}/reset-fp-slots
func (h *Handler) resetCredentialFpSlots(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.fpSlots == nil || !h.fpSlots.Enabled() {
		writeError(w, http.StatusBadRequest, "fingerprint slots not enabled")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Fetch credential to get fp_slot_limit (the fingerprint pool size).
	// This is conceptually independent from concurrency_limit — see
	// EffectiveFpSlotLimit for the distinction.
	var fpSlotLimit *int
	err := h.db.QueryRow(ctx, `
		SELECT fp_slot_limit
		FROM credentials
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID).Scan(&fpSlotLimit)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	// Reset all slots
	deletedSlots, deletedPins, err := h.fpSlots.ResetSlots(ctx, credID, fpSlotLimit)
	if err != nil {
		slog.Error("reset fp slots failed", "credential_id", credID, "error", err)
		writeError(w, http.StatusInternalServerError, "reset failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":       "reset completed",
		"deleted_slots": deletedSlots,
		"deleted_pins":  deletedPins,
	})
}

// releaseCredentialFpSlot releases a single fingerprint slot (and its pin)
// for a credential. Used by the admin UI "释放槽位" button to free up a
// specific occupied slot without affecting other slots.
//
// POST /api/providers/{provider_id}/credentials/{cred_id}/release-fp-slot
func (h *Handler) releaseCredentialFpSlot(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.fpSlots == nil || !h.fpSlots.Enabled() {
		writeError(w, http.StatusBadRequest, "fingerprint slots not enabled")
		return
	}

	var body struct {
		SlotIndex int `json:"slot_index"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	released, err := h.fpSlots.ReleaseSlot(ctx, credID, body.SlotIndex)
	if err != nil {
		slog.Error("release fp slot failed", "credential_id", credID, "slot_index", body.SlotIndex, "error", err)
		writeError(w, http.StatusInternalServerError, "release failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":    "slot released",
		"released":   released,
		"slot_index": body.SlotIndex,
	})
}

// getCredentialFpSlotStats returns detailed fingerprint slot statistics for
// monitoring and diagnostics.
//
// GET /api/providers/{provider_id}/credentials/{cred_id}/fp-slot-stats
//
// Returns per-slot details including holder identifier, remaining TTL, and
// the session title (if known) for human-readable display. Used to diagnose
// issues like the "minimax-m3 alternating success/failure" pattern where
// a session repeatedly bounces between credentials.
func (h *Handler) getCredentialFpSlotStats(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if h.fpSlots == nil || !h.fpSlots.Enabled() {
		writeError(w, http.StatusBadRequest, "fingerprint slots not enabled")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var fpSlotLimit *int
	err := h.db.QueryRow(ctx, `
		SELECT fp_slot_limit
		FROM credentials
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID).Scan(&fpSlotLimit)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	limit, holders, details, healthySlots := h.fpSlots.DetailedStats(ctx, credID, fpSlotLimit)

	if limit == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"credential_id": credID,
			"unlimited":     true,
			"message":       "credential has no fingerprint slot limit",
		})
		return
	}

	// Look up session titles for each holder (best-effort; missing title
	// means the session has not generated one yet — show the holder ID).
	titleMap := h.lookupSessionTitles(ctx, holders)

	// Enrich details with session titles so the UI can render readable tooltips.
	type enrichedDetail struct {
		credentialfpslot.SlotDetail
		SessionTitle string `json:"session_title,omitempty"`
		SessionID    string `json:"session_id,omitempty"`
	}
	enrichedDetails := make([]enrichedDetail, len(details))
	for i, d := range details {
		ed := enrichedDetail{SlotDetail: d}
		if d.Holder != "" {
			ed.SessionID = d.Holder
			if title, ok := titleMap[d.Holder]; ok && title != "" {
				ed.SessionTitle = title
			}
		}
		enrichedDetails[i] = ed
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"credential_id":  credID,
		"slot_limit":     *limit,
		"healthy_slots":  healthySlots,
		"occupied_slots": len(holders),
		"free_slots":     *limit - healthySlots,
		"holders":        holders,
		"details":        enrichedDetails,
	})
}

// lookupSessionTitles queries session_titles in bulk for the given holders
// (typically 1-5 entries per credential) so the UI can render readable
// tooltips. Returns holder -> title mapping; missing entries are simply
// absent from the map.
func (h *Handler) lookupSessionTitles(ctx context.Context, holders []string) map[string]string {
	result := make(map[string]string, len(holders))
	if len(holders) == 0 || h.db == nil {
		return result
	}
	rows, err := h.db.Query(ctx, `
		SELECT session_id, title
		FROM session_titles
		WHERE session_id = ANY($1)
	`, holders)
	if err != nil {
		slog.Debug("lookupSessionTitles query failed", "error", err)
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var sid, title string
		if err := rows.Scan(&sid, &title); err == nil {
			result[sid] = title
		}
	}
	return result
}
