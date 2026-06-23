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
	"github.com/kaixuan/llm-gateway-go/memora"
	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/kaixuan/llm-gateway-go/settings"
	"github.com/redis/go-redis/v9"
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
	probeV2     *bg.CredentialProbeV2  // 900-series: mini-chat probe (spec §5)
	probePicker *bg.DefaultProbePicker // 900-series: default probe model (spec §4)
	modelProbe  *bg.ModelProbeRunner   // 2026-06-18: per-model re-probe of failing bindings (spec 2026-06-18-model-probe-rounds)
	// 2026-06-23 Phase 2/3: backs /api/candidate-failures* endpoints.
	// Wired from cmd/gateway/main.go via SetCandidateFailureHandlers so
	// /alerts can read live data from the CandidateFailureMonitor.
	cfHandlers *candidateFailureHandlers
	fpSlots    *credentialfpslot.Manager
	// pendingStore (Track C C7, 2026-06-18) is the durable cache
	// for client reconnect and vendor async retry. nil disables
	// the /api/admin/pending-responses* endpoints; the GET
	// endpoint on /v1/sessions/{id}/pending-response is
	// unaffected (it lives in the sessions package, not here).
	pendingStore  *pending.Store
	settingsStore *settings.StoreDB // settings-management: DB-backed settings backend (Q1: B)
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
	feedbackAnalyzer interface {
		AnalyzeOnce(ctx context.Context) error
	}
	// memoraClient provides connectivity status for the admin UI.
	// Structural interface avoids importing the memora package directly.
	memoraClient interface {
		Disabled() bool
		Ping(ctx context.Context) error
		BaseURL() string
	}
	// memoraSink provides write-path stats for the admin UI.
	memoraSink interface {
		Stats() memora.Stats
		Pause()
		Resume()
	}
	// modelPolicy (Round 48, 2026-06-21) is the tenant-scoped model
	// denylist cache.  admin handlers call Invalidate after every
	// write so the next chat request sees the change without waiting
	// for the 60s TTL.  Interface avoids an import cycle (admin
	// → relay would be ugly; relay → admin already exists).
	modelPolicy interface {
		Invalidate(tenantID string)
	}
	// providerSettingsResolver (2026-06-20) provides provider-level setting
	// overrides for compression, cache, etc. Wired from cmd/gateway/main.go.
	providerSettingsResolver *settings.ProviderSettingsResolver
	refreshMu                sync.Mutex            // guards lazy init of refreshState
	refreshState             *providerRefreshState // per-provider "refresh model list" tracking (see providers.go)
	// healthTracker (2026-06-22) provides access to the routing health tracker
	// for credential monitor endpoints. Wired from cmd/gateway/main.go.
	healthTracker interface {
		Enabled() bool
	}
	redisClient  interface{}         // Redis client for sliding window access
	autoTitleGen *AutoTitleGenerator // Auto session title generator (2026-06-22)

	// identityPool is the Layer 0 cap on total distinct end-user fingerprints
	// (see identitypool package). nil when the global cap feature is disabled.
	// Wired from cmd/gateway/main.go.
	// Defined as an interface here to avoid an import cycle (admin -> identitypool
	// would be a problem if identitypool imports admin in the future).
	identityPool interface {
		Stats(ctx context.Context) interface{}
		SetMaxIdentities(n int)
	}
}

func NewHandler(db *pgxpool.Pool, secretKey string, encKey []byte) *Handler {
	h := &Handler{db: db, secret: secretKey, encKey: encKey}
	// Initialize auto title generator
	h.autoTitleGen = NewAutoTitleGenerator(h)
	return h
}

// SetModelPolicy (Round 48, 2026-06-21) wires the tenant-scoped model
// denylist cache so admin write endpoints can invalidate per-tenant
// entries.  Called from cmd/gateway/main.go after constructing both
// the admin Handler and the modelpolicy.Checker.
func (h *Handler) SetModelPolicy(mp interface{ Invalidate(string) }) {
	h.modelPolicy = mp
}

// SetProviderSettingsResolver (2026-06-20) wires the provider-level settings
// resolver so admin endpoints can clear cache after updates.
func (h *Handler) SetProviderSettingsResolver(psr *settings.ProviderSettingsResolver) {
	h.providerSettingsResolver = psr
}

// SetHealthTracker (2026-06-22) wires the routing health tracker for credential monitor endpoints.
func (h *Handler) SetHealthTracker(ht interface{ Enabled() bool }) {
	h.healthTracker = ht
}

// SetRedisClient (2026-06-22) wires the Redis client for sliding window access.
func (h *Handler) SetRedisClient(rc interface{}) {
	h.redisClient = rc
}

// GetAutoTitleGenerator (2026-06-22) returns the auto title generator for use by routing package.
func (h *Handler) GetAutoTitleGenerator() *AutoTitleGenerator {
	return h.autoTitleGen
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

// SetModelProbeRunner wires the per-model re-probe worker (spec
// 2026-06-18-model-probe-rounds).  nil-safe — admin keeps working
// without the manual-trigger endpoint if the worker isn't running.
func (h *Handler) SetModelProbeRunner(r *bg.ModelProbeRunner) { h.modelProbe = r }

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

// SetPendingStore (Track C C7, 2026-06-18) injects the durable
// pending response cache. nil disables the /api/admin/pending-
// responses* endpoints; the operator can still hit the public
// /v1/sessions/{id}/pending-response endpoint (lives in
// sessions/handler.go, not here).
func (h *Handler) SetPendingStore(s *pending.Store) {
	h.pendingStore = s
}

// SetSettingsStore (settings-management, 2026-06-20) injects the
// DB-backed settings backend so /api/admin/settings/* endpoints
// can read/write settings_kv / tenant_settings_kv.
func (h *Handler) SetSettingsStore(s *settings.StoreDB) {
	h.settingsStore = s
}

// SetFeedbackAnalyzer wires the daily feedback analyzer for tuning endpoints.
func (h *Handler) SetFeedbackAnalyzer(a interface {
	AnalyzeOnce(ctx context.Context) error
}) {
	h.feedbackAnalyzer = a
}

// SetMemoraServices wires the Memora client and sink so the admin UI
// can display connectivity status and sink statistics.
func (h *Handler) SetMemoraServices(client interface {
	Disabled() bool
	Ping(ctx context.Context) error
	BaseURL() string
	AddMessage(ctx context.Context, userID string, messages []memora.Message, info map[string]any) error
	Search(ctx context.Context, userID, query string, topK int) ([]memora.Memory, error)
}, sink interface {
	Stats() memora.Stats
	Pause()
	Resume()
}) {
	h.memoraClient = client
	h.memoraSink = sink
}

func (h *Handler) fpSlotsDefaultLimit() int {
	if h.fpSlots == nil {
		return 5
	}
	return h.fpSlots.DefaultLimit()
}

func (h *Handler) admin(fn http.HandlerFunc) http.HandlerFunc {
	return AdminMiddleware(fn, h.db, h.secret)
}

// superAdmin wraps the handler with auth + super_admin role enforcement.
// tenant_admin gets 403 Forbidden.
func (h *Handler) superAdmin(fn http.HandlerFunc) http.HandlerFunc {
	return SuperAdminMiddleware(fn, h.db, h.secret)
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// Public routes (no Bearer admin key)
	mux.HandleFunc("/api/auth/token", h.handleLogin)
	// JWT-authenticated routes (JWT or admin key)
	mux.Handle("/v1/keys/apply", h.admin(h.handleV1KeysApply))

	admin := h.admin
	// JWT-authenticated routes (JWT or admin key)
	mux.HandleFunc("/api/auth/me", admin(h.handleAuthMe))
	mux.HandleFunc("/api/auth/change-password", admin(h.handleChangePassword))
	mux.HandleFunc("/api/users", admin(h.handleUsers))
	mux.HandleFunc("/api/admin/audit-logs", h.superAdmin(h.handleListAuditLogs))
	mux.HandleFunc("/api/admin/tenants", h.superAdmin(h.handleTenants))
	mux.HandleFunc("/api/admin/tenants/", h.superAdmin(h.handleTenants))
	mux.HandleFunc("/api/users/", admin(h.handleUsers))
	mux.HandleFunc("/api/routing/resolve", admin(h.handleRoutingResolve))
	mux.HandleFunc("/api/routing/overview", admin(h.handleRoutingOverview))
	mux.HandleFunc("/api/routing/recent-model-failures", admin(h.handleRoutingRecentModelFailures))
	// 2026-06-24: credential monitor summary (per-credential model breakdown)
	monitorH := NewCredentialMonitorHandlers(h, nil, nil)
	mux.HandleFunc("/api/credentials/monitor-summary", admin(monitorH.handleMonitorSummary))
	mux.HandleFunc("/api/admin/compression/stats", admin(h.handleCompressionStats))
	mux.HandleFunc("/api/admin/compression/sessions", admin(h.handleCompressionSessions))
	mux.HandleFunc("/api/admin/data-lifecycle/stats", admin(h.handleDataLifecycleStats))
	mux.HandleFunc("/api/admin/data-lifecycle/cleanup/preview", admin(h.handleDataLifecycleCleanupPreview))
	mux.HandleFunc("/api/admin/data-lifecycle/metrics", admin(h.handleDataLifecycleMetrics))

	// settings-management (Q1: B, Q2: A, Q3: B): 4 platform + 4 tenant endpoints.
	// Tenant endpoints require super_admin (enforced inside the handler).
	h.registerSettingsRoutes(mux)

	// Identity pool (Layer 0 cap) — admin can inspect live stats and
	// update the global max-identities setting. superAdmin-only because
	// raising the cap affects upstream rate-limit exposure.
	mux.HandleFunc("/api/admin/identity-pool/stats", h.superAdmin(h.getIdentityPoolStats))
	mux.HandleFunc("/api/admin/identity-pool/max", h.superAdmin(h.setIdentityPoolMax))
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

	// 2026-06-23 Phase 2 (P1) + Phase 3 (P2): candidate_failure_logs query
	// endpoints + alert ring view from the CandidateFailureMonitor.
	// JWT-auth required (same as /api/routing/*).
	if h.cfHandlers == nil {
		h.cfHandlers = &candidateFailureHandlers{db: h.db}
	}
	mux.HandleFunc("/api/candidate-failures", admin(h.cfHandlers.listCandidateFailures))
	mux.HandleFunc("/api/candidate-failures/stats", admin(h.cfHandlers.getCandidateFailureStats))
	mux.HandleFunc("/api/candidate-failures/credential/{id}", admin(h.cfHandlers.getCandidateFailuresByCredential))
	mux.HandleFunc("/api/candidate-failures/alerts", admin(h.cfHandlers.listRecentAlerts))
	mux.HandleFunc("/api/keys", admin(h.handleKeysRoot))
	mux.HandleFunc("/api/keys/", admin(h.handleKeys))
	mux.HandleFunc("/api/key-applications", admin(h.handleKeyApplicationsList))
	mux.HandleFunc("/api/key-applications/", admin(h.handleKeyApplications))
	mux.HandleFunc("/api/models", admin(h.handleModelsRoot))
	mux.HandleFunc("/api/models/", admin(h.handleModels))
	mux.HandleFunc("/api/client-configs/audit", admin(h.handleClientConfigAudit))
	mux.HandleFunc("/api/usage", admin(h.handleUsageSummary))
	mux.HandleFunc("/api/usage/", admin(h.handleUsage))
	mux.HandleFunc("/api/logs", admin(h.handleLogsRoot))
	mux.HandleFunc("/api/logs/", admin(h.handleLogs))
	mux.HandleFunc("/api/catalog", h.superAdmin(h.handleCatalogRoot))
	mux.HandleFunc("/api/catalog/", h.superAdmin(h.handleCatalog))
	mux.HandleFunc("/api/tags", h.superAdmin(h.handleTags))
	mux.HandleFunc("/api/system/background-tasks", h.admin(h.handleSystemTasks))
	mux.HandleFunc("/api/system/version", admin(h.handleSystemVersion))
	mux.HandleFunc("/api/system/memora-status", h.admin(h.handleMemoraStatus))
	mux.HandleFunc("/api/system/memora-ping", h.admin(h.handleMemoraPing))
	mux.HandleFunc("/api/system/memora-sink", h.admin(h.handleMemoraSinkControl))
	mux.HandleFunc("/api/system/memora-sessions", h.admin(h.handleMemoraSessions))
	mux.HandleFunc("/api/system/memora-context/", h.admin(h.handleMemoraContext))
	mux.HandleFunc("/api/system/session-messages/", h.admin(h.handleSessionMessages))
	mux.HandleFunc("/api/system/session-context/", h.admin(h.handleSessionContextRoutes))
	mux.HandleFunc("/api/system/no-topic-session/", h.admin(h.handleNoTopicSessionRoutes))
	mux.HandleFunc("/api/admin/session-crosstalk", h.admin(h.handleSessionCrosstalkCheck))
	mux.HandleFunc("/api/tasks/", admin(h.handleTasks))
	mux.HandleFunc("/api/free-pool/status", h.admin(h.handleFreePoolStatus))
	mux.HandleFunc("/api/free-pool/register", h.superAdmin(h.handleFreePoolRegister))
	mux.HandleFunc("/api/free-pool/models", h.admin(h.handleFreePoolModels))
	mux.HandleFunc("/api/free-pool/catalog", h.admin(h.handleFreePoolCatalog))
	mux.HandleFunc("/api/free-pool/available", h.admin(h.handleFreePoolAvailable))
	mux.HandleFunc("/api/free-pool/register-all", h.superAdmin(h.handleFreePoolRegisterAll))
	mux.HandleFunc("/api/free-pool/import-env", h.superAdmin(h.handleFreePoolImportEnv))
	mux.HandleFunc("/api/free-pool/bootstrap", h.superAdmin(h.handleFreePoolBootstrap))
	mux.HandleFunc("/api/free-pool/bridge-oauth", h.superAdmin(h.handleFreePoolBridgeOAuth))
	mux.HandleFunc("/api/free-pool/discover", h.superAdmin(h.handleFreePoolDiscover))
	mux.HandleFunc("/api/free-pool/bulk-register", h.superAdmin(h.handleFreePoolBulkRegister))
	mux.HandleFunc("/api/free-pool/methods", h.admin(h.handleFreePoolMethods))
	mux.HandleFunc("/api/free-pool/discovery-status", h.admin(h.handleFreePoolDiscoveryStatus))
	mux.HandleFunc("/api/free-pool/signup-hub", h.admin(h.handleFreePoolSignupHub))
	mux.HandleFunc("/api/free-pool/temp-email", h.superAdmin(h.handleFreePoolTempEmail))
	mux.HandleFunc("/api/free-pool/temp-email/poll", h.superAdmin(h.handleFreePoolTempEmailPoll))
	mux.HandleFunc("/api/free-pool/probe", h.superAdmin(h.handleFreePoolProbe))
	mux.HandleFunc("/api/free-pool/quick-entry", h.superAdmin(h.handleFreePoolQuickEntry))
	mux.HandleFunc("/api/free-pool/keys", h.superAdmin(h.handleFreePoolKeysRouter))
	mux.HandleFunc("/api/free-pool/keys/", h.superAdmin(h.handleFreePoolKeysSubRouter))
	mux.HandleFunc("/api/pricing/", admin(h.handlePricing))
	mux.HandleFunc("/api/config/default-limits", admin(h.handleDefaultLimits))

	// Track C C7 (2026-06-18): pending response admin API.
	// Three endpoints, all under the standard admin auth wrap.
	// The /stats path is a peer of the list endpoint, registered
	// BEFORE the catch-all /api/admin/pending-responses/ to
	// avoid the slash-suffix path swallowing the literal "stats".
	mux.HandleFunc("/api/admin/pending-responses/stats", admin(h.handlePendingStats))
	mux.HandleFunc("/api/admin/pending-responses", admin(h.handlePendingList))
	mux.HandleFunc("/api/admin/pending-responses/", admin(h.handlePendingSubrouter))

	// Peak concurrency + auto-tune endpoints (Phase 2).
	if h.db != nil {
		peakH := NewPeakHandlers(h.db)
		peakH.RegisterPeakRoutes(mux, h.superAdmin)

		// v2.0 auto-route admin endpoints (Phase 3).
		autoH := NewAutoRouteHandlers(h.db)
		if h.autoIndexRefresher != nil {
			autoH.SetIndexRefresher(h.autoIndexRefresher)
		}
		if h.feedbackAnalyzer != nil {
			autoH.SetFeedbackAnalyzer(h.feedbackAnalyzer)
		}
		autoH.RegisterAutoRouteRoutes(mux, h.superAdmin)

		// Phase 2a analytics (matrix / flow / model-task-index / decision-replay).
		// superAdmin only: these expose cross-tenant credential/model routing
		// internals and auto-route tuner metrics; tenant_admin must not see them.
		analyticsH := NewAnalyticsHandlers(h.db)
		analyticsH.RegisterAnalyticsRoutes(mux, h.superAdmin)

		// Phase 1 work type config CRUD.
		wtH := NewWorkTypeHandlers(h.db)
		wtH.RegisterWorkTypeRoutes(mux, h.superAdmin)

		// Credential monitor (2026-06-22): sliding window + manual promote/demote.
		// Requires redis for sliding window access; recorder is optional.
		if h.redisClient != nil {
			var rc *redis.Client
			if r, ok := h.redisClient.(*redis.Client); ok {
				rc = r
			}
			monitorH := NewCredentialMonitorHandlers(h, nil, rc)
			monitorH.RegisterMonitorRoutes(mux, h.superAdmin)
		}

		h.registerMaasRoutes(mux)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("json marshal failed", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		//nolint:errcheck // HTTP write error non-recoverable
		w.Write([]byte(`{"error":{"detail":"json marshal failed"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write(data)
	//nolint:errcheck // HTTP write error non-recoverable
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
	//nolint:errcheck // best-effort close
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
	switch r.Method {
	case http.MethodGet:
		h.getDefaultLimits(w, r)
	case http.MethodPut:
		if !IsSuperAdminOrLegacy(r) {
			writeError(w, http.StatusForbidden, "super_admin role required to modify default limits")
			return
		}
		h.setDefaultLimits(w, r)
	default:
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

// SetCandidateFailureHandlers (2026-06-23 Phase 3) wires the alert ring
// getter onto the handler. Called by main.go right after starting the
// CandidateFailureMonitor so the /api/candidate-failures/alerts endpoint
// returns live data.
func (h *Handler) SetCandidateFailureHandlers(getter func() []bg.CandidateFailureAlert) {
	if h.cfHandlers == nil {
		h.cfHandlers = &candidateFailureHandlers{db: h.db}
	}
	h.cfHandlers.SetRecentAlerts(getter)
}
