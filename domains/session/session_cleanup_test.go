package session

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestCleanupWorkerUsesStoppedSetIndex(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	worker := NewCleanupWorker(rdb, time.Minute, time.Hour)
	ctx := context.Background()
	setKey := "session:stopped:tenant-a"

	if err := rdb.SAdd(ctx, stoppedSessionIndexKey, setKey).Err(); err != nil {
		t.Fatalf("index stopped set: %v", err)
	}
	if err := rdb.SAdd(ctx, setKey, "gone-session").Err(); err != nil {
		t.Fatalf("index session: %v", err)
	}
	if err := worker.scanOnce(ctx); err != nil {
		t.Fatalf("scanOnce: %v", err)
	}
	members, err := rdb.SMembers(ctx, setKey).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("stale stopped member remains: %v", members)
	}
}

func TestCleanupWorkerSeedsEmptyStoppedSetIndex(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	worker := NewCleanupWorker(rdb, time.Minute, time.Hour)

	if err := worker.scanOnce(context.Background()); err != nil {
		t.Fatalf("scanOnce: %v", err)
	}
	indexed, err := rdb.SIsMember(context.Background(), stoppedSessionIndexKey, stoppedSessionIndexSentinel).Result()
	if err != nil || !indexed {
		t.Fatalf("empty stopped-session index was not seeded: indexed=%v err=%v", indexed, err)
	}
}
