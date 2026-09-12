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

// maxScanBodyBytes is the upstream response size limit (defense against malicious/malformed endpoints).
const maxScanBodyBytes = 8 << 20 // 8 MiB

// ProviderScanner scans an upstream provider's model list.
type ProviderScanner interface {
	// ScanModels fetches and parses the model list, filtering out free entries.
	ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error)
}

// HTTPScanner is the generic scanner built on OpenAI-compatible GET {base_url}{models_endpoint}.
// Shared by openai-completions providers such as Groq / OpenRouter / SiliconFlow / Zhipu;
// differences are injected via the freeOf / poolKeyOf / quotaEstimator hooks.
//
// doer for outbound: production defaults to safehttpclient (blocks private networks / loopback /
// link-local / cloud metadata, and prevents DNS rebinding). Tests may inject an httptest.Server client.
type HTTPScanner struct {
	doer func(req *http.Request) (*http.Response, error)

	// freeOf judges whether an upstream model entry belongs to the free tier
	// (nil = use the openAICompatibleFreeOf default rule).
	freeOf func(m modelEntry) bool
	// poolKeyOf infers the shared-quota-pool identifier across models (nil = no shared pool).
	poolKeyOf func(m modelEntry) string
	// quotaEstimator estimates the free quota (nil = no estimation, 0).
	quotaEstimator func(m modelEntry) (monthly, daily int64)
}

// NewHTTPScanner constructs the generic scanner. When httpClient is nil, safehttpclient is used
// as the safe default (blocks private networks / loopback / link-local / cloud metadata,
// and prevents DNS rebinding).
// Production deployments should pass nil to let it pick up the safe transport automatically;
// only test scenarios may inject an httptest.NewServer client.
func NewHTTPScanner(httpClient *http.Client) *HTTPScanner {
	if httpClient != nil {
		return &HTTPScanner{doer: httpClient.Do}
	}
	safe := safehttpclient.New(30 * time.Second)
	return &HTTPScanner{doer: safe.Do}
}

// modelEntry is a single entry in the upstream /models response (OpenAI-compatible shape
// plus common extension fields).
type modelEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`                  // OpenRouter display name.
	DisplayName string `json:"display_name"`          // Groq display name.
	ContextLen  int    `json:"context_length"`        // OpenRouter.
	ContextWin  int    `json:"context_window"`        // Groq.
	MaxOut      int    `json:"max_output_tokens"`     // Groq.
	MaxComp     int    `json:"max_completion_tokens"` // OpenRouter.
	// OpenRouter pricing (stringified USD per million tokens); "0" = free.
	PricingPrompt     string `json:"pricing_prompt"`
	PricingCompletion string `json:"pricing_completion"`
	// Raw-field fallback.
	raw map[string]any
}

func (e modelEntry) displayName() string {
	switch {
	case e.DisplayName != "":
		return e.DisplayName
	case e.Name != "":
		return e.Name
	default:
		return e.ID
	}
}

func (e modelEntry) contextWindow() int {
	if e.ContextWin > 0 {
		return e.ContextWin
	}
	return e.ContextLen
}

func (e modelEntry) maxTokens() int {
	if e.MaxOut > 0 {
		return e.MaxOut
	}
	return e.MaxComp
}

// IsFree is the normalized free-decision input: the OpenRouter ":free" suffix or zero pricing.
func (e modelEntry) isZeroPriced() bool {
	return isZeroPrice(e.PricingPrompt) && isZeroPrice(e.PricingCompletion)
}

func isZeroPrice(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "0" || s == "0.0" || s == "0.00"
}

// ScanModels fetches {base_url}{models_endpoint} and parses it.
func (s *HTTPScanner) ScanModels(ctx context.Context, tpl *ProviderTemplate, apiKey string) ([]DiscoveredModel, error) {
	if tpl == nil {
		return nil, fmt.Errorf("freediscovery: nil template")
	}
	endpoint := tpl.ModelsEndpoint
	if endpoint == "" {
		endpoint = "/models"
	}
	listURL, err := joinURL(tpl.BaseURL, endpoint)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: invalid models endpoint: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	// Keyless providers allow an empty Authorization header.
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := s.doer(req)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: scan %s: %w", tpl.ProviderCode, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxScanBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("freediscovery: read scan response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("freediscovery: scan %s: upstream status %d: %.200s",
			tpl.ProviderCode, resp.StatusCode, string(body))
	}

	entries, err := parseModelsResponse(body)
	if err != nil {
		return nil, fmt.Errorf("freediscovery: parse %s response: %w", tpl.ProviderCode, err)
	}

	freeOf := s.freeOf
	if freeOf == nil {
		freeOf = openAICompatibleFreeOf
	}

	models := make([]DiscoveredModel, 0, len(entries))
	for _, e := range entries {
		if !freeOf(e) {
			continue
		}
		dm := DiscoveredModel{
			ProviderCode:  tpl.ProviderCode,
			ModelID:       e.ID,
			DisplayName:   e.displayName(),
			ContextWindow: e.contextWindow(),
			MaxTokens:     e.maxTokens(),
			FreeType:      inferFreeType(e),
			RawMetadata:   e.raw,
		}
		if s.poolKeyOf != nil {
			dm.PoolKey = s.poolKeyOf(e)
		}
		if s.quotaEstimator != nil {
			dm.MonthlyTokens, dm.DailyTokens = s.quotaEstimator(e)
		}
		models = append(models, dm)
	}
	return models, nil
}

// parseModelsResponse supports two response shapes:
//   - OpenAI/Groq:  {"data": [...]}
//   - OpenRouter:   {"data": [...]} (same shape)
//   - Bare array:   [...]
func parseModelsResponse(body []byte) ([]modelEntry, error) {
	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Data != nil {
		return decodeEntries(envelope.Data)
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(body, &arr); err == nil {
		return decodeEntries(arr)
	}
	return nil, fmt.Errorf("response is neither {data:[...]} nor [...]")
}

func decodeEntries(raw []json.RawMessage) ([]modelEntry, error) {
	out := make([]modelEntry, 0, len(raw))
	for _, r := range raw {
		var e modelEntry
		if err := json.Unmarshal(r, &e); err != nil {
			continue // Skip a single malformed entry rather than failing the whole batch.
		}
		if e.ID == "" {
			continue
		}
		var meta map[string]any
		_ = json.Unmarshal(r, &meta)
		e.raw = meta
		out = append(out, e)
	}
	return out, nil
}

// openAICompatibleFreeOf is the default free-decision rule:
//   - Model ID has the ":free" suffix (OpenRouter).
//   - Or upstream pricing is all zeros (OpenRouter pricing fields).
func openAICompatibleFreeOf(m modelEntry) bool {
	return strings.HasSuffix(m.ID, ":free") || (m.PricingPrompt != "" && m.isZeroPriced())
}

// inferFreeType infers the free type from model characteristics (writes discovery_results.free_type).
func inferFreeType(m modelEntry) string {
	id := strings.ToLower(m.ID)
	switch {
	case strings.Contains(id, "free"):
		return string(FreeTypeRecurringUncapped) // OpenRouter :free has no volume cap, only RPM/RPD.
	case strings.Contains(id, "trial"):
		return string(FreeTypeOneTimeInitial)
	default:
		return string(FreeTypeRecurringUncapped)
	}
}

// joinURL safely joins base and endpoint (uses a URL parser rather than string concatenation).
//
// endpoint must already have passed isValidModelsEndpoint (relative path, no scheme/host).
// Outbound HTTP request safety is provided by safehttpclient at the transport layer; this
// function only constructs the URL.
func joinURL(base, endpoint string) (string, error) {
	return joinBaseAndEndpoint(base, endpoint)
}
