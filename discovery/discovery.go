// Package discovery implements automatic model discovery from LLM providers.
// It periodically scans provider credentials, fetches model lists via /v1/models,
// normalizes model names, and updates the database with discovered models.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelname"
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
	s.trigger = "scheduled"
	s.startedAt = time.Now()
	s.heartbeatAt = time.Now()
	s.mu.Unlock()

	slog.Info("model discovery service starting", "interval", s.interval)

	go func() {
		// Run immediately on start
		s.runDiscovery(ctx)

		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.runDiscovery(ctx)
			case <-s.stopCh:
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
			"added":    s.discovered,
			"updated":  0,
			"skipped":  0,
			"failed":   0,
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
		"running":     running,
		"latest":      latest,
		"interval_s":  int(s.interval.Seconds()),
		"discovered":  s.discovered,
		"last_run":    s.lastRun.Format(time.RFC3339),
	}
}

// RunOnce triggers a single discovery run.
func (s *Service) RunOnce(ctx context.Context) error {
	s.mu.Lock()
	s.trigger = "manual"
	s.startedAt = time.Now()
	s.heartbeatAt = time.Now()
	s.mu.Unlock()
	return s.runDiscovery(ctx)
}

func (s *Service) runDiscovery(ctx context.Context) error {
	start := time.Now()
	slog.Info("model discovery started")

	// Get all active credentials with their providers
	credentials, err := s.loadCredentials(ctx)
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

	slog.Info("model discovery completed",
		"duration", time.Since(start).String(),
		"credentials", len(credentials),
		"models", totalDiscovered,
	)
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
}

func (s *Service) loadCredentials(ctx context.Context) ([]credential, error) {
	rows, err := s.db.Query(ctx, `
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
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.status = 'active'
		  AND c.trust_level NOT IN ('quarantine')
		  AND c.lifecycle_status NOT IN ('suspended', 'retired', 'disabled')
		  AND c.availability_state = 'ready'
		  AND (c.quota_state IS NULL OR c.quota_state NOT IN ('permanently_exhausted', 'balance_exhausted'))
		  AND p.enabled = TRUE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []credential
	for rows.Next() {
		var c credential
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.ProviderName, &c.BaseURL, &c.Protocol, &c.CatalogCode, &c.SecretCipher, &c.ModelsEndpointTemplate, &c.DiscoveryStrategy, &c.ModelsManifestJSON); err != nil {
			slog.Warn("loadCredentials scan failed", "error", err)
			continue
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func (s *Service) discoverForCredential(ctx context.Context, cred credential) ([]string, int, error) {
	if cred.Protocol == "anthropic-messages" {
		return nil, 0, nil
	}

	if cred.DiscoveryStrategy == "manifest" || cred.DiscoveryStrategy == "manifest_only" {
		return s.discoverFromManifest(ctx, cred)
	}

	modelsURL := modelsEndpointURL(cred.BaseURL, cred.ModelsEndpointTemplate)
	if modelsURL == "" {
		if cred.DiscoveryStrategy == "manifest" || cred.DiscoveryStrategy == "manifest_only" {
			return s.discoverFromManifest(ctx, cred)
		}
		modelsURL = upstreamurl.ModelsURL(cred.BaseURL)
		if modelsURL == "" {
			return s.discoverFromManifest(ctx, cred)
		}
	}

	apiKey, decErr := s.decryptCredential(cred.SecretCipher)
	if decErr != nil {
		return nil, 0, fmt.Errorf("credential decrypt failed: %w", decErr)
	}

	models, err := s.fetchModels(ctx, modelsURL, apiKey)
	if err != nil {
		slog.Debug("models API call failed, falling back to manifest",
			"provider", cred.ProviderName,
			"error", err,
		)
		return s.discoverFromManifest(ctx, cred)
	}

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
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		return nil, err
	}

	return extractModelIDs(body)
}

// extractModelIDs parses various /v1/models response formats.
func extractModelIDs(data []byte) ([]string, error) {
	// Try standard OpenAI format: {"data": [{"id": "..."}]}
	var openai struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &openai); err == nil && len(openai.Data) > 0 {
		var ids []string
		for _, m := range openai.Data {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Try alternative format: {"models": [{"id": "..."}]}
	var alt struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &alt); err == nil && len(alt.Models) > 0 {
		var ids []string
		for _, m := range alt.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Try bare array: ["model1", "model2"]
	var bare []string
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// Try array of objects: [{"id": "model1"}, {"name": "model2"}]
	var objArray []map[string]any
	if err := json.Unmarshal(data, &objArray); err == nil && len(objArray) > 0 {
		var ids []string
		for _, m := range objArray {
			if id, ok := m["id"].(string); ok && id != "" {
				ids = append(ids, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				ids = append(ids, name)
			} else if model, ok := m["model"].(string); ok && model != "" {
				ids = append(ids, model)
			}
		}
		return ids, nil
	}

	return nil, fmt.Errorf("unrecognized models response format")
}

func (s *Service) upsertModel(ctx context.Context, cred credential, rawName string) error {
	// Normalize the model name
	canonicalName := NormalizeModelName(rawName)
	family := InferFamily(canonicalName)

	// Upsert into models_canonical
	var canonicalID int
	err := s.db.QueryRow(ctx, `
		INSERT INTO models_canonical (canonical_name, family, source, status)
		VALUES ($1, $2, 'discovery', 'active')
		ON CONFLICT (canonical_name) DO UPDATE SET
			family = EXCLUDED.family,
			status = 'active'
		RETURNING id
	`, canonicalName, family).Scan(&canonicalID)
	if err != nil {
		return err
	}

	// Upsert into model_aliases
	aliases := GenerateAliases(rawName, canonicalName)
	for _, alias := range aliases {
		_, err := s.db.Exec(ctx, `
			INSERT INTO model_aliases (raw_name, canonical_id, status)
			VALUES ($1, $2, 'active')
			ON CONFLICT (raw_name) DO UPDATE SET
				canonical_id = EXCLUDED.canonical_id,
				status = 'active'
		`, alias, canonicalID)
		if err != nil {
			slog.Debug("failed to upsert alias", "alias", alias, "error", err)
		}
	}

	// NOTE: model_offers is a VIEW backed by provider_models +
	// credential_model_bindings. INSERT goes through an INSTEAD OF trigger
	// that internally upserts both tables via their own UNIQUE
	// (provider_id, raw_model_name) and (credential_id, provider_model_id)
	// constraints. We must NOT add ON CONFLICT here — there is no UNIQUE
	// constraint on the view itself, and adding ON CONFLICT yields
	// "there is no unique or exclusion constraint matching the ON CONFLICT
	// specification" (SQLSTATE 42P10).
	_, err = s.db.Exec(ctx, `
		INSERT INTO model_offers (credential_id, canonical_id, raw_model_name, standardized_name, available, last_seen_at)
		VALUES ($1, $2, $3, $4, TRUE, NOW())
	`, cred.ID, canonicalID, rawName, modelname.StandardizeName(rawName))
	if err != nil {
		return err
	}

	return nil
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

func (s *Service) expireStaleModels(ctx context.Context, seen []modelOffer) {
	if len(seen) == 0 {
		return
	}

	credModels := make(map[int]map[string]bool)
	for _, o := range seen {
		if credModels[o.CredentialID] == nil {
			credModels[o.CredentialID] = make(map[string]bool)
		}
		credModels[o.CredentialID][o.RawName] = true
	}

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

		tag, err := s.db.Exec(ctx, fmt.Sprintf(`
			UPDATE model_offers
			SET available = FALSE,
			    unavailable_reason = 'auto_discovery_expired',
			    unavailable_at = now()
			WHERE credential_id = $1
			  AND raw_model_name NOT IN (%s)
			  AND available = TRUE
			  AND COALESCE(admin_protected, FALSE) = FALSE
		`, placeholders), args...)
		if err != nil {
			slog.Debug("failed to expire stale models", "credential_id", credID, "error", err)
			continue
		}
		if tag.RowsAffected() > 0 {
			expired += int(tag.RowsAffected())
			slog.Info("expired stale models",
				"credential_id", credID,
				"count", tag.RowsAffected(),
			)
		}
	}

	if expired > 0 {
		slog.Info("total stale models expired", "count", expired)
	}
}
