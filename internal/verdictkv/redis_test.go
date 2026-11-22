package verdictkv

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestParseUsedMemory(t *testing.T) {
	info := "# Memory\r\nused_memory:1234567\r\nused_memory_human:1.18M\r\nused_memory_peak:2097152\r\nused_memory_dataset:1000\r\n"
	if n, ok := parseUsedMemory(info); !ok || n != 1234567 {
		t.Fatalf("parseUsedMemory = %d %v, want 1234567", n, ok)
	}
	if _, ok := parseUsedMemory("used_memory_human:1.18M\r\n"); ok {
		t.Fatal("只有 used_memory_human 时不得误判为 used_memory")
	}
	if _, ok := parseUsedMemory(""); ok {
		t.Fatal("空文本不得误判")
	}
}

func TestRedisKVRoundTrip(t *testing.T) {
	if NewRedis(nil, "") != nil {
		t.Fatal("nil client must yield a nil backend")
	}
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer client.Close()
	kv := NewRedis(client, "llmgw:histmemo:")
	ctx := context.Background()

	if _, ok, err := kv.Get(ctx, "k1"); ok || err != nil {
		t.Fatalf("missing key: ok=%v err=%v", ok, err)
	}
	if err := kv.Set(ctx, "k1", "0", time.Hour); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := kv.Get(ctx, "k1"); !ok || v != "0" || err != nil {
		t.Fatalf("round trip: v=%q ok=%v err=%v", v, ok, err)
	}
	if keys := srv.Keys(); len(keys) != 1 || keys[0] != "llmgw:histmemo:k1" {
		t.Fatalf("key must carry the prefix: %v", keys)
	}
	if ttl := srv.TTL("llmgw:histmemo:k1"); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("TTL = %v", ttl)
	}
	// miniredis 的 INFO 不一定带 used_memory ⇒ 这里只钉「后端可用时不报连接错」；
	// used_memory 的解析由 TestParseUsedMemory 钉。
	if _, err := kv.UsedMemory(ctx); err != nil && !errors.Is(err, errNoUsedMemory) {
		t.Fatalf("UsedMemory 对可用后端不应报连接错: %v", err)
	}
	srv.Close()
	if _, _, err := kv.Get(ctx, "k1"); err == nil {
		t.Fatal("后端不可用时必须报错（由调用方按未命中处理）")
	}
}
