package routing

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type headerProfile struct {
	Headers       map[string]string
	StripPrefixes []string
}

type HeaderProfileCache struct {
	mu      sync.RWMutex
	cache   map[string]*headerProfile
	dbPool  *pgxpool.Pool
}

func NewHeaderProfileCache(dbPool *pgxpool.Pool) *HeaderProfileCache {
	return &HeaderProfileCache{
		cache:  make(map[string]*headerProfile),
		dbPool: dbPool,
	}
}

func (h *HeaderProfileCache) load(ctx context.Context, catalogCode, protocol string) *headerProfile {
	key := catalogCode + ":" + protocol
	h.mu.RLock()
	if p, ok := h.cache[key]; ok {
		h.mu.RUnlock()
		return p
	}
	h.mu.RUnlock()

	if h.dbPool == nil {
		return nil
	}

	var headersJSON, stripJSON []byte
	err := h.dbPool.QueryRow(ctx, `
		SELECT php.headers_json, php.strip_headers_json
		FROM provider_catalog pc
		JOIN provider_header_profiles php ON php.profile_code = pc.header_profile_code
		WHERE pc.code = $1
	`, catalogCode).Scan(&headersJSON, &stripJSON)
	if err != nil {
		err = h.dbPool.QueryRow(ctx, `
			SELECT headers_json, strip_headers_json
			FROM provider_header_profiles
			WHERE protocol = $1
			ORDER BY profile_code
			LIMIT 1
		`, protocol).Scan(&headersJSON, &stripJSON)
	}
	if err != nil {
		return nil
	}

	p := &headerProfile{Headers: make(map[string]string)}
	if len(headersJSON) > 0 {
		_ = json.Unmarshal(headersJSON, &p.Headers)
	}
	if len(stripJSON) > 0 {
		var raw []string
		_ = json.Unmarshal(stripJSON, &raw)
		for _, s := range raw {
			p.StripPrefixes = append(p.StripPrefixes, strings.ToLower(s))
		}
	}

	h.mu.Lock()
	h.cache[key] = p
	h.mu.Unlock()
	return p
}

func (h *HeaderProfileCache) applyOutbound(reqHeaders map[string][]string, profile *headerProfile) {
	if profile == nil {
		return
	}
	for k, v := range profile.Headers {
		reqHeaders[k] = []string{v}
	}
}

func (h *HeaderProfileCache) stripInbound(src map[string][]string, profile *headerProfile) map[string][]string {
	if profile == nil || len(profile.StripPrefixes) == 0 {
		return src
	}
	out := make(map[string][]string, len(src))
	for k, v := range src {
		lower := strings.ToLower(k)
		skip := false
		for _, prefix := range profile.StripPrefixes {
			if lower == prefix || strings.HasPrefix(lower, prefix+"-") {
				skip = true
				break
			}
		}
		if !skip {
			out[k] = v
		}
	}
	return out
}

func (h *HeaderProfileCache) refreshLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.mu.Lock()
			h.cache = make(map[string]*headerProfile, len(h.cache))
			h.mu.Unlock()
			slog.Debug("header profile cache cleared for refresh")
		case <-stopCh:
			return
		}
	}
}
