package executors

// executor_circuit_open.go —— W1-A（2026-11-18）：把「上游熔断把候选打空」
// 这件事在执行器层**保住真实 kind**，并算出客户端该等多久。
//
// ── 缺口本体（源码核验，非推测）────────────────────────────────
//
// 熔断准入在 executor_dispatch.go：e.Circuit.Allow(provider, credential)
// 为 false ⇒ 返回 ForwardOutcome{Err: errDispatchCircuitOpen,
// ErrorKind: "circuit_open"}。planner 把 circuit_open 当 TERMINAL
// （换兄弟凭据，见 domains/dispatch/planner.go:173），全部凭据都被拒 ⇒
// 终态错误是 dispatch.ExhaustedError。
//
// ★ 但 dispatchErrToExecuteError 的 default 分支把这个真实 kind **丢了**：
// forward 路径的哨兵一律映射成 KindTransient ⇒ handler 收到的 LastKind
// 是 "transient" ⇒ 客户端看到 503 + error.code=model_not_found +
// error.kind=transient，运维在 _request_logs 里也只能按 transient 过滤。
//
// 而「熔断」与「上游挂了」对调用方是**相反**的处置：前者退避后重试即可，
// 后者要换模型/换供应商/上报。两者塌成同一个 kind ⇒ 调用方只能盲退避，
// 运维也分不清「这是我们主动熔断的」还是「上游真的挂了」。
//
// ── 本文件只做两件事（不多做）──────────────────────────────────
//
//	① 判定「这次请求是被熔断打空的」；
//	② 算出客户端该等多久（Retry-After）。
//
// 判定**必须看尝试摘要而不是只看最后一个错误**：混合场景（凭据 A 被熔断、
// 凭据 B 上游 500）里主导因是上游 500 ⇒ 不该回「熔断」码。摘要里每一条
// 尝试都是 circuit_open 才算熔断打空。

import (
	"errors"
	"math"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// dispatchExhaustedByCircuitOpen 判断「这次请求是被熔断打空的」。
//
// 判据（按优先级）：
//
//	① 有尝试摘要（dispatch.ExhaustedError.Attempts）⇒ 以摘要为准：
//	   **每一条**尝试的 Reason 都是 circuit_open 才算。
//	② 没有摘要（未包装的裸哨兵）⇒ 终态错误本身就是熔断哨兵才算。
//
// ★ 为什么不只看「最后一个错误」：ExhaustedError.Unwrap 会把最后一个具体
// 失败透出来，于是 errors.Is(exhausted, errDispatchCircuitOpen) 在混合场景
// 里也可能为真（最后一个恰好是熔断）⇒ 会把「上游 500 + 一个被熔断的兄弟」
// 误报成熔断打空 ⇒ 客户端拿到的处置建议（退避重试）就是错的。
func dispatchExhaustedByCircuitOpen(err error) bool {
	if err == nil {
		return false
	}
	if ex, ok := dispatch.AsExhaustedError(err); ok && len(ex.Attempts) > 0 {
		for _, a := range ex.Attempts {
			if a.Reason != string(errorsx.KindCircuitOpen) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, errDispatchCircuitOpen)
}

// circuitOpenRetryAfterSeconds 算出客户端在熔断恢复前该等多久（秒）。
//
// 数据源是熔断器**自己**的冷却截止时刻（Breaker.CoolingUntil）——这是唯一
// 权威来源：退避窗口写死在代码里会和实际熔断时长对不上（熔断退避是指数的，
// breaker.go 的 exponentialCooling）。
//
// ★ 取**最早**的那个冷却截止：客户端只要等到最早恢复的那个节点就能重试，
// 等更久没有意义。
//
// 返回 0 表示算不出来（没有候选 / 熔断器里没这个 key / 冷却截止是零值）
// ⇒ 调用方按缺省值处理。
func circuitOpenRetryAfterSeconds(mgr *credential.Manager, candidates []provider.Candidate) int {
	if mgr == nil || len(candidates) == 0 {
		return 0
	}
	var earliest time.Time
	for _, c := range candidates {
		b := mgr.Get(c.ProviderID, c.CredentialID)
		if b == nil {
			continue
		}
		switch b.State() {
		case credential.StateOpen, credential.StateQuarantined:
			// 只有明确处于冷却中的节点才有「还要等多久」可言。
		default:
			continue
		}
		cu := b.CoolingUntil()
		if cu.IsZero() {
			continue
		}
		if earliest.IsZero() || cu.Before(earliest) {
			earliest = cu
		}
	}
	if earliest.IsZero() {
		return 0
	}
	return circuitOpenRetrySeconds(earliest, time.Now())
}

// circuitOpenRetrySeconds 把「冷却截止时刻」换算成客户端该等多少秒。
//
// 单独抽成纯函数（而不是内联在采集逻辑里）：到期瞬间的「冷却已过」分支在
// 集成测试里**造不出来**（熔断器只在真实等待或状态迁移时推进冷却窗）⇒
// 内联的话这条分支将永远没有直测覆盖。
//
// 冷却已过时返回 1 而不是 0：Retry-After: 0 会让客户端立刻重试再撞一次
// 同一个开路熔断。
func circuitOpenRetrySeconds(coolingUntil, now time.Time) int {
	if coolingUntil.IsZero() {
		return 0
	}
	d := coolingUntil.Sub(now)
	if d <= 0 {
		return 1
	}
	return int(math.Ceil(d.Seconds()))
}
