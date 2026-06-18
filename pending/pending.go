// Package pending caches vendor responses for client reconnect and
// vendor async-retry. See docs/go-migration/2026-06-18-llm-gateway-
// stability-design.md Track C for the full design.
//
// Two related problems share this storage:
//
//  1. Client disconnects mid-stream — we keep reading upstream, cache
//     the body, and let the client pick it up on reconnect via
//     GET /v1/sessions/:id/pending-response (see sessions/handler.go).
//
//  2. Vendor is slow / overloaded beyond the short retry window — we
//     spawn an async goroutine, fall back to other credentials, and
//     write the eventual result here. The client polls the same
//     endpoint above.
//
// Storage: a dedicated *redis.Client (we do NOT share the sessions
// connection because the key space is distinct and the access
// patterns are different: pending writes are bursty and short-lived,
// sessions are mostly-read). The connection is created in main.go
// and passed in via NewStore.
package pending

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// DefaultTTL is the lifetime of a pending entry in Redis. Mirrors
	// sessions/session.go default of 7 days.
	DefaultTTL = 7 * 24 * time.Hour

	// MaxBodyBytes is the cap on what we'll cache. A 1MB LLM response
	// is enormous (>> most streamed completions). Above this we
	// keep only the metadata + an error message so a malicious
	// upstream cannot blow up Redis.
	MaxBodyBytes = 1 << 20 // 1 MiB

	// keyPrefix is the Redis key prefix for all pending entries.
	keyPrefix = "pending_response"

	// indexPrefix is the ZSET key that lists all (requestID, completedAt)
	// for a given session. Used by GET (no request_id) to find the
	// most recent entry.
	indexPrefix = "pending_response:index"
)

// Status is the lifecycle state of a pending entry.
type Status string

const (
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusFailed     Status = "failed"
)

// Response is a cached vendor response (or a failure record) keyed
// by (sessionID, requestID). The full response body is in Body.
// On overflow we store a stub in Body pointing the operator to the
// /request-logs UI for the full content.
type Response struct {
	SessionID     string `json:"session_id"`
	TenantID      string `json:"tenant_id"`
	RequestID     string `json:"request_id"`
	Status        Status `json:"status"`
	Body          string `json:"body,omitempty"`         // SSE text or JSON body
	ContentType   string `json:"content_type"`          // "text/event-stream" | "application/json"
	ProviderID    int    `json:"provider_id"`
	CredentialID  int    `json:"credential_id"`
	RequestHash   string `json:"request_hash"`           // sha256 of request body (anti-tamper)
	CreatedAt     int64  `json:"created_at"`             // unix seconds
	CompletedAt   int64  `json:"completed_at,omitempty"`
	BytesBuffered int    `json:"bytes_buffered"`
	IsStream      bool   `json:"is_stream"`
	ErrorMessage  string `json:"error_message,omitempty"` // populated when Status=StatusFailed
}

// Store is the persistence layer. Always safe for concurrent use;
// all methods are no-ops if the underlying *redis.Client is nil (so
// production code can pass nil and degrade gracefully when Redis is
// unreachable or disabled).
type Store struct {
	rdb *redis.Client
	ttl time.Duration
}

// NewStore returns a Store backed by the supplied redis client. If
// rdb is nil the Store is a no-op (every call returns ErrUnavailable).
// ttl <= 0 falls back to DefaultTTL.
func NewStore(rdb *redis.Client, ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{rdb: rdb, ttl: ttl}
}

// ErrUnavailable is returned when the Store is constructed with a
// nil redis client. Callers should treat this as a soft failure — the
// rest of the request flow continues, the cache step is just skipped.
var ErrUnavailable = errors.New("pending store unavailable (redis client nil)")

// entryKey returns the Redis hash key for a (sessionID, requestID).
func entryKey(sessionID, requestID string) string {
	return keyPrefix + ":" + sessionID + ":" + requestID
}

// indexKey returns the Redis ZSET key for a session's pending
// entries, scored by CompletedAt (unix seconds). Lets GET (no
// request_id) find the most recent entry with ZRANGE … REV.
func indexKey(sessionID string) string {
	return indexPrefix + ":" + sessionID
}

// MarkInProgress writes a placeholder row with Status=in_progress
// so a concurrent reconnect can poll status before completion.
// Idempotent: if the row already exists with a different request_id
// under the same session, the existing row is preserved (the index
// ZSET is updated to add this new request_id).
func (s *Store) MarkInProgress(ctx context.Context, r *Response) error {
	if r == nil {
		return errors.New("pending: nil response")
	}
	if r.SessionID == "" || r.RequestID == "" {
		return errors.New("pending: SessionID and RequestID required")
	}
	if s == nil || s.rdb == nil {
		return ErrUnavailable
	}
	r.Status = StatusInProgress
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	return s.write(ctx, r)
}

// Save replaces the row with the final status. Used both on success
// and failure. If body is larger than MaxBodyBytes, only a stub is
// stored and BytesBuffered reflects the actual size; the operator
// must consult request_logs for the full body.
func (s *Store) Save(ctx context.Context, r *Response) error {
	if r == nil {
		return errors.New("pending: nil response")
	}
	if r.SessionID == "" || r.RequestID == "" {
		return errors.New("pending: SessionID and RequestID required")
	}
	if s == nil || s.rdb == nil {
		return ErrUnavailable
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = time.Now().Unix()
	}
	r.CompletedAt = time.Now().Unix()

	if len(r.Body) > MaxBodyBytes {
		originalBytes := len(r.Body)
		r.Body = fmt.Sprintf(`{"error":{"message":"response_too_large: %d bytes (limit %d); see request_logs for full body","code":"response_too_large"}}`,
			originalBytes, MaxBodyBytes)
		r.BytesBuffered = MaxBodyBytes
	} else {
		r.BytesBuffered = len(r.Body)
	}

	return s.write(ctx, r)
}

// Get returns the entry for (sessionID, requestID), or
// (sessionID, ErrNotFound) if absent. The bool is false in that case.
func (s *Store) Get(ctx context.Context, sessionID, requestID string) (*Response, bool, error) {
	if s == nil || s.rdb == nil {
		return nil, false, ErrUnavailable
	}
	if sessionID == "" || requestID == "" {
		return nil, false, errors.New("pending: SessionID and RequestID required")
	}
	fields, err := s.rdb.HGetAll(ctx, entryKey(sessionID, requestID)).Result()
	if err != nil {
		return nil, false, fmt.Errorf("pending: hgetall: %w", err)
	}
	if len(fields) == 0 {
		return nil, false, nil
	}
	return parseResponse(sessionID, fields), true, nil
}

// GetLatest returns the most-recently-completed entry for sessionID,
// used by the GET endpoint when no request_id is supplied. Returns
// (nil, false, nil) when the session has no entries.
func (s *Store) GetLatest(ctx context.Context, sessionID string) (*Response, string, bool, error) {
	if s == nil || s.rdb == nil {
		return nil, "", false, ErrUnavailable
	}
	if sessionID == "" {
		return nil, "", false, errors.New("pending: SessionID required")
	}
	// ZRANGE with REV + LIMIT 1 = most-recent.
	zs, err := s.rdb.ZRevRange(ctx, indexKey(sessionID), 0, 0).Result()
	if err != nil {
		return nil, "", false, fmt.Errorf("pending: zrevrange: %w", err)
	}
	if len(zs) == 0 {
		return nil, "", false, nil
	}
	requestID := zs[0]
	r, ok, err := s.Get(ctx, sessionID, requestID)
	return r, requestID, ok, err
}

// Delete removes the entry. Used by tests and the admin API.
func (s *Store) Delete(ctx context.Context, sessionID, requestID string) error {
	if s == nil || s.rdb == nil {
		return ErrUnavailable
	}
	if sessionID == "" || requestID == "" {
		return errors.New("pending: SessionID and RequestID required")
	}
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, entryKey(sessionID, requestID))
	pipe.ZRem(ctx, indexKey(sessionID), requestID)
	_, err := pipe.Exec(ctx)
	return err
}

// StaleEntry is the lightweight projection returned by
// ListStaleInProgress — just the fields the sweeper needs to
// decide what to do, not the full Response. The full entry is
// reloaded by the sweeper if it wants to mark it failed.
type StaleEntry struct {
	SessionID string
	RequestID string
	CreatedAt int64
	TenantID  string
}

// ListStaleInProgress returns entries that are still
// status=in_progress but whose created_at is older than
// staleBefore (typically now - staleTimeout). The sweeper uses
// this to clean up entries abandoned by a crashed async
// goroutine or a misbehaving client.
//
// Implementation notes:
//   - Uses SCAN (not KEYS) so it does not block the Redis
//     event loop on large key spaces. COUNT is a hint, not a
//     guarantee; we loop until cursor==0.
//   - For each candidate hash, we HGETALL and parse only the
//     status + created_at + tenant_id fields. Bodies are not
//     pulled — the sweeper is not interested in the cached
//     response, only in deciding whether to mark it failed.
//   - The candidate is dropped if the hash is missing (race
//     with Delete) or if parsing fails (schema drift).
//
// The store-level cap (cap) is a no-op for reads; SCAN returns
// whatever the backing Redis has, and we filter in Go. The
// caller (the sweeper) is expected to call this with a small
// batch size to avoid OOM on mis-configured installs.
func (s *Store) ListStaleInProgress(ctx context.Context, staleBefore time.Time, batchSize int64) ([]StaleEntry, error) {
	if s == nil || s.rdb == nil {
		return nil, ErrUnavailable
	}
	if batchSize <= 0 {
		batchSize = 100
	}
	staleBeforeUnix := staleBefore.Unix()
	pattern := keyPrefix + ":*"
	var (
		cursor   uint64
		results  []StaleEntry
		seen     = make(map[string]struct{})
	)
	for {
		keys, nextCursor, err := s.rdb.Scan(ctx, cursor, pattern, batchSize).Result()
		if err != nil {
			return nil, fmt.Errorf("pending: scan: %w", err)
		}
		for _, k := range keys {
			// We only want hash keys (not the ZSET index keys).
			// The index key has the form
			// pending_response:index:{sid} which doesn't match
			// the keyPrefix+":*:*" pattern; the entry key has
			// the form pending_response:{sid}:{rid} which
			// does. So this loop only sees entry keys.
			sid, rid, ok := splitEntryKey(k)
			if !ok {
				continue
			}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			fields, err := s.rdb.HGetAll(ctx, k).Result()
			if err != nil || len(fields) == 0 {
				continue
			}
			r := parseResponse(sid, fields)
			if r == nil || r.Status != StatusInProgress {
				continue
			}
			if r.CreatedAt == 0 || r.CreatedAt > staleBeforeUnix {
				continue
			}
			results = append(results, StaleEntry{
				SessionID: sid,
				RequestID: rid,
				CreatedAt: r.CreatedAt,
				TenantID:  r.TenantID,
			})
		}
		if nextCursor == 0 {
			break
		}
		cursor = nextCursor
	}
	return results, nil
}

// splitEntryKey is the inverse of entryKey; it parses
// "pending_response:{sid}:{rid}" and returns the two halves.
// Returns ok=false for the index key (which is
// "pending_response:index:{sid}" — different shape).
func splitEntryKey(k string) (sessionID, requestID string, ok bool) {
	const entryPrefix = "pending_response:"
	if !strings.HasPrefix(k, entryPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(k, entryPrefix)
	// Skip the index key (pending_response:index:{sid}).
	if strings.HasPrefix(rest, "index:") {
		return "", "", false
	}
	// Expect exactly one ':' separator between sid and rid.
	idx := strings.IndexByte(rest, ':')
	if idx <= 0 || idx == len(rest)-1 {
		return "", "", false
	}
	return rest[:idx], rest[idx+1:], true
}

// write persists the response (hash + index) atomically. Internal.
func (s *Store) write(ctx context.Context, r *Response) error {
	fields := map[string]any{
		"session_id":      r.SessionID,
		"tenant_id":       r.TenantID,
		"request_id":      r.RequestID,
		"status":          string(r.Status),
		"body":            r.Body,
		"content_type":    r.ContentType,
		"provider_id":     r.ProviderID,
		"credential_id":   r.CredentialID,
		"request_hash":    r.RequestHash,
		"created_at":      r.CreatedAt,
		"completed_at":    r.CompletedAt,
		"bytes_buffered":  r.BytesBuffered,
		"is_stream":       r.IsStream,
		"error_message":   r.ErrorMessage,
	}
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, entryKey(r.SessionID, r.RequestID), fields)
	pipe.Expire(ctx, entryKey(r.SessionID, r.RequestID), s.ttl)
	score := r.CompletedAt
	if score == 0 {
		score = r.CreatedAt
	}
	pipe.ZAdd(ctx, indexKey(r.SessionID), redis.Z{
		Score:  float64(score),
		Member: r.RequestID,
	})
	pipe.Expire(ctx, indexKey(r.SessionID), s.ttl)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("pending: write: %w", err)
	}
	return nil
}

// parseResponse deserializes a Redis hash map back into a Response.
// Tolerant of missing fields (Redis doesn't store empty strings with
// HSet unless we explicitly set them).
func parseResponse(sessionID string, fields map[string]string) *Response {
	r := &Response{SessionID: sessionID}
	if v, ok := fields["tenant_id"]; ok {
		r.TenantID = v
	}
	if v, ok := fields["request_id"]; ok {
		r.RequestID = v
	}
	if v, ok := fields["status"]; ok {
		r.Status = Status(v)
	}
	if v, ok := fields["body"]; ok {
		r.Body = v
	}
	if v, ok := fields["content_type"]; ok {
		r.ContentType = v
	}
	if v, ok := fields["provider_id"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			r.ProviderID = n
		}
	}
	if v, ok := fields["credential_id"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			r.CredentialID = n
		}
	}
	if v, ok := fields["request_hash"]; ok {
		r.RequestHash = v
	}
	if v, ok := fields["created_at"]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			r.CreatedAt = n
		}
	}
	if v, ok := fields["completed_at"]; ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			r.CompletedAt = n
		}
	}
	if v, ok := fields["bytes_buffered"]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			r.BytesBuffered = n
		}
	}
	if v, ok := fields["is_stream"]; ok {
		r.IsStream = v == "1" || v == "true"
	}
	if v, ok := fields["error_message"]; ok {
		r.ErrorMessage = v
	}
	return r
}

// Enabled reports whether the Store has a live Redis backend. The
// rest of the request flow uses this to skip the cache step
// gracefully when Redis is unavailable.
func (s *Store) Enabled() bool {
	return s != nil && s.rdb != nil
}

// TTL returns the configured lifetime. Used by the sweeper to
// determine when an "in_progress" row is stale.
func (s *Store) TTL() time.Duration {
	if s == nil {
		return 0
	}
	return s.ttl
}
