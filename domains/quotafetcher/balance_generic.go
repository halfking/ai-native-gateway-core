package quotafetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/providercap"
	"github.com/kaixuan/llm-gateway-go/pkg/httputil"
)

// balanceFetcher covers providers whose "quota" is a USD balance exposed via a
// single GET endpoint (DeepSeek /user/balance, SiliconFlow /user/info, OpenAI
// /dashboard/billing/credit_grants). It is config-driven: it reads the endpoint
// path + JSON path from providercap.Descriptor, so adding a new balance vendor
// only needs a providercap.Resolve case (no new fetcher code).
//
// This does NOT replace the background balance probe (bg/credential_probe_v2.go
// probeBalance) — that writes credentials.balance_usd for the UI on a 2min/5min
// schedule. This fetcher reads the same endpoint synchronously in the request
// path (cached 45s) so preflight has a fresh LimitReached signal. The two
// coexist: probeBalance keeps balance_usd current for display; this fetcher
// keeps preflight current for gating.
//
// Fail-open: any error or non-200 returns nil (graceful unknown).
type balanceFetcher struct {
	cache    *quotaCache
	throttle *MinIntervalThrottle
	hc       *http.Client
}

const balanceFetchBudget = 8 * time.Second

func newBalanceFetcher(cache *quotaCache, throttle *MinIntervalThrottle) *balanceFetcher {
	return &balanceFetcher{
		cache:    cache,
		throttle: throttle,
		hc:       &http.Client{Timeout: balanceFetchBudget},
	}
}

func (f *balanceFetcher) Fetch(ctx context.Context, req FetchRequest) (*QuotaInfo, error) {
	if req.APIKey == "" || req.BaseURL == "" {
		return nil, nil
	}
	if qi, ok := f.cache.Get(req.CredentialID); ok {
		return qi, nil
	}

	desc := providercap.Resolve("", req.ProviderCode)
	balURL := providercap.BalanceURL(req.BaseURL, desc)
	if balURL == "" {
		// No balance endpoint configured for this vendor — not an error, just
		// unsupported here. Caller falls back to DB Preflight.
		return nil, nil
	}

	f.throttle.Acquire(ctx)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, balURL, nil)
	if err != nil {
		return nil, nil
	}
	providercap.ApplyAuthHeaders(httpReq, desc, req.APIKey)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := f.hc.Do(httpReq)
	if err != nil {
		slog.Debug("quotafetcher: balance get failed",
			"credential_id", req.CredentialID, "url", balURL, "error", err.Error())
		return nil, nil
	}
	body, bodyErr := httputil.ReadPrefixAndDrain(resp.Body, 64*1024)
	if bodyErr != nil {
		return nil, nil
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		f.cache.Invalidate(req.CredentialID)
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}

	balance, ok := walkJSONFloat(body, desc.BalanceJSONPath)
	if !ok {
		return nil, nil
	}

	qi := balanceToQuota(balance)
	f.cache.Set(req.CredentialID, qi)
	return qi, nil
}

// balanceToQuota converts a USD balance into a QuotaInfo. A balance-driven
// vendor has no fractional "percent used" — it's binary: balance > 0 means
// usable, <= 0 means exhausted. Mirrors OmniRoute deepseekQuotaFetcher's
// `percentUsed = limitReached ? 1 : 0`.
func balanceToQuota(balance float64) *QuotaInfo {
	qi := &QuotaInfo{
		Total:        balance,
		Extra:        map[string]any{"balance_usd": balance},
		LimitReached: balance <= 0,
	}
	if qi.LimitReached {
		qi.PercentUsed = 1
	}
	return qi
}

// walkJSONFloat navigates a dot-separated path (e.g. "balance_infos.0.total_balance")
// into a parsed JSON blob and returns the leaf as a float64. Supports map keys
// and array indices. Mirrors the inline logic in credential_probe_v2.probeBalance
// so both paths agree on parsing; kept here to avoid a bg→quotafetcher dependency.
func walkJSONFloat(body []byte, path string) (float64, bool) {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, false
	}
	parts := strings.Split(path, ".")
	cur := parsed
	for _, p := range parts {
		switch v := cur.(type) {
		case map[string]any:
			cur = v[p]
		case []any:
			idx := 0
			if _, err := fmt.Sscanf(p, "%d", &idx); err != nil {
				return 0, false
			}
			if idx >= len(v) {
				return 0, false
			}
			cur = v[idx]
		default:
			return 0, false
		}
		if cur == nil {
			return 0, false
		}
	}
	switch v := cur.(type) {
	case float64:
		return v, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}
