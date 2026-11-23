package verdictkv

// typed_nil_test.go —— 类型化 nil（typed-nil）的 Redis 客户端（2026-11-23，
// 12h 变更审计 A）。
//
// 背景：Redis 客户端是以**具体指针类型**（*redis.Client）被装进
// redis.UniversalClient 接口的。一个 nil 的 *redis.Client 装进接口后：
//
//	接口值 != nil   ⇒ 任何 `client == nil` 闸都放行
//	底层指针 == nil  ⇒ 第一次调用方法就空指针 panic
//
// 生产链条（实测）：cmd/gateway 把可能为 nil 的 *redis.Client 传进
// redis.UniversalClient 形参 ⇒ 本包构造共享层后端 ⇒ 守卫在
// internal/sessioncontext 的 worker 协程里调用 client.Get(...) ⇒
// 无 recover ⇒ 进程级崩溃。

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// typedNilClient 返回一个「接口非 nil、底下是空指针」的 Redis 客户端。
func typedNilClient() redis.UniversalClient {
	var c *redis.Client // nil 的具体指针
	return c
}

func TestIsNilClientCatchesTypedNil(t *testing.T) {
	if !IsNilClient(nil) {
		t.Fatal("nil 接口必须判为不可用")
	}
	if !IsNilClient(typedNilClient()) {
		t.Fatal("类型化 nil（接口非 nil、底下是 nil 指针）必须判为不可用")
	}
	srv := miniredis.RunT(t)
	real := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	defer real.Close()
	if IsNilClient(real) {
		t.Fatal("可用客户端不得判为不可用")
	}
}

// TestNewRedisReturnsTrueNilInterface —— 构造失败时返回的是**真 nil 接口**。
//
// ★ 判据是 `kv == nil`：只有真 nil 接口才等于 nil。
//  - 类型化 nil 的接口用 `== nil` 判**不**出来（它非 nil）⇒ 这正是要防的那类；
//  - 反过来，对**真** nil 接口调 reflect.ValueOf(kv).IsNil() 会 panic
//    （reflect.ValueOf(真 nil 接口) 是零 Value，IsNil 对零 Value 会 panic）
//    ⇒ 断言真 nil 时只能用 `== nil`，不能用 IsNil()。
func TestNewRedisReturnsTrueNilInterface(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client redis.UniversalClient
	}{
		{"nil 接口", nil},
		{"类型化 nil", typedNilClient()},
	} {
		kv := NewRedis(tc.client, "")
		if kv != nil {
			t.Fatalf("%s: NewRedis 必须返回真 nil 接口, got %#v", tc.name, kv)
		}
	}
	// 对照：证明「类型化 nil 的接口」确实躲得过 `== nil` —— 所以 NewRedis
	// 若退回返回具体指针（*RedisKV），上面那条断言就会失败。
	var typedNil *RedisKV
	var boxed KV = typedNil
	if boxed == nil {
		t.Fatal("对照失败：类型化 nil 的接口应当 != nil（这正是要堵的洞）")
	}
}

// TestTypedNilClientIsNotUsable —— 钉住「危害真实存在」。
//
// 类型化 nil 的客户端**看起来**可用（接口 != nil）⇒ 一旦被当成可用客户端，
// 第一条请求就会踩空指针（panic）或至少不是一次安静的成功。
func TestTypedNilClientIsNotUsable(t *testing.T) {
	client := typedNilClient()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("类型化 nil 客户端不得被当成可用客户端（期望 panic 或错误）")
			}
		}()
		if _, err := client.Get(context.Background(), "k").Result(); err == nil {
			t.Fatal("类型化 nil 客户端不得安静地成功")
		}
	}()
}

// TestRedisKVZeroValueDoesNotPanic —— 零值 RedisKV（外部可以写
// &verdictkv.RedisKV{}）的方法必须返回错误，而不是空指针 panic。
func TestRedisKVZeroValueDoesNotPanic(t *testing.T) {
	var kv RedisKV
	ctx := context.Background()
	if _, _, err := kv.Get(ctx, "k"); err == nil {
		t.Fatal("零值后端的 Get 必须报错")
	}
	if err := kv.Set(ctx, "k", "v", 0); err == nil {
		t.Fatal("零值后端的 Set 必须报错")
	}
	if _, err := kv.UsedMemory(ctx); err == nil {
		t.Fatal("零值后端的 UsedMemory 必须报错")
	}
}
