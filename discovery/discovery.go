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
	"github.com/kaixuan/llm-gateway-go/modelname"
)

// Service manages model discovery from providers.
type Service struct {
	db          *pgxpool.Pool
	httpClient  *http.Client
	interval    time.Duration
	stopCh      chan struct{}
	mu          sync.RWMutex
	running     bool
	lastRun     time.Time
	discovered  int
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

// Start begins the periodic discovery loop.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
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
	return map[string]any{
		"running":     s.running,
		"last_run":    s.lastRun.Format(time.RFC3339),
		"discovered":  s.discovered,
		"interval_s":  int(s.interval.Seconds()),
	}
}

// RunOnce triggers a single discovery run.
func (s *Service) RunOnce(ctx context.Context) error {
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
	for _, cred := range credentials {
		if ctx.Err() != nil {
			break
		}
		count, err := s.discoverForCredential(ctx, cred)
		if err != nil {
			slog.Warn("discovery failed for credential",
				"credential_id", cred.ID,
				"provider", cred.ProviderName,
				"error", err,
			)
			continue
		}
		totalDiscovered += count
	}

	s.mu.Lock()
	s.lastRun = time.Now()
	s.discovered = totalDiscovered
	s.mu.Unlock()

	slog.Info("model discovery completed",
		"duration", time.Since(start).String(),
		"credentials", len(credentials),
		"models", totalDiscovered,
	)
	return nil
}

// credential holds provider credential info for discovery.
type credential struct {
	ID           int
	ProviderID   int
	ProviderName string
	BaseURL      string
	Protocol     string
	CatalogCode  string
	SecretCipher string
}

func (s *Service) loadCredentials(ctx context.Context) ([]credential, error) {
	rows, err := s.db.Query(ctx, `
		SELECT
			c.id,
			p.id AS provider_id,
			p.display_name AS provider_name,
			p.base_url,
			p.protocol,
			p.catalog_code,
			c.secret_ciphertext
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE c.status = 'active'
		  AND c.trust_level NOT IN ('quarantine')
		  AND p.enabled = TRUE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var creds []credential
	for rows.Next() {
		var c credential
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.ProviderName, &c.BaseURL, &c.Protocol, &c.CatalogCode, &c.SecretCipher); err != nil {
			continue
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func (s *Service) discoverForCredential(ctx context.Context, cred credential) (int, error) {
	// Skip Anthropic - no /models endpoint
	if cred.Protocol == "anthropic-messages" {
		return 0, nil
	}

	// Build models endpoint URL
	modelsURL := buildModelsURL(cred.BaseURL)
	if modelsURL == "" {
		return 0, fmt.Errorf("cannot build models URL for %s", cred.BaseURL)
	}

	// Fetch models from provider
	models, err := s.fetchModels(ctx, modelsURL, cred.SecretCipher)
	if err != nil {
		return 0, err
	}

	// Update database with discovered models
	count := 0
	for _, rawName := range models {
		if err := s.upsertModel(ctx, cred, rawName); err != nil {
			slog.Debug("failed to upsert model", "model", rawName, "error", err)
			continue
		}
		count++
	}

	// Update credential health
	s.updateCredentialHealth(ctx, cred.ID, "healthy", "")

	return count, nil
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

	// Upsert into model_offers
	_, err = s.db.Exec(ctx, `
		INSERT INTO model_offers (credential_id, canonical_id, raw_model_name, standardized_name, available, last_seen_at)
		VALUES ($1, $2, $3, $4, TRUE, NOW())
		ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
			canonical_id = EXCLUDED.canonical_id,
			standardized_name = COALESCE(model_offers.standardized_name, EXCLUDED.standardized_name),
			available = TRUE,
			last_seen_at = NOW()
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

// buildModelsURL constructs the /v1/models URL from a base URL.
func buildModelsURL(baseURL string) string {
	if baseURL == "" {
		return ""
	}
	// Remove trailing slash
	baseURL = strings.TrimRight(baseURL, "/")
	// Remove common suffixes
	for _, suffix := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/messages"} {
		if strings.HasSuffix(baseURL, suffix) {
			baseURL = baseURL[:len(baseURL)-len(suffix)]
			break
		}
	}
	return baseURL + "/v1/models"
}
