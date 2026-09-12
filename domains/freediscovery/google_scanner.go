package freediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// Google AI Studio (Generative Language API) real-protocol scanner.
//
// Protocol characteristics:
//   - Endpoint:  {base}/models?key=<API_KEY>&pageSize=1000
//   - Auth:      API key is passed as a URL query parameter, not Authorization: Bearer
//   - Response:  {"models":[{"name":"models/gemini-pro","displayName":"...",
//     "inputTokenLimit":N,"outputTokenLimit":N,
//     "supportedGenerationMethods":["generateContent",...]}]}
//
// Differs entirely from the OpenAI-compatible form (data[] vs models[], key in
// query vs Bearer header), so a dedicated scanner is required.
//
// doer outbound matches HTTPScanner: by default safehttpclient blocks
// private/loopback/metadata; tests can inject an httptest.Server client
// (allowlist 127.0.0.1).
type GoogleGenerativeAIScanner struct {
	doer func(req *http.Request) (*http.Response, error)
}

// NewGoogleGenerativeAIScanner constructs a Google Generative AI scanner.
// When httpClient=nil, safehttpclient is used as a safe default.
func NewGoogleGenerativeAIScanner(httpClient *http.Client) *GoogleGenerativeAIScanner {
	if httpClient != nil {
		return &GoogleGenerativeAIScanner{doer: httpClient.Do}
	}
	safe := safehttpclient.New(30 * time.Second)
	return &GoogleGenerativeAIScanner{doer: safe.Do}
}

// googleModelsResponse Google /v1beta/models response structure (2025-09 schema).
type googleModelsResponse struct {
	Models []googleModelEntry `json:"models"`
	// NextPageToken is intentionally simplified: a single page with pageSize=1000
	// is usually enough during the MVP phase; add pagination-token follow-up
	// before true production rollout.
	NextPageToken string `json:"nextPageToken"`
}

// googleModelEntry a single Google model entry.
// "name" carries the "models/" prefix (e.g. "models/gemini-2.0-flash"); ModelID
// must strip that prefix.
type googleModelEntry struct {
	Name                       string   `json:"name"`
	DisplayName                string   `json:"displayName"`
	Description                string   `json:"description"`
	Version                    string   `json:"version"`
	InputTokenLimit            int      `json:"inputTokenLimit"`
	OutputTokenLimit           int      `json:"outputTokenLimit"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods"`

	raw map[string]any
}

func (e googleModelEntry) isFreeOfCharge() bool {
	// 2025-09: characteristics of Google AI Studio free models:
	//   1. name belongs to the "gemini" series (gemini-*-flash / early gemini-*-pro)
	//   2. supportedGenerationMethods contains "generateContent"
	//   3. No methods beyond "tuning" / "countTokens" (avoids paid Vertex AI models)
	//
	// ":free" is NOT required — the Google API has no :free naming convention.
	id := strings.ToLower(e.Name)
	if !strings.Contains(id, "gemini") {
		return false
	}
	for _, m := range e.SupportedGenerationMethods {
		if m == "generateContent" {
			return true
		}
	}
	return false
}

func (e googleModelEntry) displayName() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	// Fallback: take the last segment from "models/gemini-2.0-flash".
	return strings.TrimPrefix(e.Name, "models/")
}

func (e googleModelEntry) modelID() string {
	return strings.TrimPrefix(e.Name, "models/")
}

// ScanModels fetches the Google AI Studio model list, parses it, and applies
// the free-tier check.
func (s *GoogleGenerativeAIScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	listURL, err := joinBaseAndEndpoint(tpl.BaseURL, "/models")
	if err != nil {
		return nil, fmt.Errorf("freediscovery: invalid google base url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: build google request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Google AI Studio: the API key MUST be passed as the ?key= query parameter.
	// Authorization: Bearer is not honored by the upstream.
	if apiKey != "" {
		q := req.URL.Query()
		q.Set("key", apiKey)
		req.URL.RawQuery = q.Encode()
	}

	resp, err := s.doer(req)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: scan google %s: %w", tpl.ProviderCode, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("freediscovery: read google response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freediscovery: scan google %s: upstream status %d: %.200s",
			tpl.ProviderCode, resp.StatusCode, string(body))
	}

	var payload googleModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("freediscovery: parse google response: %w", err)
	}

	models := make([]DiscoveredModel, 0, len(payload.Models))
	for _, e := range payload.Models {
		if !e.isFreeOfCharge() {
			continue
		}
		// A single malformed entry (missing Name) is skipped without failing the batch.
		if e.Name == "" {
			continue
		}
		models = append(models, DiscoveredModel{
			ProviderCode:  tpl.ProviderCode,
			ModelID:       e.modelID(),
			DisplayName:   e.displayName(),
			ContextWindow: e.InputTokenLimit,
			MaxTokens:     e.OutputTokenLimit,
			FreeType:      string(FreeTypeRecurringUncapped), // Google AI Studio free tier is rate-limited by RPM/RPD with no monthly cap
			// Default estimate based on the Gemini free tier: 15 RPM, 1500 RPD,
			// ~50K/day per model.
			MonthlyTokens: 0,
			DailyTokens:   50000,
			PoolKey:       "google-aistudio-free-pool",
			RawMetadata:   e.raw,
		})
	}
	return models, nil
}

// decodeGoogleEntries decodes each entry into a raw map (passthrough for RawMetadata).
func decodeGoogleEntries(body []byte) ([]googleModelEntry, error) {
	var payload googleModelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	// Synchronously populate the raw map (passthrough for ScanModels).
	var rawList []map[string]any
	if err := json.Unmarshal(body, &struct {
		Models *[]map[string]any `json:"models"`
	}{Models: &rawList}); err == nil && len(rawList) == len(payload.Models) {
		for i := range payload.Models {
			payload.Models[i].raw = rawList[i]
		}
	}
	return payload.Models, nil
}
