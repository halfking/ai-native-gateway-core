package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/modelname"
	"golang.org/x/sync/singleflight"
)

type Resolution struct {
	ClientModel    string   `json:"client_model"`
	CanonicalID    *int     `json:"canonical_id"`
	CanonicalName  *string  `json:"canonical_name"`
	RawModels      []string `json:"raw_models"`
	ResolutionPath string   `json:"resolution_path"`
}

type cacheEntry struct {
	resolved   *Resolution
	expiration time.Time
}

type Resolver struct {
	mu       sync.RWMutex
	cache    map[string]cacheEntry
	ttl      time.Duration
	endpoint string
	client   *http.Client
	sfGroup  singleflight.Group
	stopCh   chan struct{}
}

func NewResolver(pythonEndpoint string, cacheTTL time.Duration) *Resolver {
	if cacheTTL == 0 {
		cacheTTL = 120 * time.Second
	}
	r := &Resolver{
		cache:    make(map[string]cacheEntry),
		ttl:      cacheTTL,
		stopCh:   make(chan struct{}),
	}
	if pythonEndpoint == "" {
		slog.Warn("resolve: no Python endpoint configured, resolution disabled")
		return r
	}
	r.endpoint = strings.TrimRight(pythonEndpoint, "/")
	r.client = &http.Client{
		Timeout: 5 * time.Second,
	}
	go r.evictLoop()
	return r
}

func cacheKey(model, profile string) string {
	model = modelname.NormalizeRouteKey(model)
	if profile == "" {
		return strings.ToLower(model)
	}
	return strings.ToLower(model) + "|" + strings.ToLower(profile)
}

func (r *Resolver) Resolve(ctx context.Context, clientModel, clientProfile string) *Resolution {
	if r.endpoint == "" {
		return passthrough(clientModel)
	}

	key := cacheKey(clientModel, clientProfile)

	r.mu.RLock()
	if entry, ok := r.cache[key]; ok && time.Now().Before(entry.expiration) {
		r.mu.RUnlock()
		return entry.resolved
	}
	r.mu.RUnlock()

	v, err, _ := r.sfGroup.Do(key, func() (any, error) {
		resolved, fetchErr := r.fetch(ctx, clientModel, clientProfile)
		if fetchErr != nil {
			slog.Warn("resolve: fetch failed, using passthrough",
				"model", clientModel,
				"error", fetchErr,
			)
			return passthrough(clientModel), nil
		}

		r.mu.Lock()
		r.cache[key] = cacheEntry{
			resolved:   resolved,
			expiration: time.Now().Add(r.ttl),
		}
		r.mu.Unlock()

		return resolved, nil
	})
	if err != nil {
		return passthrough(clientModel)
	}
	return v.(*Resolution)
}

func (r *Resolver) fetch(ctx context.Context, clientModel, clientProfile string) (*Resolution, error) {
	params := url.Values{"model": {modelname.NormalizeRouteKey(clientModel)}}
	if clientProfile != "" {
		params.Set("profile", clientProfile)
	}
	fetchURL := fmt.Sprintf("%s/api/routing/resolve?%s", r.endpoint, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("python returned %d", resp.StatusCode)
	}

	var result Resolution
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &result, nil
}

func (r *Resolver) CachedCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cache)
}

func (r *Resolver) EvictExpired() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for k, v := range r.cache {
		if now.After(v.expiration) {
			delete(r.cache, k)
		}
	}
}

func passthrough(model string) *Resolution {
	lowered := modelname.NormalizeRouteKey(model)
	return &Resolution{
		ClientModel:    model,
		CanonicalID:    nil,
		CanonicalName:  nil,
		RawModels:      []string{lowered},
		ResolutionPath: "direct",
	}
}

func (r *Resolver) evictLoop() {
	ticker := time.NewTicker(r.ttl)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.EvictExpired()
		case <-r.stopCh:
			return
		}
	}
}

func (r *Resolver) Stop() {
	close(r.stopCh)
}
