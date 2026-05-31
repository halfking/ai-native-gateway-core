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
)

type Handler struct {
	db          *pgxpool.Pool
	secret      string
	encKey      []byte
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
	mux.HandleFunc("/api/telemetry/decision-log", h.handleTelemetryDecisionLog)
	mux.HandleFunc("/api/telemetry/request-log", h.handleTelemetryRequestLog)
	mux.HandleFunc("/api/telemetry/batch", h.handleTelemetryBatch)
	mux.HandleFunc("/api/providers", h.handleProvidersRoot)
	mux.HandleFunc("/api/providers/", h.handleProviders)
	mux.HandleFunc("/api/providers/seed-from-catalog", h.handleSeedFromCatalog)
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
	mux.HandleFunc("/v1/keys/apply/", h.handleV1KeysApplyStatus)
	mux.HandleFunc("/v1/keys/apply", h.handleV1KeysApply)
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
