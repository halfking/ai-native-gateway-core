package quotafetcher

import (
	"context"
	"strings"
	"sync"
)

// registry maps provider catalog_code → Fetcher. Lookup is case-insensitive
// (mirrors OmniRoute's provider.toLowerCase() fallback). The registry is
// populated once at startup via Manager.RegisterBuiltins / Register.
type registry struct {
	mu       sync.RWMutex
	fetchers map[string]Fetcher
}

func newRegistry() *registry {
	return &registry{fetchers: make(map[string]Fetcher)}
}

// Register adds (or replaces) a fetcher for a provider code. Safe to call
// concurrently; intended for startup registration.
func (r *registry) Register(providerCode string, f Fetcher) {
	if providerCode == "" || f == nil {
		return
	}
	r.mu.Lock()
	r.fetchers[strings.ToLower(strings.TrimSpace(providerCode))] = f
	r.mu.Unlock()
}

// Get returns the fetcher for a provider code (case-insensitive).
func (r *registry) Get(providerCode string) (Fetcher, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	f, ok := r.fetchers[strings.ToLower(strings.TrimSpace(providerCode))]
	return f, ok
}

// IsSupported reports whether a fetcher is registered for this provider.
func (r *registry) IsSupported(providerCode string) bool {
	_, ok := r.Get(providerCode)
	return ok
}

// KeyRevealer resolves a decrypted API key for a credential when the caller
// hasn't enriched the candidate (the virtual_factory path). Implemented by
// provider.Client.RevealAPIKey (cached + singleflighted).
type KeyRevealer interface {
	RevealAPIKey(ctx context.Context, providerID int, credentialID int) (string, error)
}
