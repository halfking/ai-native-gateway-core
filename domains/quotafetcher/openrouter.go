package quotafetcher

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// openrouterFetcher hits OpenRouter's documented usage endpoints to read the
// per-key USD cap + account credits. Mirrors OmniRoute openrouterQuotaFetcher.ts.
//
// Two endpoints (same auth, both GET):
//   - GET /api/v1/key      → { data: { limit, limit_remaining, limit_reset,
//     is_free_tier, usage, usage_daily, ... } }
//   - GET /api/v1/credits   → { data: { total_credits, total_usage } }
//
// limit/limit_remaining are per-key USD caps (null = unlimited/never set).
// limit_reset is null when the cap never resets. We merge both into one
// QuotaInfo: LimitReached when hasCap && limitRemaining<=0; PercentUsed from
// the cap; credits fill in Extra for observability.
//
// Fail-open: 401/403 invalidates the cache + returns nil (revoked/rotated key).
// Any other error returns nil (graceful unknown, never blocks preflight).
type openrouterFetcher struct {
	cache    *quotaCache
	throttle *MinIntervalThrottle
	hc       *http.Client
}

const (
	orKeyPath     = "/api/v1/key"
	orCreditsPath = "/api/v1/credits"
	orFetchBudget = 8 * time.Second
)

func newOpenrouterFetcher(cache *quotaCache, throttle *MinIntervalThrottle) *openrouterFetcher {
	return &openrouterFetcher{
		cache:    cache,
		throttle: throttle,
		hc:       &http.Client{Timeout: orFetchBudget},
	}
}

// orKeyResponse is the subset of GET /api/v1/key we consume.
type orKeyResponse struct {
	Data struct {
		Limit          *float64 `json:"limit"`           // per-key USD cap, null = unlimited
		LimitRemaining *float64 `json:"limit_remaining"` // null = unlimited
		LimitReset     *string  `json:"limit_reset"`     // ISO time, null = never resets
		IsFreeTier     bool     `json:"is_free_tier"`
		Usage          float64  `json:"usage"`
		UsageDaily     float64  `json:"usage_daily"`
		UsageWeekly    float64  `json:"usage_weekly"`
		UsageMonthly   float64  `json:"usage_monthly"`
	} `json:"data"`
}

// orCreditsResponse is the subset of GET /api/v1/credits we consume.
type orCreditsResponse struct {
	Data struct {
		TotalCredits *float64 `json:"total_credits"`
		TotalUsage   *float64 `json:"total_usage"`
	} `json:"data"`
}

func (f *openrouterFetcher) Fetch(ctx context.Context, req FetchRequest) (*QuotaInfo, error) {
	if req.APIKey == "" || req.BaseURL == "" {
		return nil, nil
	}
	// Cache hit → no network.
	if qi, ok := f.cache.Get(req.CredentialID); ok {
		return qi, nil
	}

	// Throttle: serialize genuine network calls across all fetchers using the
	// shared throttle so N free-pool keys don't all hit OpenRouter at once.
	f.throttle.Acquire(ctx)

	base := strings.TrimRight(req.BaseURL, "/")

	// Call #1 — /key (authoritative for the cap + remaining).
	keyData, status, ok := f.getJSON(ctx, base+orKeyPath, req.APIKey)
	if !ok {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			f.cache.Invalidate(req.CredentialID)
		}
		return nil, nil // fail-open
	}
	var keyRes orKeyResponse
	if err := json.Unmarshal(keyData, &keyRes); err != nil {
		return nil, nil
	}

	// Call #2 — /credits (best-effort account totals).
	var creditsRes orCreditsResponse
	if creditsData, _, cok := f.getJSON(ctx, base+orCreditsPath, req.APIKey); cok {
		_ = json.Unmarshal(creditsData, &creditsRes)
	}

	qi := f.buildQuota(keyRes, creditsRes)
	f.cache.Set(req.CredentialID, qi)
	return qi, nil
}

// buildQuota merges /key + /credits into a QuotaInfo.
func (f *openrouterFetcher) buildQuota(keyRes orKeyResponse, creditsRes orCreditsResponse) *QuotaInfo {
	qi := &QuotaInfo{Extra: map[string]any{}}
	d := keyRes.Data

	// Cap-derived usage (the signal that drives LimitReached).
	hasCap := d.Limit != nil && *d.Limit > 0
	if hasCap {
		limit := *d.Limit
		remaining := 0.0
		if d.LimitRemaining != nil {
			remaining = *d.LimitRemaining
		}
		qi.Total = limit
		qi.Used = limit - remaining
		if remaining <= 0 {
			qi.LimitReached = true
			qi.PercentUsed = 1
		} else {
			qi.PercentUsed = qi.Used / limit
		}
	}

	// Reset time.
	if d.LimitReset != nil && *d.LimitReset != "" {
		if t, err := time.Parse(time.RFC3339, *d.LimitReset); err == nil {
			utc := t.UTC()
			qi.ResetAt = &utc
		}
	}

	// Observability extras (surface to UI/logs, not consumed by preflight).
	qi.Extra["is_free_tier"] = d.IsFreeTier
	qi.Extra["usage_daily"] = d.UsageDaily
	qi.Extra["usage_monthly"] = d.UsageMonthly
	if creditsRes.Data.TotalCredits != nil {
		qi.Extra["total_credits"] = *creditsRes.Data.TotalCredits
	}
	if creditsRes.Data.TotalUsage != nil {
		qi.Extra["total_usage"] = *creditsRes.Data.TotalUsage
	}
	// Credit balance as a fallback signal when no cap is set.
	if !hasCap && creditsRes.Data.TotalCredits != nil && creditsRes.Data.TotalUsage != nil {
		bal := *creditsRes.Data.TotalCredits - *creditsRes.Data.TotalUsage
		qi.Extra["credit_balance"] = bal
		if bal <= 0 {
			qi.LimitReached = true
			qi.PercentUsed = 1
		}
	}
	return qi
}

// getJSON does a GET with bearer auth and returns (body, statusCode, ok).
// ok=false on any transport error or non-200. Body is capped at 64KB.
func (f *openrouterFetcher) getJSON(ctx context.Context, url, apiKey string) ([]byte, int, bool) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, false
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := f.hc.Do(httpReq)
	if err != nil {
		slog.Debug("quotafetcher: openrouter get failed", "url", url, "error", err.Error())
		return nil, 0, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, resp.StatusCode, false
	}
	return body, resp.StatusCode, true
}
