// Package quotafetcher implements proactive upstream quota preflight.
//
// 模型对齐 OmniRoute open-sse/services/quotaPreflight.ts + 各 *QuotaFetcher.ts:
// 请求前主动调上游 usage API（如 OpenRouter /api/v1/key）预判配额，而不是
// 被动等 429。fetcher 结果通过 freeresource.QuotaTracker.ApplyFetchedQuota
// 写回 free_quota_tracker，让现有 Preflight 读到准确值。
//
// 设计要点（与 OmniRoute 一致）：
//   - Fetcher 接口 + registry：每 provider 一个 adapter
//   - per-fetcher 缓存（45s TTL，credentialID key，401/403 失效）
//   - 全局 min-interval 节流（250ms + jitter，fail-open）
//   - fail-open：任何错误或 nil quota → 放行，走现有 Preflight（不比现状差）
package quotafetcher

import (
	"context"
	"time"
)

// QuotaInfo is the normalized result of an upstream quota fetch. All fields
// optional except PercentUsed/LimitReached which drive the preflight decision.
type QuotaInfo struct {
	// Used / Total are vendor-specific units (USD for balance fetchers, request
	// counts for window fetchers). Surfaced to the UI via ApplyFetchedQuota.
	Used  float64 `json:"used"`
	Total float64 `json:"total"`
	// PercentUsed is the fraction used, 0..1. Drives the threshold check.
	PercentUsed float64 `json:"percent_used"`
	// ResetAt is when the current window resets (UTC). nil = unknown / never.
	ResetAt *time.Time `json:"reset_at,omitempty"`
	// LimitReached is the upstream's explicit "exhausted" signal (e.g. balance
	// <= 0, or limit_remaining <= 0). Short-circuits the threshold check.
	LimitReached bool `json:"limit_reached"`
	// Windows is an optional per-window breakdown (e.g. "daily","monthly").
	// When non-empty, preflight blocks if ANY window is at/below its cutoff.
	// Mirrors OmniRoute QuotaInfo.windows.
	Windows map[string]Window `json:"windows,omitempty"`
	// Extra carries vendor-specific fields for observability (balance_usd,
	// is_free_tier, etc.). Not consumed by preflight.
	Extra map[string]any `json:"extra,omitempty"`
}

// Window is a single quota window's usage (multi-window fetchers only).
type Window struct {
	PercentUsed float64    `json:"percent_used"` // 0..1
	ResetAt     *time.Time `json:"reset_at,omitempty"`
}

// FetchRequest carries everything a fetcher needs to hit the upstream usage API.
// APIKey may be empty when the caller hasn't enriched the candidate yet; in
// that case the Manager resolves it via the injected KeyRevealer (cached).
type FetchRequest struct {
	CredentialID int64
	ProviderID   int64
	ProviderCode string // catalog_code — registry lookup key
	BaseURL      string
	APIKey       string
	Model        string // for :free-variant window detection
	TenantID     string
}

// Fetcher adapts one provider's usage API into a normalized QuotaInfo.
// Implementations MUST be fail-open: return (nil, nil) on any transient
// failure (network, 401/403, parse error) so preflight falls back to the
// existing DB-based Preflight. A non-nil error indicates only a bug.
type Fetcher interface {
	Fetch(ctx context.Context, req FetchRequest) (*QuotaInfo, error)
}
