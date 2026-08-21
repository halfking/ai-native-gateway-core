package admin

// provider_refresh.go — extracted from providers.go (2026-06-22 audit §3
// single-file-bloat remediation, seventh cut, refresh slice). Owns the
// background-refresh machinery: per-provider run state, the picker that
// picks a probe model, the "force refresh from vendor API" trigger, and
// the reload-on-config-change list hook.
//
// Endpoints (provider_refresh.go):
//   POST /api/providers/{id}/refresh-models       startRefreshProviderModels
//   GET  /api/admin/providers/{id}/refresh-state  (handled inline in listProviders)
//
// Helpers:
//   bgPickProbeModel — wraps the shared pickProbeModel adapter for the bg
//     worker path. Thin shim that exists so providers.go can pass through
//     without importing the bg package directly.
//   providerRefreshState / getProviderRefreshState / recordProviderRefresh /
//     getProviderRefresh — per-provider last-run state, protected by h.refreshMu.
//   startRefreshProviderModels — POST /refresh-models trigger.
//
// Self-contained: stdlib only + same-package helpers (writeJSON / writeError /
// h.db / h.bgTasks / h.refreshMu / h.refreshState / providersRefreshSingleton /
// providerRefreshRun). The bg package is reached via h.bgTasks, not via a
// direct import, to avoid an import cycle.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func bgPickProbeModel(ctx context.Context, db *pgxpool.Pool, credID int) (pickProbeResult, error) {
	return pickProbeModelForCredentialAdapter(ctx, db, credID)
}

// ── Per-provider model list refresh (force fetch from vendor API) ───────
//
// POST /api/providers/{id}/refresh-models
//
//	202 Accepted    : { "accepted": true, "reason": "started", "run": {...} }
//	404             : provider not found
//	503             : discovery service not available
//
// GET /api/providers/{id}/refresh-models
//
//	200             : { "running": {...} | null, "latest": {...} | null }
//
// The endpoint differs from the global /api/models/discover in two ways:
//  1. scope is restricted to a single provider (and its credentials)
//  2. it tracks per-provider status so the UI can show a live progress
//     message ("正在从供应商读取数据…") and re-fetch offers on success.
type providerRefreshStatus string

const (
	providerRefreshIdle    providerRefreshStatus = "idle"
	providerRefreshRunning providerRefreshStatus = "running"
	providerRefreshSucceed providerRefreshStatus = "succeeded"
	providerRefreshFailed  providerRefreshStatus = "failed"
)

type providerRefreshRun struct {
	RunID              string                `json:"run_id"`
	ProviderID         int                   `json:"provider_id"`
	Status             providerRefreshStatus `json:"status"`
	StartedAt          time.Time             `json:"started_at"`
	FinishedAt         *time.Time            `json:"finished_at,omitempty"`
	HeartbeatAt        *time.Time            `json:"heartbeat_at,omitempty"`
	CredentialsScanned int                   `json:"credentials_scanned"`
	ModelsUpserted     int                   `json:"models_upserted"`
	CredentialsFailed  int                   `json:"credentials_failed"`
	Errors             []string              `json:"errors,omitempty"`
	Message            string                `json:"message,omitempty"`
}

type providerRefreshState struct {
	mu     sync.Mutex
	latest map[int]*providerRefreshRun
}

// getProviderRefreshState lazily initialises the in-memory state tracker
// the first time a refresh endpoint touches it.
func (h *Handler) getProviderRefreshState() *providerRefreshState {
	h.refreshMu.Lock()
	defer h.refreshMu.Unlock()
	if h.refreshState == nil {
		h.refreshState = &providerRefreshState{latest: make(map[int]*providerRefreshRun)}
	}
	return h.refreshState
}

func (h *Handler) recordProviderRefresh(providerID int, run *providerRefreshRun) {
	st := h.getProviderRefreshState()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.latest[providerID] = run
}

func (h *Handler) getProviderRefresh(providerID int) *providerRefreshRun {
	st := h.getProviderRefreshState()
	st.mu.Lock()
	defer st.mu.Unlock()
	if run, ok := st.latest[providerID]; ok {
		copy := *run
		return &copy
	}
	return nil
}

func completeProviderRefreshRun(run *providerRefreshRun, credentialsScanned int, credentialsErr error, modelsUpserted, credentialsFailed int, errs []string, finishedAt time.Time) {
	run.FinishedAt = &finishedAt
	run.CredentialsScanned = credentialsScanned
	run.ModelsUpserted = modelsUpserted
	run.CredentialsFailed = credentialsFailed
	run.Errors = errs
	if credentialsErr != nil {
		run.Status = providerRefreshFailed
		run.Errors = append(run.Errors, fmt.Sprintf("读取凭据失败: %s", credentialsErr.Error()))
		run.Message = "刷新失败：读取凭据列表失败"
		return
	}
	if credentialsFailed > 0 && modelsUpserted == 0 {
		run.Status = providerRefreshFailed
		run.Message = "刷新失败：所有凭据都返回错误"
		return
	}
	if credentialsScanned == 0 {
		run.Status = providerRefreshSucceed
		run.Message = "未找到符合条件的可刷新凭据（请检查凭据状态和 provider 归属）"
		return
	}
	run.Status = providerRefreshSucceed
	run.Message = fmt.Sprintf("新增/更新 %d 个模型（凭据 %d 个，失败 %d 个）",
		modelsUpserted, credentialsScanned, credentialsFailed)
}

func (h *Handler) startRefreshProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	var (
		providerCode    string
		providerName    string
		providerEnabled bool
	)
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(code,''), COALESCE(display_name,''), enabled
		FROM providers WHERE id = $1 AND tenant_id = 'default'
	`, providerID).Scan(&providerCode, &providerName, &providerEnabled)
	if err != nil {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if !providerEnabled {
		writeError(w, http.StatusConflict, "provider is disabled")
		return
	}

	runID := fmt.Sprintf("provider-refresh-%d-%d", providerID, time.Now().UnixNano())
	now := time.Now()
	run := &providerRefreshRun{
		RunID:       runID,
		ProviderID:  providerID,
		Status:      providerRefreshRunning,
		StartedAt:   now,
		HeartbeatAt: &now,
		Message:     "从供应商接口读取模型列表",
	}
	h.recordProviderRefresh(providerID, run)

	slog.Info("provider refresh started",
		"run_id", runID,
		"provider_id", providerID,
		"provider_code", providerCode,
		"provider_name", providerName,
	)

	// Run in background so the POST returns 202 immediately.  We use a
	// detached context so a client disconnect does not abort the
	// upstream calls mid-flight.
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer bgCancel()

		creds, credsErr := h.fetchActiveCredentialsForProvider(bgCtx, providerID)

		var (
			totalUpserted int
			totalFailed   int
			errs          []string
		)
		if credsErr == nil {
			for _, cred := range creds {
				heartbeat := time.Now()
				run.HeartbeatAt = &heartbeat
				h.recordProviderRefresh(providerID, run)

				upserted, failed, err := h.discoverAndUpsertForCredential(bgCtx, cred)
				if err != nil {
					totalFailed++
					errs = append(errs, fmt.Sprintf("credential #%d %s: %s", cred.id, cred.label, err.Error()))
					slog.Warn("provider refresh: credential failed",
						"run_id", runID,
						"provider_id", providerID,
						"credential_id", cred.id,
						"error", err,
					)
					continue
				}
				totalUpserted += upserted
				totalFailed += failed
			}
		} else {
			slog.Error("provider refresh: credentials query failed",
				"run_id", runID,
				"provider_id", providerID,
				"error", credsErr,
			)
		}

		completeProviderRefreshRun(run, len(creds), credsErr, totalUpserted, totalFailed, errs, time.Now())
		h.recordProviderRefresh(providerID, run)

		// 2026-06-19 audit: provider refresh re-writes the rows
		// /api/routing/available-models aggregates.  Invalidate the
		// process-wide cache so the next admin page render sees the
		// new model list.
		InvalidateAvailableModelsCache()

		slog.Info("provider refresh finished",
			"run_id", runID,
			"provider_id", providerID,
			"credentials", len(creds),
			"upserted", totalUpserted,
			"failed", totalFailed,
		)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"accepted": true,
		"reason":   "started",
		"run":      run,
	})
}

func (h *Handler) getRefreshProviderModelsStatus(w http.ResponseWriter, r *http.Request, providerID int) {
	run := h.getProviderRefresh(providerID)
	resp := map[string]any{
		"running": nil,
		"latest":  nil,
	}
	if run != nil {
		if run.Status == providerRefreshRunning {
			resp["running"] = run
		} else {
			resp["latest"] = run
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// credentialRowLite is a slim credential view used only by the per-provider
// refresh path; we keep the columns tight so the background goroutine does
// not pull fields it never reads.
type credentialRowLite struct {
	id                 int
	label              string
	providerID         int
	providerName       string
	baseURL            string
	protocol           string
	catalogCode        string
	secretCipher       []byte
	modelsEndpointTpl  *string
	discoveryStrategy  string
	modelsManifestJSON *string
}

func (h *Handler) fetchActiveCredentialsForProvider(ctx context.Context, providerID int) ([]credentialRowLite, error) {
	rows, err := h.db.Query(ctx, `
		SELECT
			c.id, COALESCE(c.label,''), p.id, p.display_name,
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN LATERAL (
			SELECT pc.models_endpoint_template,
				COALESCE(pc.discovery_strategy, 'auto') AS discovery_strategy,
				pc.models_manifest_json
			FROM provider_catalog pc
			WHERE pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
			ORDER BY pc.catalog_version DESC, pc.updated_at DESC
			LIMIT 1
		) pc ON TRUE
		WHERE c.provider_id = $1
		  AND c.tenant_id = 'default'
		  AND p.tenant_id = 'default'
		  AND c.secret_ciphertext IS NOT NULL
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND p.enabled = TRUE
		  AND (
		      c.status = 'active'
		      OR COALESCE(c.api_models_ok, FALSE) = TRUE
		  )
		  AND (
		      c.lifecycle_status IS NULL
		      OR c.lifecycle_status = 'active'
		      OR (c.lifecycle_status = 'disabled' AND COALESCE(c.api_models_ok, FALSE) = TRUE)
		  )
		ORDER BY c.id
	`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []credentialRowLite
	for rows.Next() {
		var c credentialRowLite
		if err := rows.Scan(&c.id, &c.label, &c.providerID, &c.providerName,
			&c.baseURL, &c.protocol, &c.catalogCode,
			&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON); err != nil {
			return nil, fmt.Errorf("scan credential row: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return out, nil
}

func (h *Handler) loadCredentialRowLite(ctx context.Context, providerID, credID int) (credentialRowLite, error) {
	var c credentialRowLite
	err := h.db.QueryRow(ctx, `
		SELECT
			c.id, COALESCE(c.label,''), p.id, p.display_name,
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.id = $1 AND c.provider_id = $2
	`, credID, providerID).Scan(&c.id, &c.label, &c.providerID, &c.providerName,
		&c.baseURL, &c.protocol, &c.catalogCode,
		&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON)
	return c, err
}

// loadCredentialRowLiteAny loads a credential by id alone (no provider
// scoping); used only by diagnostic probes that already know the cred id.
