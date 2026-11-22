package main

// history_guard_memo_init_typednil_test.go —— 类型化 nil 的 Redis 客户端
// 不得被装进共享层（2026-11-23，12h 变更审计 A）。
//
// 生产形态：cmd/gateway 把可能为 nil 的 *redis.Client 传进
// redis.UniversalClient 形参 ⇒ 接口值非 nil、底下是空指针 ⇒ 两道
// `== nil` 闸都放行 ⇒ 共享层被装上 ⇒ 首次请求 client.Get(...) 空指针
// panic；该守卫又跑在 internal/sessioncontext 的 worker 协程里 ⇒ 进程级崩溃。

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/internal/verdictkv"
)

// assertNoPanic 把 panic 变成一条可读的失败信息（而不是让测试二进制直接死）。
func assertNoPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s 触发了 panic（共享层被错误装上 ⇒ 首次请求会进程级崩溃）: %v", what, r)
		}
	}()
	fn()
}

func TestNewHistoryVerdictMemoTypedNilRedisClient(t *testing.T) {
	// 生产形态：nil 的 *redis.Client 被装进 redis.UniversalClient 接口。
	var typedNil *redis.Client
	var client redis.UniversalClient = typedNil
	if !verdictkv.IsNilClient(client) {
		t.Fatal("测试前提不成立：类型化 nil 必须被判为不可用")
	}

	cfg := historyMemoEnv{
		LocalEntries:       64,
		SharedTTL:          time.Hour,
		SharedMaxUsedBytes: 1 << 20,
		SharedMaxWrites:    100,
	}
	memo := newHistoryVerdictMemo(cfg, client)
	if memo == nil {
		t.Fatal("memo 不应为 nil")
	}

	// 共享层不得被装上：Lookup / Store 都不得 panic。
	assertNoPanic(t, "Lookup", func() {
		if _, ok := memo.Lookup(context.Background(), [32]byte{1}); ok {
			t.Fatal("没有共享层时不应命中")
		}
	})
	assertNoPanic(t, "Store", func() {
		memo.Store(context.Background(), [32]byte{1}, true)
	})
	// ★ 判据用「共享层压根没装上」，而不是「没 panic」：
	//   共享层没装上 ⇒ Lookup 直接记 miss、Store 直接返回 ⇒
	//     SharedHits/SharedErrors/SharedWrites/SharedSkipped 全为 0；
	//   共享层装了但客户端是坏的 ⇒ 每次调用都记一次 SharedErrors。
	// 这样「装了共享层但客户端不可用」这种半吊子状态也会被钉住。
	st := memo.Stats()
	if st.SharedWrites != 0 || st.SharedErrors != 0 || st.SharedHits != 0 || st.SharedSkipped != 0 {
		t.Fatalf("类型化 nil ⇒ 共享层不得被装上, stats = %+v", st)
	}
}

// TestNewHistoryVerdictMemoRealRedisClientInstallsSharedLayer —— 对照用例：
// 可用的客户端**必须**把共享层装上（否则上一条断言就成了空断言）。
func TestNewHistoryVerdictMemoRealRedisClientInstallsSharedLayer(t *testing.T) {
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer client.Close()

	cfg := historyMemoEnv{
		LocalEntries:       64,
		SharedTTL:          time.Hour,
		SharedMaxUsedBytes: 1 << 20,
		SharedMaxWrites:    100,
	}
	memo := newHistoryVerdictMemo(cfg, client)
	if memo == nil {
		t.Fatal("memo 不应为 nil")
	}
	memo.Store(context.Background(), [32]byte{2}, true)
	if got := memo.Stats().SharedWrites; got != 1 {
		t.Fatalf("SharedWrites = %d, want 1（可用客户端 ⇒ 共享层应当装上）", got)
	}
	if keys := srv.Keys(); len(keys) != 1 {
		t.Fatalf("共享层应当写进 Redis, keys = %v", keys)
	}
}
