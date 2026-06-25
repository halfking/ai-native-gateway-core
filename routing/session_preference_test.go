package routing

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSessionPreferenceStore_GetMiss(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	_, found, err := store.Get(ctx, "missing-session-id")
	if err != nil || found {
		t.Fatalf("expected miss, got found=%v err=%v", found, err)
	}
}

func TestSessionPreferenceStore_SetGet(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-sess-pref-1"
	const wantCredID = 42
	t.Cleanup(func() {
		_ = store.Clear(ctx, sessionID)
	})

	if err := store.Set(ctx, sessionID, wantCredID, "minimax-m3"); err != nil {
		t.Fatalf("Set err=%v", err)
	}

	got, found, err := store.Get(ctx, sessionID); var gotCredID int = 0; if got != nil { gotCredID = got.CredentialID }
	if err != nil || !found {
		t.Fatalf("Get err=%v found=%v", err, found)
	}
	if gotCredID != wantCredID {
		t.Fatalf("credID=%d, want %d", gotCredID, wantCredID)
	}
}

func TestSessionPreferenceStore_Clear(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-sess-pref-clear"
	_ = store.Set(ctx, sessionID, 99, "minimax-m3")

	// Clear 应成功
	if err := store.Clear(ctx, sessionID); err != nil {
		t.Fatalf("Clear err=%v", err)
	}

	// 再次 Get 应 miss
	_, found, _ := store.Get(ctx, sessionID)
	if found {
		t.Fatal("expected miss after Clear")
	}

	// 再次 Clear 应返回 ErrSessionPreferenceNotFound
	err := store.Clear(ctx, sessionID)
	if err != ErrSessionPreferenceNotFound {
		t.Fatalf("Clear on missing key err=%v, want ErrSessionPreferenceNotFound", err)
	}
}

func TestSessionPreferenceStore_Overwrite(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-sess-pref-overwrite"
	t.Cleanup(func() { _ = store.Clear(ctx, sessionID) })

	_ = store.Set(ctx, sessionID, 1, "minimax-m3")
	_ = store.Set(ctx, sessionID, 2, "minimax-m3")

	got, found, _ := store.Get(ctx, sessionID); var gotCredID int = 0; if got != nil { gotCredID = got.CredentialID }
	if !found || gotCredID != 2 {
		t.Fatalf("gotCredID=%d found=%v, want gotCredID=2 found=true", gotCredID, found)
	}
}

func TestSessionPreferenceStore_DifferentSessions(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessA = "test-sess-a"
	const sessB = "test-sess-b"
	t.Cleanup(func() {
		_ = store.Clear(ctx, sessA)
		_ = store.Clear(ctx, sessB)
	})

	_ = store.Set(ctx, sessA, 100, "minimax-m3")
	_ = store.Set(ctx, sessB, 200, "minimax-m3")

	gotA, _, _ := store.Get(ctx, sessA); var gotAID int = 0; if gotA != nil { gotAID = gotA.CredentialID }
	gotB, _, _ := store.Get(ctx, sessB); var gotBID int = 0; if gotB != nil { gotBID = gotB.CredentialID }

	if gotAID != 100 || gotBID != 200 {
		t.Fatalf("isolation broken: A=%d B=%d, want A=100 B=200", gotAID, gotBID)
	}
}

func TestSessionPreferenceStore_NilClient(t *testing.T) {
	store := NewSessionPreferenceStore(nil, 0)
	ctx := context.Background()

	_, found, err := store.Get(ctx, "any")
	if found || err != nil {
		t.Fatalf("nil client Get: found=%v err=%v", found, err)
	}

	if err := store.Set(ctx, "any", 1, "minimax-m3"); err != nil {
		t.Fatalf("nil client Set: err=%v", err)
	}
	if err := store.Clear(ctx, "any"); err != nil {
		t.Fatalf("nil client Clear: err=%v", err)
	}

	// nil receiver
	var nilStore *SessionPreferenceStore
	if _, _, err := nilStore.Get(ctx, "any"); err != nil {
		t.Fatalf("nil receiver Get: err=%v", err)
	}
}

func TestSessionPreferenceStore_EmptySessionID(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	// 空 sessionID 应被忽略（避免生成 session_pref: 这种空 key）
	_, found, _ := store.Get(ctx, "")
	if found {
		t.Fatal("empty sessionID should miss")
	}
	if err := store.Set(ctx, "", 1, "minimax-m3"); err != nil {
		t.Fatalf("empty sessionID Set err=%v", err)
	}
	if err := store.Clear(ctx, ""); err != nil {
		t.Fatalf("empty sessionID Clear err=%v", err)
	}
}

func TestSessionPreferenceStore_CorruptData(t *testing.T) {
	c := getTestRedis(t)
	defer c.Close()
	store := NewSessionPreferenceStore(c, 0)
	ctx := context.Background()

	const sessionID = "test-sess-pref-corrupt"
	key := sessionPreferenceRedisKey(sessionID)
	// 写入非法字符串
	if err := c.Set(ctx, key, "not-a-number", 0).Err(); err != nil {
		t.Fatalf("seed err=%v", err)
	}
	t.Cleanup(func() { _ = c.Del(ctx, key).Err() })

	_, found, _ := store.Get(ctx, sessionID)
	if found {
		t.Fatal("corrupt data should be treated as miss")
	}
}

func TestSessionPreferenceStore_RedisKeyFormat(t *testing.T) {
	// 验证 Redis key 格式与文档 §5.1 一致
	got := sessionPreferenceRedisKey("abc-123")
	want := "session_pref:abc-123"
	if got != want {
		t.Fatalf("key=%s, want %s", got, want)
	}
}

// 防止 import 警告
var _ = json.Valid