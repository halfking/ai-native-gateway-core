package admin

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
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

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/token", h.handleLogin)
	mux.HandleFunc("/api/routing/resolve", h.handleRoutingResolve)
	mux.HandleFunc("/api/routing/overview", h.handleRoutingOverview)
	mux.HandleFunc("/api/routing/model-tree", h.handleRoutingModelTree)
	mux.HandleFunc("/api/routing/policy", h.handleRoutingPolicy)
	mux.HandleFunc("/api/routing/featured", h.handleRoutingFeatured)
	mux.HandleFunc("/api/routing/available-models", h.handleRoutingAvailableModels)
	mux.HandleFunc("/api/routing/available-models/raw", h.handleRoutingAvailableModelsRaw)
	mux.HandleFunc("/api/routing/decisions", h.handleRoutingDecisions)
	mux.HandleFunc("/api/routing/health", h.handleRoutingHealth)
	mux.HandleFunc("/api/routing/audit", h.handleRoutingAudit)
	mux.HandleFunc("/api/routing/probe", h.handleRoutingProbe)
	mux.HandleFunc("/api/routing/manual-priority", h.handleRoutingManualPriority)
	mux.HandleFunc("/api/routing/score-details", h.handleRoutingScoreDetails)
	mux.HandleFunc("/api/routing/scoring-weights", h.handleRoutingScoringWeights)
	mux.HandleFunc("/api/routing/featured-models", h.handleRoutingFeaturedModelsDynamic)
	mux.HandleFunc("/api/telemetry/decision-log", h.handleTelemetryDecisionLog)
	mux.HandleFunc("/api/telemetry/request-log", h.handleTelemetryRequestLog)
	mux.HandleFunc("/api/telemetry/batch", h.handleTelemetryBatch)
	mux.HandleFunc("/api/providers", h.handleProvidersRoot)
	mux.HandleFunc("/api/providers/", h.handleProviders)
	mux.HandleFunc("/api/providers/seed-from-catalog", h.handleSeedFromCatalog)
	mux.HandleFunc("/api/providers/credentials/", h.handleForceRecover)
	mux.HandleFunc("/api/keys", h.handleKeysRoot)
	mux.HandleFunc("/api/keys/", h.handleKeys)
	mux.HandleFunc("/api/key-applications", h.handleKeyApplicationsList)
	mux.HandleFunc("/api/key-applications/", h.handleKeyApplications)
	mux.HandleFunc("/api/models", h.handleModelsRoot)
	mux.HandleFunc("/api/models/", h.handleModels)
	mux.HandleFunc("/api/usage", h.handleUsageSummary)
	mux.HandleFunc("/api/usage/", h.handleUsage)
	mux.HandleFunc("/api/logs", h.handleLogsRoot)
	mux.HandleFunc("/api/logs/", h.handleLogs)
	mux.HandleFunc("/api/catalog", h.handleCatalogRoot)
	mux.HandleFunc("/api/catalog/", h.handleCatalog)
	mux.HandleFunc("/api/tags", h.handleTags)
	mux.HandleFunc("/api/system/background-tasks", h.handleSystemTasks)
	mux.HandleFunc("/api/system/version", h.handleSystemVersion)
	mux.HandleFunc("/api/tasks/", h.handleTasks)
	mux.HandleFunc("/v1/keys/apply/", h.handleV1KeysApplyStatus)
	mux.HandleFunc("/v1/keys/apply", h.handleV1KeysApply)
	mux.HandleFunc("/api/free-pool/status", h.handleFreePoolStatus)
	mux.HandleFunc("/api/free-pool/register", h.handleFreePoolRegister)
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
