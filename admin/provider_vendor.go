package admin

// provider_vendor.go — extracted from providers.go (2026-06-22 audit §3
// single-file-bloat remediation, eighth cut, vendor-model fetch + upsert
// slice). Owns the path that pulls the live model catalog from a vendor
// API and reconciles it with the local DB.
//
// Workflow:
//
//   resolveModelsEndpointURL(baseURL, template)
//     → modelsURLCandidatesForBase / modelsURLCandidatesForCred
//     → resolveModelsForCredential
//     → fetchVendorModels / fetchVendorModelsFromURLs
//     → parseVendorModelsBody / extractManifestModels / mergeModelIDs
//     → discoverAndUpsertForCredential → upsertModelForProvider
//     → updateCredHealth (records health on each upsert)
//
// Debug helpers:
//
//   DebugFetchVendorModelsRaw — exposes raw HTTP body for a vendor URL,
//     used by the diagnose UI to show why a probe failed.
//   DebugChatProbe — exposes the chat-completion 404 path used to detect
//     "endpoint ID required" errors (see internal/probeutil).
//
// Background:
//
//   VerifyAllCredentialModelFetches — pure-fetch path used by refresh.
//   VerifyAllCredentialModelUpserts — fetch + upsert path used by verify.
//   loadCredentialRowLiteAny — cred lookup without provider scoping, used
//     only by diagnostic probes that already know the cred id.
//
// Self-contained: stdlib + providercap + modelname. No direct bg import;
// background work is reached through h.bgTasks.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/modelcatalog"
	"github.com/kaixuan/llm-gateway-go/modelname"
)

func (h *Handler) loadCredentialRowLiteAny(ctx context.Context, credID int) (credentialRowLite, error) {
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
		WHERE c.id = $1
	`, credID).Scan(&c.id, &c.label, &c.providerID, &c.providerName,
		&c.baseURL, &c.protocol, &c.catalogCode,
		&c.secretCipher, &c.modelsEndpointTpl, &c.discoveryStrategy, &c.modelsManifestJSON)
	return c, err
}

// resolveModelsEndpointURL maps catalog template + base_url to the models list URL.
func resolveModelsEndpointURL(baseURL string, template *string) (url string, explicitTemplate bool) {
	if template == nil {
		return "", false
	}
	tpl := strings.TrimSpace(*template)
	if tpl == "" {
		return "", true
	}
	explicitTemplate = true
	if strings.HasPrefix(tpl, "http://") || strings.HasPrefix(tpl, "https://") {
		return tpl, true
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	return base + tpl, true
}

// resolveModelsForCredential loads model IDs from vendor API and/or manifest.
// forceAPI=true (manual refresh / health probe): always try vendor API first
// even when discovery_strategy is "manifest"; manifest is fallback only.
// forceAPI=false (scheduled discovery): manifest_only and empty template skip API.
func (h *Handler) resolveModelsForCredential(ctx context.Context, cred credentialRowLite, apiKey string, forceAPI bool) (models []string, source string, err error) {
	desc := providercap.Resolve(cred.protocol, cred.catalogCode)
	if !desc.SupportsModelsEndpoint {
		// Anthropic-compatible upstreams (e.g. minimax /anthropic) have no /models
		// listing; use catalog manifest when operator forces a refresh or probe.
		models, mErr := extractManifestModels(cred.modelsManifestJSON)
		if mErr != nil || len(models) == 0 {
			return nil, "none", fmt.Errorf("provider has no /models endpoint and manifest is empty")
		}
		return models, "manifest_only", nil
	}

	modelsURL, explicitTemplate := resolveModelsEndpointURL(cred.baseURL, cred.modelsEndpointTpl)
	skipAPI := !forceAPI && (cred.discoveryStrategy == "manifest_only" || (explicitTemplate && modelsURL == ""))
	if skipAPI {
		models, err = extractManifestModels(cred.modelsManifestJSON)
		if err != nil || len(models) == 0 {
			return nil, "manifest_only", err
		}
		return models, "manifest_only", nil
	}

	var fetchErr error
	if explicitTemplate && modelsURL != "" {
		models, fetchErr = h.fetchVendorModelsFromURLs(ctx, []string{modelsURL}, cred, apiKey)
	} else {
		models, fetchErr = h.fetchVendorModelsFromURLs(ctx, modelsURLCandidatesForCred(cred.baseURL, cred.modelsEndpointTpl, desc), cred, apiKey)
	}
	if fetchErr == nil && len(models) > 0 {
		// Merge in catalog-manifest models that the live /models list omits.
		// Some vendors publish a model (e.g. zhipu glm-5.2) before their
		// /models endpoint lists it; the model is callable but invisible to
		// discovery. Catalogs register these "known but unlisted" models so a
		// refresh still surfaces them without dropping the live list.
		manifestModels, _ := extractManifestModels(cred.modelsManifestJSON)
		if len(manifestModels) > 0 {
			models = mergeModelIDs(models, manifestModels)
			return models, "api+manifest", nil
		}
		return models, "api", nil
	}

	// Fallback to manifest when API fails or returns empty.
	fallback, _ := extractManifestModels(cred.modelsManifestJSON)
	if len(fallback) > 0 {
		return fallback, "manifest", nil
	}
	if fetchErr != nil {
		return nil, "api", fetchErr
	}
	return nil, "api", fmt.Errorf("no models found from vendor API or manifest")
}

// mergeModelIDs appends manifest entries that are not already present in the
// live list, preserving order and de-duplicating case-insensitively on the
// normalized model id. Returns the live list unchanged when manifest is empty.
func mergeModelIDs(live, manifest []string) []string {
	if len(manifest) == 0 {
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

// CredentialModelFetchResult is one row from VerifyAllCredentialModelFetches.
type CredentialModelFetchResult struct {
	ProviderID   int    `json:"provider_id"`
	ProviderCode string `json:"provider_code"`
	ProviderName string `json:"provider_name"`
	CredentialID int    `json:"credential_id"`
	Label        string `json:"label"`
	BaseURL      string `json:"base_url"`
	CatalogCode  string `json:"catalog_code"`
	Template     string `json:"models_endpoint_template"`
	Strategy     string `json:"discovery_strategy"`
	ResolvedURL  string `json:"resolved_url"`
	Source       string `json:"source"`
	ModelCount   int    `json:"model_count"`
	SampleModels []string `json:"sample_models"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// VerifyAllCredentialModelFetches probes every active credential using the
// same resolveModelsForCredential path as manual refresh (forceAPI=true).
func (h *Handler) VerifyAllCredentialModelFetches(ctx context.Context, providerIDFilter int) ([]CredentialModelFetchResult, error) {
	query := `
		SELECT
			p.id, COALESCE(p.code,''), COALESCE(p.display_name,''),
			c.id, COALESCE(c.label,''),
			COALESCE(p.base_url,''), COALESCE(p.protocol,''),
			COALESCE(p.catalog_code, ''),
			c.secret_ciphertext,
			COALESCE(pc.models_endpoint_template, ''),
			COALESCE(pc.discovery_strategy, 'auto'),
			pc.models_manifest_json
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN provider_catalog pc ON pc.code = COALESCE(NULLIF(p.catalog_code, ''), p.code)
		WHERE c.status = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') NOT IN ('suspended', 'retired', 'disabled')
		  AND p.enabled = TRUE
		  AND p.tenant_id = 'default'
	`
	args := []any{}
	if providerIDFilter > 0 {
		query += ` AND p.id = $1`
		args = append(args, providerIDFilter)
	}
	query += ` ORDER BY p.id, c.id`

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CredentialModelFetchResult
	for rows.Next() {
		var (
			providerID, credID                                    int
			providerCode, providerName, label, baseURL, protocol string
			catalogCode, template, strategy                       string
			secretCipher                                          []byte
			manifestJSON                                          *string
		)
		if err := rows.Scan(&providerID, &providerCode, &providerName,
			&credID, &label, &baseURL, &protocol, &catalogCode, &secretCipher,
			&template, &strategy, &manifestJSON); err != nil {
			continue
		}

		res := CredentialModelFetchResult{
			ProviderID:   providerID,
			ProviderCode: providerCode,
			ProviderName: providerName,
			CredentialID: credID,
			Label:        label,
			BaseURL:      baseURL,
			CatalogCode:  catalogCode,
			Template:     template,
			Strategy:     strategy,
		}

		var tplPtr *string
		if template != "" {
			tplPtr = &template
		}
		if u, explicit := resolveModelsEndpointURL(baseURL, tplPtr); explicit && u != "" {
			res.ResolvedURL = u
		} else if u := modelsURLCandidatesForCred(baseURL, tplPtr, providercap.Resolve(protocol, catalogCode)); len(u) > 0 {
			res.ResolvedURL = strings.Join(u, " | ")
		}

		apiKey, decErr := h.decryptCredStr(string(secretCipher))
		if decErr != nil {
			res.Error = "decrypt: " + decErr.Error()
			out = append(out, res)
			continue
		}

		cred := credentialRowLite{
			id: credID, label: label, providerID: providerID, providerName: providerName,
			baseURL: baseURL, protocol: protocol, catalogCode: catalogCode,
			secretCipher: secretCipher, discoveryStrategy: strategy,
			modelsManifestJSON: manifestJSON,
		}
		if tplPtr != nil {
			cred.modelsEndpointTpl = tplPtr
		}

		models, source, fetchErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
		res.Source = source
		res.ModelCount = len(models)
		if fetchErr != nil {
			res.Error = fetchErr.Error()
		} else if len(models) == 0 {
			res.Error = "no models returned"
		} else {
			res.OK = source == "api" || source == "api+manifest" || source == "manifest_only"
			limit := 3
			if len(models) < limit {
				limit = len(models)
			}
			res.SampleModels = models[:limit]
			if source == "manifest" {
				res.Error = "vendor API unavailable; manifest fallback only"
			}
		}
		out = append(out, res)
	}
	return out, rows.Err()
}

// CredentialModelUpsertResult reports the outcome of discoverAndUpsertForCredential
// (same code path as POST /api/providers/{id}/refresh-models).
type CredentialModelUpsertResult struct {
	ProviderID   int    `json:"provider_id"`
	ProviderCode string `json:"provider_code"`
	CredentialID int    `json:"credential_id"`
	Label        string `json:"label"`
	Upserted     int    `json:"upserted"`
	Failed       int    `json:"failed"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
}

// VerifyAllCredentialModelUpserts runs the full refresh upsert path for each
// active credential (without clearing existing bindings first).
func (h *Handler) VerifyAllCredentialModelUpserts(ctx context.Context, providerIDFilter int) ([]CredentialModelUpsertResult, error) {
	fetches, err := h.VerifyAllCredentialModelFetches(ctx, providerIDFilter)
	if err != nil {
		return nil, err
	}
	var out []CredentialModelUpsertResult
	for _, f := range fetches {
		res := CredentialModelUpsertResult{
			ProviderID:   f.ProviderID,
			ProviderCode: f.ProviderCode,
			CredentialID: f.CredentialID,
			Label:        f.Label,
		}
		if strings.HasPrefix(f.Error, "decrypt:") {
			res.Error = f.Error
			out = append(out, res)
			continue
		}
		cred, lerr := h.loadCredentialRowLite(ctx, f.ProviderID, f.CredentialID)
		if lerr != nil {
			res.Error = "load credential: " + lerr.Error()
			out = append(out, res)
			continue
		}
		upserted, failed, uerr := h.discoverAndUpsertForCredential(ctx, cred)
		res.Upserted = upserted
		res.Failed = failed
		if uerr != nil {
			res.Error = uerr.Error()
		} else {
			res.OK = true
		}
		out = append(out, res)
	}
	return out, nil
}

// discoverAndUpsertForCredential replicates the vendor-API fetch +
// upsert logic from discovery.discoverForCredential but stays in the
// admin package so it can write to a per-provider status record and
// remain decoupled from the global discovery Service.  Behavior must
// match discovery.discoverForCredential: existing rows are kept, new
// model names are inserted, and the credential health is updated.
// Manual refresh always forceAPI so catalog "manifest" strategy still
// hits the live /models endpoint. Duplicate upserts preserve manual
// disable state via modelcatalog.UpsertCredentialModel.
func (h *Handler) discoverAndUpsertForCredential(ctx context.Context, cred credentialRowLite) (upserted int, failed int, err error) {
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, 0, fmt.Errorf("decrypt credential: %w", decErr)
	}

	models, source, fErr := h.resolveModelsForCredential(ctx, cred, apiKey, true)
	if len(models) == 0 {
		msg := "no models returned"
		if fErr != nil {
			msg = fErr.Error()
		} else {
			msg = fmt.Sprintf("no models returned (source=%s)", source)
		}
		h.updateCredHealth(ctx, cred.id, "unreachable", msg)
		return 0, 0, fmt.Errorf("%s", msg)
	}
	// Manual refresh requires a live vendor API list; manifest-only fallback
	// is useful for health diagnostics but must not masquerade as a refresh.
	// Exception: anthropic-messages suppliers never expose /models — manifest
	// is the authoritative source for those bindings.
	// "api+manifest" is a successful live fetch with extra known-but-unlisted
	// models merged in, so it counts as a real refresh.
	if source != "api" && source != "api+manifest" && source != "manifest_only" {
		h.updateCredHealth(ctx, cred.id, "unreachable", "vendor API failed; manifest fallback only")
		return 0, 0, fmt.Errorf("vendor API failed; only manifest fallback available (%d models)", len(models))
	}

	for _, m := range models {
		stdName := modelname.StandardizeName(m)
		if stdName != "" {
			h.db.Exec(ctx, `
				INSERT INTO models_canonical (canonical_name, family, source, status)
				VALUES ($1, 'unknown', 'provider_refresh', 'active')
				ON CONFLICT (canonical_name) DO NOTHING
			`, stdName)
		}
		if uErr := h.upsertModelForProvider(ctx, cred.id, m); uErr != nil {
			failed++
			continue
		}
		upserted++
	}
	h.updateCredHealth(ctx, cred.id, "healthy", "")
	return upserted, failed, nil
}

func modelsURLCandidatesForBase(baseURL string) []string {
	return modelsURLCandidatesForCred(baseURL, nil, providercap.Resolve("", ""))
}

func modelsURLCandidatesForCred(baseURL string, template *string, desc providercap.Descriptor) []string {
	return providercap.ModelsURLCandidates(baseURL, template, desc)
}

func setModelsAuthHeaders(req *http.Request, protocol, apiKey string) {
	providercap.ApplyAuthHeaders(req, providercap.Resolve(protocol, ""), apiKey)
}

func (h *Handler) applyCatalogHeaderProfile(ctx context.Context, req *http.Request, catalogCode string) {
	if h.db == nil || strings.TrimSpace(catalogCode) == "" {
		return
	}
	var headersJSON []byte
	err := h.db.QueryRow(ctx, `
		SELECT php.headers_json
		FROM provider_catalog pc
		JOIN provider_header_profiles php ON php.profile_code = pc.header_profile_code
		WHERE pc.code = $1
	`, catalogCode).Scan(&headersJSON)
	if err != nil || len(headersJSON) == 0 {
		return
	}
	var headers map[string]string
	if json.Unmarshal(headersJSON, &headers) != nil {
		return
	}
	for k, v := range headers {
		if strings.TrimSpace(v) != "" {
			req.Header.Set(k, v)
		}
	}
}

func (h *Handler) fetchVendorModels(ctx context.Context, url string, cred credentialRowLite, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("models endpoint returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	return parseVendorModelsBody(body)
}

// DebugFetchVendorModelsRaw is a diagnostic helper: it fetches the given URL
// with the credential's auth headers and returns the HTTP status, the raw
// response body, and the parsed model IDs. It exists purely to support
// the verify-model-fetch CLI probe and is not wired to any HTTP route.
func (h *Handler) DebugFetchVendorModelsRaw(ctx context.Context, credID int, url string) (status int, rawBody []byte, models []string, err error) {
	cred, err := h.loadCredentialRowLiteAny(ctx, credID)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("load credential: %w", err)
	}
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, nil, nil, fmt.Errorf("decrypt credential: %w", decErr)
	}
	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if rerr != nil {
		return 0, nil, nil, rerr
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	rawBody, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if status == http.StatusOK {
		models, err = parseVendorModelsBody(rawBody)
	}
	return status, rawBody, models, err
}

// DebugChatProbe issues a minimal chat completion against the credential's
// base URL for the given model name, returning the HTTP status and a short
// body preview. It is used to detect models that are callable but not listed
// by the vendor's /models endpoint.
func (h *Handler) DebugChatProbe(ctx context.Context, credID int, model string) (status int, body []byte, err error) {
	cred, err := h.loadCredentialRowLiteAny(ctx, credID)
	if err != nil {
		return 0, nil, fmt.Errorf("load credential: %w", err)
	}
	apiKey, decErr := h.decryptCredStr(string(cred.secretCipher))
	if decErr != nil {
		return 0, nil, fmt.Errorf("decrypt credential: %w", decErr)
	}
	base := strings.TrimRight(strings.TrimSpace(cred.baseURL), "/")
	payload := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":false}`)
	req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if rerr != nil {
		return 0, nil, rerr
	}
	setModelsAuthHeaders(req, cred.protocol, apiKey)
	h.applyCatalogHeaderProfile(ctx, req, cred.catalogCode)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	status = resp.StatusCode
	body, _ = io.ReadAll(io.LimitReader(resp.Body, 4096))
	return status, body, nil
}

// fetchVendorModelsFromURLs tries catalog-resolved candidate URLs in order;
// requires HTTP 200 with a parseable model list (used by refresh + health probe).
func (h *Handler) fetchVendorModelsFromURLs(ctx context.Context, urls []string, cred credentialRowLite, apiKey string) ([]string, error) {
	var lastErr error
	for _, u := range urls {
		models, err := h.fetchVendorModels(ctx, u, cred, apiKey)
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

func parseVendorModelsBody(data []byte) ([]string, error) {
	// Standard OpenAI format: {"data": [{"id": "..."}]}
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
		if len(ids) > 0 {
			return ids, nil
		}
	}

	// Alt format: {"models": [{"id": "..."}]}
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
		if len(ids) > 0 {
			return ids, nil
		}
	}

	// Bare array
	var bare []string
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// Array of objects
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
		if len(ids) > 0 {
			return ids, nil
		}
	}

	return nil, fmt.Errorf("unrecognized models response format")
}

func extractManifestModels(manifest *string) ([]string, error) {
	if manifest == nil || *manifest == "" {
		return nil, nil
	}
	data := []byte(*manifest)

	// Try {"models": [{"id": "..."}]}
	var wrap struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &wrap); err == nil && len(wrap.Models) > 0 {
		var ids []string
		for _, m := range wrap.Models {
			if m.ID != "" {
				ids = append(ids, m.ID)
			}
		}
		return ids, nil
	}

	// Try {"data": [{"id": "..."}]}
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

	// Try bare array
	var bare []string
	if err := json.Unmarshal(data, &bare); err == nil && len(bare) > 0 {
		return bare, nil
	}

	// Try array of objects
	var objArray []map[string]any
	if err := json.Unmarshal(data, &objArray); err == nil && len(objArray) > 0 {
		var ids []string
		for _, m := range objArray {
			if id, ok := m["id"].(string); ok && id != "" {
				ids = append(ids, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				ids = append(ids, name)
			}
		}
		return ids, nil
	}
	return nil, nil
}

// upsertModelForProvider upserts one binding directly on base tables.
// Manual disables (reason LIKE 'manual%') are preserved; legacy soft-deletes
// and auto disables are re-enabled when the vendor still lists the model.
func (h *Handler) upsertModelForProvider(ctx context.Context, credentialID int, rawName string) error {
	return modelcatalog.UpsertCredentialModel(ctx, h.db, credentialID, rawName, modelname.StandardizeName(rawName), nil)
}

func (h *Handler) updateCredHealth(ctx context.Context, credentialID int, status, errMsg string) {
	_, err := h.db.Exec(ctx, `
		UPDATE credentials
		SET health_status = $1, health_error = $2, health_checked_at = NOW()
		WHERE id = $3
	`, status, errMsg, credentialID)
	if err != nil {
		slog.Debug("updateCredHealth failed", "credential_id", credentialID, "error", err)
	}
}

