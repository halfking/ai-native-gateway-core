package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/session"        //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

func newTestLastSystemSessionIndex(t *testing.T) (*session.LastSystemSessionIndex, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := session.NewRedisClient(mr.Addr(), "", 0)
	return session.NewLastSystemSessionIndex(rc), mr
}

// setRawIndexEntry writes an index entry verbatim (bypassing Set's
// time.Now stamp) so tests can control LastAssignedAt.
func setRawIndexEntry(t *testing.T, mr *miniredis.Miniredis, apiKeyID int, entry session.LastSystemSessionEntry) {
	t.Helper()
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := mr.Set(fmt.Sprintf("client:%d:last_system_session", apiKeyID), string(data)); err != nil {
		t.Fatalf("raw redis set: %v", err)
	}
}

// TestAssignGatewaySession_RedisIndexHitSkipsDBFinder pins the Wave4-D1
// fast path: a fresh index entry for (api_key, device seed) resumes the
// session without paying the request_logs_hot DB query.
func TestAssignGatewaySession_RedisIndexHitSkipsDBFinder(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	idx, _ := newTestLastSystemSessionIndex(t)
	h.SetSessionRouting(idx, nil)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_redis_hit": {SessionID: "gw_redis_hit", APIKeyID: 11, TenantID: "tenant-a", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	if err := idx.Set(context.Background(), 11, &session.LastSystemSessionEntry{
		SessionID:  "gw_redis_hit",
		DeviceSeed: "device-1",
	}); err != nil {
		t.Fatalf("index Set: %v", err)
	}
	finder := &stubRecentSessionFinder{sessionID: "gw_db_hit"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-1")
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`),
		r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !assignment.Resumed || !assignment.FromRecent {
		t.Fatalf("expected redis resume, got %+v", assignment)
	}
	if assignment.SessionID != "gw_redis_hit" {
		t.Fatalf("session_id = %q, want gw_redis_hit", assignment.SessionID)
	}
	if finder.called {
		t.Fatal("DB finder must not be consulted on a redis index hit")
	}
	if len(getter.created) != 0 {
		t.Fatalf("created sessions = %d, want 0", len(getter.created))
	}
}

// TestAssignGatewaySession_RedisIndexDeviceMismatchFallsToDB pins the
// identity gate: an entry seeded by another device must not be reused; the
// DB finder stays the authority.
func TestAssignGatewaySession_RedisIndexDeviceMismatchFallsToDB(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	idx, _ := newTestLastSystemSessionIndex(t)
	h.SetSessionRouting(idx, nil)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_db_hit": {SessionID: "gw_db_hit", APIKeyID: 11, TenantID: "tenant-a", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	if err := idx.Set(context.Background(), 11, &session.LastSystemSessionEntry{
		SessionID:  "gw_other_device",
		DeviceSeed: "device-2",
	}); err != nil {
		t.Fatalf("index Set: %v", err)
	}
	finder := &stubRecentSessionFinder{sessionID: "gw_db_hit"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-1")
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`),
		r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !finder.called {
		t.Fatal("device mismatch must fall through to the DB finder")
	}
	if assignment.SessionID != "gw_db_hit" {
		t.Fatalf("session_id = %q, want gw_db_hit from DB finder", assignment.SessionID)
	}
}

// TestAssignGatewaySession_RedisIndexRespectsReuseWindow pins the window
// re-check: the index TTL is fixed at 5 minutes but the reuse window is
// configurable — an entry older than the configured window falls to the DB
// finder even though the index still holds it.
func TestAssignGatewaySession_RedisIndexRespectsReuseWindow(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	idx, mr := newTestLastSystemSessionIndex(t)
	h.SetSessionRouting(idx, nil)
	h.SetSessionReuseWindow(10 * time.Second)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_db_hit": {SessionID: "gw_db_hit", APIKeyID: 11, TenantID: "tenant-a", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	setRawIndexEntry(t, mr, 11, session.LastSystemSessionEntry{
		SessionID:      "gw_too_old",
		DeviceSeed:     "device-1",
		LastAssignedAt: time.Now().Add(-time.Minute),
	})
	finder := &stubRecentSessionFinder{sessionID: "gw_db_hit"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-1")
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`),
		r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !finder.called {
		t.Fatal("stale entry must fall through to the DB finder")
	}
	if assignment.SessionID != "gw_db_hit" {
		t.Fatalf("session_id = %q, want gw_db_hit from DB finder", assignment.SessionID)
	}
}

// TestAssignGatewaySession_RedisIndexGoneSessionFallsToDB pins the
// existence check: an index entry whose session no longer exists must fall
// through to the DB finder instead of resuming a dead id.
func TestAssignGatewaySession_RedisIndexGoneSessionFallsToDB(t *testing.T) {
	h := NewChatHandler(nil, nil, nil, nil, nil, nil)
	idx, _ := newTestLastSystemSessionIndex(t)
	h.SetSessionRouting(idx, nil)
	getter := &stubSessionGetter{got: map[string]*session.Session{
		"gw_db_hit": {SessionID: "gw_db_hit", APIKeyID: 11, TenantID: "tenant-a", Namespace: "gw"},
	}}
	h.SetSessionGetter(getter)
	if err := idx.Set(context.Background(), 11, &session.LastSystemSessionEntry{
		SessionID:  "gw_vanished",
		DeviceSeed: "device-1",
	}); err != nil {
		t.Fatalf("index Set: %v", err)
	}
	finder := &stubRecentSessionFinder{sessionID: "gw_db_hit"}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Device-Seed", "device-1")
	keyInfo := &authentication.KeyInfo{ID: 11, TenantID: "tenant-a"}

	assignment, err := h.assignGatewaySessionWithFinder(context.Background(),
		[]byte(`{"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`),
		r, keyInfo, "", nil, "roocode", finder)
	if err != nil {
		t.Fatalf("assignGatewaySessionWithFinder() error = %v", err)
	}
	if !finder.called {
		t.Fatal("dead session id must fall through to the DB finder")
	}
	if assignment.SessionID != "gw_db_hit" {
		t.Fatalf("session_id = %q, want gw_db_hit from DB finder", assignment.SessionID)
	}
}
