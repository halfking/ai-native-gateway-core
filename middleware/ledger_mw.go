package middleware

// ledger_mw.go —— **主链的扩展点台账记账**（P2-2 续）。
//
// # 这道改造补的是哪块缺口
//
// P2-2 落了扩展点台账（`internal/extpointledger`），但 6 个机制里
// **只有 `response_intercept` 真的在记账** ⇒ 主链（`http_middleware_chain`）
// 的调用次数/p99/失败数**恒为 0** ⇒ 台账在最重要的那个机制上是空的。
//
// ⇒ 本文件把主链接进台账 ⇒ ★ 读端 `unwired` 标志随之变 false。
//
// # ★ 为什么需要**两个**探针
//
// 主链包着真正的业务处理（LLM 调用动辄百毫秒～数十秒）⇒
// 若把「整条请求的耗时」当成扩展点的 p99 ⇒ ★★ 那个数**完全不可用**
// （它量的是 LLM，不是中间件）⇒ ⇒ 读端会被一个「秒级 p99」误导。
//
// ⇒ 而**只在外面包一层也减不出来**：外层看到的 `inner` 包含了主链自己
// ⇒ `总耗时 − inner` ≈ 0 ⇒ 量不到链的开销。
//
// ⇒ ⇒ 所以拆成两个探针：
//
//	- ★ **外层 `LedgerMiddleware`**：包住**整条链** ⇒ 量「总耗时」；
//	- ★ **内层 `LedgerInnerProbe`**：装在链的**最里侧**（业务处理器外面）
//	  ⇒ 量「业务耗时」。
//
// ⇒ 链自身的开销 = 总耗时 − 业务耗时。
// ⇒ 两者通过 **request context** 里的一份**每请求**计时结构通信
// ⇒ ★ **并发安全**（不共享变量、不用全局）。
//
// # ★ 口径：拒绝与出错**分开记**（`rejected` vs `failed`）
//
// 主链有两种「没让请求通过」：
//
//	- **拒绝**（4xx）：Auth / Origin / SecurityHeaders 按设计拦下来了
//	  ⇒ 这是扩展点**在正常工作**；
//	- **出错**（5xx）：panic 兜底、下游处理器报错
//	  ⇒ 这才是**真的失败**。
//
// ⇒ 混在一个「失败数」里 ⇒ ★ 运维看到失败数飙升会以为「网关坏了」，
// 而真相可能只是**有人在狂刷坏 key** ⇒ ⇒ 口径失真。
//
// ⇒ ⇒ 4xx ⇒ `rejected`；5xx ⇒ `failed`；ctx 到期 ⇒ `timeout`；其余 ⇒ `ok`。
// ⚠ **口径边界**：4xx 也可能来自**客户端**（400 参数错、404 路由不存在）⇒
// ⚠ 本仓的 4xx **绝大多数**来自鉴权闸门 ⇒ 口径成立；★ 若日后要区分
// 「闸门拒绝」与「客户端自己错」，那是**读端**的事（拆两个字段），不是这里的事。
//
// # 为什么复用 `statusRecordingWriter`
//
// ★ 本包已有 `tracing.go` 里的 `statusRecordingWriter`（Flusher / FlushError /
// Hijack / Pusher / ReadFrom / Unwrap 都实现了）⇒ ★★ **不重写第二个**：
// 重写会在 SSE 流式响应上踩坑（丢了 Flusher ⇒ 流式挂死）⇒
// 而本仓的 LLM 响应**全是 SSE** ⇒ ⇒ 复用是唯一安全的选择。

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/extpointledger"
	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// chainTimingKey 是「每请求的链内计时」在 context 里的键。
type chainTimingKey struct{}

// chainTiming 是一次请求的计时（**每请求一份** ⇒ 并发安全）。
type chainTiming struct {
	// inner 是业务处理耗时（由内层探针填写）。
	inner time.Duration
	// innerSet 报告内层探针有没有跑到（没跑到 ⇒ 链自己拦下了请求）。
	innerSet bool
}

// LedgerMiddleware 把「主链的一次调用」记进扩展点台账（**外层探针**）。
//
// ★ 机制 ID 取自 `internal/extpoints`（裁决表的单一真值源）。
// ★ 它是**纯观测**：不改状态码、不改响应体、不吞错误 ⇒ 观测面不会改变数据面。
//
// ★ 装法（`cmd/gateway/main.go`）：
//
//	finalHandler := middleware.LedgerMiddleware(handler)              // 外层
//	handler := chain.Then(middleware.LedgerInnerProbe(publicSurface)) // 内层
func LedgerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		timing := &chainTiming{}

		rw := &statusRecordingWriter{ResponseWriter: w}
		next.ServeHTTP(rw, r.WithContext(context.WithValue(r.Context(), chainTimingKey{}, timing)))

		total := time.Since(start)
		// ★ 链自身的开销 = 总耗时 − 业务耗时。
		//   业务耗时拿不到（内层探针没跑到 ⇒ 链在业务之前就拦下了）
		//   ⇒ ⇒ 开销就是整段。
		overhead := total
		if timing.innerSet {
			overhead = total - timing.inner
			if overhead < 0 {
				overhead = 0
			}
		}

		status := rw.status
		if status == 0 {
			status = http.StatusOK
		}

		outcome, err := classify(r, status)
		extpointledger.Default().Record(extpoints.IDHTTPMiddlewareChain, outcome, overhead, err)
	})
}

// LedgerInnerProbe 是**内层探针**：装在链的最里侧，量业务处理耗时。
//
// ★ 若它没被装（外层探针单独用），则外层退化为「整段耗时都算链的开销」
// ⇒ 仍然可工作（只是口径变粗）⇒ **不会因为漏装就数据全错**。
func LedgerInnerProbe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timing, _ := r.Context().Value(chainTimingKey{}).(*chainTiming)
		if timing == nil {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		next.ServeHTTP(w, r)
		timing.inner = time.Since(start)
		timing.innerSet = true
	})
}

// classify 决定这次主链调用的结局（口径见文件头）。
func classify(r *http.Request, status int) (extpointledger.Outcome, error) {
	// ★ 优先判超时：ctx 到期是「时间维度」的失败，与状态码无关。
	if r != nil && r.Context() != nil {
		if err := r.Context().Err(); errors.Is(err, context.DeadlineExceeded) {
			return extpointledger.OutcomeTimeout, err
		}
	}
	switch {
	case status >= http.StatusInternalServerError:
		return extpointledger.OutcomeFailed, fmt.Errorf("http_status=%d", status)
	case status >= http.StatusBadRequest:
		return extpointledger.OutcomeRejected, fmt.Errorf("http_status=%d", status)
	default:
		return extpointledger.OutcomeOK, nil
	}
}
