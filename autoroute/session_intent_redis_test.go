package autoroute

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	ursmcache "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
)

func TestIntentDoubleWrite(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewIntentStore(rdb, 100, time.Minute)

	c := NewSessionIntentCache(time.Minute)
	c.SetRedisStore(store)

	c.Put("sess1", CachedIntent{TaskType: TaskChat, WorkType: "chat_general", ChosenModel: "m", CredentialID: 3})
	got, ok := c.Get("sess1")
	if !ok || got.CredentialID != 3 || got.ChosenModel != "m" {
		t.Fatalf("in-memory miss: %+v ok=%v", got, ok)
	}
	redisIn, ok := store.Get(context.Background(), "sess1")
	if !ok || redisIn.CredentialID != 3 || redisIn.ChosenModel != "m" || redisIn.WorkType != "chat_general" {
		t.Fatalf("Redis double-write missing: %+v ok=%v", redisIn, ok)
	}
}

func TestIntentRedisFallbackOnMemoryMiss(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := ursmcache.NewIntentStore(rdb, 100, time.Minute)

	c := NewSessionIntentCache(time.Minute)
	c.SetRedisStore(store)

	// 直接写 Redis (不经 Put), 模拟内存 miss 后回源
	ctx := context.Background()
	store.Set(ctx, "sess2", ursmcache.Intent{
		TaskType: string(TaskChat), ChosenModel: "gpt", CredentialID: 5,
	}, time.Minute)

	got, ok := c.Get("sess2")
	if !ok {
		t.Fatal("expected Redis fallback hit on memory miss")
	}
	if got.CredentialID != 5 || got.ChosenModel != "gpt" {
		t.Fatalf("fallback value wrong: %+v", got)
	}
	// TaskType 应从 string 还原回 TaskType 类型
	if got.TaskType != TaskChat {
		t.Errorf("TaskType roundtrip: want TaskChat, got %v", got.TaskType)
	}
	// 回填后内存应有
	if c.Len() != 1 {
		t.Errorf("memory backfill failed: Len=%d", c.Len())
	}
}

func TestIntentNilRedisStoreDegraded(t *testing.T) {
	c := NewSessionIntentCache(time.Minute)
	c.Put("s", CachedIntent{TaskType: TaskChat, ChosenModel: "m", CredentialID: 1})
	got, ok := c.Get("s")
	if !ok || got.CredentialID != 1 {
		t.Fatalf("nil-store lookup failed: %+v ok=%v", got, ok)
	}
}
