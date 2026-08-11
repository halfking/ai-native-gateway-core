package quotafetcher

import (
	"context"
	"log/slog"
)

// Manager is the top-level orchestrator injected into the candidate-build
// path (virtual_factory). It owns: the per-provider fetcher registry, a shared
// throttle (serializes genuine network calls), a per-fetcher cache, and an
// optional KeyRevealer for resolving decrypted API keys when the caller hasn't
// enriched the candidate.
//
// Usage:
//
//	mgr := NewManager(providerClient /* implements KeyRevealer */)
//	mgr.RegisterBuiltins() // openrouter + deepseek/siliconflow/openai
//	virtualFactory.SetQuotaFetcher(mgr)
//
// FetchQuota is fail-open: an unsupported provider, a missing key, or any
// upstream error returns (nil, nil) so preflight falls back to the existing
// DB-based Preflight. It never blocks a request on its own.
type Manager struct {
	registry    *registry
	throttle    *MinIntervalThrottle
	openrouter  *openrouterFetcher
	balance     *balanceFetcher
	keyRevealer KeyRevealer
}

// NewManager constructs an empty manager. keyRevealer may be nil — callers
// must then pass a non-empty FetchRequest.APIKey.
func NewManager(keyRevealer KeyRevealer) *Manager {
	throttle := NewMinIntervalThrottle()
	return &Manager{
		registry:    newRegistry(),
		throttle:    throttle,
		openrouter:  newOpenrouterFetcher(newQuotaCache(), throttle),
		balance:     newBalanceFetcher(newQuotaCache(), throttle),
		keyRevealer: keyRevealer,
	}
}

// RegisterBuiltins registers the built-in fetchers:
//   - openrouter: dedicated /api/v1/key + /api/v1/credits fetcher
//   - deepseek, siliconflow, openai: config-driven balance fetcher
//     (reads endpoint/path from providercap.Resolve)
//
// All share one MinIntervalThrottle so cross-vendor network calls are paced
// together (prevents token revocation from bursty concurrent fetches).
func (m *Manager) RegisterBuiltins() {
	m.registry.Register("openrouter", m.openrouter)
	// Balance-driven vendors share one balanceFetcher instance; it resolves
	// the endpoint per-call via providercap, so one adapter covers all three.
	for _, code := range []string{"deepseek", "siliconflow", "openai"} {
		m.registry.Register(code, m.balance)
	}
}

// Register adds a custom fetcher for a provider code (for extension).
func (m *Manager) Register(providerCode string, f Fetcher) {
	m.registry.Register(providerCode, f)
}

// IsSupported reports whether a fetcher is registered for this provider.
func (m *Manager) IsSupported(providerCode string) bool {
	return m.registry.IsSupported(providerCode)
}

// FetchQuota is the single entry point used by the preflight hook. It:
//  1. Looks up the fetcher for req.ProviderCode (nil → return nil, fail-open).
//  2. Resolves the API key if missing (via KeyRevealer, cached upstream).
//  3. Delegates to the fetcher (which checks its own cache + throttle).
//
// Returns (nil, nil) for graceful-unknown (unsupported / missing key / upstream
// error). A non-nil error indicates only a programming bug.
func (m *Manager) FetchQuota(ctx context.Context, req FetchRequest) (*QuotaInfo, error) {
	f, ok := m.registry.Get(req.ProviderCode)
	if !ok {
		return nil, nil
	}

	// Resolve the API key if the caller didn't provide one (the virtual_factory
	// path builds candidates before key enrichment). RevealAPIKey is cached
	// (5min) + singleflighted upstream, so this is cheap on cache hits.
	if req.APIKey == "" && m.keyRevealer != nil && req.ProviderID != 0 && req.CredentialID != 0 {
		key, err := m.keyRevealer.RevealAPIKey(ctx, int(req.ProviderID), int(req.CredentialID))
		if err != nil {
			slog.Debug("quotafetcher: key reveal failed",
				"credential_id", req.CredentialID, "error", err.Error())
			return nil, nil
		}
		req.APIKey = key
	}

	return f.Fetch(ctx, req)
}
