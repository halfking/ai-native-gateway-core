package pending

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStore_SaveDurableCAS(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	store := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	expiresAt := time.Now().Add(time.Hour)

	base := &Response{
		SessionID:     "sess-1",
		TenantID:      "tenant-1",
		RequestID:     "req-1",
		Status:        StatusCompleted,
		Body:          "v1",
		ContentType:   "application/json",
		RequestHash:   "request-hash",
		TaskID:        "task-1",
		FencingToken:  3,
		ResultVersion: 1,
		ResultHash:    "result-hash-1",
	}

	result, err := store.SaveDurableCAS(ctx, base, expiresAt)
	if err != nil || result != DurableCASApplied {
		t.Fatalf("initial save: result=%v err=%v", result, err)
	}

	cases := []struct {
		name        string
		taskID      string
		version     int64
		body        string
		want        DurableCASResult
		wantStored  string
		wantVersion int64
	}{
		{"same task higher version applies", "task-1", 2, "v2", DurableCASApplied, "v2", 2},
		{"same task stale version rejected", "task-1", 1, "stale", DurableCASStale, "v2", 2},
		{"different task rejected", "task-2", 3, "foreign", DurableCASRejectedTask, "v2", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := *base
			next.TaskID = tc.taskID
			next.ResultVersion = tc.version
			next.Body = tc.body
			result, err := store.SaveDurableCAS(ctx, &next, expiresAt)
			if err != nil || result != tc.want {
				t.Fatalf("save: result=%v err=%v, want %v", result, err, tc.want)
			}
			got, found, err := store.Get(ctx, "sess-1", "req-1")
			if err != nil || !found {
				t.Fatalf("get: found=%v err=%v", found, err)
			}
			if got.Body != tc.wantStored || got.ResultVersion != tc.wantVersion || got.TaskID != "task-1" {
				t.Fatalf("stored = %+v", got)
			}
		})
	}
}

func TestStore_SaveDurableCAS_DoesNotDowngradeTerminalEntry(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	store := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	expiresAt := time.Now().Add(time.Hour)
	terminal := &Response{
		SessionID: "sess-1", TenantID: "tenant-1", RequestID: "req-1",
		TaskID: "approval-1", ResultVersion: 2, Status: StatusCompleted, Body: "done",
	}
	if result, err := store.SaveDurableCAS(ctx, terminal, expiresAt); err != nil || result != DurableCASApplied {
		t.Fatalf("save terminal: result=%v err=%v", result, err)
	}
	inProgress := *terminal
	inProgress.Status = StatusInProgress
	inProgress.Body = "resuming"
	inProgress.ResultVersion = 3
	if result, err := store.SaveDurableCAS(ctx, &inProgress, expiresAt); err != nil || result != DurableCASStale {
		t.Fatalf("save in-progress after terminal: result=%v err=%v", result, err)
	}
	got, found, err := store.Get(ctx, "sess-1", "req-1")
	if err != nil || !found || got.Status != StatusCompleted || got.Body != "done" {
		t.Fatalf("terminal entry was downgraded: got=%+v found=%v err=%v", got, found, err)
	}
}

func TestStore_SaveDurableCAS_BindsLegacyInProgressEntry(t *testing.T) {
	ctx := context.Background()
	mr := miniredis.RunT(t)
	store := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	if err := store.MarkInProgress(ctx, &Response{
		SessionID: "sess-1", RequestID: "req-1", TenantID: "tenant-1",
	}); err != nil {
		t.Fatalf("mark in progress: %v", err)
	}
	result, err := store.SaveDurableCAS(ctx, &Response{
		SessionID: "sess-1", RequestID: "req-1", TenantID: "tenant-1",
		TaskID: "task-1", ResultVersion: 1, Status: StatusCompleted, Body: "done",
	}, time.Now().Add(time.Hour))
	if err != nil || result != DurableCASApplied {
		t.Fatalf("result=%v err=%v", result, err)
	}
	got, found, err := store.Get(ctx, "sess-1", "req-1")
	if err != nil || !found || got.TaskID != "task-1" || got.Body != "done" {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
}

func TestStore_SaveDurableCAS_ExpiredProjectionSkipped(t *testing.T) {
	mr := miniredis.RunT(t)
	store := NewStore(redis.NewClient(&redis.Options{Addr: mr.Addr()}), time.Hour)
	result, err := store.SaveDurableCAS(context.Background(), &Response{
		SessionID: "sess-1", RequestID: "req-1", TaskID: "task-1", ResultVersion: 1,
	}, time.Now().Add(-time.Second))
	if err != nil || result != DurableCASStale {
		t.Fatalf("result=%v err=%v", result, err)
	}
	if mr.Exists(entryKey("sess-1", "req-1")) {
		t.Fatal("expired projection must not create a Redis entry")
	}
}
