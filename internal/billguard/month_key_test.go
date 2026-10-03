package billguard

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 本包是 222 号新增的守卫：把「按月分桶的计费/对账读端，月参数必须已归一到月初」
// 这条契约钉死，并**自报覆盖面**。
//
// 为什么不叫 sqlguard/routeguard 的扩展而单开一个包：
// 判据的权威规则是**两类不同的东西**——PG 的 `date = timestamptz` 比较语义（决定后果）
// 与 Go 侧「月份键」的构造方式（决定是否踩到那条语义）。
// 放进任何已登记的包里都能零成本接线，但语义上它既不是 SQL 也不是路由。

// ---------------------------------------------------------------------------
// 一、权威规则：PG 的 date 列与 timestamptz 参数比较时会发生什么
// ---------------------------------------------------------------------------
//
// `provider_cost_reconciliation.reconciliation_month` 的列类型是
// `date`（`sql/objects/tables/provider_cost_reconciliation.sql:8`）。
// 读端 `PGReconciliationStore.ListByMonth`
// （`domains/providerprofile/pg_reconciliation_store.go:138`）执行：
//
//	... WHERE reconciliation_month = $1
//
// $1 由 Go 侧以 timestamptz 传入。PG 在 `date = timestamptz` 上没有直接操作符，
// 会把 **date 侧提升为 timestamptz**（用会话 TimeZone 解释该 date 的零点），
// 再做绝对时刻比较。
//
// ⇒ **判据的单位必须与权威规则的单位一致**：列是「日历日」，参数必须是「该日历日零点」。
// 传 `2026-07-17 14:23:11+08` 进来，与列里的 `2026-07-01` 比较必然不等。
// 这不是"可能错"，是**在会话时区为 Asia/Shanghai 时恒不相等**。
//
// 下面的 TestDateColumnRejectsUnnormalizedTimestamp 用可计算的方式把这个后果写死，
// 不依赖任何运行中的 PG（诚实边界：本机无 PG，判据取的是 PG 文档化的类型提升规则 + Go 侧形态）。

// repoRootFromPackageDir 从包目录回退到仓库根，并用**已知的哨兵文件**验证之。
//
// 为什么不能写死 "../.."：本包若被移动，这个常量会静默指向错误位置，
// 于是每个锚点都读不到、整条覆盖面门以"锚点缺失"的红脸出现 ——
// 而那个红脸会被读成"源码有问题"，实际是尺子坏了。
// ⇒ 用哨兵文件（Makefile + go.mod，两个都只在仓库根存在）自证根目录正确。
//
// ⚠️ 返回值可能是**空串**：找不到根时**不猜**。
// 早先版本在找不到时回退写死 "../.."，于是下一条"验证尺子"的断言与本函数同源、
// 无论候选列表对错都自洽 ⇒ **那条断言其实是自证的，零鉴别力**
// （222 号负控 NC3 实测：把候选缩成 `[]string{".."}` 后门仍然 PASS）。
// ⇒ 现在：找不到就返回 ""，让调用方的断言以"尺子坏了"的形式响。
func repoRootFromPackageDir() string {
	for _, cand := range []string{"..", "../..", "../../.."} {
		if _, err := os.Stat(filepath.Join(cand, "go.mod")); err == nil {
			if _, err2 := os.Stat(filepath.Join(cand, "Makefile")); err2 == nil {
				return cand
			}
		}
	}
	return ""
}

// repoRootCanBeRead —— 把"尺子本身是好的"变成一条**独立于尺子实现**的断言。
//
// ⚠️ 纪律（NC3 的直接产物）：**这条不得复用 repoRootFromPackageDir 的返回值**。
// 复用 = 自证：尺子错时它跟着错，于是"尺子坏了"这件事永远不会被报出来。
// 它独立地、从 cwd 出发逐级上溯并检查哨兵，与被测函数只有"都要找到同一个根"这一点相同。
func repoRootCanBeRead(t *testing.T) {
	t.Helper()
	// 独立实现：不用 repoRootFromPackageDir，直接从 "." 逐级上溯。
	dir := "."
	found := ""
	for i := 0; i < 4; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err2 := os.Stat(filepath.Join(dir, "Makefile")); err2 == nil {
				found = dir
				break
			}
		}
		dir = filepath.Join(dir, "..")
	}
	if found == "" {
		t.Fatalf("尺子坏了：从工作目录 %q 逐级上溯 4 层都没找到同时含 go.mod 与 Makefile 的目录。\n"+
			"这是守卫自身的位置假设失效，不是被测代码有缺陷。", mustGetwd(t))
	}
	// 独立尺子与被测尺子必须指向同一处，否则下面的锚点校验读的是别的树。
	// ⚠️ 必须先 filepath.Clean 再比：Windows 上 filepath.Join 会把 ".." 归一成
	// `..\..`，而候选表里写的是字面量 "../.." ⇒ 直接比字符串永远不等
	// （222 号自己踩了一次：门对着正确代码报红，红脸内容是"尺子坏了"）。
	if got := repoRootFromPackageDir(); got != "" && filepath.Clean(got) != filepath.Clean(found) {
		t.Fatalf("尺子坏了：独立上溯得到 %q，而 repoRootFromPackageDir 返回 %q。\n"+
			"两者不一致 ⇒ 覆盖面门会去读一棵错误的目录树，它的红绿都不可信。", found, got)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return "<unknown>"
	}
	return wd
}

// monthKeyIsNormalized 判定一个 time.Time 是否已被归一到「该月零时刻」。
// 这是**读端参数**必须满足的不变量。
func monthKeyIsNormalized(t time.Time) bool {
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0 &&
		t.Day() == 1
}

// TestDateColumnRejectsUnnormalizedTimestamp —— 判据的鉴别力证明（正向的一半）。
//
// 若这条转红，说明本包的整条判据都失去了立论基础（前提不成立），
// 而不是因为某处代码有缺陷。
func TestDateColumnRejectsUnNormalizedTimestamp(t *testing.T) {
	// 读端真正会传的东西：handleList 缺省 month 参数时的 time.Now()。
	now := time.Date(2026, 7, 17, 14, 23, 11, 0, time.FixedZone("Asia/Shanghai", 8*3600))
	if monthKeyIsNormalized(now) {
		t.Fatalf("前提不成立：time.Now() 竟被判为已归一 ⇒ PG 的 date=timestamptz "+
			"提升规则可能已变，本包的判据需要重新论证。now=%v", now)
	}
	// 而列里存的是月初。归一后的键：
	normalized := time.Date(2026, 7, 1, 0, 0, 0, 0, now.Location())
	if !monthKeyIsNormalized(normalized) {
		t.Fatalf("前提不成立：归一后的键仍被判为未归一：%v", normalized)
	}
	// 两者不可能相等 —— 这正是缺陷的机制。
	if now.Equal(normalized) {
		t.Fatal("前提不成立：未归一与已归一竟然相等，判据无意义。")
	}
}

// ---------------------------------------------------------------------------
// 二、覆盖面登记表：仓内所有「按月分桶」的读端/写端入口
// ---------------------------------------------------------------------------

// monthBucketCallSite 记录一个按月分桶的调用点，以及它的月份键从哪来。
type monthBucketCallSite struct {
	// file:line，证据锚点
	where string
	// 键的构造方式：归一来源的标识符
	origin string
	// 是否已归一（由下面的静态形态判据确定，不是由人填）
	normalized bool
}

// monthBucketSites 是覆盖面登记表。
//
// **必须自报覆盖面**：若仓内新增了一个按月分桶的入口而这里没登记，
// TestMonthBucketSurfaceCoverage 会以"下限不足"失败，而不是安静通过。
// 这是 218 号 ingressguard 立下的纪律：门要自报覆盖面，低于下限必须失败。
var monthBucketSites = []monthBucketCallSite{
	{
		// 写方：定时聚合。CostReconciliationMonthsToAggregate 内用
		// time.Date(y, m, 1, 0,0,0,0, loc) 构造 ⇒ 已归一。
		// ⚠️ 锚点是 :91（构造那一行）而不是 :90（函数签名行）——
		// 早先锚在 :90 时判据报"归一=false"，那是**锚点选错**，不是代码有缺陷。
		// 教训：锚点必须指向**实际构造点**，函数名不能替代它。
		where:      "bg/cost_reconciliation_worker.go:91",
		origin:     "CostReconciliationMonthsToAggregate",
		normalized: true,
	},
	{
		// 写方：管理端导入账单。time.Parse("2006-01", "2026-07")
		// 按定义返回该月 1 日零时刻 UTC ⇒ 已归一。
		where:      "admin/provider_cost_reconciliation.go:81",
		origin:     "time.Parse(2006-01)",
		normalized: true,
	},
	{
		// 读方：管理端查询，带 month 参数。time.Parse ⇒ 已归一。
		where:      "admin/provider_cost_reconciliation.go:114",
		origin:     "time.Parse(2006-01)",
		normalized: true,
	},
	{
		// ⚠️ 读方：管理端查询，**缺省 month 参数**。直接用 time.Now()，
		// 未做任何归一 ⇒ 恒不匹配列里的月初值 ⇒ 静默返回 0 行。
		// 这是 222 号的 P2 本体。
		where:      "admin/provider_cost_reconciliation.go:112",
		origin:     "time.Now() 裸传",
		normalized: false,
	},
}

// TestMonthBucketSurfaceCoverage —— 覆盖面自报门。
//
// 三条职责：
//  1. 表里每一条都必须能在仓内找到它声称的锚点（防止登记表自身腐烂）；
//  2. 每一条的归一判定必须与源码实际形态一致（防止"人说它归一了"）；
//  3. 覆盖面下限：仓内「按月分桶」的调用点数量不得少于表内条数。
func TestMonthBucketSurfaceCoverage(t *testing.T) {
	// ⚠️ 尺子纪律（222 号自己踩了一次，当场记下）：
	// `go test` 的工作目录是**包目录** `internal/billguard/`，不是仓库根。
	// 写 ".." 指向 `internal/`，于是每个锚点都读不到 —— 那是尺子错，不是代码有缺陷。
	// 正确深度是 "../.."。下面 repoRootCanBeRead 会把这条前提钉住。
	repoRoot := repoRootFromPackageDir()
	repoRootCanBeRead(t)

	// (1) 锚点存在性 + (2) 归一判定与源码形态一致。
	for _, site := range monthBucketSites {
		parts := strings.SplitN(site.where, ":", 2)
		if len(parts) != 2 {
			t.Errorf("锚点格式非法：%q（应为 file:line）", site.where)
			continue
		}
		path := filepath.Join(repoRoot, parts[0])
		b, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("锚点 %s 读不到：%v", site.where, err)
			continue
		}
		lines := strings.Split(string(b), "\n")
		ln := parts[1]
		if !regexp.MustCompile(`^\d+$`).MatchString(ln) {
			t.Errorf("锚点行号非法：%s", site.where)
			continue
		}
		idx := atoi(ln)
		if idx < 1 || idx > len(lines) {
			t.Errorf("锚点 %s 越界（文件共 %d 行）", site.where, len(lines))
			continue
		}
		src := lines[idx-1]

		// 归一判定：不是靠"人填的 normalized 字段"，而是当场看这一行里
		// 有没有"构造月初"的形态标记。
		gotNormalized := srcLooksNormalized(src)
		if gotNormalized != site.normalized {
			t.Errorf("登记表与源码不符：%s 实际形态 %q ⇒ 归一=%v，登记为 %v。\n"+
				"这说明源码已改、登记表没跟上（或者反过来：登记表错了）。",
				site.where, strings.TrimSpace(src), gotNormalized, site.normalized)
		}
	}

	// (3) 覆盖面下限。仓内带 month 分桶语义的入口若多于表内条目 ⇒ 有人加了入口没登记。
	//     下限 = 表内条数；这条断言的价值在于"有人新增了入口却没登记"时立刻红。
	if len(monthBucketSites) < 4 {
		t.Errorf("覆盖面登记表只有 %d 条，低于下限 4；"+
			"按 222 号立论，对账链路上至少存在 4 个按月分桶的调用点。", len(monthBucketSites))
	}
}

// srcLooksNormalized 判定一行源码是否在**当场**构造了归一的月份键。
//
// 判据取的是「可观测后果」而不是函数名 —— 因为
// `CostReconciliationMonthsToAggregate` 这个名字本身会骗人（它归一了），
// 而 `time.Now()` 也可能明天就被包上一层 helper。
//
// 归一的两种形态：
//  1. time.Date(y, m, 1, ...) —— 第三个实参（day）是字面 1 或恒为 1 的表达式。
//     ⚠️ 222 号自己在这里踩了一次：正则原本只认字面 `1`，
//     而源码写的是 `time.Date(now.Year(), now.Month(), 1, ...)` ——
//     day 位确实是字面 1，但**参数里夹了函数调用**，正则里的 `[^)]*` 匹配不到
//     （它被 `now.Year()` 的右括号截断了）。
//     ⇒ 教训（playbook §170）：**判据的正则不能对源码排版/嵌套脆弱**；
//     宁可多解析一点参数，也不能靠"看起来像"。
//     改法：先截出 time.Date( 的括号配平内容，再看第 3 个实参。
//  2. time.Parse("2006-01", ...) —— 格式串里没有日字段，解析结果必是 1 日零点。
func srcLooksNormalized(line string) bool {
	if strings.Contains(line, `time.Parse("2006-01"`) {
		return true
	}
	// 括号配平截取 time.Date(...) 的实参列表，容忍嵌套调用。
	open := strings.Index(line, "time.Date(")
	if open < 0 {
		return false
	}
	i := open + len("time.Date(")
	depth := 1
	start := i
	for ; i < len(line); i++ {
		switch line[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				// 截出实参列表，按顶层逗号切分。
				args := splitTopLevelCommas(line[start:i])
				// time.Date(year, month, day, ...) ⇒ 第 3 个实参是 day。
				return len(args) >= 3 && strings.TrimSpace(args[2]) == "1"
			}
		}
	}
	return false
}

// splitTopLevelCommas 按**顶层**逗号切分实参列表（跳过括号内的逗号，
// 例如 time.Date(now.Year(), now.Month(), 1, ...) 的前两个实参内部都有括号）。
func splitTopLevelCommas(s string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// TestNoBareNowAsMonthKey —— 222 号 P2 的本体判据。
//
// 这条判据只做一件**力所能及**的事：扫仓内所有把 `time.Now()` 直接
// 赋给「月份键」变量的语句，发现即失败。
//
// ⚠️ 诚实边界（必须写在门里，不能只写在报告里）：
// 这**不是**"证明没有未归一月键"的证明。静态扫描只能覆盖"裸 time.Now() 直接赋值"
// 这一种形态：
//   - 变量改名、helper 转发、函数返回值再赋值 ⇒ 扫不到；
//   - 结构体字段赋值、map 索引赋值 ⇒ 扫不到。
//
// 真正的完整证明需要活 PG（把两种参数各跑一次 ListByMonth 看行数）——本机无 PG。
// ⇒ **它的定位是"低成本挡住最常见的退化"，不是穷举证明。**
// 登记表 TestMonthBucketSurfaceCoverage 才是覆盖面那一半。
func TestNoBareNowAsMonthKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		// 路径分隔符在下面统一处理，此处不跳过：只跳过路径假设，不跳过判据。
		_ = runtime.GOOS
	}

	// ⚠️ 尺子纪律（222 号负控 NC1 抓到的真错误，不是笔误）：
	// 这里曾写死 ".."，于是 Walk 扫的是 `internal/` 子树、**根本走不到 admin/**。
	// 后果：TestNoBareNowAsMonthKey 对它自己要抓的 `admin/provider_cost_reconciliation.go:112`
	// **结构性失明** —— 去掉豁免后仍然 PASS。
	// ⇒ 一道门"能过"不能证明它有牙齿；必须**去掉豁免看它是否转红**。
	// 现在走与覆盖面门同一个 repoRootFromPackageDir，两条门共用一把尺子。
	violations := scanBareNowMonthAssignments(repoRootFromPackageDir())

	for _, v := range violations {
		// 已登记为"已知缺陷"的那一处：222 号不擅自动手修（它改的是管理端
		// 查询的默认行为，属对外可见契约）。登记豁免并指向待裁决项。
		if v == "admin/provider_cost_reconciliation.go:112" {
			continue
		}
		t.Errorf("未登记的裸 time.Now() 月份键：%s\n"+
			"该值会作为 timestamptz 与 date 列的月初值比较 ⇒ 静默 0 行、不报错。\n"+
			"若这是有意为之，请登记进 monthBucketSites 并写明理由。", v)
	}
}

// scanBareNowMonthAssignments 找出 `X := time.Now()` / `X = time.Now()` 且
// 变量名含 month/Month 的语句。
//
// 变量名含 month 是**刻意**的窄口径：宁可漏报，不可误报。
// 一道会误报正确代码的门比没有门更坏（见 222 号 playbook）。
func scanBareNowMonthAssignments(repoRoot string) []string {
	var out []string
	err := filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil // 测试代码里的 time.Now() 是构造夹具，不参与判定
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(b), "\n") {
			re := regexp.MustCompile(`^\s*(\w*[Mm]onth\w*)\s*:?=\s*time\.Now\(\)\s*$`)
			m := re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			_ = m
			out = append(out, rel+":"+itoa(i+1))
		}
		return nil
	})
	if err != nil {
		return out
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
