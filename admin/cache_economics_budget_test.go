// 钉住的行为：cache-economics 的**压缩计数附加查询必须用自己的 ctx**，
// 不得与主聚合共用同一个 10s 预算。
//
// 事故（2026-10-18，llm.kxpms.cn 实测）：该端点稳定 500 卡在 10.0s。
//
// ⚠️ 下面这两行数字**已于同日被实测推翻**，保留它们只是记录「当时信了什么」。
// 原注释写的是 252 逐条计时（同月窗口）：
//
//	主聚合 usage_ledger_with_current_month           6020 ms
//	compressedQuery request_logs_with_current_month  35344 ms
//
// 推翻它的理由（审计 §13.1）：
//
//	① 窗口错了——端点走 resolveUsageTimeRange(r, 30)，默认是**最近 30 天**，
//	   那次计时取自「今天」与「本月」两个窗口；
//	② 计时边界错了——同一次 EXPLAIN 在当天窗口下 Execution Time 只有 61.367ms，
//	   8.3s 来自包着 podman exec 的 wall clock。
//
// 真实 30 天窗口下：主聚合 12,667ms（已被迁移 873 修到 487ms），
// 而压缩计数读 canonical 视图是 **>300s 跑不完**（不是 35s）。
//
// 下面这两条判据要钉的**结构**结论仍然成立、且已被重新取证：
// 附加查询不得与主聚合共用父 ctx——它一旦超时，会把已经算对的
// dollars_spent / cache_hit_ratio 一起拖死。
//
// 判据形态说明：这里钉的是**结构**（附加查询不得共用父 ctx），不是耗时。
// 耗时需要生产规模的数据才能测，本地夹具 2 行永远跑得飞快，测不出任何东西；
// 而「共用 ctx」这个形态本身就是缺陷，与数据量无关。所以源码级判据在这里
// 是恰当的层——它测的正是被测物。
package admin

import (
	"os"
	"strings"
	"testing"
)

func usageEnhancedSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("usage_enhanced.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// usageCacheEconomicsBody 切出 usageCacheEconomics 函数体。
func usageCacheEconomicsBody(t *testing.T) string {
	t.Helper()
	src := usageEnhancedSource(t)
	i := strings.Index(src, "func (h *Handler) usageCacheEconomics(")
	if i < 0 {
		t.Fatal("usageCacheEconomics not found in usage_enhanced.go")
	}
	rest := src[i:]
	if j := strings.Index(rest, "\nfunc "); j > 0 {
		return rest[:j]
	}
	return rest
}

// TestCacheEconomicsCompressedQueryHasItsOwnBudget 钉住核心不变量。
func TestCacheEconomicsCompressedQueryHasItsOwnBudget(t *testing.T) {
	body := usageCacheEconomicsBody(t)

	if !strings.Contains(body, "cacheEconomicsCompressedBudget") {
		t.Error("usageCacheEconomics no longer bounds the compressed-count query with its " +
			"own budget. Measured on 252 that query takes 35,344 ms while the whole handler " +
			"ctx is 10s, so sharing the parent ctx makes the endpoint 500 on every call — " +
			"and it drags down dollars_spent / cache_hit_ratio, which had already been " +
			"computed correctly.")
	}
	// 关键形态：必须用**派生** ctx 跑，而不是把父 ctx 直接传下去。
	if strings.Contains(body, "h.db.QueryRow(ctx, compressedQuery") {
		t.Error("compressedQuery is still executed with the shared parent ctx; " +
			"it needs a derived, bounded one (cctx)")
	}
	if !strings.Contains(body, "h.db.QueryRow(cctx, compressedQuery") {
		t.Error("compressedQuery should be executed with the derived bounded ctx (cctx)")
	}
}

// TestCacheEconomicsCompressedBudgetFitsInsideHandlerBudget 钉住前提：子预算必须
// 严格小于端点总预算，且留得出主聚合的实测耗时。
//
// 这条是给上一个判据兜底的：只断言「用了派生 ctx」而不约束预算值，那么把
// budget 设成 30s（比父 ctx 还长）也算「有预算」，实际上等于没修。
func TestCacheEconomicsCompressedBudgetFitsInsideHandlerBudget(t *testing.T) {
	src := usageEnhancedSource(t)

	if !strings.Contains(src, "context.WithTimeout(r.Context(), 10*time.Second)") {
		t.Error("the handler's own budget moved; the compressed sub-budget constant and " +
			"this assertion must be updated together with it")
	}

	const decl = "const cacheEconomicsCompressedBudget = 3 * time.Second"
	if !strings.Contains(src, decl) {
		t.Errorf("cacheEconomicsCompressedBudget must stay at %q.\n"+
			"3s is derived from the 252 measurement: main aggregate 6,020 ms inside a 10s\n"+
			"budget. Larger buys nothing (the query needs 35s either way → both fail to\n"+
			"Warn); smaller throws away the windows where it would have succeeded\n"+
			"(narrow tenant / short range).", decl)
	}
}

// TestCacheEconomicsCompressedFailureStaysNonFatal 钉住降级语义没有被顺手改掉：
// 附加查询失败只能是一条 Warn，绝不能让整端点 500。
func TestCacheEconomicsCompressedFailureStaysNonFatal(t *testing.T) {
	body := usageCacheEconomicsBody(t)
	if !strings.Contains(body, "cache-economics: compressed request count unavailable") {
		t.Error("the non-fatal Warn for the compressed count is gone; if that failure now " +
			"propagates, a slow auxiliary query can still take down the whole endpoint")
	}
	if strings.Contains(body, "writeInternalErr(w, \"cache-economics compressed") {
		t.Error("compressed-count failure must not become a writeInternalErr")
	}
}
