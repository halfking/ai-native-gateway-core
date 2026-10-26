package middleware

// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES

// ledger_mw_test.go —— 主链接进扩展点台账的**接线证明 + 口径门**（P2-2 续）。
//
// # 为什么这些门不可省
//
// 契约（`internal/extpointledger`）+ 实现（`ledger_mw.go`）+ 读端都齐了，
// 但**接线没通**的话，台账在主链上永远是空的 ⇒ admin 页面看着正常、
// 其实「主链」那一行没有任何数据。
//
// ⇒ 且更要紧的是**口径**：主链的「失败」有两种完全不同的形态
// （**拒绝** vs **出错**），混在一起 ⇒ 运维会把「有人在狂刷坏 key」
// 读成「网关坏了」⇒ ⇒ 口径必须被门钉住。
//
// ★ 全局台账是**进程级**的 ⇒ 断言一律用**增量**（before/after 差值）。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/extpointledger"
	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// ledgerSnapshot 取当前主链的台账行（before 用；未被记录过时返回零值行）。
func ledgerSnapshot(t *testing.T) extpointledger.Entry {
	t.Helper()
	e, ok := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if !ok {
		return extpointledger.Entry{MechanismID: extpoints.IDHTTPMiddlewareChain}
	}
	return e
}

// ledgerServe 跑一次「外层探针 + 内层探针 + 业务处理器」。
func ledgerServe(t *testing.T, status int, inner func(http.ResponseWriter, *http.Request)) {
	t.Helper()
	before := ledgerSnapshot(t)

	innerProbe := LedgerInnerProbe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inner != nil {
			inner(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	h := LedgerMiddleware(innerProbe)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))

	after, ok := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if !ok {
		t.Fatal("台账里没有 http_middleware_chain ⇒ 主链没记账")
	}
	if after.Calls != before.Calls+1 {
		t.Fatalf("调用次数 = %d，want %d（主链没接线）", after.Calls, before.Calls+1)
	}
}

// TestLedgerMiddlewareRecordsMainChain —— **接线证明**：
// 跑一次请求 ⇒ 主链的调用次数**涨 1**，且 200 不算失败也不算拒绝。
func TestLedgerMiddlewareRecordsMainChain(t *testing.T) {
	before := ledgerSnapshot(t)
	ledgerServe(t, http.StatusOK, nil)

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if after.Failures != before.Failures {
		t.Errorf("失败数变了（%d → %d）：一个 200 的请求不该算失败",
			before.Failures, after.Failures)
	}
	if after.Rejected != before.Rejected {
		t.Errorf("拒绝数变了（%d → %d）：一个 200 的请求不该算拒绝",
			before.Rejected, after.Rejected)
	}
	if after.Timeouts != before.Timeouts {
		t.Errorf("超时数变了（%d → %d）", before.Timeouts, after.Timeouts)
	}
}

// TestLedgerMiddlewareCountsRejectionSeparately —— ★ **口径**：
// 4xx ⇒ 记 **rejected**，**不是** failed。
//
// ⇒ 这条门存在的理由：若把 4xx 算成失败，运维看到「主链失败数飙升」
// ⇒ 会以为网关坏了，而真相可能只是**有人在狂刷坏 key**。
func TestLedgerMiddlewareCountsRejectionSeparately(t *testing.T) {
	before := ledgerSnapshot(t)
	ledgerServe(t, http.StatusUnauthorized, nil)

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if after.Rejected != before.Rejected+1 {
		t.Fatalf("拒绝数 = %d，want %d（4xx 必须记成拒绝）", after.Rejected, before.Rejected+1)
	}
	if after.Failures != before.Failures {
		t.Fatalf("失败数 = %d，want %d（★ 拒绝**不是**出错）", after.Failures, before.Failures)
	}
}

// TestLedgerMiddlewareCountsServerErrorAsFailure —— ★ **口径**：
// 5xx ⇒ 记 **failed**（且**不是** rejected）。
func TestLedgerMiddlewareCountsServerErrorAsFailure(t *testing.T) {
	before := ledgerSnapshot(t)
	ledgerServe(t, http.StatusInternalServerError, nil)

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if after.Failures != before.Failures+1 {
		t.Fatalf("失败数 = %d，want %d（5xx 必须算失败）", after.Failures, before.Failures+1)
	}
	if after.Rejected != before.Rejected {
		t.Fatalf("拒绝数 = %d，want %d（5xx 不是拒绝）", after.Rejected, before.Rejected)
	}
}

// TestLedgerMiddlewareMeasuresChainOverheadNotTotalDuration —— ★★ **口径**：
// 业务处理很慢 ⇒ 记进台账的**样本**必须是**主链自身的开销**，
// 而不是整个请求的耗时。
//
// ⇒ 这是本门存在的全部理由：若量的是总耗时，p99 会被 LLM 调用主导
// ⇒ 「扩展点耗时」这个指标就废了。
//
// ★ 两个探针都装上 ⇒ 才有「总耗时 − 业务耗时」这个差。
func TestLedgerMiddlewareMeasuresChainOverheadNotTotalDuration(t *testing.T) {
	before := ledgerSnapshot(t)

	const innerSleep = 150 * time.Millisecond
	innerProbe := LedgerInnerProbe(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(innerSleep)
		w.WriteHeader(http.StatusOK)
	}))
	h := LedgerMiddleware(innerProbe)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	// ★ 样本单位是**毫秒**（Entry.P99Millis）⇒ 内层睡了 150ms ⇒
	// 若量的是总耗时，样本会 ≥ 150 ⇒ 这里必须显著小于它。
	if after.P99Millis >= uint64(innerSleep.Milliseconds()) {
		t.Fatalf("p99 = %d ms，want << %d ms（★ 量的是主链开销，不是整段耗时）",
			after.P99Millis, innerSleep.Milliseconds())
	}
	if after.Calls != before.Calls+1 {
		t.Fatalf("调用次数 = %d，want %d", after.Calls, before.Calls+1)
	}
}

// TestLedgerMiddlewareWithoutInnerProbeStillRecords —— **降级可工作**：
// 只装外层探针（漏装内层）⇒ 仍然记账（口径变粗：整段都算链的开销）。
//
// ⇒ 门禁的价值：**漏装内层探针不会让数据全错**，只会让 p99 变粗。
func TestLedgerMiddlewareWithoutInnerProbeStillRecords(t *testing.T) {
	before := ledgerSnapshot(t)

	h := LedgerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if after.Calls != before.Calls+1 {
		t.Fatalf("调用次数 = %d，want %d（只装外层也必须记账）", after.Calls, before.Calls+1)
	}
}

// TestLedgerMiddlewareCountsTimeout —— ★ **口径**：
// 请求 ctx 到期 ⇒ 记 **timeout**。
func TestLedgerMiddlewareCountsTimeout(t *testing.T) {
	before := ledgerSnapshot(t)

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // 让 ctx 真的到期

	h := LedgerMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	after, _ := extpointledger.Default().Entry(extpoints.IDHTTPMiddlewareChain)
	if after.Timeouts != before.Timeouts+1 {
		t.Fatalf("超时数 = %d，want %d（ctx 到期必须算超时）",
			after.Timeouts, before.Timeouts+1)
	}
}
