package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestStickyStoreSetGet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewStickyStore(rdb, 100, time.Hour)

	ctx := context.Background()
	// L1 key 模拟 buildStickyKeys 产出
	l1 := "tenant1:1:2:default:sess1:m"
	if err := s.Set(ctx, 1, l1, time.Hour); err != nil {
		t.Fatal(err)
	}
	credID, ok := s.Get(ctx, l1)
	if !ok || credID != 1 {
		t.Fatalf("expected credID=1, got %d ok=%v", credID, ok)
	}
	// LRU 命中第二次(不再回源 Redis)
	credID, ok = s.Get(ctx, l1)
	if !ok || credID != 1 {
		t.Fatalf("LRU hit failed: %d ok=%v", credID, ok)
	}
	// TTL 落到 Redis
	ttl := mr.TTL(StickyKey(1, l1))
	if ttl <= 0 {
		t.Fatal("sticky key missing TTL in Redis")
	}
}

func TestStickyStoreMissReturnsFalse(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewStickyStore(rdb, 100, time.Hour)
	_, ok := s.Get(context.Background(), "nonexistent:0:0:default")
	if ok {
		t.Fatal("expected miss for nonexistent key")
	}
}

func TestStickyStoreLevelInference(t *testing.T) {
	// levelOf 通过段数(冒号分隔)判定: 6段=L1, 5段=L2, 4段=L3
	tests := []struct {
		key  string
		want int
	}{
		{"t:1:2:default:sess:m", 1}, // 6 段 → L1
		{"t:1:2:default:m", 2},      // 5 段 → L2
		{"t:1:2:default", 3},        // 4 段 → L3
	}
	for _, tt := range tests {
		if got := levelOf(tt.key); got != tt.want {
			t.Errorf("levelOf(%q)=%d want %d", tt.key, got, tt.want)
		}
	}
}

func TestStickyStoreSetLevelExplicit(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	s := NewStickyStore(rdb, 100, time.Hour)
	ctx := context.Background()

	// 显式传 level=1 (L1), 不依赖 colon 计数
	rawKey := "tenant:app:1:profile-with:colon:sess:m" // profile 含冒号, 7 段
	if err := s.SetLevel(ctx, 1, 9, rawKey, time.Hour); err != nil {
		t.Fatal(err)
	}
	// Redis key 必须是 L1 前缀, 不是被 levelOf 误判
	redisKey := StickyKey(1, rawKey)
	if !mr.Exists(redisKey) {
		t.Fatalf("explicit L1 key not in redis: %s", redisKey)
	}
	credID, ok := s.GetLevel(ctx, 1, rawKey)
	if !ok || credID != 9 {
		t.Fatalf("GetLevel L1: want 9, got %d ok=%v", credID, ok)
	}
}
