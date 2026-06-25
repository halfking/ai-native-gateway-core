package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// helper: create a Manager backed by miniredis
func newTestManager(t *testing.T) (*Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rc := NewRedisClient(mr.Addr(), "", 0)
	mgr := NewManager(rc, time.Hour)
	return mgr, mr
}

func TestNewManager_DefaultTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := NewRedisClient(mr.Addr(), "", 0)
	mgr := NewManager(rc, 0) // 0 → 7 days
	if mgr.ttl != 7*24*time.Hour {
		t.Fatalf("default TTL = %v, want 7d", mgr.ttl)
	}
}

func TestManager_CreateAndGet(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, err := mgr.Create(ctx, 42, "tenant-1", "device-x")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.SessionID == "" || sess.SessionKey == "" {
		t.Fatal("Create did not populate IDs")
	}

	got, err := mgr.Get(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.APIKeyID != 42 || got.TenantID != "tenant-1" {
		t.Fatalf("Get returned wrong fields: %+v", got)
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Get(context.Background(), "nonexistent")
	if err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_Get_Expired(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	// 直接在 redis 中把 expires_at 改成过去时间，模拟过期
	timeStr := time.Now().Add(-time.Hour).Format(time.RFC3339)
	_ = mgr.redis.HSet(ctx, "session:"+sess.SessionID, map[string]any{
		"expires_at": timeStr,
	})
	_, err := mgr.Get(ctx, sess.SessionID)
	if err != ErrSessionExpired {
		t.Fatalf("err = %v, want ErrSessionExpired", err)
	}
}

func TestManager_Delete(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	if err := mgr.Delete(ctx, sess.SessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := mgr.Get(ctx, sess.SessionID)
	if err != ErrSessionNotFound {
		t.Fatalf("after delete: err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_Delete_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	err := mgr.Delete(context.Background(), "nonexistent")
	if err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_Touch(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	if err := mgr.Touch(ctx, sess.SessionID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
}

func TestManager_BindAPIKey(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 0, "", "d") // orphan
	if err := mgr.BindAPIKey(ctx, sess.SessionID, 99, "tenant-x"); err != nil {
		t.Fatalf("BindAPIKey: %v", err)
	}
	got, _ := mgr.Get(ctx, sess.SessionID)
	if got.APIKeyID != 99 || got.TenantID != "tenant-x" {
		t.Fatalf("bind failed: %+v", got)
	}
}

func TestManager_BindAPIKey_AlreadyBound(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 7, "t", "d")
	err := mgr.BindAPIKey(ctx, sess.SessionID, 99, "tenant-x")
	if err == nil {
		t.Fatal("expected error when binding already-bound session")
	}
}

func TestManager_BindAPIKey_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	err := mgr.BindAPIKey(context.Background(), "nope", 99, "t")
	if err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_UpdateCacheInfo(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	cinfo := CacheInfo{OpenAICheckpoint: "ck-1"}
	if err := mgr.UpdateCacheInfo(ctx, sess.SessionID, cinfo); err != nil {
		t.Fatalf("UpdateCacheInfo: %v", err)
	}
	got, _ := mgr.Get(ctx, sess.SessionID)
	if got.ProviderCache.OpenAICheckpoint != "ck-1" {
		t.Fatalf("cache info not persisted: %+v", got.ProviderCache)
	}
}

func TestManager_GetBySessionKey(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d")
	got, err := mgr.GetBySessionKey(ctx, sess.SessionKey)
	if err != nil {
		t.Fatalf("GetBySessionKey: %v", err)
	}
	if got.SessionID != sess.SessionID {
		t.Fatalf("GetBySessionKey returned %s, want %s", got.SessionID, sess.SessionID)
	}
}

func TestManager_GetBySessionKey_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.GetBySessionKey(context.Background(), "bogus")
	if err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_Migrate_NewDevice(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d-1")
	migrated, err := mgr.Migrate(ctx, sess.SessionID, "d-2")
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(migrated.Devices) != 2 {
		t.Fatalf("expected 2 devices, got %d", len(migrated.Devices))
	}
}

func TestManager_Migrate_ExistingDevice(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, _ := mgr.Create(ctx, 1, "t", "d-1")
	migrated, _ := mgr.Migrate(ctx, sess.SessionID, "d-1") // same device
	if len(migrated.Devices) != 1 {
		t.Fatalf("expected 1 device (existing), got %d", len(migrated.Devices))
	}
}

func TestManager_Migrate_NotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	_, err := mgr.Migrate(context.Background(), "nope", "d")
	if err != ErrSessionNotFound {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestManager_CreateV2(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	sess, err := mgr.CreateV2(ctx, 1, "t", "d", "task-x")
	if err != nil {
		t.Fatalf("CreateV2: %v", err)
	}
	if sess.SessionID == "" {
		t.Fatal("CreateV2 did not return session")
	}
	if sess.TaskID != "task-x" {
		t.Fatalf("TaskID = %q", sess.TaskID)
	}
}

func TestManager_CreateV2_DefaultTask(t *testing.T) {
	mgr, _ := newTestManager(t)
	sess, _ := mgr.CreateV2(context.Background(), 1, "t", "d", "")
	if sess.TaskID != "default" {
		t.Fatalf("TaskID = %q, want default", sess.TaskID)
	}
	if sess.Namespace != "gw" {
		t.Fatalf("Namespace = %q, want gw", sess.Namespace)
	}
}

func TestSessionGetters(t *testing.T) {
	s := &Session{
		APIKeyID:   42,
		TenantID:   "tenant-x",
		SessionKey: "key-abc",
	}
	if s.GetAPIKeyID() != 42 {
		t.Errorf("GetAPIKeyID = %d", s.GetAPIKeyID())
	}
	if s.GetTenantID() != "tenant-x" {
		t.Errorf("GetTenantID = %q", s.GetTenantID())
	}
	if s.GetSessionKey() != "key-abc" {
		t.Errorf("GetSessionKey = %q", s.GetSessionKey())
	}
}

func TestParseTime_Empty(t *testing.T) {
	got, err := parseTime("")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("expected zero time, got %v", got)
	}
}

func TestParseTime_RFC3339(t *testing.T) {
	got, err := parseTime("2026-06-25T10:00:00Z")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.Year() != 2026 {
		t.Fatalf("year = %d", got.Year())
	}
}

func TestParseTime_Invalid(t *testing.T) {
	_, err := parseTime("not a time")
	if err == nil {
		t.Fatal("expected error for invalid time")
	}
}

func TestRedisClient_Ping(t *testing.T) {
	mr := miniredis.RunT(t)
	rc := NewRedisClient(mr.Addr(), "", 0)
	if err := rc.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestSessionFromContext_Empty(t *testing.T) {
	if s := SessionFromContext(context.Background()); s != nil {
		t.Fatal("empty ctx should yield nil")
	}
}

func TestSessionFromContext_RoundTrip(t *testing.T) {
	s := &Session{SessionID: "x"}
	ctx := SessionFromContextWith(context.Background(), s)
	if got := SessionFromContext(ctx); got != s {
		t.Fatal("round-trip failed")
	}
}

func TestSessionFromContextWith_NilIgnored(t *testing.T) {
	ctx := SessionFromContextWith(context.Background(), nil)
	if got := SessionFromContext(ctx); got != nil {
		t.Fatal("nil session should not be set in context")
	}
}

func TestSetAPIKeyIDAndGet(t *testing.T) {
	ctx := SetAPIKeyID(context.Background(), 99)
	if got := GetAPIKeyIDFromContext(ctx); got != 99 {
		t.Fatalf("got %d, want 99", got)
	}
	if got := GetAPIKeyIDFromContext(context.Background()); got != 0 {
		t.Fatalf("default got %d, want 0", got)
	}
}

func TestSetTenantIDAndGet(t *testing.T) {
	ctx := SetTenantID(context.Background(), "tenant-x")
	if got := GetTenantIDFromContext(ctx); got != "tenant-x" {
		t.Fatalf("got %q, want tenant-x", got)
	}
	if got := GetTenantIDFromContext(context.Background()); got != "default" {
		t.Fatalf("default got %q, want default", got)
	}
}
