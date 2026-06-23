package routing

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StickyCache struct {
	mu     sync.RWMutex
	items  map[string]stickyEntry
	dbPool *pgxpool.Pool
}

type stickyEntry struct {
	credentialID int
	failures     int
	expiresAt    time.Time
}

func NewStickyCache() *StickyCache {
	return &StickyCache{items: make(map[string]stickyEntry)}
}

func (s *StickyCache) SetDB(pool *pgxpool.Pool) {
	s.dbPool = pool
}

func (s *StickyCache) Get(key string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		return 0, false
	}
	return e.credentialID, true
}

func (s *StickyCache) GetEntry(key string) (int, int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		return 0, 0, false
	}
	return e.credentialID, e.failures, true
}

func (s *StickyCache) Set(key string, credentialID int, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[key] = stickyEntry{
		credentialID: credentialID,
		failures:     0,
		expiresAt:    time.Now().Add(ttl),
	}
}

func (s *StickyCache) RecordFailure(key string, threshold int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		delete(s.items, key)
		return true
	}
	e.failures++
	if threshold <= 0 {
		threshold = 3
	}
	if e.failures >= threshold {
		delete(s.items, key)
		return true
	}
	s.items[key] = e
	return false
}

func (s *StickyCache) RecordSuccess(key string, credentialID int, ttl time.Duration) {
	s.Set(key, credentialID, ttl)
	if s.dbPool != nil {
		go s.dbSet(key, credentialID, ttl)
	}
}

func (s *StickyCache) dbSet(key string, credentialID int, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := s.dbPool.Exec(ctx, `
		INSERT INTO sticky_sessions (sticky_key, credential_id, set_at, expires_at)
		VALUES ($1, $2, now(), $3)
		ON CONFLICT (sticky_key) DO UPDATE SET
			credential_id = EXCLUDED.credential_id,
			set_at = EXCLUDED.set_at,
			expires_at = EXCLUDED.expires_at
	`, key, credentialID, expiresAt)
	if err != nil {
		slog.Debug("sticky DB write failed", "key", key, "error", err)
	}
}

func (s *StickyCache) RestoreFromDB(ctx context.Context) error {
	if s.dbPool == nil {
		return nil
	}
	rows, err := s.dbPool.Query(ctx, `
		SELECT sticky_key, credential_id, expires_at
		FROM sticky_sessions
		WHERE expires_at > now()
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	loaded := 0
	for rows.Next() {
		var key string
		var credID int
		var expiresAt time.Time
		if err := rows.Scan(&key, &credID, &expiresAt); err != nil {
			continue
		}
		s.items[key] = stickyEntry{
			credentialID: credID,
			failures:     0,
			expiresAt:    expiresAt.Local(),
		}
		loaded++
	}
	if loaded > 0 {
		slog.Info("sticky cache restored from DB", "entries", loaded)
	}
	return rows.Err()
}

func (s *StickyCache) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
}

func (s *StickyCache) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// BuildClientStickyKey builds a stable client-scoped sticky key.
// The format is {tenant}:{app}:{key}:{profile}:{model}.
// Session ID and device fingerprint (fpSeed) are intentionally excluded:
// same client + same model = same fingerprint slot, regardless of which
// session the request belongs to or which device the request came from.
// This eliminates the per-session and per-fpHash fragmentation that
// used to consume one slot per chat session.
func BuildClientStickyKey(tenantID string, appID, apiKeyID *int, clientProfile, model string) string {
	profile := strings.TrimSpace(strings.ToLower(clientProfile))
	if profile == "" {
		profile = "default"
	}
	m := strings.TrimSpace(model)
	if m == "" {
		m = "*"
	}
	var app, key int
	if appID != nil {
		app = *appID
	}
	if apiKeyID != nil {
		key = *apiKeyID
	}
	return fmt.Sprintf("%s:%d:%d:%s:%s", tenantID, app, key, profile, m)
}
