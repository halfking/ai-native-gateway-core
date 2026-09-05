package session

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

type redisCommandCounter struct {
	hgetAll atomic.Int64
	hget    atomic.Int64
}

func (h *redisCommandCounter) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *redisCommandCounter) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		switch cmd.Name() {
		case "hgetall":
			h.hgetAll.Add(1)
		case "hget":
			h.hget.Add(1)
		}
		return next(ctx, cmd)
	}
}

func (h *redisCommandCounter) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error { return next(ctx, cmds) }
}

func newCountedSessionManager(t *testing.T) (*Manager, *redisCommandCounter) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	counter := &redisCommandCounter{}
	client.AddHook(counter)
	t.Cleanup(func() { _ = client.Close() })
	return NewManager(NewRedisClientFromClient(client), time.Hour), counter
}

func TestGetEnrichedSessionReadsSessionHashOnce(t *testing.T) {
	mgr, counter := newCountedSessionManager(t)
	ctx := context.Background()
	sess, err := mgr.Create(ctx, 7, "tenant-1", "device-1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	counter.hgetAll.Store(0)
	counter.hget.Store(0)

	got, stats, rotations, err := mgr.GetEnrichedSession(ctx, sess.SessionID)
	if err != nil {
		t.Fatalf("GetEnrichedSession failed: %v", err)
	}
	if got.SessionID != sess.SessionID || stats == nil || rotations == nil {
		t.Fatalf("unexpected enriched session result: session=%+v stats=%+v rotations=%v", got, stats, rotations)
	}
	if gotCount := counter.hgetAll.Load(); gotCount != 1 {
		t.Fatalf("HGETALL count = %d, want 1", gotCount)
	}
	if gotCount := counter.hget.Load(); gotCount != 0 {
		t.Fatalf("HGET count = %d, want 0", gotCount)
	}
}

func TestStopSessionReusesInitialSessionHash(t *testing.T) {
	mgr, counter := newCountedSessionManager(t)
	ctx := context.Background()
	sess, err := mgr.Create(ctx, 7, "tenant-1", "device-1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if err := mgr.StartCredRotation(ctx, sess.SessionID, 11, "model", "provider", SwitchReasonInitial); err != nil {
		t.Fatalf("StartCredRotation failed: %v", err)
	}
	counter.hgetAll.Store(0)
	counter.hget.Store(0)

	if err := mgr.StopSession(ctx, sess.SessionID, "test"); err != nil {
		t.Fatalf("StopSession failed: %v", err)
	}
	if gotCount := counter.hgetAll.Load(); gotCount != 1 {
		t.Fatalf("HGETALL count = %d, want 1", gotCount)
	}
	if gotCount := counter.hget.Load(); gotCount != 0 {
		t.Fatalf("HGET count = %d, want 0", gotCount)
	}
}

func TestStartCredRotationPreservesTurnAndTTLWithLuaUpdate(t *testing.T) {
	mgr, _ := newCountedSessionManager(t)
	ctx := context.Background()
	sess, err := mgr.Create(ctx, 7, "tenant-1", "device-1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if err := mgr.TouchUsage(ctx, sess.SessionID, UsageUpdate{PromptTokens: 1}); err != nil {
		t.Fatalf("TouchUsage failed: %v", err)
	}
	if err := mgr.StartCredRotation(ctx, sess.SessionID, 11, "model", "provider", SwitchReasonRotate); err != nil {
		t.Fatalf("StartCredRotation failed: %v", err)
	}

	data, err := mgr.redis.HGetAll(ctx, "session:"+sess.SessionID)
	if err != nil {
		t.Fatalf("HGetAll failed: %v", err)
	}
	if got := data[FieldCurrentCredStartTurn]; got != "1" {
		t.Fatalf("current credential start turn = %q, want 1", got)
	}
	if ttl, err := mgr.redis.client.TTL(ctx, "session:"+sess.SessionID).Result(); err != nil || ttl <= 0 {
		t.Fatalf("session TTL = %s, err=%v; want positive TTL", ttl, err)
	}

	rotationJSON, err := mgr.redis.client.LIndex(ctx, credRotationsKey(sess.SessionID), 0).Result()
	if err != nil {
		t.Fatalf("LIndex failed: %v", err)
	}
	var entry CredRotationEntry
	if err := json.Unmarshal([]byte(rotationJSON), &entry); err != nil {
		t.Fatalf("unmarshal rotation: %v", err)
	}
	if entry.CredentialID != 11 || entry.SwitchReason != SwitchReasonRotate || entry.EndedAt != nil {
		t.Fatalf("rotation entry = %+v", entry)
	}
}
