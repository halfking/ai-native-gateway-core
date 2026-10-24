package response

// AUDIT-GUARD: 本文件的门**必须随包在 CI 上执行** ⇒ 该包已登记进 Makefile 的 GUARD_PACKAGES

// chain_ledger_test.go —— P2-2 的接线证明：**响应拦截链真的在记账**。
//
// # 为什么这条门不可省
//
// 契约 + 实现 + 读端都齐了，但**接线没通**的话，台账会永远是空的
// ⇒ admin 页面看着正常、其实一列数据都没有 ⇒ ★ **「契约好看、链路不通」**
// —— 这正是本仓反复吃过的亏（`domains/hooks` 的注册表从未被构造）。
//
// ⇒ 本门**真的跑一次链**，然后看**默认台账**的计数**涨了**。
//
// ★ 因为默认台账是**进程级全局**（同包其它测试也可能记账）⇒
// 断言用**增量**（before/after 差值），不用绝对值。

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/internal/extpointledger"
	"github.com/kaixuan/llm-gateway-go/internal/extpoints"
)

// ledgerFailingInterceptor 恒返回错误的拦截器（会被链吞掉 ⇒ 但**必须**被记账）。
type ledgerFailingInterceptor struct{}

func (ledgerFailingInterceptor) InterceptNonStream(context.Context, *InterceptRequest) (*InterceptResult, error) {
	return nil, errors.New("ledger-fail")
}
func (ledgerFailingInterceptor) InterceptStreamChunk(context.Context, []byte, *StreamMeta) (*ChunkResult, error) {
	return nil, errors.New("ledger-fail")
}
func (ledgerFailingInterceptor) InterceptStreamEnd(context.Context, *StreamMeta) (*EndResult, error) {
	return nil, errors.New("ledger-fail")
}

// ledgerOKInterceptor 恒成功的拦截器。
type ledgerOKInterceptor struct{}

func (ledgerOKInterceptor) InterceptNonStream(context.Context, *InterceptRequest) (*InterceptResult, error) {
	return &InterceptResult{}, nil
}
func (ledgerOKInterceptor) InterceptStreamChunk(context.Context, []byte, *StreamMeta) (*ChunkResult, error) {
	return &ChunkResult{}, nil
}
func (ledgerOKInterceptor) InterceptStreamEnd(context.Context, *StreamMeta) (*EndResult, error) {
	return &EndResult{}, nil
}

// TestInterceptorChainRecordsIntoExtensionPointLedger：
// ★ 跑一次链 ⇒ 台账的**调用次数**与**失败数**都涨。
//
// ★ 关键细节：失败的拦截器**被链吞掉**（链继续往下走、整体不报错）
// ⇒ ⇒ 如果记账粒度是「一次链调用」，**失败数永远是 0**
// ⇒ ⇒ 本门同时钉住「失败数也要涨」⇒ 粒度必须是「单个拦截器的一次调用」。
func TestInterceptorChainRecordsIntoExtensionPointLedger(t *testing.T) {
	ledger := extpointledger.Default()

	before, hadBefore := ledger.Entry(extpoints.IDResponseIntercept)
	beforeCalls := before.Calls
	beforeFailures := before.Failures

	chain := NewInterceptorChain(
		ledgerFailingInterceptor{},
		ledgerOKInterceptor{},
	)
	if _, err := chain.InterceptNonStream(context.Background(), &InterceptRequest{}); err != nil {
		t.Fatalf("链不应整体报错（失败被吞是既有语义）：%v", err)
	}

	after, ok := ledger.Entry(extpoints.IDResponseIntercept)
	if !ok {
		t.Fatal("台账里没有 response_intercept ⇒ 链没记账")
	}
	if after.Calls != beforeCalls+2 {
		t.Fatalf("调用次数 = %d，want %d（两个拦截器各记一次）", after.Calls, beforeCalls+2)
	}
	if after.Failures != beforeFailures+1 {
		t.Fatalf("失败数 = %d，want %d（★ 被吞掉的失败也必须被记账）",
			after.Failures, beforeFailures+1)
	}
	if after.LastError == "" {
		t.Error("最近错误应留得住")
	}
	_ = hadBefore
}

// TestInterceptorChainLedgerStopsGrowingWhenTurnedOff：
// ★ 计划书 §6-P2-2 验收②：「**关闭某点后其计数不再增长**」。
//
// ⇒ 「关闭」的形态 = **空链**（与 P0-5 的 off 用例同形态：
// `NewInterceptorChain()` 无拦截器 ⇒ 链体不执行 ⇒ body 原样）。
// ⇒ ⇒ 空链跑一次 ⇒ 台账的调用次数**必须一点不涨**。
//
// ★ 为什么这条值得单独一条门：若记账点被写在「链入口」而不是「逐拦截器」，
// ⇒ ★ 空链也会被记一次 ⇒ 台上就出现了「**关掉的扩展点仍有调用量**」
// ⇒ ⇒ 运维读台账时会以为它还活着 ⇒ ★ **关掉必须等于 0 增量**。
func TestInterceptorChainLedgerStopsGrowingWhenTurnedOff(t *testing.T) {
	ledger := extpointledger.Default()
	before, _ := ledger.Entry(extpoints.IDResponseIntercept)

	// ★ 关闭：空链。
	chain := NewInterceptorChain()
	if _, err := chain.InterceptNonStream(context.Background(), &InterceptRequest{}); err != nil {
		t.Fatalf("关掉时链不应报错：%v", err)
	}

	after, _ := ledger.Entry(extpoints.IDResponseIntercept)
	if after.Calls != before.Calls {
		t.Fatalf("关闭后调用次数从 %d 涨到 %d ⇒ ★ 关掉的扩展点不该被记账",
			before.Calls, after.Calls)
	}
	if after.Failures != before.Failures {
		t.Fatalf("关闭后失败数从 %d 涨到 %d ⇒ ★ 关掉的扩展点不该被记账",
			before.Failures, after.Failures)
	}
}
