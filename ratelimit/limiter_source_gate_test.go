package ratelimit

// 2026-11-23（211 配置优化 · §七 WARN #2）· 守卫：
//
// 限流器的 Redis 来源必须是**三级选择**（★ 见 NewRedisLimiterPreferEnvOrClient）：
//
//	① RATE_LIMIT_REDIS_URL / REDIS_URL（★ 运维显式指定）⇒ 优先；
//	② 网关**已经装配好**的共享 Redis 客户端 ⇒ 次之；
//	③ 都没有 ⇒ in-process（★ 既有降级）。
//
// ★ 为什么这条要有门（★ 211 生产实况）：
//   · 部署的 Redis 走 **Config 装配路径**（LLM_GATEWAY_REDIS_ADDR 等），
//     而限流器原先只认 RATE_LIMIT_REDIS_URL / REDIS_URL
//     ⇒ ★ **两者从不对上** ⇒ ★ 限流器静默退化成 in-process；
//   · 211 是**蓝绿双槽**部署 ⇒ ★ 每实例一份计数
//     ⇒ ★ **RPM/TPM 配额被放大到实例数倍** ⇒ ★ **成本保护被稀释**。
//
// ★ 门的三个断言各对应一条腿，★ 少一条腿就会静默退化成 in-process：
//   ① 显式 env 胜出（★ 运维的显式意图优先、★ incl. 指向独立 db）；
//   ② 共享客户端被用上（★ 无 env 时）；
//   ③ 全都没有 ⇒ 纯内存（★ 不 panic、★ 不写任何 redis）。
//
// ★ 判定方式：★ **看限流器把计数写到了哪个 Redis**（★ key 空间 rl:rpm:<id> /
// rl:tpm:<id>）⇒ ★ 这比断言「返回了某个 client」更硬（★ 它验证的是**行为**）。

import (
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// limiterClient 返回限流器内部用的 redis client（★ 守卫专用）。
func limiterClient(l *RedisLimiter) *redis.Client {
	if l == nil {
		return nil
	}
	return l.client
}

// ── 门 1：显式 env 胜出（★ 不许被共享客户端抢走）─────────────────────────
func TestLimiterPrefersExplicitEnvOverSharedClient(t *testing.T) {
	envSrv := miniredis.RunT(t)
	sharedSrv := miniredis.RunT(t)

	t.Setenv("RATE_LIMIT_REDIS_URL", "redis://"+envSrv.Addr()+"/0")
	t.Setenv("REDIS_URL", "")

	shared := redis.NewClient(&redis.Options{Addr: sharedSrv.Addr()})
	t.Cleanup(func() { _ = shared.Close() })

	l := NewRedisLimiterPreferEnvOrClient(shared)

	// ★ 显式 env 优先 ⇒ 计数必须落到 env 那个 Redis。
	l.CheckRPM(4242, 100)
	if !envSrv.Exists("rl:rpm:4242") {
		t.Errorf("显式 RATE_LIMIT_REDIS_URL 未被使用（★ env 里没有 rl:rpm:4242）")
	}
	if sharedSrv.Exists("rl:rpm:4242") {
		t.Errorf("★ 共享客户端抢走了显式 env 的优先级（★ shared 里有 rl:rpm:4242）")
	}
}

// ── 门 2：无 env 时用共享客户端（★ 这正是 211 缺的那条腿）───────────────
func TestLimiterUsesSharedClientWhenEnvAbsent(t *testing.T) {
	sharedSrv := miniredis.RunT(t)

	t.Setenv("RATE_LIMIT_REDIS_URL", "")
	t.Setenv("REDIS_URL", "")

	shared := redis.NewClient(&redis.Options{Addr: sharedSrv.Addr()})
	t.Cleanup(func() { _ = shared.Close() })

	l := NewRedisLimiterPreferEnvOrClient(shared)
	if limiterClient(l) == nil {
		t.Fatal("★ 无 env 时必须用共享客户端，★ 却退成了纯内存（★ 这就是 211 的现状）")
	}
	l.CheckRPM(4243, 100)
	if !sharedSrv.Exists("rl:rpm:4243") {
		t.Errorf("共享客户端没被真正用上（★ shared 里没有 rl:rpm:4243）")
	}
}

// ── 门 3：全都没有 ⇒ 纯内存（★ 不 panic、★ 不写任何 redis）────────────────
func TestLimiterFallsBackToInMemoryWhenNothingConfigured(t *testing.T) {
	t.Setenv("RATE_LIMIT_REDIS_URL", "")
	t.Setenv("REDIS_URL", "")

	l := NewRedisLimiterPreferEnvOrClient(nil)
	if limiterClient(l) != nil {
		t.Error("★ 没有任何 Redis 时必须退成纯内存（★ client 应为 nil）")
	}
	// ★ 纯内存路径必须能正常判定（★ 不 panic、★ 配额生效）。
	if !l.CheckRPM(4244, 2) {
		t.Error("纯内存路径的第一笔应当放行")
	}
	for i := 0; i < 5; i++ {
		l.CheckRPM(4244, 2)
	}
	if l.CheckRPM(4244, 2) {
		t.Error("★ 纯内存路径的超限请求必须被拒（★ 限流在内存路径上也得生效）")
	}
}

// ── 门 4：装配点必须走三级选择（★ 静态：★ 防止有人改回只看 env）──────────
func TestGatewayWiresLimiterThroughThreeStepChoice(t *testing.T) {
	src, err := os.ReadFile("../cmd/gateway/main.go")
	if err != nil {
		t.Fatalf("读 main.go: %v", err)
	}
	body := string(src)
	if !contains(body, "NewRedisLimiterPreferEnvOrClient") {
		t.Error("★ main.go 没有走 NewRedisLimiterPreferEnvOrClient ⇒ ★ " +
			"★ 限流器又变回『只认 env』⇒ ★ 211 上会重新静默退化成 in-process")
	}
	// ★ 必须把共享客户端传进去（★ 只调函数但不传 client 没用）。
	if !contains(body, "NewRedisLimiterPreferEnvOrClient(sharedRLClient)") {
		t.Error("★ main.go 没有把共享客户端传给限流器（★ sharedRLClient）")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
