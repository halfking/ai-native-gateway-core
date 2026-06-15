package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/secret"
)

type Handler struct {
	db          *pgxpool.Pool
	secret      string
	encKey      []byte
	keyring     *secret.Keyring // AES-256-GCM keyring; nil → Fernet legacy
	discSvc     *discovery.Service
	credCycler  *bg.CredentialCycler
	credRecov   *bg.CredentialRecovery
	envCleaner  *bg.EnvelopeCleaner
	stickyClean *bg.StickyCleaner
	taxSync     *bg.TaxonomySync
	probeV2     *bg.CredentialProbeV2    // 900-series: mini-chat probe (spec §5)
	probePicker *bg.DefaultProbePicker   // 900-series: default probe model (spec §4)
	fpSlots     *credentialfpslot.Manager
	peakCollector interface {
		Acquire(credID int64, model string)
		Release(credID int64, model string)
		Stats() map[string]interface{}
		GetLiveConcurrent(credID int64, model string) int64
	}
	// autoIndexRefresher is wired from cmd/gateway/main.go to let the
	// /api/admin/auto-route/refresh endpoint trigger immediate
	// credential_model_index refresh. nil in test mode.
	autoIndexRefresher interface {
		RefreshOnce(ctx context.Context) error
	}
	refreshMu   sync.Mutex               // guards lazy init of refreshState
	refreshState *providerRefreshState   // per-provider "refresh model list" tracking (see providers.go)
}

func NewHandler(db *pgxpool.Pool, secretKey string, encKey []byte) *Handler {
	return &Handler{db: db, secret: secretKey, encKey: encKey}
}

// SetKeyring configures AES-256-GCM key rotation.  Call this at startup after
// loading the KEYRING_JSON environment variable.
func (h *Handler) SetKeyring(kr *secret.Keyring) {
	h.keyring = kr
}

// encryptCred encrypts a plaintext credential using AES-256-GCM (if keyring is
// configured) or Fernet-CBC legacy (backward-compat fallback).
// Returns the envelope string ready to store in key_ciphertext.
func (h *Handler) encryptCred(plaintext []byte) (string, error) {
	if h.keyring != nil {
		return secret.EncryptAESGCM(plaintext, h.keyring)
	}
	enc, err := encryptFernet(plaintext, h.encKey)
	if err != nil {
		return "", err
	}
	return string(enc), nil
}

// decryptCred decrypts a credential stored as either a v1 AES-GCM envelope or
// a legacy Fernet token.  Returns (plaintext, isLegacy, error).
// When isLegacy=true the caller MAY re-encrypt and update the DB row.
func (h *Handler) decryptCred(ciphertext string) (string, bool, error) {
	if secret.IsV1Envelope(ciphertext) {
		if h.keyring == nil {
			return "", false, errorf("AES-GCM keyring not configured")
		}
		pt, err := secret.DecryptAESGCM([]byte(ciphertext), h.keyring)
		if err != nil {
			return "", false, err
		}
		return string(pt), false, nil
	}
	// Legacy Fernet path
	if len(h.encKey) != 32 {
		return "", false, errorf("legacy encryption key not configured")
	}
	pt, err := decryptFernet([]byte(ciphertext), h.encKey)
	return pt, err == nil, err
}

// decryptCredStr is a convenience wrapper over decryptCred that returns only
// the plaintext string and an error, discarding the isLegacy flag.
// Use this where lazy re-encryption is not needed.
func (h *Handler) decryptCredStr(ciphertext string) (string, error) {
	pt, _, err := h.decryptCred(ciphertext)
	return pt, err
}

func (h *Handler) SetDiscoveryService(svc *discovery.Service) {
	h.discSvc = svc
}

func (h *Handler) SetBackgroundServices(credCycler *bg.CredentialCycler, credRecov *bg.CredentialRecovery, envCleaner *bg.EnvelopeCleaner, stickyClean *bg.StickyCleaner, taxSync *bg.TaxonomySync) {
	h.credCycler = credCycler
	h.credRecov = credRecov
	h.envCleaner = envCleaner
	h.stickyClean = stickyClean
	h.taxSync = taxSync
}

// SetProbeServices injects the 900-series background services (spec §4-5).
func (h *Handler) SetProbeServices(probeV2 *bg.CredentialProbeV2, picker *bg.DefaultProbePicker) {
	h.probeV2 = probeV2
	h.probePicker = picker
}

func (h *Handler) SetFpSlots(m *credentialfpslot.Manager) {
	h.fpSlots = m
}

// SetPeakCollector injects the concurrency peak collector so the admin
// API can report live stats. Accepts a structural interface to avoid
// an import cycle with the bg package's optional methods.
func (h *Handler) SetPeakCollector(pc interface {
	Acquire(credID int64, model string)
	Release(credID int64, model string)
	Stats() map[string]interface{}
	GetLiveConcurrent(credID int64, model string) int64
}) {
	h.peakCollector = pc
}

// SetAutoIndexRefresher injects the bg.AutoIndexRefresher so the
// /api/admin/auto-route/refresh endpoint can trigger an immediate
// credential_model_index refresh. Accepts a structural interface to
// avoid an import cycle with the bg package.
func (h *Handler) SetAutoIndexRefresher(r interface {
	RefreshOnce(ctx context.Context) error
}) {
	h.autoIndexRefresher = r
}

func (h *Handler) fpSlotsDefaultLimit() int {
	if h.fpSlots == nil {
		return 5
	}
	return h.fpSlots.DefaultLimit()
}

func (h *Handler) admin(fn http.HandlerFunc) http.HandlerFunc {
	return adminMiddleware(fn, h.db, h.secret)
}

// superAdmin wraps the handler with auth + super_admin role enforcement.
// tenant_admin gets 403 Forbidden.
func (h *Handler) superAdmin(fn http.HandlerFunc) http.HandlerFunc {
	return superAdminMiddleware(fn, h.db, h.secret)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public routes (no Bearer admin key)
	mux.HandleFunc("/api/auth/token", h.handleLogin)
	// JWT-authenticated routes (JWT or admin key)
	mux.HandleFunc("/v1/keys/apply", h.handleV1KeysApply)

	admin := h.admin
	// JWT-authenticated routes (JWT or admin key)
	mux.HandleFunc("/api/auth/me", admin(h.handleAuthMe))
	mux.HandleFunc("/api/auth/change-password", admin(h.handleChangePassword))
	mux.HandleFunc("/api/users", h.superAdmin(h.handleUsers))
	mux.HandleFunc("/api/users/", admin(h.handleUsers))
	mux.HandleFunc("/api/routing/resolve", admin(h.handleRoutingResolve))
	mux.HandleFunc("/api/routing/overview", admin(h.handleRoutingOverview))
	mux.HandleFunc("/api/routing/model-tree", admin(h.handleRoutingModelTree))
	mux.HandleFunc("/api/routing/policy", h.superAdmin(h.handleRoutingPolicy))
	mux.HandleFunc("/api/routing/featured", h.superAdmin(h.handleRoutingFeatured))
	mux.HandleFunc("/api/routing/available-models", admin(h.handleRoutingAvailableModels))
	mux.HandleFunc("/api/routing/available-models/raw", admin(h.handleRoutingAvailableModelsRaw))
	mux.HandleFunc("/api/routing/decisions", admin(h.handleRoutingDecisions))
	mux.HandleFunc("/api/routing/health", admin(h.handleRoutingHealth))
	mux.HandleFunc("/api/routing/audit", h.superAdmin(h.handleRoutingAudit))
	mux.HandleFunc("/api/routing/probe", h.superAdmin(h.handleRoutingProbe))
	mux.HandleFunc("/api/routing/manual-priority", h.superAdmin(h.handleRoutingManualPriority))
	mux.HandleFunc("/api/routing/score-details", admin(h.handleRoutingScoreDetails))
	mux.HandleFunc("/api/routing/scoring-weights", h.superAdmin(h.handleRoutingScoringWeights))
	mux.HandleFunc("/api/routing/featured-models", admin(h.handleRoutingFeaturedModelsDynamic))
	mux.HandleFunc("/api/telemetry/decision-log", admin(h.handleTelemetryDecisionLog))
	mux.HandleFunc("/api/telemetry/request-log", admin(h.handleTelemetryRequestLog))
	mux.HandleFunc("/api/telemetry/batch", admin(h.handleTelemetryBatch))
	mux.HandleFunc("/api/providers", h.superAdmin(h.handleProvidersRoot))
	mux.HandleFunc("/api/providers/", h.superAdmin(h.handleProviders))
	mux.HandleFunc("/api/providers/seed-from-catalog", h.superAdmin(h.handleSeedFromCatalog))
	mux.HandleFunc("/api/providers/credentials/", h.superAdmin(h.handleForceRecover))
	mux.HandleFunc("/api/keys", admin(h.handleKeysRoot))
	mux.HandleFunc("/api/keys/", admin(h.handleKeys))
	mux.HandleFunc("/api/key-applications", admin(h.handleKeyApplicationsList))
	mux.HandleFunc("/api/key-applications/", admin(h.handleKeyApplications))
	mux.HandleFunc("/api/models", h.superAdmin(h.handleModelsRoot))
	mux.HandleFunc("/api/models/", h.superAdmin(h.handleModels))
	mux.HandleFunc("/api/usage", admin(h.handleUsageSummary))
	mux.HandleFunc("/api/usage/", admin(h.handleUsage))
	mux.HandleFunc("/api/logs", admin(h.handleLogsRoot))
	mux.HandleFunc("/api/logs/", admin(h.handleLogs))
	mux.HandleFunc("/api/catalog", h.superAdmin(h.handleCatalogRoot))
	mux.HandleFunc("/api/catalog/", h.superAdmin(h.handleCatalog))
	mux.HandleFunc("/api/tags", h.superAdmin(h.handleTags))
	mux.HandleFunc("/api/system/background-tasks", h.superAdmin(h.handleSystemTasks))
	mux.HandleFunc("/api/system/version", admin(h.handleSystemVersion))
	mux.HandleFunc("/api/tasks/", admin(h.handleTasks))
	mux.HandleFunc("/api/free-pool/status", h.superAdmin(h.handleFreePoolStatus))
	mux.HandleFunc("/api/free-pool/register", h.superAdmin(h.handleFreePoolRegister))
	mux.HandleFunc("/api/free-pool/models", h.superAdmin(h.handleFreePoolModels))
	mux.HandleFunc("/api/free-pool/catalog", h.superAdmin(h.handleFreePoolCatalog))
	mux.HandleFunc("/api/free-pool/available", h.superAdmin(h.handleFreePoolAvailable))
	mux.HandleFunc("/api/free-pool/register-all", h.superAdmin(h.handleFreePoolRegisterAll))
	mux.HandleFunc("/api/free-pool/import-env", h.superAdmin(h.handleFreePoolImportEnv))
	mux.HandleFunc("/api/free-pool/bootstrap", h.superAdmin(h.handleFreePoolBootstrap))
	mux.HandleFunc("/api/free-pool/bridge-oauth", h.superAdmin(h.handleFreePoolBridgeOAuth))
	mux.HandleFunc("/api/free-pool/discover", h.superAdmin(h.handleFreePoolDiscover))
	mux.HandleFunc("/api/free-pool/bulk-register", h.superAdmin(h.handleFreePoolBulkRegister))
	mux.HandleFunc("/api/free-pool/methods", h.superAdmin(h.handleFreePoolMethods))
	mux.HandleFunc("/api/free-pool/discovery-status", h.superAdmin(h.handleFreePoolDiscoveryStatus))
	mux.HandleFunc("/api/free-pool/signup-hub", h.superAdmin(h.handleFreePoolSignupHub))
	mux.HandleFunc("/api/free-pool/temp-email", h.superAdmin(h.handleFreePoolTempEmail))
	mux.HandleFunc("/api/free-pool/temp-email/poll", h.superAdmin(h.handleFreePoolTempEmailPoll))
	mux.HandleFunc("/api/free-pool/probe", h.superAdmin(h.handleFreePoolProbe))
	mux.HandleFunc("/api/free-pool/quick-entry", h.superAdmin(h.handleFreePoolQuickEntry))
	mux.HandleFunc("/api/free-pool/keys", h.superAdmin(h.handleFreePoolKeysRouter))
	mux.HandleFunc("/api/free-pool/keys/", h.superAdmin(h.handleFreePoolKeysSubRouter))
	mux.HandleFunc("/api/pricing/", h.superAdmin(h.handlePricing))
	mux.HandleFunc("/api/config/default-limits", h.superAdmin(h.handleDefaultLimits))

	// Peak concurrency + auto-tune endpoints (Phase 2).
	if h.db != nil {
		peakH := NewPeakHandlers(h.db)
		peakH.RegisterPeakRoutes(mux, h.superAdmin)

		// v2.0 auto-route admin endpoints (Phase 3).
		autoH := NewAutoRouteHandlers(h.db)
		if h.autoIndexRefresher != nil {
			autoH.SetIndexRefresher(h.autoIndexRefresher)
		}
		autoH.RegisterAutoRouteRoutes(mux, admin)

		// Phase 1 work type config CRUD.
		wtH := NewWorkTypeHandlers(h.db)
		wtH.RegisterWorkTypeRoutes(mux, admin)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("json marshal failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"detail":"json marshal failed"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
	w.Write([]byte("\n"))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"detail": msg},
	})
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

func queryInt(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func queryBool(r *http.Request, key string) bool {
	s := r.URL.Query().Get(key)
	return strings.EqualFold(s, "true") || s == "1"
}

func queryString(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

func queryOptionalBool(r *http.Request, key string) *bool {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return nil
	}
	v := strings.EqualFold(raw, "true") || raw == "1"
	return &v
}

// handleDefaultLimits handles GET and PUT /api/config/default-limits
// It stores the default limits in the database (app_settings table).
func (h *Handler) handleDefaultLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.getDefaultLimits(w, r)
	} else if r.Method == http.MethodPut {
		h.setDefaultLimits(w, r)
	} else {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) getDefaultLimits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var rateLimitRPM, rateLimitConcurrent *int
	var rateLimitTPM *int

	err := h.db.QueryRow(ctx, `
		SELECT rate_limit_rpm, rate_limit_concurrent, rate_limit_tpm
		FROM app_settings
		WHERE tenant_id = 'default' AND app_id = 'gateway'
	`).Scan(&rateLimitRPM, &rateLimitConcurrent, &rateLimitTPM)

	if err != nil {
		// Return hardcoded defaults if not found
		writeJSON(w, http.StatusOK, map[string]any{
			"rate_limit_rpm":        60,
			"rate_limit_concurrent": 20,
			"rate_limit_tpm":        nil,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rate_limit_rpm":        rateLimitRPM,
		"rate_limit_concurrent": rateLimitConcurrent,
		"rate_limit_tpm":        rateLimitTPM,
	})
}

func (h *Handler) setDefaultLimits(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RateLimitRPM        *int `json:"rate_limit_rpm"`
		RateLimitConcurrent *int `json:"rate_limit_concurrent"`
		RateLimitTPM        *int `json:"rate_limit_tpm"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := h.db.Exec(ctx, `
		INSERT INTO app_settings (tenant_id, app_id, rate_limit_rpm, rate_limit_concurrent, rate_limit_tpm, updated_at)
		VALUES ('default', 'gateway', $1, $2, $3, now())
		ON CONFLICT (tenant_id, app_id) DO UPDATE SET
			rate_limit_rpm = EXCLUDED.rate_limit_rpm,
			rate_limit_concurrent = EXCLUDED.rate_limit_concurrent,
			rate_limit_tpm = EXCLUDED.rate_limit_tpm,
			updated_at = now()
	`, req.RateLimitRPM, req.RateLimitConcurrent, req.RateLimitTPM)

	if err != nil {
		slog.Error("setDefaultLimits failed", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to save default limits")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                "ok",
		"rate_limit_rpm":        req.RateLimitRPM,
		"rate_limit_concurrent": req.RateLimitConcurrent,
		"rate_limit_tpm":        req.RateLimitTPM,
	})
}
