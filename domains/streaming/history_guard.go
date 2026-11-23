package streaming

import (
	"context"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/sessioncontext"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

type historyGuardFactory func(context.Context) func(context.Context, func() io.Reader) (bool, error)

var defaultHistoryDetector = sanitize.NewPatternDetector()

// SetHistoryGuardFactory shares the pinned input policy with the file reducer.
// Configure before serving requests, together with the sanitizer middleware.
func (h *ChatHandler) SetHistoryGuardFactory(factory func(context.Context) func(context.Context, func() io.Reader) (bool, error)) {
	h.historyGuardFactory = factory
}

// SetHistoryGuardConcurrencySafe 声明「当前装配的历史守卫可以并发调用」。
// 须在开始服务请求之前调用。
//
// ★ 为什么是显式声明而不是默认开启：守卫是任意外部注入的函数（生产是
// sanitize 的策略守卫；测试常注入带计数的闭包）。归约器无法判断一个函数
// 是否线程安全 ⇒ 默认串行，只有注入方确认安全后才并行（2026-11-23 大请求体
// 归约提速轮）。
func (h *ChatHandler) SetHistoryGuardConcurrencySafe(safe bool) {
	h.historyGuardConcurrencySafe = safe
}

// reduceWorkerCount 是单次归约可用的最大工作协程数。
//
// env LLM_GATEWAY_REDUCE_WORKERS 覆盖；默认 min(GOMAXPROCS, 8)，夹在 [1, 32]。
// 上限 8 是有意的：入口并发槽可达 32，每个请求再各开满核只会互相抢 CPU；
// 守卫之外的工作（读归档、装配输出）仍是一次只做一件。
func reduceWorkerCount() int { return ReduceWorkersConfigured(os.Getenv) }

// ReduceWorkersConfigured 返回 env 声明下的归约并行度（给装配层记日志）。
func ReduceWorkersConfigured(getenv func(string) string) int {
	n := runtime.GOMAXPROCS(0)
	if n > 8 {
		n = 8
	}
	if raw := strings.TrimSpace(getenv("LLM_GATEWAY_REDUCE_WORKERS")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			n = v
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 32 {
		n = 32
	}
	return n
}

// HistoryGuardContextForTest 返回 prepareHistoryGuard 之后的 ctx——归约器读的
// 就是它。给装配测试确认「并行度真的进了请求 ctx」；生产代码不调用。
func HistoryGuardContextForTest(h *ChatHandler, r *http.Request) context.Context {
	return h.prepareHistoryGuard(r).Context()
}

// SetDigestConfig 声明归约时是否生成分段抽取式摘要及其参数。
// 须在开始服务请求之前调用；未调用 ⇒ 关闭。
func (h *ChatHandler) SetDigestConfig(cfg sessioncontext.DigestConfig) {
	if h == nil {
		return
	}
	h.digest = cfg
}

// DigestConfigured 返回当前生效的摘要配置。
func (h *ChatHandler) DigestConfigured() sessioncontext.DigestConfig {
	if h == nil {
		return sessioncontext.DigestConfig{}
	}
	return h.digest
}

func (h *ChatHandler) prepareHistoryGuard(r *http.Request) *http.Request {
	r = h.pinRules(r)
	if sessioncontext.HasHistoryGuard(r.Context()) {
		return r
	}
	factory := h.historyGuardFactory
	if factory == nil {
		factory = defaultHistoryDetector.HistoryGuard
	}
	ctx := sessioncontext.WithHistoryGuard(r.Context(), factory(r.Context()))
	if h.historyGuardConcurrencySafe {
		ctx = sessioncontext.WithReduceWorkers(ctx, reduceWorkerCount())
	}
	if h.digest.Enabled {
		// 分段摘要（2026-11-23 第三步）。归约器只在 ctx 里有声明时才做。
		ctx = sessioncontext.WithDigest(ctx, h.digest)
	}
	return r.WithContext(ctx)
}
