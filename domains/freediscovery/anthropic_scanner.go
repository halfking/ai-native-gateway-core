package freediscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/safehttpclient"
)

// Anthropic Messages API real-protocol scanner.
//
// Protocol characteristics:
//   - Endpoint:  {base}/v1/models?limit=1000 (base defaults to https://api.anthropic.com)
//   - Auth:      x-api-key request header + anthropic-version: 2023-06-01
//   - Response:  {"data":[{"id":"claude-...","type":"model","display_name":"..."}],
//     "has_more":bool,"first_id":...,"last_id":...}
//
// Differences from the OpenAI-compatible form: data[] uses display_name
// (camelCase) and lacks numeric context_window fields; auth is x-api-key
// instead of Bearer, so HTTPScanner cannot be reused and a dedicated
// implementation is required.
//
// doer outbound matches HTTPScanner: by default safehttpclient blocks
// private/loopback/metadata; tests can inject an httptest.Server client
// (allowlist 127.0.0.1).
type AnthropicScanner struct {
	doer func(req *http.Request) (*http.Response, error)
}

// NewAnthropicScanner constructs an Anthropic scanner. When httpClient=nil,
// safehttpclient is used as a safe default.
func NewAnthropicScanner(httpClient *http.Client) *AnthropicScanner {
	if httpClient != nil {
		return &AnthropicScanner{doer: httpClient.Do}
	}
	safe := safehttpclient.New(30 * time.Second)
	return &AnthropicScanner{doer: safe.Do}
}

// anthropicModelsResponse Anthropic /v1/models response structure.
type anthropicModelsResponse struct {
	Data    []anthropicModelEntry `json:"data"`
	HasMore bool                  `json:"has_more"`
	LastID  string                `json:"last_id"`
}

// anthropicModelEntry a single Anthropic model entry.
type anthropicModelEntry struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`

	raw map[string]any
}

func (e anthropicModelEntry) displayName() string {
	if e.DisplayName != "" {
		return e.DisplayName
	}
	return e.ID
}

// anthropicModelsPerPage Anthropic per-page maximum (API upper bound; reduces
// pagination round-trips).
const anthropicModelsPerPage = 1000

// ScanModels fetches the Anthropic model list (auto-paginates following has_more)
// and returns all entries as review candidates — the Anthropic API exposes no
// public free tier, so free-tier judgement is delegated to ToSChecker
// (preset verdict=caution → not auto-imported by default).
func (s *AnthropicScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("freediscovery: scan anthropic %s: api key required (x-api-key)", tpl.ProviderCode)
	}

	base := tpl.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}

	models := make([]DiscoveredModel, 0, 64)
	afterID := ""
	for page := 0; page < 10; page++ { // Hard cap 10 pages = 10000 models; defends against a misbehaving upstream.
		pageURL, err := joinBaseAndEndpoint(base, "/v1/models")
		if err != nil {
			return nil, fmt.Errorf("freediscovery: invalid anthropic base url: %w", err)
		}
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", anthropicModelsPerPage))
		if afterID != "" {
			q.Set("after_id", afterID)
		}
		listURL := pageURL + "?" + q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
		if err != nil {
			return nil, fmt.Errorf("freediscovery: build anthropic request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := s.doer(req)
		if err != nil {
			return nil, fmt.Errorf("freediscovery: scan anthropic %s: %w", tpl.ProviderCode, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("freediscovery: read anthropic response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("freediscovery: scan anthropic %s: upstream status %d: %.200s",
				tpl.ProviderCode, resp.StatusCode, string(body))
		}

		var payload anthropicModelsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("freediscovery: parse anthropic response: %w", err)
		}

		var rawList []map[string]any
		_ = json.Unmarshal(body, &struct {
			Data *[]map[string]any `json:"data"`
		}{Data: &rawList})

		for i, e := range payload.Data {
			if e.ID == "" {
				continue // Skip a single malformed entry without failing the whole batch.
			}
			var raw map[string]any
			if i < len(rawList) {
				raw = rawList[i]
			}
			models = append(models, DiscoveredModel{
				ProviderCode: tpl.ProviderCode,
				ModelID:      e.ID,
				DisplayName:  e.displayName(),
				FreeType:     string(FreeTypeRecurringUncapped), // Awaiting manual ToS review
				PoolKey:      "anthropic-api-pool",
				RawMetadata:  raw,
			})
		}

		if !payload.HasMore || payload.LastID == "" || payload.LastID == afterID {
			break
		}
		afterID = payload.LastID
	}
	return models, nil
}
