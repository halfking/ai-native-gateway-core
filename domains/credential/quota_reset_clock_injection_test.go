package credential

// ★★ 2026-11-02 第二十三轮：恢复点推导链上的**时钟必须同一口**。
//
// ── 这道门为什么存在 ────────────────────────────────────────────────────
//
// 第二十二轮的整轮回归里，`domains/credential` 的
// **TestQuotaRecoveryBodyResetWins** 红了（HEAD 上就红 ⇒ 与那一轮无关）。
// 根因（实测，不是推测）：
//
//	domains/credential/writer.go 的 parseQuotaResetTimestamp 里写死了
//	time.Now() ⇒ 同一条恢复点推导链上**混了两个时钟**：
//	  · 来源 1（body 词表的绝对 reset 时间）用**墙钟**
//	  · 来源 2（Retry-After）与来源 3（窗口启发式）用**注入时钟**（nowArg）
//
// 而 errorsx.ParseQuotaResetAt **只接受未来的绝对时间戳**
// （lead := t.Sub(now); lead > 0 && lead <= maxQuotaResetLead）。
// ⇒ 于是那条测试里写死的 `2026-11-02 08:00:00 UTC`：
//	· 相对**注入时钟**（2026-10-31 12:00）在未来 ⇒ 应当胜出；
//	· 相对**墙钟**（2026-11-02 09:33 UTC 之后）已经是过去 ⇒ 被解析器拒绝
//	  ⇒ 落回窗口启发式（次日零点）⇒ 测试必然转红。
//
// ★★ 这是一条**定时炸弹**：它不会一直红，它是「过了某个时刻开始红」。
//	而且**不会有人**在那个时刻注意到它 —— 只有下一次有人碰这个包才会看见。
//
// ⇒ 修法在**根因**：参考时钟由调用方传入（parseQuotaResetTimestamp(detail, now)）
// ⇒ 生产侧两个调用点传的本来就是 time.Now() ⇒ **生产行为不变**；
//	测试侧注入时钟对**三条来源**都生效 ⇒ 断言不再依赖墙钟。
//
// ── 两道门 ──────────────────────────────────────────────────────────────
//
//	门 C1（行为）：**注入时钟必须真的被用上**。
//	  ★ 用**固定的历史日期对**（注入时钟 2020-01-01 → body 2020-01-02 08:00）：
//	    相对注入时钟在未来（应当胜出）、相对墙钟在**过去**（会被解析器拒绝）
//	    ⇒ 这条断言**只有在实现用注入时钟时才成立** ⇒ 门有牙；
//	    ★ 而且因为两个日期都是历史日期 ⇒ **任何时候跑都成立**（永不定时炸弹）。
//	门 C2（AST · 全仓）：**任何生产调用点**都不得把 time.Now() 直接当作
//	  errorsx.ParseQuotaResetAt 的 now 实参（⇒ 注入时钟不许在最后一跳被偷换）。
//	  ★ 分母锚在**全仓生产文件**上（不是手写文件清单）。
//	  ★ 恒绿护栏：必须扫到 ≥1 个生产调用点 ⇒ 否则「一个都没扫到」会恒绿。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── 门 C1：注入时钟必须真的被用上 ──────────────────────────────────────

func TestQuotaRecoveryHonorsInjectedClock(t *testing.T) {
	// ★ 固定的历史日期对：注入时钟 2020-01-01 12:00，body 里的绝对时间戳
	//   2020-01-02 08:00 —— 相对**注入时钟**在未来（应当胜出），
	//   相对**墙钟**（永远是 2026 及以后）在过去（解析器会拒绝）。
	// ⇒ 实现若改回写死墙钟，这里立刻红。
	now := time.Date(2020, 1, 1, 12, 0, 0, 0, time.UTC)
	want := time.Date(2020, 1, 2, 8, 0, 0, 0, time.UTC)
	detail := "quota exceeded, resets at 2020-01-02 08:00:00 UTC"

	got := inferQuotaRecoveryAtNow(detail, 0, now)
	if !got.at.Equal(want) {
		t.Fatalf("body reset recovery = %s want %s（★ 注入时钟必须被用上；"+
			"若这里红了，多半是 parseQuotaResetTimestamp 又写死了 time.Now()）",
			got.at, want)
	}
	if got.source != quotaSourceBodyReset {
		t.Fatalf("source = %s want body_reset", got.source)
	}

	// 同样的断言走另一条入口（inferQuotaRecoverAtNow）⇒ 两条入口都必须同一口。
	got2 := inferQuotaRecoverAtNow(detail, now)
	if !got2.Equal(want) {
		t.Fatalf("inferQuotaRecoverAtNow = %s want %s（★ 两条入口必须同一口）",
			got2, want)
	}
}

// ── 门 C2：全仓生产调用点不得把 time.Now() 当 now 实参 ──────────────────

// parseQuotaResetAtCallSites 找出全仓**生产**文件里对 errorsx.ParseQuotaResetAt
// 的调用点，并报告「now 实参是 time.Now()」的那些。
//
// ★ 返回 (总调用点数, 违规调用点列表)。
func parseQuotaResetAtCallSites(t *testing.T) (int, []string) {
	t.Helper()
	repoRoot := filepath.Join("..", "..")
	skipDirs := map[string]bool{
		".git": true, "vendor": true, "node_modules": true,
		"bin": true, "docs": true, "web": true,
	}
	fset := token.NewFileSet()
	total := 0
	var violations []string

	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] || skipDirs[filepath.Base(path)] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		// 解析 errorsx 的本地名（含别名）。
		localName := ""
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if p == "github.com/kaixuan/llm-gateway-go/errorsx" {
				if imp.Name != nil {
					localName = imp.Name.Name
				} else {
					localName = "errorsx"
				}
			}
		}
		if localName == "" {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ParseQuotaResetAt" {
				return true
			}
			x, ok := sel.X.(*ast.Ident)
			if !ok || x.Name != localName {
				return true
			}
			total++
			if len(call.Args) < 2 {
				return true
			}
			inner, ok := call.Args[1].(*ast.CallExpr)
			if !ok {
				return true
			}
			isel, ok := inner.Fun.(*ast.SelectorExpr)
			if !ok || isel.Sel.Name != "Now" {
				return true
			}
			ix, ok := isel.X.(*ast.Ident)
			if !ok || ix.Name != "time" {
				return true
			}
			pos := fset.Position(call.Pos())
			violations = append(violations,
				pos.Filename+":"+itoa(pos.Line)+" ⇒ now 实参是 time.Now()")
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", repoRoot, err)
	}
	return total, violations
}

// itoa 是极简整数转字符串（避免为一个门引入 strconv 的 import 噪声）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestParseQuotaResetAtNeverReceivesWallClock(t *testing.T) {
	total, violations := parseQuotaResetAtCallSites(t)
	// ★ 恒绿护栏：一个生产调用点都没扫到 ⇒ 门是恒绿的 ⇒ 必须报出来。
	if total < 1 {
		t.Fatalf("全仓生产文件里没扫到任何 errorsx.ParseQuotaResetAt 调用点 ⇒ " +
			"扫描面失效（文件挪了位置？导入别名变了？），这道门会恒绿")
	}
	if len(violations) > 0 {
		t.Fatalf("★ 注入时钟在最后一跳被偷换（这些调用点把 time.Now() 当 now 实参）：\n  %s\n"+
			"⇒ 时间来源混用 ⇒ 依赖注入时钟的测试会变成定时炸弹",
			strings.Join(violations, "\n  "))
	}
}
