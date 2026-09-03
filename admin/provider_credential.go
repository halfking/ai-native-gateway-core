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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/settings"
)

const maxRotateCredentialPrimaryKeyBodyBytes = 64 << 10

func (h *Handler) addCredential(w http.ResponseWriter, r *http.Request, providerID int) {
	var req struct {
		Label            *string  `json:"label"`
		APIKey           string   `json:"api_key"`
		ExtraAPIKeys     []string `json:"extra_api_keys"` // 2026-08-10: multi-key rotation (N 倍放大免费额度)
		ConcurrencyLimit *int     `json:"concurrency_limit"`
		FpSlotLimit      *int     `json:"fp_slot_limit"`
		RPMLimit         *int     `json:"rpm_limit"`
		PlanType         *string  `json:"plan_type"`
		// 479: 并发/限流模式与队列参数（见 docs/会话优化v2/57）。
		ConcurrencyMode *string `json:"concurrency_mode"` // concurrency|rpm|tpm|disabled
		TPMLimit        *int    `json:"tpm_limit"`
		MaxQueueDepth   *int    `json:"max_queue_depth"`
		MaxQueueWaitMS  *int    `json:"max_queue_wait_ms"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key required")
		return
	}
	planType := "token"
	if req.PlanType != nil && *req.PlanType != "" {
		if !isValidPlanType(*req.PlanType) {
			writeError(w, http.StatusBadRequest, "invalid plan_type; allowed: token, token_plan, code_plan, agent_plan, monthly, free")
			return
		}
		planType = *req.PlanType
	}

	encrypted, err := h.encryptCred([]byte(req.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	// 加密 extra keys (用于 per-credential 多 key 轮转). 主 key 存 credentials,
	// extras 存 credential_keys 子表 (migration 076).
	var extraEncrypted [][]byte
	for i, k := range req.ExtraAPIKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		enc, encErr := h.encryptCred([]byte(k))
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, fmt.Sprintf("encrypt extra key %d failed: %v", i, encErr))
			return
		}
		extraEncrypted = append(extraEncrypted, []byte(enc))
	}

	label := "default"
	if req.Label != nil && *req.Label != "" {
		label = *req.Label
	}
	concurrencyLimit := 20
	if req.ConcurrencyLimit != nil {
		concurrencyLimit = *req.ConcurrencyLimit
	}
	fpSlotLimit := 20 // 2026-06-24: 5 → 20, matches DefaultDefaultLimit
	if req.FpSlotLimit != nil {
		fpSlotLimit = *req.FpSlotLimit
	}
	// 479: 并发模式默认 concurrency；校验取值。
	concurrencyMode := "concurrency"
	if req.ConcurrencyMode != nil && *req.ConcurrencyMode != "" {
		if !isValidConcurrencyMode(*req.ConcurrencyMode) {
			writeError(w, http.StatusBadRequest, "invalid concurrency_mode; allowed: concurrency, rpm, tpm, disabled")
			return
		}
		concurrencyMode = *req.ConcurrencyMode
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var id int
	err = h.db.QueryRow(ctx, `
		INSERT INTO credentials (provider_id, label, secret_ciphertext, status, concurrency_limit, fp_slot_limit, balance_usd, plan_type,
			                         concurrency_mode, rpm_limit, tpm_limit, max_queue_depth, max_queue_wait_ms)
			VALUES ($1, $2, $3, 'active', $4, $5, 1000.0, $6, $7, $8, $9, $10, $11)
			RETURNING id
		`, providerID, label, encrypted, concurrencyLimit, fpSlotLimit, planType,
		concurrencyMode, req.RPMLimit, req.TPMLimit, req.MaxQueueDepth, req.MaxQueueWaitMS).Scan(&id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create failed: "+err.Error())
		return
	}

	// 插入 extra keys 到 credential_keys 子表 (kid_index 从 1 起).
	for i, enc := range extraEncrypted {
		_, err = h.db.Exec(ctx, `
			INSERT INTO credential_keys (credential_id, kid_index, secret_ciphertext, status, tenant_id)
			VALUES ($1, $2, $3, 'active', public.get_current_tenant())
			ON CONFLICT (credential_id, kid_index) DO UPDATE SET secret_ciphertext = EXCLUDED.secret_ciphertext, status = 'active'
		`, id, i+1, enc)
		if err != nil {
			slog.Warn("addCredential: insert extra key failed", "credential_id", id, "kid_index", i+1, "error", err)
			// non-fatal: credential 已建, extra keys 可后续补.
		}
	}
	// audit round 2 L1: new keys on a brand-new credential don't have a cached
	// candidate entry yet, but a parallel GetCandidates call that ran between
	// the INSERT and now might have cached a no-extra-keys version. Invalidate
	// both candidate cache and key rotator so the first enrichment re-reads
	// the new credential_keys rows.
	if len(extraEncrypted) > 0 {
		provider.InvalidateCandidateCacheForCredential(id)
		provider.ResetKeyRotatorForCredential(id)
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
		h.runHealthCheck(pid, cid, "", taskID)
	}(providerID, id)

	provider.InvalidateAllCandidateCache()
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "message": "ok"})
}

func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, COALESCE(c.label,''), COALESCE(c.status,'active'),
		       COALESCE(c.trust_level,'trusted'), c.concurrency_limit,
		       COALESCE(c.fp_slot_limit, 20) AS fp_slot_limit,  -- 2026-06-24: 5→20
		       c.balance_usd::float8,
		       COALESCE(c.plan_type,'per_token') AS plan_type,
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
		       c.tags,
		       COALESCE(c.notes,''),
		       c.secret_ciphertext,
		       COALESCE(c.manual_disabled, false),
		       c.created_at,
		       c.updated_at,
			       COALESCE(c.concurrency_mode,'concurrency'),
			       c.rpm_limit,
			       c.tpm_limit,
			       c.max_queue_depth,
		       c.max_queue_wait_ms
		FROM credentials c
		WHERE c.provider_id = $1
		  -- 2026-08-31: 软删除的凭据不在任何列表中返回
		  AND c.status <> 'deleted'
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
		PlanType               string     `json:"plan_type"`
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
		EffectiveState         string     `json:"effective_state"`
		EffectiveReason        string     `json:"effective_reason,omitempty"`
		CreatedAt              *time.Time `json:"created_at"`
		UpdatedAt              *time.Time `json:"updated_at"`
		ConcurrencyMode        string     `json:"concurrency_mode"`
		RPMLimit               *int       `json:"rpm_limit"`
		TPMLimit               *int       `json:"tpm_limit"`
		MaxQueueDepth          *int       `json:"max_queue_depth"`
		MaxQueueWaitMS         *int       `json:"max_queue_wait_ms"`
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
			&c.PlanType,
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
			&c.ConcurrencyMode,
			&c.RPMLimit,
			&c.TPMLimit,
			&c.MaxQueueDepth,
			&c.MaxQueueWaitMS,
		); err != nil {
			slog.Warn("listCredentials scan failed", "error", err)
			continue
		}

		if balanceUSD.Valid {
			c.BalanceUSD = &balanceUSD.Float64
		}

		c.Tags = parseTags(tagsStr)
		state := deriveCredentialDisplayState(credentialStateInput{
			Status:          c.Status,
			LifecycleStatus: c.LifecycleStatus,
			Availability:    c.AvailabilityState,
			QuotaState:      c.QuotaState,
			HealthStatus:    c.HealthStatus,
			ManualDisabled:  c.ManualDisabled,
		})
		c.EffectiveState = string(state.State)
		c.EffectiveReason = state.Reason
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

// marshalCredentialTags renders PATCH tags into the JSON document stored in
// credentials.tags. The column is jsonb, so the value must be a JSON array
// (`[]` for empty); the pre-2026-08-23 comma-join wrote ""/"a,b" and PG rejected
// every tags-bearing PATCH with 22P02 invalid input syntax for type json.
// parseTags on the read side already decodes the array form.
func marshalCredentialTags(tags []string) (string, error) {
	b, err := json.Marshal(tags)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type updateCredentialRequest struct {
	Label            *string  `json:"label"`
	Status           *string  `json:"status"`
	ConcurrencyLimit *int     `json:"concurrency_limit"`
	FpSlotLimit      *int     `json:"fp_slot_limit"`
	EffectiveAt      *string  `json:"effective_at"`
	ExpiresAt        *string  `json:"expires_at"`
	Tags             []string `json:"tags"`
	Notes            *string  `json:"notes"`
	BalanceUSD       *float64 `json:"balance_usd"`
	PlanType         *string  `json:"plan_type"`
	ConcurrencyMode  *string  `json:"concurrency_mode"`
	RPMLimit         *int     `json:"rpm_limit"`
	TPMLimit         *int     `json:"tpm_limit"`
	MaxQueueDepth    *int     `json:"max_queue_depth"`
	MaxQueueWaitMS   *int     `json:"max_queue_wait_ms"`
}

func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	var req updateCredentialRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Status != nil && !isCredentialStatus(strings.TrimSpace(*req.Status)) {
		writeError(w, http.StatusBadRequest, "invalid status; allowed: active, cooling, degraded, quarantine, quota_expired, disabled, deleted")
		return
	}
	if req.ConcurrencyMode != nil && *req.ConcurrencyMode != "" && !isValidConcurrencyMode(*req.ConcurrencyMode) {
		writeError(w, http.StatusBadRequest, "invalid concurrency_mode; allowed: concurrency, rpm, tpm, disabled")
		return
	}
	if req.PlanType != nil && !isValidPlanType(*req.PlanType) {
		writeError(w, http.StatusBadRequest, "invalid plan_type; allowed: token, token_plan, code_plan, agent_plan, monthly, free")
		return
	}
	if req.RPMLimit != nil && *req.RPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "rpm_limit must be >= 0")
		return
	}
	if req.TPMLimit != nil && *req.TPMLimit < 0 {
		writeError(w, http.StatusBadRequest, "tpm_limit must be >= 0")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin update failed: "+err.Error())
		return
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !strings.Contains(rbErr.Error(), "tx is closed") {
			slog.Warn("credential update rollback failed", "credential_id", credID, "error", rbErr)
		}
	}()

	var currentConcurrency sql.NullInt32
	var currentFpSlot sql.NullInt32
	var previousPlan sql.NullString
	var currentStatus string
	var currentRevision int64
	if err := tx.QueryRow(ctx, `
		SELECT concurrency_limit, fp_slot_limit, plan_type, status, revision
		FROM credentials
		WHERE id = $1 AND provider_id = $2
		FOR UPDATE`, credID, providerID).Scan(&currentConcurrency, &currentFpSlot, &previousPlan, &currentStatus, &currentRevision); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "credential not found")
		} else {
			writeError(w, http.StatusInternalServerError, "load credential failed: "+err.Error())
		}
		return
	}
	// 2026-08-31: 'deleted' 是终态。凭据抽屉的状态下拉可以 PATCH 任意
	// status（含 active），不拦会把已删凭据复活回列表/路由。
	if currentStatus == "deleted" {
		writeError(w, http.StatusConflict, "credential is deleted (terminal state); re-create it instead")
		return
	}

	finalConcurrency := currentConcurrency
	if req.ConcurrencyLimit != nil {
		finalConcurrency = sql.NullInt32{Int32: int32(*req.ConcurrencyLimit), Valid: true}
	}
	finalFpSlot := currentFpSlot
	if req.FpSlotLimit != nil {
		finalFpSlot = sql.NullInt32{Int32: int32(*req.FpSlotLimit), Valid: true}
		if *req.FpSlotLimit < 1 {
			writeError(w, http.StatusBadRequest, "fp_slot_limit must be >= 1")
			return
		}
	}
	if finalConcurrency.Valid && finalFpSlot.Valid && finalFpSlot.Int32 > finalConcurrency.Int32 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("fp_slot_limit (%d) cannot exceed concurrency_limit (%d)", finalFpSlot.Int32, finalConcurrency.Int32))
		return
	}
	if req.FpSlotLimit != nil {
		var sysMax sql.NullInt32
		_ = tx.QueryRow(ctx, `SELECT (value #>> '{}')::int4 FROM settings_kv WHERE key = 'llmgw_fp_slot_max_per_credential'`).Scan(&sysMax)
		maxAllowed := 100
		if sysMax.Valid {
			maxAllowed = int(sysMax.Int32)
		}
		if *req.FpSlotLimit > maxAllowed {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("fp_slot_limit (%d) exceeds system max (%d)", *req.FpSlotLimit, maxAllowed))
			return
		}
	}

	sets := make([]string, 0, 12)
	args := make([]any, 0, 14)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if req.Label != nil {
		sets = append(sets, "label = "+arg(*req.Label))
	}
	if req.Status != nil {
		sets = append(sets, "status = "+arg(*req.Status))
	}
	if req.ConcurrencyLimit != nil {
		sets = append(sets, "concurrency_limit = "+arg(*req.ConcurrencyLimit))
	}
	if req.FpSlotLimit != nil {
		sets = append(sets, "fp_slot_limit = "+arg(*req.FpSlotLimit))
	}
	if req.EffectiveAt != nil {
		sets = append(sets, "effective_at = "+arg(*req.EffectiveAt))
	}
	if req.ExpiresAt != nil {
		sets = append(sets, "expires_at = "+arg(*req.ExpiresAt))
	}
	if req.Tags != nil {
		tagsJSON, mErr := marshalCredentialTags(req.Tags)
		if mErr != nil {
			writeError(w, http.StatusBadRequest, "invalid tags")
			return
		}
		sets = append(sets, "tags = "+arg(tagsJSON)+"::jsonb")
	}
	if req.Notes != nil {
		sets = append(sets, "notes = "+arg(*req.Notes))
	}
	if req.BalanceUSD != nil {
		sets = append(sets, "balance_usd = "+arg(*req.BalanceUSD))
	}
	if req.PlanType != nil {
		sets = append(sets, "plan_type = "+arg(*req.PlanType), "plan_type_updated_at = NOW()")
	}
	if req.ConcurrencyMode != nil {
		sets = append(sets, "concurrency_mode = "+arg(*req.ConcurrencyMode))
	}
	if req.RPMLimit != nil {
		sets = append(sets, "rpm_limit = "+arg(*req.RPMLimit))
	}
	if req.TPMLimit != nil {
		sets = append(sets, "tpm_limit = "+arg(*req.TPMLimit))
	}
	if req.MaxQueueDepth != nil {
		sets = append(sets, "max_queue_depth = "+arg(*req.MaxQueueDepth))
	}
	if req.MaxQueueWaitMS != nil {
		sets = append(sets, "max_queue_wait_ms = "+arg(*req.MaxQueueWaitMS))
	}
	if len(sets) == 0 {
		if err := tx.Commit(ctx); err != nil {
			writeError(w, http.StatusInternalServerError, "commit: "+err.Error())
			return
		}
		provider.InvalidateAllCandidateCache()
		writeJSON(w, http.StatusOK, map[string]any{"credential_id": credID, "revision": currentRevision, "message": "updated"})
		return
	}
	sets = append(sets, "updated_at = NOW()")
	args = append(args, credID, providerID)
	query := "UPDATE credentials SET " + strings.Join(sets, ", ") + fmt.Sprintf(" WHERE id = $%d AND provider_id = $%d RETURNING revision", len(args)-1, len(args))
	var revision int64
	if err := tx.QueryRow(ctx, query, args...).Scan(&revision); err != nil {
		writeError(w, http.StatusInternalServerError, "update credential failed: "+err.Error())
		return
	}
	if req.PlanType != nil && (!previousPlan.Valid || previousPlan.String != *req.PlanType) {
		if _, err := tx.Exec(ctx, `
			UPDATE credential_model_bindings cmb
			SET billing_mode = CASE WHEN $1 = 'token' THEN 'per_token' ELSE $1 END,
			    plan_type_origin = 'auto', updated_at = NOW()
			WHERE cmb.credential_id = $2 AND cmb.plan_type_origin = 'auto'`, *req.PlanType, credID); err != nil {
			writeError(w, http.StatusInternalServerError, "cascade credential model bindings failed: "+err.Error())
			return
		}
	}
	// Capture the fp_slot_limit change for the audit log. It is written
	// AFTER the main transaction commits (see below) so a failure in the
	// best-effort audit path can never roll back the credential update.
	// The previous code inserted into a non-existent `settings_history`
	// table inside the same transaction; that statement error aborted the
	// transaction and surfaced to the caller as "commit: ... rollback".
	var fpSlotAudit *settings.AuditEntry
	if req.FpSlotLimit != nil {
		actor := "admin"
		if v := r.Header.Get("X-Admin-User"); v != "" {
			actor = v
		}
		oldVal := "null"
		if currentFpSlot.Valid {
			oldVal = strconv.Itoa(int(currentFpSlot.Int32))
		}
		role := "admin"
		tenantID := "default"
		if ac := GetAuthContext(r); ac != nil {
			if ac.Role != "" {
				role = ac.Role
			}
			if ac.TenantID != "" {
				tenantID = ac.TenantID
			}
		}
		fpSlotAudit = &settings.AuditEntry{
			SettingKey:   fmt.Sprintf("credential:%d:fp_slot_limit", credID),
			TenantID:     tenantID,
			Action:       "update",
			OldValue:     json.RawMessage(fmt.Sprintf("%q", oldVal)),
			NewValue:     json.RawMessage(fmt.Sprintf("%q", strconv.Itoa(*req.FpSlotLimit))),
			OperatorUser: actor,
			OperatorRole: role,
			ClientIP:     clientIPFromRequest(r),
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit: "+err.Error())
		return
	}
	if fpSlotAudit != nil {
		settings.WriteAudit(ctx, h.db, *fpSlotAudit)
	}
	provider.InvalidateAllCandidateCache()

	// 2026-08-26 hot-reload: when concurrency_limit changes, refresh the
	// in-process Limiter semaphore so the new capacity takes effect for
	// new in-flight requests within the same PATCH round-trip — no service
	// restart, no 30s candCache wait. Also clear every sticky binding that
	// points at this credential so new sessions can re-enter load
	// balancing (otherwise L2 sticky would still pin new sessions to the
	// previous credential for up to 60s).
	if h.limiter != nil && req.ConcurrencyLimit != nil {
		h.limiter.SetCredentialCapacity(providerID, credID, *req.ConcurrencyLimit)
	}
	if h.stickyCache != nil {
		if cleared, err := h.stickyCache.ClearForCredential(credID); err != nil {
			slog.Warn("sticky hot-reload: clear failed on credential PATCH",
				"credential_id", credID, "error", err)
		} else if cleared > 0 {
			slog.Info("sticky hot-reload: cleared bindings on credential PATCH",
				"credential_id", credID, "cleared", cleared)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential_id": credID, "revision": revision, "message": "updated"})
}

// rotateCredentialPrimaryKey replaces one credential's primary secret without
// changing its identity or model bindings. The selected bound model is probed
// after commit so operators get immediate recovery evidence.
func (h *Handler) rotateCredentialPrimaryKey(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	h.rotateCredentialPrimaryKeyWithOptions(w, r, providerID, credID, false)
}

func (h *Handler) rotateCredentialPrimaryKeyWithOptions(w http.ResponseWriter, r *http.Request, providerID, credID int, allowEmptyModel bool) {
	var req struct {
		APIKey       string `json:"api_key"`
		RawModelName string `json:"raw_model_name"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRotateCredentialPrimaryKeyBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	apiKeyBlank := strings.TrimSpace(req.APIKey) == ""
	req.RawModelName = strings.TrimSpace(req.RawModelName)
	if apiKeyBlank || (!allowEmptyModel && req.RawModelName == "") {
		if apiKeyBlank {
			writeError(w, http.StatusBadRequest, "api_key required")
		} else {
			writeError(w, http.StatusBadRequest, "api_key and raw_model_name required")
		}
		return
	}

	encrypted, err := h.encryptCred([]byte(req.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encryption failed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin rotation failed")
		return
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !strings.Contains(rbErr.Error(), "tx is closed") {
			slog.Warn("credential primary key rotation rollback failed", "credential_id", credID, "error", rbErr)
		}
	}()

	if req.RawModelName != "" {
		var bindingExists bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM credentials c
				JOIN credential_model_bindings cmb ON cmb.credential_id = c.id
				JOIN provider_models pm ON pm.id = cmb.provider_model_id
				WHERE c.id = $1
				  AND c.provider_id = $2
				  AND pm.provider_id = c.provider_id
				  AND pm.raw_model_name = $3
				FOR UPDATE OF c, cmb, pm
			)`, credID, providerID, req.RawModelName).Scan(&bindingExists)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "validate model binding failed")
			return
		}
		if !bindingExists {
			writeError(w, http.StatusBadRequest, "raw_model_name is not bound to credential")
			return
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE credentials
		SET secret_ciphertext = $1, updated_at = NOW()
		WHERE id = $2 AND provider_id = $3`, encrypted, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "rotate primary key failed")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit rotation failed")
		return
	}

	provider.InvalidateCredentialKeyCache(credID)
	provider.InvalidateCandidateCacheForCredential(credID)
	provider.ResetKeyRotatorForCredential(credID)
	h.writeAuditLog(r, "credential.primary_key_rotated", "credential", credID, map[string]any{
		"provider_id":         providerID,
		"raw_model_name":      req.RawModelName,
		"primary_key_rotated": true,
	})

	probeStatus := h.queueCredentialRotationProbe(credID, req.RawModelName)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"message":      "primary key rotated",
		"probe_status": probeStatus,
		"probe_queued": probeStatus == "queued",
	})
}

func (h *Handler) queueCredentialRotationProbe(credID int, rawModelName string) string {
	if h.modelProbe == nil {
		return "not_configured"
	}
	if err := h.modelProbe.SubmitManualProbe(credID, rawModelName); err != nil {
		slog.Warn("credential primary key rotation probe submission failed",
			"credential_id", credID,
			"model", rawModelName,
			"error", err,
		)
		return "queue_unavailable"
	}
	return "queued"
}

// deleteCredential 软删除凭据（status → 'deleted'）。
//
// 2026-08-31 operator request: 凭据删除后将状态置为 'deleted'，在所有
// 列表中不再出现。区别于 updateCredential 将 status 置为 'disabled'：
// 'disabled' 仍可在 UI 的"已停用"分组内恢复；'deleted' 是终态，列表
// 端点（listCredentials / listProviders / v_routable_credential_models）
// 一律通过 status = 'active' 过滤自然排除它。model_offers、credential_keys
// 等子表保留行以维持 FK 与历史日志完整。
//
// 同步失效候选缓存、清理 sticky 路由、并写一条 routing_audit_log 记录
// 终态来源（管理员 / API key / 触发的具体路径）。
func (h *Handler) deleteCredential(w http.ResponseWriter, r *http.Request, providerID, credID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// 2026-08-31: 软删除 → status='deleted' + lifecycle_status='retired'。
	// 只改 status 不够：domains/credential、providerprofile 等域代码以
	// lifecycle_status='active' 为存活判据，batchRecover 等后台任务也按
	// lifecycle 过滤。两轴同时置终态，任一过滤路径都不会再命中。
	// 仅当当前 status 不是 'deleted' 时执行 UPDATE，避免重复点击产生
	// 空 audit 行。
	tag, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET status = 'deleted',
		    lifecycle_status = 'retired',
		    updated_at = NOW()
		WHERE id = $1
		  AND provider_id = $2
		  AND status <> 'deleted'
	`, credID, providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		// 已是 'deleted' 状态；幂等返回 200，避免前端重复点按钮报错。
		writeJSON(w, http.StatusOK, map[string]any{
			"message":       "already deleted",
			"credential_id": credID,
		})
		return
	}

	provider.InvalidateAllCandidateCache()
	provider.InvalidateCredentialKeyCache(credID)
	provider.ResetKeyRotatorForCredential(credID)
	if h.stickyCache != nil {
		if cleared, scErr := h.stickyCache.ClearForCredential(credID); scErr != nil {
			slog.Warn("deleteCredential: clear sticky failed", "credential_id", credID, "error", scErr)
		} else if cleared > 0 {
			slog.Info("deleteCredential: cleared sticky bindings", "credential_id", credID, "cleared", cleared)
		}
	}

	h.writeAuditLog(r, "credential.deleted", "credential", credID, map[string]any{
		"provider_id":  providerID,
		"soft_deleted": true,
		"new_status":   "deleted",
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"message":       "deleted",
		"credential_id": credID,
		"new_status":    "deleted",
	})
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
	var tenantID string
	err := h.db.QueryRow(ctx, `
		SELECT fp_slot_limit, COALESCE(tenant_id, 'default')
		FROM credentials
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID).Scan(&fpSlotLimit, &tenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	// Reset all slots
	deletedSlots, deletedPins, err := h.fpSlots.ResetSlotsForTenant(ctx, credID, fpSlotLimit, tenantID)
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
	if err := readJSONRequired(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var tenantID string
	if err := h.db.QueryRow(ctx, `
		SELECT COALESCE(tenant_id, 'default')
		FROM credentials
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID).Scan(&tenantID); err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	released, err := h.fpSlots.ReleaseSlotForTenant(ctx, credID, body.SlotIndex, tenantID)
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
	var tenantID string
	err := h.db.QueryRow(ctx, `
		SELECT fp_slot_limit, COALESCE(tenant_id, 'default')
		FROM credentials
		WHERE id = $1 AND provider_id = $2
	`, credID, providerID).Scan(&fpSlotLimit, &tenantID)
	if err != nil {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}

	limit, holders, details, healthySlots := h.fpSlots.DetailedStatsForTenant(ctx, credID, fpSlotLimit, tenantID)

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
		SELECT scoped_session_id, title
		FROM session_titles
		WHERE scoped_session_id = ANY($1)
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

// getProviderErrorStats 返回指定 provider 的错误统计。
// 审计修复 (2026-08-30)：P1-8 - Admin API 错误统计展示
//
// GET /admin/providers/{id}/error-stats?hours=24&limit=100
//
// 查询参数：
//   - hours: 统计时间范围（小时），默认 24
//   - limit: 返回记录数限制，默认 100
//   - resolved: 是否只显示已解决的错误（true/false/all），默认 all
func (h *Handler) getProviderErrorStats(w http.ResponseWriter, r *http.Request, providerID int) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// 解析查询参数（带边界校验）
	hours := 24
	if hoursStr := r.URL.Query().Get("hours"); hoursStr != "" {
		if parsed, err := strconv.Atoi(hoursStr); err == nil && parsed > 0 && parsed <= 720 {
			hours = parsed
		} else {
			slog.Debug("getProviderErrorStats: invalid hours, using default",
				"input", hoursStr, "default", 24)
		}
	}

	limit := 100
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		} else {
			slog.Debug("getProviderErrorStats: invalid limit, using default",
				"input", limitStr, "default", 100)
		}
	}

	resolvedFilter := r.URL.Query().Get("resolved") // "true", "false", "all"
	if resolvedFilter == "" {
		resolvedFilter = "all"
	}

	// 2026-09-01 (P0-1 24h-audit round2): optional credential attribution
	// filter. provider_error_details.credential_id was added by migration 639.
	// Empty / invalid / "all" → aggregate every credential (previous
	// behaviour). Valid ints narrow the result to that credential only, which
	// is what the credential-detail panel needs ("this credential's errors").
	credentialFilter := r.URL.Query().Get("credential_id")
	if credentialFilter != "" && credentialFilter != "all" {
		if parsed, err := strconv.Atoi(credentialFilter); err != nil || parsed <= 0 {
			slog.Debug("getProviderErrorStats: invalid credential_id, aggregating all",
				"input", credentialFilter)
			credentialFilter = ""
		}
	} else {
		credentialFilter = ""
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 验证 provider 存在性（与 getProvider 保持一致语义）
	var exists bool
	if err := h.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM providers WHERE id = $1 AND tenant_id = 'default' AND deleted_at IS NULL)`,
		providerID).Scan(&exists); err != nil {
		slog.Error("getProviderErrorStats: provider existence check failed",
			"provider_id", providerID, "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}

	// 参数化 SQL：所有用户输入通过 $N 传递，无字符串拼接
	// credential_id 过滤（2026-09-01 P0-1）：$5 为空时聚合全部凭据；非空时
	// 按 provider_error_details.credential_id::text 精确匹配（迁移 639 新列，
	// TEXT 类型，来源 candidate_failure_logs_hot.credential_id::text）。
	query := `
		SELECT
			model_name,
			endpoint,
			credential_id::text,
			error_type,
			error_code,
			error_message,
			aggregation_bucket,
			occurrences,
			first_seen_at,
			last_seen_at,
			resolved,
			created_at,
			updated_at
		FROM provider_error_details
		WHERE provider_id = $1
		  AND aggregation_bucket >= NOW() - ($3 * INTERVAL '1 hour')
		  AND ($4 = 'all' OR resolved = ($4 = 'true'))
		  AND ($5 = '' OR credential_id::text = $5)
		ORDER BY last_seen_at DESC, occurrences DESC
		LIMIT $2
	`

	rows, err := h.db.Query(ctx, query, providerID, limit, hours, resolvedFilter, credentialFilter)
	if err != nil {
		slog.Error("getProviderErrorStats query failed", "error", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type errorStat struct {
		ModelName         string    `json:"model_name"`
		Endpoint          string    `json:"endpoint"`
		CredentialID      *string   `json:"credential_id"` // 迁移639前的历史行为 NULL
		ErrorType         string    `json:"error_type"`
		ErrorCode         *string   `json:"error_code"`
		ErrorMessage      string    `json:"error_message"`
		AggregationBucket time.Time `json:"aggregation_bucket"`
		Occurrences       int       `json:"occurrences"`
		FirstSeenAt       time.Time `json:"first_seen_at"`
		LastSeenAt        time.Time `json:"last_seen_at"`
		Resolved          bool      `json:"resolved"`
		CreatedAt         time.Time `json:"created_at"`
		UpdatedAt         time.Time `json:"updated_at"`
	}

	var stats []errorStat
	for rows.Next() {
		var s errorStat
		if err := rows.Scan(
			&s.ModelName,
			&s.Endpoint,
			&s.CredentialID,
			&s.ErrorType,
			&s.ErrorCode,
			&s.ErrorMessage,
			&s.AggregationBucket,
			&s.Occurrences,
			&s.FirstSeenAt,
			&s.LastSeenAt,
			&s.Resolved,
			&s.CreatedAt,
			&s.UpdatedAt,
		); err != nil {
			slog.Warn("getProviderErrorStats scan failed", "error", err)
			continue
		}
		stats = append(stats, s)
	}

	// 审计修复 (2026-08-30)：检查迭代过程中的错误
	if err := rows.Err(); err != nil {
		slog.Warn("getProviderErrorStats rows iteration error",
			"provider_id", providerID, "error", err)
	}

	if stats == nil {
		stats = []errorStat{}
	}

	// 统计汇总
	var totalOccurrences int64
	resolvedCount := 0
	for _, s := range stats {
		totalOccurrences += int64(s.Occurrences)
		if s.Resolved {
			resolvedCount++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"provider_id":       providerID,
		"time_range_hours":  hours,
		"credential_id":     nilIfEmpty(credentialFilter),
		"total_errors":      len(stats),
		"total_occurrences": totalOccurrences,
		"resolved_count":    resolvedCount,
		"unresolved_count":  len(stats) - resolvedCount,
		"errors":            stats,
	})
}
