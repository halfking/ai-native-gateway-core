package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R67-A 回归：R66 把「跳行」改成「上抛」的两处站点，其**调用方丢弃错误**，
// 于是上抛没有产生任何可观察收益，反而把原本的「跳过一行坏数据」变成
// 「整体截断且调用方看不见」。这类缺陷静态守卫抓不到——守卫只判
// `rows.Err()` 在不在循环旁边，不判分型对不对、更不判调用方是否检查。
//
// 本文件把两条契约钉成断言，钉的是**失败形态**而不是实现细节。

// TestR67A_ProviderRefreshCallSiteMustNotDiscardError 钉 D1。
//
// fetchActiveCredentialsForProvider 在读取失败时返回 (nil, err)。若调用点
// 写成 `creds, _ :=`，则：for 循环不执行 → totalUpserted/totalFailed 都是 0
// → `totalFailed > 0 && totalUpserted == 0` 不成立 → 运行被记成
// providerRefreshSucceed，文案「新增/更新 0 个模型（凭据 0 个，失败 0 个）」。
// 一次什么都没做的刷新被记成成功，且没有任何错误可追。
func TestR67A_ProviderRefreshCallSiteMustNotDiscardError(t *testing.T) {
	src := r67ReadSrc(t, "provider_refresh.go")

	// 定位 fetchActiveCredentialsForProvider 的调用点
	idx := strings.Index(src, "h.fetchActiveCredentialsForProvider(bgCtx, providerID)")
	if idx < 0 {
		t.Fatal("call site of fetchActiveCredentialsForProvider not found in provider_refresh.go")
	}
	// 取该调用所在的整行（可能跨行，取前后一小段窗口内的赋值语句）
	window := src[r67Max(0, idx-200):r67Min(len(src), idx+900)]

	if strings.Contains(window, "creds, _ :=") || strings.Contains(window, ", _ = h.fetchActiveCredentialsForProvider") {
		t.Errorf("D1 regression: the call site discards the error again — "+
			"a credential-load failure would be recorded as a successful refresh that did nothing:\n%s",
			window)
	}
	if !strings.Contains(window, "credErr") {
		t.Errorf("D1 regression: expected the call site to bind the error (credErr) and handle it; got:\n%s", window)
	}
	// 失败必须被记账成 failed，而不能只是打日志
	if !strings.Contains(window, "providerRefreshFailed") {
		t.Errorf("D1 regression: a credential-load failure must be recorded as providerRefreshFailed, "+
			"otherwise the run is reported green; got:\n%s", window)
	}
}

// TestR67A_ModelAliasIndexPerRowScanMustNotTruncate 钉 D2。
//
// 两个调用方（analytics.go 的 matrix 与 funnel）都是 `aliasIdx, _ :=`
// 丢弃错误。因此 loadModelAliasIndex 的**单行 Scan 失败**若上抛，实际效果
// 是在第一行坏数据处截断整个别名索引，且截断对调用方完全不可见——比原来
// 的「跳过该行」更差：analytics 的 canonical 列会静默退化成 raw 名，
// 看起来像「模型换了名字」。
func TestR67A_ModelAliasIndexPerRowScanMustNotTruncate(t *testing.T) {
	src := r67ReadSrc(t, "model_normalize.go")

	start := strings.Index(src, "func loadModelAliasIndex")
	if start < 0 {
		t.Fatal("loadModelAliasIndex not found in model_normalize.go")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		end = len(src) - start
	}
	body := src[start : start+end]

	// 单行 Scan 失败必须跳行留痕，不能 return
	if !strings.Contains(body, "warnRowSkip") {
		t.Errorf("D2 regression: loadModelAliasIndex's per-row Scan failure must warn+continue "+
			"(callers discard the error, so an up-throw silently truncates the whole index); got:\n%s", body)
	}
	// 只取 rows.Scan() 所在的循环体，别把下面 rows.Err() 的合法上抛算进来
	scanStart := strings.Index(body, "rows.Scan")
	scanBlock := body[scanStart:]
	if loopEnd := strings.Index(scanBlock, "\n\t}"); loopEnd > 0 {
		scanBlock = scanBlock[:loopEnd]
	}
	if idx := strings.Index(scanBlock, "return idx,"); idx >= 0 {
		t.Errorf("D2 regression: per-row Scan failure must not `return idx, ...` — "+
			"the two analytics callers use `aliasIdx, _ :=` and would silently render raw model names:\n%s",
			scanBlock[:r67Min(len(scanBlock), 400)])
	}
	// 迭代终检仍必须保留 —— 那是另一种故障，不能一起被「修」掉
	if !strings.Contains(body, "rows.Err()") {
		t.Errorf("D2 fix must not remove the rows.Err() terminal check; it is the actual " +
			"iteration-abort protection and R66 added it deliberately")
	}
}

// TestR67A_AliasIndexCallersMustTraceLoadFailure 钉住调用方侧。
//
// 即使 loadModelAliasIndex 正确上抛，丢弃错误的调用方仍会让「索引缺失/
// 截断」不可见。端点照常返回（别名只是展示层归并）是对的，但必须留痕。
func TestR67A_AliasIndexCallersMustTraceLoadFailure(t *testing.T) {
	src := r67ReadSrc(t, "analytics.go")
	occurrences := strings.Count(src, "loadModelAliasIndex(ctx, h.db)")
	if occurrences == 0 {
		t.Fatal("no loadModelAliasIndex call site found in analytics.go")
	}
	if strings.Contains(src, "aliasIdx, _ := loadModelAliasIndex") {
		t.Errorf("D2 caller regression: analytics.go still discards the alias-index error "+
			"at %d call site(s); a truncated index must leave a trace", occurrences)
	}
	// 至少要有一处把错误记下来
	if !strings.Contains(src, "warnRowSkip") {
		t.Errorf("expected the analytics call sites to trace an alias-index load failure")
	}
}

// TestR67A_WorkTypeL1CountsUpThrowIsLoadBearing 把 D3 钉成**有意决策**。
//
// 复审代理把这里标为「机制不精确」。核对后结论相反：`dbCounts, _ :=`
// 丢弃错误，所以 `return nil, err` 的作用不是「让调用方知道」，而是把
// 「半个 map」换成 nil，使 mergeL1TaskTypes(nil) 走**调用方自己文档化的**
// canonical-only 回退。若把它降级成 warn，调用方就会把半个 map 当全量用。
// 因此这里的上抛是承重的，必须防止后来者「顺手改成 warn」。
func TestR67A_WorkTypeL1CountsUpThrowIsLoadBearing(t *testing.T) {
	src := r67ReadSrc(t, "work_types.go")
	start := strings.Index(src, "func (h *WorkTypeHandlers) fetchL1Counts")
	if start < 0 {
		t.Fatal("fetchL1Counts not found")
	}
	end := strings.Index(src[start:], "\nfunc ")
	if end < 0 {
		end = len(src) - start
	}
	body := src[start : start+end]
	if !strings.Contains(body, "rows.Err()") {
		t.Errorf("D3: fetchL1Counts must keep the rows.Err() up-throw. The caller uses " +
			"`dbCounts, _ :=`, so an up-throw is what converts a half-filled map into nil, " +
			"triggering the caller's documented canonical-only fallback. A warn here would " +
			"let a truncated map be used as if complete.")
	}
	// 注释里必须保留这条推理，否则后人无从判断是否可以"简化"
	if !strings.Contains(body, "canonical-only") {
		t.Errorf("D3: the reasoning comment explaining why the up-throw is load-bearing was removed")
	}
}

func r67ReadSrc(t *testing.T, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("admin", file))
	if err != nil {
		b, err = os.ReadFile(file) // go test 的工作目录可能已是包目录
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
	}
	return string(b)
}

func r67Max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func r67Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
