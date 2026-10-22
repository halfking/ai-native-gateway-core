// Package discovery implements automatic model discovery from LLM providers.
// It periodically scans provider credentials, fetches model lists via /v1/models,
// normalizes model names, and updates the database with discovered models.
package discovery

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/modelresponse"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// Service manages model discovery from providers.
type Service struct {
	db          *pgxpool.Pool
	httpClient  *http.Client
	interval    time.Duration
	stopCh      chan struct{}
	mu          sync.RWMutex
	running     bool
	trigger     string
	startedAt   time.Time
	heartbeatAt time.Time
	lastRun     time.Time
	discovered  int
	keyring     *secret.Keyring
	fernetKey   []byte

	// 2026-09-10: short-lived snapshot of the active standard-model
	// catalog used by matchExistingCanonicalCached so the per-model
	// upsert loop links to existing standard rows without re-scanning
	// models_canonical for every raw name. Guarded by canonMu, NOT the
	// status mu above (different contention profile).
	canonMu       sync.Mutex
	canonCatalog  []canonicalRef
	canonCachedAt time.Time
}

// NewService creates a new discovery service.
func NewService(db *pgxpool.Pool, interval time.Duration) *Service {
	if interval <= 0 {
		interval = 1 * time.Hour
	}
	return &Service{
		db: db,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// SetKeyring sets the AES-GCM keyring for credential decryption.
// CONTRACT: call once at boot, BEFORE Start — the field is written without a
// lock while the discovery goroutine reads it in decryptCredential. Today's
// wiring (cmd/gateway) only ever calls this before Start, which establishes
// the happens-before edge; runtime key rotation would be a data race and must
// switch to atomic.Pointer first (R34 2026-09-17 audit note).
func (s *Service) SetKeyring(kr *secret.Keyring) {
	s.keyring = kr
}

// SetFernetKey sets the legacy Fernet key for credential decryption.
func (s *Service) SetFernetKey(key []byte) {
	s.fernetKey = key
}

// decryptCredential decrypts the secret_ciphertext using the available
// keyring (AES-GCM v1) or legacy Fernet key, matching the logic used
// by the relay (provider/client.go) and admin handler.
func (s *Service) decryptCredential(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	ct := string(ciphertext)
	if s.keyring != nil {
		pt, _, err := secret.DecryptAny(ct, s.keyring, s.fernetKey)
		if err == nil {
			return string(pt), nil
		}
		slog.Debug("AES-GCM/Fernet decrypt failed, trying Fernet-only fallback", "error", err)
	}
	if len(s.fernetKey) == 32 {
		pt, err := secret.DecryptFernet([]byte(ct), s.fernetKey)
		if err == nil {
			return pt, nil
		}
		slog.Debug("Fernet decrypt also failed", "error", err)
	}
	return "", fmt.Errorf("no decryption key available (keyring=%v, fernet=%d bytes)", s.keyring != nil, len(s.fernetKey))
}

// Start begins the periodic discovery loop.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	// 2026-07-27 concurrency fix: Stop() closes stopCh permanently, so a
	// later Start() used to spawn a loop whose select fired immediately on
	// the already-closed channel — the service looked "running" but was
	// silently dead. Recreate the channel under the same lock that sets
	// running=true, and hand the loop its own reference so a concurrent
	// Stop()/Start() pair can't make it read a newer channel.
	s.stopCh = make(chan struct{})
	stopCh := s.stopCh
	s.trigger = "scheduled"
	s.startedAt = time.Now()
	s.heartbeatAt = time.Now()
	s.mu.Unlock()

	slog.Info("model discovery service starting", "interval", s.interval)

	go func() {
		// Run immediately on start (full sweep; providerID=0 means all)
		_ = s.runDiscovery(ctx, 0)

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				_ = s.runDiscovery(ctx, 0)
			case <-stopCh:
				slog.Info("model discovery service stopping")
				return
			case <-ctx.Done():
				slog.Info("model discovery service stopping (context done)")
				return
			}
		}
	}()
}

// Stop stops the discovery service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		close(s.stopCh)
		s.running = false
	}
}

// Status returns the current service status.
func (s *Service) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	running := map[string]any{}
	latest := map[string]any{}
	if s.lastRun != (time.Time{}) {
		latest["finished_at"] = s.lastRun.Format(time.RFC3339)
		latest["status"] = "succeeded"
		latest["discovered"] = s.discovered
		latest["trigger"] = s.trigger
		latest["started_at"] = s.startedAt.Format(time.RFC3339)
		latest["summary"] = map[string]any{
			"added":       s.discovered,
			"updated":     0,
			"skipped":     0,
			"failed":      0,
			"duration_ms": 0,
		}
	}
	if s.running {
		running["status"] = "running"
		running["trigger"] = s.trigger
		running["started_at"] = s.startedAt.Format(time.RFC3339)
		running["heartbeat_at"] = s.heartbeatAt.Format(time.RFC3339)
		running["id"] = 0
	}
	return map[string]any{
		"running":    running,
		"latest":     latest,
		"interval_s": int(s.interval.Seconds()),
		"discovered": s.discovered,
		"last_run":   s.lastRun.Format(time.RFC3339),
	}
}

// RunOnce triggers a single discovery run for every active credential.
// providerID == 0 means "all providers"; pass a positive ID to scope the
// run to a single provider.
func (s *Service) RunOnce(ctx context.Context, providerID int) error {
	s.mu.Lock()
	s.trigger = "manual"
	s.startedAt = time.Now()
	s.heartbeatAt = time.Now()
	s.mu.Unlock()
	return s.runDiscovery(ctx, providerID)
}

func (s *Service) runDiscovery(ctx context.Context, providerID int) error {
	start := time.Now()
	if providerID > 0 {
		slog.Info("model discovery started (scoped)", "provider_id", providerID)
	} else {
		slog.Info("model discovery started")
	}

	// Get all active credentials with their providers
	credentials, err := s.loadCredentials(ctx, providerID)
	if err != nil {
		slog.Error("failed to load credentials", "error", err)
		return err
	}

	totalDiscovered := 0
	var seenOffers []modelOffer
	for _, cred := range credentials {
		if ctx.Err() != nil {
			break
		}
		s.mu.Lock()
		s.heartbeatAt = time.Now()
		s.mu.Unlock()
		models, count, err := s.discoverForCredential(ctx, cred)
		if err != nil {
			slog.Warn("discovery failed for credential",
				"credential_id", cred.ID,
				"provider", cred.ProviderName,
				"error", err,
			)
			continue
		}
		totalDiscovered += count
		for _, m := range models {
			seenOffers = append(seenOffers, modelOffer{CredentialID: cred.ID, RawName: m})
		}
	}

	if len(seenOffers) > 0 {
		s.expireStaleModels(ctx, seenOffers)
	}

	s.mu.Lock()
	s.lastRun = time.Now()
	s.discovered = totalDiscovered
	s.heartbeatAt = time.Now()
	s.mu.Unlock()

	if providerID > 0 {
		slog.Info("model discovery completed (scoped)",
			"provider_id", providerID,
			"duration", time.Since(start).String(),
			"credentials", len(credentials),
			"models", totalDiscovered,
		)
	} else {
		slog.Info("model discovery completed",
			"duration", time.Since(start).String(),
			"credentials", len(credentials),
			"models", totalDiscovered,
		)
	}
	return nil
}

// modelOffer tracks which models were seen in the current discovery run
// so we can expire stale ones afterwards.
type modelOffer struct {
	CredentialID int
	RawName      string
}

// credential holds provider credential info for discovery.
type credential struct {
	ID                     int
	ProviderID             int
	ProviderName           string
	BaseURL                string
	Protocol               string
	CatalogCode            string
	SecretCipher           []byte  // bytea in DB, must be []byte for pgx scan
	ModelsEndpointTemplate *string // from provider_catalog, may be NULL
	DiscoveryStrategy      string  // from provider_catalog
	ModelsManifestJSON     *string // from provider_catalog, JSON manifest fallback
	ProviderKind           string  // 2026-09-07: providers.kind — "local" 启用本地上下文回填
	CatalogCapabilities    []byte  // 2026-09-07: provider_catalog.capabilities JSONB（本地托管元数据）
}

func (s *Service) loadCredentials(ctx context.Context, providerID int) ([]credential, error) {
	// Base filter: only active, ready, non-quarantined credentials attached
	// to an enabled provider.  When providerID > 0 we further restrict the
	// scan to a single provider, which is what the per-provider refresh
	// endpoint uses.
	baseWhere := `
		c.status = 'active'
		  AND c.trust_level NOT IN ('quarantine')
		  AND c.lifecycle_status NOT IN ('suspended', 'retired', 'disabled')
		  AND c.availability_state = 'ready'
		  AND (c.quota_state IS NULL OR c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted'))
		  AND p.enabled = TRUE
	`
	var query string
	var args []any
	if providerID > 0 {
		query = `
		SELECT
			c.id,
			p.id AS provider_id,
			p.display_name AS provider_name,
			p.base_url,
			p.protocol,
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json,
			COALESCE(p.kind, 'cloud'),
			COALESCE(pc.capabilities, '{}'::jsonb)
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE p.id = $1 AND ` + baseWhere
		args = []any{providerID}
	} else {
		query = `
		SELECT
			c.id,
			p.id AS provider_id,
			p.display_name AS provider_name,
			p.base_url,
			p.protocol,
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			pc.models_endpoint_template,
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json,
			COALESCE(p.kind, 'cloud'),
			COALESCE(pc.capabilities, '{}'::jsonb)
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE ` + baseWhere
	}

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []credential
	for rows.Next() {
		var c credential
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.ProviderName, &c.BaseURL, &c.Protocol, &c.CatalogCode, &c.SecretCipher, &c.ModelsEndpointTemplate, &c.DiscoveryStrategy, &c.ModelsManifestJSON, &c.ProviderKind, &c.CatalogCapabilities); err != nil {
			slog.Warn("loadCredentials scan failed", "error", err)
			continue
		}
		creds = append(creds, c)
	}
	// R66: 迭代被静默截断 = 部分 credential 整轮不做模型发现，其
	// provider_models 停留在旧快照/空集，表现为「某些 provider 突然
	// 没有可用模型」且无任何 error。调用方已能正确处理 error，上抛。
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("discovery.Service.loadCredentials: iterate rows: %w", err)
	}
	return creds, nil
}

func (s *Service) discoverForCredential(ctx context.Context, cred credential) ([]string, int, error) {
	// 2026-09-23 vapeur 事故读面归一：providers.protocol 无 CHECK 约束，
	// 存量行可能带别名拼写（"anthropic"、"claude" 等）。比较前先归一到
	// catalog 枚举；归一失败保留原值（行为不变）。cred 是值拷贝，这里
	// 的归一只影响本次发现决策，不回写存储。
	if normed, normErr := providercatalog.NormalizeProviderProtocol(cred.Protocol); normErr == nil {
		cred.Protocol = normed
	}
	if cred.Protocol == "anthropic-messages" {
		return nil, 0, nil
	}

	if cred.DiscoveryStrategy == "manifest_only" {
		return s.discoverFromManifest(ctx, cred)
	}

	modelsURL := modelsEndpointURL(cred.BaseURL, cred.ModelsEndpointTemplate)
	explicitTemplate := cred.ModelsEndpointTemplate != nil && strings.TrimSpace(*cred.ModelsEndpointTemplate) != ""
	if modelsURL == "" {
		if cred.DiscoveryStrategy == "manifest_only" {
			return s.discoverFromManifest(ctx, cred)
		}
		// Empty template: manifest-only supplier (azure-openai, github-copilot).
		if explicitTemplate {
			return s.discoverFromManifest(ctx, cred)
		}
		// Fall through: try ModelsURLCandidates below.
	}

	apiKey, decErr := s.decryptCredential(cred.SecretCipher)
	if decErr != nil {
		return nil, 0, fmt.Errorf("credential decrypt failed: %w (credential_id=%d)", decErr, cred.ID)
	}

	var (
		models      []string
		rawModelsJS []byte
		err         error
	)
	isLocal := isLocalProviderKind(cred.ProviderKind)
	if explicitTemplate && modelsURL != "" {
		models, rawModelsJS, err = s.fetchModelsRaw(ctx, modelsURL, apiKey)
	} else {
		models, rawModelsJS, err = s.fetchModelsRawFromURLs(ctx, upstreamurl.ModelsURLCandidates(cred.BaseURL), apiKey)
	}
	if err != nil {
		slog.Debug("models API call failed, falling back to manifest",
			"provider", cred.ProviderName,
			"error", err,
		)
		return s.discoverFromManifest(ctx, cred)
	}

	// Merge catalog-manifest models the live /models list omits. Vendors
	// sometimes ship a model (e.g. zhipu glm-5.2) before their /models
	// endpoint lists it; the model is callable but invisible to discovery.
	// Catalogs register these "known but unlisted" models so auto-discovery
	// still upserts them alongside the live list.
	models = mergeManifestModels(models, cred.ModelsManifestJSON)

	count := 0
	failed := 0
	for _, rawName := range models {
		if err := s.upsertModel(ctx, cred, rawName); err != nil {
			slog.Warn("failed to upsert model", "model", rawName, "credential_id", cred.ID, "error", err)
			failed++
			continue
		}
		count++
	}

	// 2026-09-07 本地托管供应商：回填 context window。
	// 本地 /v1/models 通常不带 context 信息，这里按优先级解析：
	//   1. /v1/models 响应中的 max_model_len / context_length / context_window
	//   2. ollama: POST /api/show 逐模型查询 model_info.*.context_length
	//   3. catalog capabilities.default_context_window 兜底
	// 结果写入 credential_model_bindings.context_window_override（source='local-discovery'）。
	if isLocal {
		s.applyLocalContextWindows(ctx, cred, models, rawModelsJS)
	}

	// 2026-08-31 hzx-2 round-4: auto-fill default_probe_model so the
	// periodic / balance / fast probe paths have a probe target even
	// when the operator never set one. The pick is "newest model
	// (provider_models.created_at DESC) under this credential that is
	// still routable" — see modelcatalog.AutoFillDefaultProbeModel for
	// the full contract and guard list. Best-effort: a DB error here
	// is logged but does not abort the rest of the refresh.
	if picked, autoErr := modelcatalog.AutoFillDefaultProbeModel(ctx, s.db, cred.ID); autoErr != nil {
		slog.Warn("discovery: auto-fill default_probe_model failed",
			"credential_id", cred.ID, "provider", cred.ProviderName, "error", autoErr)
	} else if picked != "" {
		slog.Info("discovery: auto-filled default_probe_model",
			"credential_id", cred.ID,
			"provider", cred.ProviderName,
			"default_probe_model", picked,
			"source", modelcatalog.DefaultProbeModelSourceRefreshLatest,
		)
	}

	s.updateCredentialHealth(ctx, cred.ID, "healthy", "")

	if failed > 0 {
		slog.Warn("some models failed to upsert",
			"provider", cred.ProviderName,
			"credential_id", cred.ID,
			"succeeded", count,
			"failed", failed,
		)
	}

	return models, count, nil
}

func (s *Service) discoverFromManifest(ctx context.Context, cred credential) ([]string, int, error) {
	if cred.ModelsManifestJSON == nil || *cred.ModelsManifestJSON == "" {
		return nil, 0, nil
	}
	data := []byte(*cred.ModelsManifestJSON)
	models, err := extractModelIDs(data)
	if err != nil {
		return nil, 0, err
	}
	count := 0
	for _, rawName := range models {
		if err := s.upsertModel(ctx, cred, rawName); err != nil {
			slog.Warn("failed to upsert manifest model", "model", rawName, "credential_id", cred.ID, "error", err)
			continue
		}
		count++
	}
	// 2026-08-31 hzx-2 round-4: same auto-fill hook as the API path —
	// see discoverForCredential. Manifest-only suppliers (e.g. azure-openai)
	// must also get a default_probe_model the moment their first
	// manifest comes through.
	if count > 0 {
		if picked, autoErr := modelcatalog.AutoFillDefaultProbeModel(ctx, s.db, cred.ID); autoErr != nil {
			slog.Warn("discovery: manifest auto-fill default_probe_model failed",
				"credential_id", cred.ID, "error", autoErr)
		} else if picked != "" {
			slog.Info("discovery: manifest auto-filled default_probe_model",
				"credential_id", cred.ID,
				"default_probe_model", picked,
				"source", modelcatalog.DefaultProbeModelSourceRefreshLatest,
			)
		}
	}
	return models, count, nil
}

// modelsEndpointURL resolves the model-list endpoint URL using the catalog
// template, mirroring Python discovery_utils._models_endpoint().
//
// Contract:
//   - template is nil  → fall back to upstreamurl.ModelsURL (strip /vN, append /v1/models)
//   - template == ""   → return "" (manifest-only supplier, skip API discovery)
//   - template starts with http(s):// → use as full URL
//   - template starts with "/" → append to base_url
func modelsEndpointURL(baseURL string, template *string) string {
	if template != nil {
		tpl := strings.TrimSpace(*template)
		if tpl == "" {
			return ""
		}
		if strings.HasPrefix(tpl, "http://") || strings.HasPrefix(tpl, "https://") {
			return tpl
		}
		base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
		return base + tpl
	}
	return upstreamurl.ModelsURL(baseURL)
}

func (s *Service) fetchModels(ctx context.Context, url, apiKey string) ([]string, error) {
	models, _, err := s.fetchModelsRaw(ctx, url, apiKey)
	return models, err
}

// fetchModelsRaw 返回模型列表和原始响应体。原始响应体供本地供应商
// （kind='local'）解析 context window 字段（vLLM max_model_len 等）。
func (s *Service) fetchModelsRaw(ctx context.Context, url, apiKey string) ([]string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	//nolint:errcheck // best-effort close
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, nil, &modelresponse.HTTPBodyError{StatusCode: resp.StatusCode, Body: body}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, nil, err
	}

	models, err := extractModelIDs(body)
	return models, body, err
}

func (s *Service) fetchModelsFromURLs(ctx context.Context, urls []string, apiKey string) ([]string, error) {
	var lastErr error
	for _, u := range urls {
		models, err := s.fetchModels(ctx, u, apiKey)
		if err == nil && len(models) > 0 {
			return models, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no models found from any candidate URL")
}

// fetchModelsRawFromURLs 同 fetchModelsFromURLs，但返回首个成功响应的原始
// JSON（本地供应商的 context window 解析需要）。
func (s *Service) fetchModelsRawFromURLs(ctx context.Context, urls []string, apiKey string) ([]string, []byte, error) {
	var lastErr error
	for _, u := range urls {
		models, raw, err := s.fetchModelsRaw(ctx, u, apiKey)
		if err == nil && len(models) > 0 {
			return models, raw, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, fmt.Errorf("no models found from any candidate URL")
}

// extractModelIDs parses various /v1/models response formats.
// Returns a slice of discovered models with ID and inferred modality.
func extractModelIDs(data []byte) ([]string, error) {
	return modelresponse.ParseModelIDs(data)
}

// mergeManifestModels appends manifest-registered model ids that the live
// /models list omitted, de-duplicating case-insensitively. Returns live
// unchanged when manifestJSON is nil/empty or fails to parse.
func mergeManifestModels(live []string, manifestJSON *string) []string {
	if manifestJSON == nil || strings.TrimSpace(*manifestJSON) == "" {
		return live
	}
	manifest, err := extractModelIDs([]byte(*manifestJSON))
	if err != nil || len(manifest) == 0 {
		return live
	}
	seen := make(map[string]bool, len(live))
	for _, m := range live {
		seen[strings.ToLower(strings.TrimSpace(m))] = true
	}
	for _, m := range manifest {
		key := strings.ToLower(strings.TrimSpace(m))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		live = append(live, m)
	}
	return live
}

// splitFamilyIDs is the set of "raw" family tokens that the legacy
// Python admin UI / old Go scans stored in models_canonical.family
// but which should now be canonicalized to the vendor-prefixed form
// (see vendorCanonicalFamilies in normalize.go).  Used by
// upsertModel to decide whether to overwrite an existing
// models_canonical.family value: if the existing value is a known
// split, we update to the canonical; otherwise we leave the
// admin-edited value alone.  Order MUST match the keys of
// vendorCanonicalFamilies.
var splitFamilyIDs = func() []string {
	keys := make([]string, 0, len(vendorCanonicalFamilies))
	for k := range vendorCanonicalFamilies {
		keys = append(keys, k)
	}
	return keys
}()

func (s *Service) upsertModel(ctx context.Context, cred credential, rawName string) error {
	// 2026-07-14: provider-vs-client name split.
	//   - canonicalRawName is the CLIENT-facing lowercase key, persisted
	//     in provider_models.canonical_raw_name and used by SQL matching.
	//   - standardizedName is the historical "provider-canonical" column
	//     (kept lowercase for the same reason).
	//   - rawName is the PROVIDER-facing name (e.g. NVIDIA NIM
	//     "z-ai/glm-5.2", Meta "meta/llama-3.3-70b-instruct") and is kept
	//     unchanged in provider_models.raw_model_name.
	canonicalRawName := modelname.CanonicalizeClientModel(rawName)
	standardizedName := NormalizeModelName(rawName)

	// 2026-09-10: prefer matching the raw name to an EXISTING standard
	// model over deriving a fresh canonical from it. Provider feeds like
	// "cluade/opus-5" or "grok/4.6" used to seed junk standard rows
	// ("opus-5" / "4.6" — NormalizeRouteKey simply drops the vendor
	// prefix) even though "claude-opus-5" / "grok-4.6" were already in
	// models_canonical. A confident match (score ≥ AutoLinkThreshold)
	// reuses that standard row and its name for standardized_name.
	var canonicalID int
	var canonicalName string
	matchedRef, matched := s.matchExistingCanonicalCached(ctx, rawName)
	if matched {
		canonicalID = matchedRef.id
		canonicalName = matchedRef.name
		standardizedName = matchedRef.name
		slog.Debug("raw model matched to existing standard model",
			"raw_model_name", rawName,
			"canonical_name", canonicalName)
		// 2026-09-11: the fast path used to return before the ON CONFLICT
		// maintenance below ever ran, so a matched row never got the
		// family:<id> tag backfill, the split-family normalization, or the
		// 2026-08-09 sticky-modality repair (a matched row stuck at
		// modality='text' 503s every image request). Re-apply the same
		// three rules with one guarded UPDATE. Best-effort: the matched
		// row itself is valid, so a failed repair must not block alias /
		// binding ingestion.
		if err := maintainMatchedCanonical(ctx, s.db, matchedRef.id, matchedRef.name, rawName); err != nil {
			slog.Warn("matched canonical maintenance failed",
				"canonical_id", matchedRef.id,
				"raw_model_name", rawName,
				"error", err)
		}
	} else {
		canonicalName = NormalizeModelName(rawName)
		family := InferFamily(canonicalName)
		// 2026-09-23 audit round: migration 735's folded-name unique index
		// fires 23505 when an active row folds equal to the incoming name but
		// its raw canonical_name differs — ON CONFLICT (canonical_name) below
		// cannot cover the expression index. Pre-resolve to the existing row
		// so the upsert takes the DO UPDATE merge branch instead (252 log
		// 2026-09-23 05:06: 18 failures in 32s, model sync stalled).
		adoptFoldedExisting(ctx, s.db, &canonicalName, &family)
		// Upsert into models_canonical. The INSERT path writes a seed tags
		// array with `family:<id>` so a freshly discovered model is never
		// visible in the /models family-chip filter with an empty tag set
		// (2026-06-20 incident: 459 active models had family=... but
		// tags='{}', making the family chip in ModelsView's quick-filter
		// row return 0 rows). The ON CONFLICT branch:
		//   1. preserves any existing tag set the admin might have
		//      edited by hand, and only appends `family:<id>` if not
		//      already there;
		//   2. normalizes a stale "split" family id (e.g. existing row
		//      has family='claude' from a pre-P1 scan, but the canonical
		//      id is 'anthropic-claude') — when the existing family is
		//      one of the known split tokens we update to the canonical
		//      form and swap the corresponding family:<id> tag; an
		//      admin-edited family that *differs* from what we'd
		//      compute (i.e. NOT a known split token) is left alone so
		//      we don't trample manual classifications.
		//
		// 2026-07-14: canonical_name is now always written as lowercase so
		// internal SQL joins don't need `lower(...) = lower(...)` wrappers.
		//
		// 2026-07-20: modality is seeded using modelname.InferModality for
		// zero-cost initialization. Actual probe validation happens later.
		inferredModality := modelname.InferModality(rawName)

		err := s.db.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, tags, source, status, modality, modality_source)
		VALUES ($1, $2, ARRAY['family:' || $2]::text[], 'discovery', 'active', $4, 'inferred')
		ON CONFLICT (canonical_name) DO UPDATE SET
			family = CASE
				WHEN models_canonical.family = $2
				THEN models_canonical.family
				WHEN models_canonical.family = ANY($3::text[])
				THEN $2
				ELSE models_canonical.family
			END,
			tags = CASE
				WHEN models_canonical.family = $2
				THEN (
					-- family unchanged: just ensure the family:<id> tag
					SELECT array_agg(DISTINCT t)
					FROM unnest(models_canonical.tags || ARRAY['family:' || $2]) AS t
				)
				WHEN models_canonical.family = ANY($3::text[])
				THEN (
					-- family normalized from split → canonical: drop the
					-- stale family:<old> tag and add the new family:<new>
					SELECT array_agg(DISTINCT t)
					FROM unnest(
						array_remove(
							array_remove(
								models_canonical.tags,
								'family:' || models_canonical.family
							),
							'family:' || $2
						) || ARRAY['family:' || $2]
					) AS t
				)
				ELSE models_canonical.tags
			END,
			-- 2026-09-21: a 'disabled' canonical is an explicit operator
			-- kill — the discovery/refresh feed must never resurrect it
			-- (same rule as taxonomy_sync upsertAlias and alias_sync's
			-- WHERE status <> 'disabled' guard). Before this guard, every
			-- provider tick flipped 09-20's disabled qwen3-max-cn /
			-- deepseek-v4-*-ga rows back to 'active' (observed live).
			status = CASE WHEN models_canonical.status = 'disabled'
				THEN models_canonical.status
				ELSE 'active' END,
			/* 2026-08-09 sticky-modality fix.
			   The previous expression was
			     modality = COALESCE(models_canonical.modality, $4)
			   which is dead code: models_canonical.modality is
			   NOT NULL DEFAULT 'text', so the left operand is never NULL and
			   COALESCE always returned the stored value. Any row seeded before
			   an inference-rule fix therefore kept its stale modality forever —
			   re-discovery could not repair it. Because
			   loadCandidatesByModalityDB only admits modality IN
			   ('vision','multimodal') for an image request, a model stuck at
			   'text' is dropped from the candidate set and the request 503s.

			   Upgrade-only semantics: adopt the freshly inferred value ONLY
			   when the stored value is still the column default 'text' AND
			   inference now claims a richer modality. This repairs stale rows
			   while never downgrading a row and never trampling a Layer-3
			   super_admin override.

			   2026-10-04 (migration 825): the sentence above used to end with
			   "there is no override-marker column, so stored = 'text' is the
			   only safe repair predicate available". modality_source is now
			   that marker, so a second gate is added: annotations whose source
			   is semantic or manual are IMMUNE to rule re-inference.

			   Without it the two sides overwrite each other.
			   bg/modality_verification downgrades a vision model to text on
			   semantic negative evidence (source='semantic'); the next
			   provider tick sees stored='text' with inferred='vision' and
			   pushes it straight back. A downgrade would never survive one
			   tick. The two rules make mutually exclusive demands on the
			   same column. */

modality_source = CASE
				   WHEN COALESCE(models_canonical.modality_source, '') = 'inferred'
				   THEN 'inferred'
				   ELSE models_canonical.modality_source
			   END,
			modality = CASE
				WHEN models_canonical.modality = 'text' AND $4 <> 'text'
				 AND COALESCE(models_canonical.modality_source, '')
					 NOT IN ('semantic', 'manual')
				THEN $4
				ELSE models_canonical.modality
			END
		RETURNING id
	`, canonicalName, family, splitFamilyIDs, inferredModality).Scan(&canonicalID)
		if err != nil {
			return err
		}
	}

	// Upsert into model_aliases. raw_name is also enforced lowercase here
	// (alias_sync.go's rebuildAliasIndex does the same in batch).
	aliases := GenerateAliases(rawName, canonicalName)
	for _, alias := range aliases {
		normalizedAlias := modelname.CanonicalizeClientModel(alias)
		if normalizedAlias == "" {
			continue
		}
		// 2026-09-21: a 'disabled' alias pair is an explicit operator kill —
		// discovery must never resurrect it (same WHERE guard as alias_sync's
		// rebuildAliasIndex; 'deprecated' pairs still reactivate). Without the
		// guard every provider tick flipped disabled aliases back to 'active'.
		_, err := s.db.Exec(ctx, `
				INSERT INTO model_aliases (canonical_id, raw_name, status)
				VALUES ($1, $2, 'active')
				ON CONFLICT (canonical_id, raw_name) DO UPDATE SET
					status = 'active',
					updated_at = NOW()
				WHERE model_aliases.status <> 'disabled'
			`, canonicalID, normalizedAlias)
		if err != nil {
			return fmt.Errorf("upsert model alias %q: %w", normalizedAlias, err)
		}
	}

	return modelcatalog.UpsertCredentialModel(
		ctx,
		s.db,
		cred.ID,
		rawName,          // provider-facing: keep casing
		canonicalRawName, // client-facing lowercase key (no vendor prefix)
		// 2026-07-14: lowercase stripped form; 2026-09-10: when the raw
		// name matched an existing standard model, standardizedName now
		// carries that canonical name instead of a truncated raw name.
		standardizedName,
		&canonicalID,
	)
}

func (s *Service) updateCredentialHealth(ctx context.Context, credentialID int, status, errMsg string) {
	_, err := s.db.Exec(ctx, `
		UPDATE credentials
		SET health_status = $1,
			health_error = $2,
			health_checked_at = NOW()
		WHERE id = $3
	`, status, errMsg, credentialID)
	if err != nil {
		slog.Debug("failed to update credential health", "error", err)
	}
}

// EnsureCanonicalAndAliases is the shared "seed the standard-model row +
// alias rows" path used by both the periodic discovery worker and the manual
// POST /api/providers/{id}/refresh-models handler.
//
// It mirrors Service.upsertModel so refresh and discovery agree on what a
// freshly observed model name produces in models_canonical and
// model_aliases:
//
//   - models_canonical is upserted with the same family/modality/tags
//     logic as the periodic worker (modality upgrade-only; admin-edited
//     families preserved; split-family tokens normalized to canonical).
//   - model_aliases rows are seeded for every variant produced by
//     GenerateAliases, ensuring ResolveRawBinding can find the binding
//     from the client-side model name the first time it is requested.
//
// The source tag distinguishes callers: pass "discovery" for the periodic
// worker, "provider_refresh" for the manual admin handler.
//
// 2026-08-22: takes a modelcatalog.Querier (small interface) so refresh-time
// tests can drive the upsert path via pgxmock without a real database.
// Production callers pass *pgxpool.Pool, which already satisfies the
// interface.
func EnsureCanonicalAndAliases(ctx context.Context, db modelcatalog.Querier, rawName, source string) (canonicalID int, canonicalName string, err error) {
	if db == nil {
		return 0, "", fmt.Errorf("database not configured")
	}
	if source == "" {
		source = "discovery"
	}

	// 2026-09-10: match against the EXISTING standard-model catalog first
	// (see upsertModel). Only when no catalog name scores above
	// AutoLinkThreshold do we fall back to seeding a new canonical row
	// derived from the raw name.
	if lister, ok := db.(canonicalListQuerier); ok {
		if ref, matched := matchExistingCanonical(ctx, lister, rawName); matched {
			// 2026-09-11: maintenance parity with Service.upsertModel —
			// see maintainMatchedCanonical. Best-effort (warn-only),
			// matching the policy there.
			if maintErr := maintainMatchedCanonical(ctx, db, ref.id, ref.name, rawName); maintErr != nil {
				slog.Warn("matched canonical maintenance failed",
					"canonical_id", ref.id,
					"raw_model_name", rawName,
					"error", maintErr)
			}
			if aliasErr := seedCanonicalAliases(ctx, db, rawName, ref.id, ref.name); aliasErr != nil {
				return 0, "", aliasErr
			}
			return ref.id, ref.name, nil
		}
	}

	canonicalName = NormalizeModelName(rawName)
	family := InferFamily(canonicalName)
	inferredModality := modelname.InferModality(rawName)
	// 2026-09-23 audit round: same migration-735 folded-name pre-resolve as
	// upsertModel (see the comment there) — 23505 otherwise stalls refresh
	// feeds when an active row folds equal but differs in raw name.
	adoptFoldedExisting(ctx, db, &canonicalName, &family)

	err = db.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, tags, source, status, modality, modality_source)
		VALUES ($1, $2, ARRAY['family:' || $2]::text[], $4, 'active', $5, 'inferred')
		ON CONFLICT (canonical_name) DO UPDATE SET
			family = CASE
				WHEN models_canonical.family = $2
				THEN models_canonical.family
				WHEN models_canonical.family = ANY($3::text[])
				THEN $2
				ELSE models_canonical.family
			END,
			tags = CASE
				WHEN models_canonical.family = $2
				THEN (
					SELECT array_agg(DISTINCT t)
					FROM unnest(models_canonical.tags || ARRAY['family:' || $2]) AS t
				)
				WHEN models_canonical.family = ANY($3::text[])
				THEN (
					SELECT array_agg(DISTINCT t)
					FROM unnest(
						array_remove(
							array_remove(
								models_canonical.tags,
								'family:' || models_canonical.family
							),
							'family:' || $2
						) || ARRAY['family:' || $2]
					) AS t
				)
				ELSE models_canonical.tags
			END,
			-- 2026-09-21: a 'disabled' canonical is an explicit operator
			-- kill — the discovery/refresh feed must never resurrect it
			-- (same rule as taxonomy_sync upsertAlias and alias_sync's
			-- WHERE status <> 'disabled' guard). Before this guard, every
			-- provider tick flipped 09-20's disabled qwen3-max-cn /
			-- deepseek-v4-*-ga rows back to 'active' (observed live).
			status = CASE WHEN models_canonical.status = 'disabled'
				THEN models_canonical.status
				ELSE 'active' END,
			-- 2026-10-04（迁移 825）：同上面的 $4 分支，semantic/manual
			-- 来源的标注对规则重推免疫。逐字理由见该分支的注释。
			modality_source = CASE
				WHEN COALESCE(models_canonical.modality_source, '') = 'inferred'
				THEN 'inferred'
				ELSE models_canonical.modality_source
			END,
			modality = CASE
				WHEN models_canonical.modality = 'text' AND $5 <> 'text'
				 AND COALESCE(models_canonical.modality_source, '')
					 NOT IN ('semantic', 'manual')
				THEN $5
				ELSE models_canonical.modality
			END
		RETURNING id
	`, canonicalName, family, splitFamilyIDs, source, inferredModality).Scan(&canonicalID)
	if err != nil {
		return 0, "", fmt.Errorf("upsert models_canonical for %q: %w", canonicalName, err)
	}

	aliases := GenerateAliases(rawName, canonicalName)
	seenAliases := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		normalizedAlias := modelname.CanonicalizeClientModel(alias)
		if normalizedAlias == "" {
			continue
		}
		if _, seen := seenAliases[normalizedAlias]; seen {
			continue
		}
		seenAliases[normalizedAlias] = struct{}{}
		// 2026-09-21: 'disabled' alias pairs stay disabled — same WHERE guard
		// as alias_sync's rebuildAliasIndex ('deprecated' still reactivates).
		if _, execErr := db.Exec(ctx, `
			INSERT INTO model_aliases (canonical_id, raw_name, status)
			VALUES ($1, $2, 'active')
			ON CONFLICT (canonical_id, raw_name) DO UPDATE SET
				status = 'active',
				updated_at = NOW()
			WHERE model_aliases.status <> 'disabled'
		`, canonicalID, normalizedAlias); execErr != nil {
			return 0, "", fmt.Errorf("upsert model_alias %q: %w", normalizedAlias, execErr)
		}
	}

	return canonicalID, canonicalName, nil
}

// seedCanonicalAliases writes the alias rows that make rawName (and its
// generated variants) resolve to canonicalID. Shared by the matched-standard
// fast path and the legacy seed-new-canonical path of
// EnsureCanonicalAndAliases.
func seedCanonicalAliases(ctx context.Context, db modelcatalog.Querier, rawName string, canonicalID int, canonicalName string) error {
	aliases := GenerateAliases(rawName, canonicalName)
	seenAliases := make(map[string]struct{}, len(aliases))
	for _, alias := range aliases {
		normalizedAlias := modelname.CanonicalizeClientModel(alias)
		if normalizedAlias == "" {
			continue
		}
		if _, seen := seenAliases[normalizedAlias]; seen {
			continue
		}
		seenAliases[normalizedAlias] = struct{}{}
		// 2026-09-21: 'disabled' alias pairs stay disabled — same WHERE guard
		// as alias_sync's rebuildAliasIndex ('deprecated' still reactivates).
		if _, execErr := db.Exec(ctx, `
			INSERT INTO model_aliases (canonical_id, raw_name, status)
			VALUES ($1, $2, 'active')
			ON CONFLICT (canonical_id, raw_name) DO UPDATE SET
				status = 'active',
				updated_at = NOW()
			WHERE model_aliases.status <> 'disabled'
		`, canonicalID, normalizedAlias); execErr != nil {
			return fmt.Errorf("upsert model_alias %q: %w", normalizedAlias, execErr)
		}
	}
	return nil
}

// expireStaleModels marks any model_offers row that wasn't returned by the
// upstream /v1/models call (or the manifest) as unavailable. The previous
// implementation only wrote to model_offers, which caused data drift:
// v_routable_credential_models reads cmb.available / pm.available (NOT
// model_offers.available), so the UI could show a model as unavailable
// while the routing layer still served it.
//
// Step 7 (2026-06-18) adds an opt-in (ENABLE_CMB_EXPIRE=1) companion
// write to credential_model_bindings so the two flags stay in sync. The
// feature is OFF by default — we want to observe production behaviour for
// ~24h before promoting it. When ON, the same set of expired models is
// also marked unavailable on the cmb side, with the existing
// unavailable_reason NOT LIKE 'manual%' guard preserved so admin-pinned
// bindings survive expiry.
//
// BUG-2 fix (2026-06-18): Added a grace period guard — bindings that had
// at least one successful request in the last 24h are NOT expired, even
// if the model is absent from the upstream /v1/models list. This prevents
// intermittent discovery failures (e.g. upstream /v1/models temporarily
// omitting a model, or list pagination) from disabling the only working
// credential for a model. The grace period is configurable via
// DISCOVERY_GRACE_HOURS (default 24).
// staleExpiryBlockedEvidenceReason 非空表示下架被禁止，原因见此常量。
//
// 2026-10-02（审计 §9.10.2）：下面 model_offers 的宽限守卫是
// `NOT EXISTS (近 N 小时成功)` —— 一条**否定式守卫**。当**证据源停止写入**时
// 该子查询不是「查不到」，而是**结构性恒空**：守卫从「已验证近期无成功」
// 退化成「根本没有证据」，而代码会照常执行下架 ⇒ 无论模型是否真在用，
// 都被判为 auto_discovery_expired。这不是降级，是主动禁用仍在工作的凭据模型，
// 且写成功就是成功，不会有任何异常信号。
//
// 否定式比肯定式危险一个量级：肯定式在证据消失时不做动作（安全降级），
// 否定式在证据消失时**做动作**。
//
// ★ 2026-10-21（§143.19 轮 R72g，属主裁决「退役该护栏」）：**判据换源，
// 护栏不撤**。原判据是 `settings.RequestLogsWriteEnabled()`（v1 是否可写），
// 而 890 之后 v1 族已退役 ⇒ 该判据恒不成立 ⇒ 护栏恒 skip ⇒ 陈旧模型
// **永不再自动下架**（211 生产日志实证）。
//
// ⇒ 退役**不是删掉安全网**：安全网的**目的**（否定式守卫在证据缺失时必须不动）
// 依然成立，只是**证据源**从 v1 换成了会话族。所以判据换成
// **「会话族证据源在近 N 小时内是否有成功行」**——它直接问「证据在不在」，
// 而不是问「另一个（已退役的）源是否可写」。
//
// ★ 为什么不能简单删除这个判断：会话族**也可能**停止写入
// （例：R62 修过的 23514——新装库缺当月分区 ⇒ 写路径直接失败）。
// 那一刻否定式守卫同样会结构性恒空 ⇒ 在用模型会被全量下架。
// ⇒ 判据必须保留，只是要问对问题。
//
// 消息里仍点名 _request_logs：那是这道护栏**当初依赖的证据源**（运维要查
// 它的来历），也是本仓对「v1 裸读」检测器（internal/sqlreadguard）仅剩的
// 那个可解释的白名单命中点。
const staleExpiryBlockedEvidenceReason = "宽限守卫的证据源（会话族）近期没有成功行 ⇒ 下架判定不成立（证据缺失时不做动作）；★ 该守卫的判据历经两代：初版依赖 v1 证据源（_request_logs，已随 890 退役 ⇒ 旧判据恒不成立、护栏恒 skip），现版直接问会话族证据源本身"

// staleExpiryMayRun 判定「能否基于证据源执行 stale 下架」。
//
// 抽成纯函数是为了可无库测试：缺陷所在的正是「证据源停止」这个分支，
// 而它在线上是停写之后才出现的，集成测试永远撞不到。
//
// ★ 2026-10-21：参数语义从「v1 是否可写」改为「**会话族证据源近期是否有
// 成功行**」（见 staleExpiryBlockedEvidenceReason 的完整论证）。
func staleExpiryMayRun(evidenceHasRecentSuccess bool) (bool, string) {
	if !evidenceHasRecentSuccess {
		return false, staleExpiryBlockedEvidenceReason
	}
	return true, ""
}

// sessionFamilyHasRecentSuccess reports whether the session-turn family
// (the grace guard's evidence source since migration 890) has recorded any
// successful turn recently.
//
// Why this is a **measurement** and not another switch:
//
//	The guard below is a *negative* guard (`NOT EXISTS (recent success)`).
//	When the evidence source stops writing, that subquery becomes
//	structurally empty and the guard degenerates from "verified: no recent
//	success" into "no evidence at all" — while the code still expires the
//	model. That is the failure mode §9.10.2 was written to prevent.
//
//	The previous criterion (`settings.RequestLogsWriteEnabled()`) asked
//	whether a *different* source (v1) could be written. That question is
//	only a proxy for "does the evidence source have data", and the proxy
//	became permanently false once 890 retired v1 — so the guard skipped
//	forever and stale models were never expired again.
//
//	Asking the source directly removes the proxy. It also keeps the guard
//	armed for the case where the session family itself stops writing (e.g.
//	the 23514 "no partition of relation session_turns" failure that R62
//	fixed on fresh installs): in that case the guard still refuses to act.
//
//	Window: DISCOVERY_GRACE_HOURS (default 24h) — the same window the grace
//	guard itself uses, so "no evidence" and "no recent success" can never
//	disagree about which rows the guard would have seen.
//
//	Query shape: EXISTS over both arms of the session family (hot ∪ parent)
//	with LIMIT-1 semantics, so it stops at the first hit and never scans the
//	whole family.
func (s *Service) sessionFamilyHasRecentSuccess(ctx context.Context) bool {
	window := s.staleExpiryGraceHours()

	const q = `SELECT EXISTS (
	  SELECT 1 FROM public.session_turns_hot
	  WHERE success = TRUE AND ts > now() - ($1 || ' hours')::interval
	  UNION ALL
	  SELECT 1 FROM public.session_turns
	  WHERE success = TRUE AND ts > now() - ($1 || ' hours')::interval
	)`
	var ok bool
	if err := s.db.QueryRow(ctx, q, window).Scan(&ok); err != nil {
		// 查不到证据 ⇒ 按「证据缺失时不动」处理（安全方向）。
		// slog 而非 return：这是运维必须看见的信号。
		slog.Warn("discovery: stale-expiry evidence probe failed, treating as no evidence",
			"error", err, "window_hours", window)
		return false
	}
	return ok
}

// staleExpiryGraceHours returns the grace window used both by the evidence
// probe above and by the guard's own `ts >` predicate.
//
// Extracted so the two can never drift apart (they must agree about which
// rows count as "recent success"), and so the env override has exactly one
// implementation site.
func (s *Service) staleExpiryGraceHours() int {
	hours := 24
	if v := os.Getenv("DISCOVERY_GRACE_HOURS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			hours = n
		}
	}
	return hours
}

func (s *Service) expireStaleModels(ctx context.Context, seen []modelOffer) {
	if len(seen) == 0 {
		return
	}

	// S4 停写护栏：证据源停止写入时，宽限守卫失去判据，此时**不动**才是安全方向。
	//
	// ★ 2026-10-20（迁移 890，v1 族退役）：证据源已换成会话族，
	// **当初不换的理由已经消失**——本注释说「session_turns 的 raw_model_name
	// 目前 0 填充（源头 RequestLogEntry 无该字段）」，这条现在**不成立**：
	// 会话族的 raw_model_name 已随镜像写入（s1a_fields.go §9.60.8：
	// raw_model_name = COALESCE(outbound_model, client_model)），真库实测
	// 近 3 天 625/627 行非空非空串，且带的是**上游名**（如
	// "nvidia/llama-3.1-nemoguard-8b-topic-control"）⇒ 它正是 v1 侧
	// outbound_model 的同义列（且是它的超集：多了 client_model 回落）
	// ⇒ 守卫只会更宽松（更少误下架），方向安全。
	// ⚠ 而「拿 session_turns.model 代替是错的」那条**仍然成立**（model 恒等于
	// client_model，是客户端名不是上游名）⇒ 这里比的是 raw_model_name，不是 model。
	//
	// ★ 2026-10-21（属主裁决「退役该护栏」）：判据**换源不撤网**——
	// 原判据 `settings.RequestLogsWriteEnabled()` 问的是「v1 能否写」，
	// 而 v1 已随 890 退役 ⇒ 该问法恒为 false ⇒ 护栏恒 skip
	// （211 生产日志实证："skip stale-model expiry"）。
	// 新判据直接问**要问的那个问题**：「会话族证据源近期有没有成功行」。
	// 这既让护栏重新有判据（不再恒 skip），又保住了它的安全方向
	// （证据缺失时不动）。
	// ⚠ 判据本身是**实测事实**（一次只读计数查询），不是另一个源的开关 ——
	// 「问 A 是否可写来推断 B 是否有数据」正是当初把判据问错的原因。
	if mayExpire, blockedReason := staleExpiryMayRun(s.sessionFamilyHasRecentSuccess(ctx)); !mayExpire {
		slog.Info("discovery: skip stale-model expiry",
			"reason", blockedReason,
			"would_expire", "model_offers(available=FALSE) + credential_model_bindings(opt-in)")
		return
	}

	credModels := make(map[int]map[string]bool)
	for _, o := range seen {
		if credModels[o.CredentialID] == nil {
			credModels[o.CredentialID] = make(map[string]bool)
		}
		credModels[o.CredentialID][o.RawName] = true
	}

	cmbExpireEnabled := os.Getenv("ENABLE_CMB_EXPIRE") == "1"

	// BUG-2 fix: grace period in hours. Bindings with at least one
	// successful request within this window are immune to expiry.
	//
	// ★ 2026-10-21：改用 staleExpiryGraceHours()（与证据探针同一个实现点）——
	// 探针问的窗口必须与守卫自己的 `ts >` 窗口一致，否则两者可能对
	// 「哪些行算近期成功」给出不同答案（探针说有证据、守卫说没有）。
	graceHours := s.staleExpiryGraceHours()

	expired := 0
	for credID, modelSet := range credModels {
		placeholders := ""
		args := []any{credID}
		i := 2
		for name := range modelSet {
			if placeholders != "" {
				placeholders += ","
			}
			placeholders += fmt.Sprintf("$%d", i)
			args = append(args, name)
			i++
		}

		tag, err := s.db.Exec(ctx, staleExpiryOffersSQL(placeholders, graceHours), args...)
		if err != nil {
			// 2026-10-22 审计修正：Debug → Warn。
			// 默认日志级别是 INFO（cmd/gateway/main.go 的 level := slog.LevelInfo）
			// ⇒ Debug **不落盘**；而「成功过期 0 条」这一路也不出 Info
			// ⇒ 下架整轮静默失效时**零可见性**。
			// 2026-10-21 的 252 生产事故（stale 下架 UPDATE 被 Sprintf % 损坏）
			// 被漏掉的原因正是这一条：功能全灭、而日志里什么都没有。
			// ⇒ 失败必须至少 Warn：可发现性与「修好了」同等重要。
			slog.Warn("failed to expire stale models", "credential_id", credID, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			expired += int(tag.RowsAffected())
			slog.Info("expired stale models",
				"credential_id", credID,
				"count", tag.RowsAffected(),
			)
		}

		// Companion write to credential_model_bindings (opt-in via env).
		// Skipped silently when the flag is off; no error path needed.
		if !cmbExpireEnabled {
			continue
		}
		cmbTag, err := s.db.Exec(ctx, fmt.Sprintf(`
			UPDATE credential_model_bindings cmb
			SET available = FALSE,
			    unavailable_reason = 'auto_discovery_expired',
			    unavailable_at = now()
			FROM provider_models pm
			WHERE pm.id = cmb.provider_model_id
			  AND cmb.credential_id = $1
			  AND pm.raw_model_name NOT IN (%s)
			  AND cmb.available = TRUE
			  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%%'
			  AND COALESCE(cmb.admin_protected, FALSE) = FALSE
		`, placeholders), args...)
		if err != nil {
			slog.Warn("failed to expire stale cmb rows", "credential_id", credID, "error", err)
			continue
		}
		if cmbTag.RowsAffected() > 0 {
			slog.Info("expired stale credential_model_bindings",
				"credential_id", credID,
				"count", cmbTag.RowsAffected(),
			)
		}
	}

	if expired > 0 {
		slog.Info("total stale models expired", "count", expired)
	}
}

// staleExpiryOffersSQL composes the stale-expiry UPDATE over model_offers.
//
// ⚠ This SQL must never be routed through fmt.Sprintf: the shared projection
// inside SessionFamilyTurnsSourceSQL() contains a literal `%`
// (`LIKE 'sys:%'`), and as a format string Go treats it as an unknown verb —
// production evidence 2026-10-21: `LIKE 'sys:%!'(int=24)` plus
// `interval '%!d(MISSING) hours'`, so every UPDATE failed to parse
// (syntax error at or near "(") and stale-model expiry was dead since the
// 890 cutover deploy. Both injected values are therefore concatenated, never
// formatted; scripts/ursmcheck's sqlfmt_sessionfamily_gate_test.go keeps the
// whole repo to this rule.
func staleExpiryOffersSQL(placeholders string, graceHours int) string {
	return `
		UPDATE model_offers
		SET available = FALSE,
		    unavailable_reason = 'auto_discovery_expired',
		    unavailable_at = now()
		WHERE credential_id = $1
		  AND raw_model_name NOT IN (` + placeholders + `)
		  AND available = TRUE
		  AND COALESCE(admin_protected, FALSE) = FALSE
		  -- ★ 890（v1 族退役）：证据源换成**会话族原生源**（hot ∪ parent）。
		  -- 模型名口径用 rl.raw_model_name —— 会话族里它就是 v1 outbound_model
		  -- 的同义列（COALESCE(outbound_model, client_model)），且是 v1 侧
		  -- outbound_model 的**超集**（多了 client_model 回落）⇒ 守卫只会更
		  -- 宽松（更少误下架），方向安全。
		  AND NOT EXISTS (
		    SELECT 1 FROM ` + db.SessionFamilyTurnsSourceSQL() + ` rl
		    WHERE rl.credential_id = $1
		      -- case-insensitive: the model name can differ in case from
		      -- model_offers.raw_model_name (e.g. "MiniMax-M3" vs
		      -- "minimax-m3"). A plain = would miss the rows and wrongly
		      -- expire a model that's actively succeeding.
		      AND lower(rl.raw_model_name) = lower(model_offers.raw_model_name)
		      AND rl.success = TRUE
		      AND rl.ts > now() - interval '` + strconv.Itoa(graceHours) + ` hours'
		  )
	`
}
