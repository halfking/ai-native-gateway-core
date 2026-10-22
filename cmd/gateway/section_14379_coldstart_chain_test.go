//go:build !integration

package main

// §143.79 的门：把「压缩器【冷启动读链】是一条【四段瀑布】」这条结论及其依赖的
// 前提，钉成**结构判据**（★ 用 go/ast 查形态，不查裸子串）。
//
// ★★★ 为什么这道门要写（§143.79 的起因）：
//
//	§143.77 钉死了「L3 兜底腿 4 天烧 63 分钟」的根因（并行 worker 启动开销）。
//	§143.78 钉死了「25,492 次为什么全返 0 行」（⇒ 那个会话还没有上一轮）。
//	§143.79 把【整条读链】摊开 ⇒ ★★ 发现：
//	  ① 冷启动读链不是【一段】，是【四段瀑布】：
//	     LoadLatestOutbound(final_full) → LoadLatestOutbound(回退腿)
//	     → LoadChain(delta 链) → L3 兜底
//	  ② ★★ **回退腿（`ORDER BY turn_no DESC`）此前完全无人知晓**：
//	     生产 29,269 次 / 213,103.6 ms / 均值 7.28 ms ⇒ ⇒ 它比快照腿贵 3.6 倍。
//	  ③ ★★★ **落空瀑布自洽**：快照腿的 calls = 冷启动总次数；L3 的 calls =
//	     「v2 主路径落空后继续下走」的次数 ⇒ ⇒ 二者相除 = v2 主路径命中率（26.8%）
//	     ⇒ ⇒ **73.2% 的冷启动【三段全落空】**。
//	  ④ ★★★★ **订正**：库里「没有正文」的行存的是 jsonb `'null'` 而不是 SQL NULL
//	     ⇒ ⇒ **`outbound_body IS NOT NULL` 是恒真判据**
//	     ⇒ ⇒ 真正的「有正文」判据在 Go 侧（`string(raw) == "null"`）。
//
// ★★ 这道门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//
//	① ★★ `loadFromDB` 里 **v2 主路径在前、L3 兜底腿在后**（★ §143.78 已钉）。
//	② ★★★★ **回退腿存在，且它的过滤【没有 kind 条件】**（★ 这正是它的成因）。
//	③ ★★★★ **回退腿的过滤是 `outbound_body IS NOT NULL`**（★ ⇒ 与 ④ 合起来
//	   ⇒ ⇒ 【恒真】⇒ ⇒ 在 E1 门下它必然取到 `'null'` ⇒ ⇒ 必然落空）。
//	④ ★★★★ **Go 侧真正的「有正文」判据是 jsonb 口径**（★ `string(raw) == "null"`）
//	   ⇒ ⇒ ★ 这是 §143.79 的核心订正，★ 一旦有人把它改回 `IS NULL` 语义，
//	   ⇒ ⇒ **E1 门下的读侧成本面就又会被隐藏起来**。
//	⑤ ★★ 复算脚本必须存在且保留关键口径。
//	⑥ ★★ `FIXME-14379（` 登记块必须在。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const section14379AuditPath = "scripts/audit/verify_coldstart_read_chain.sql"

func repoRoot14379(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14379(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14379(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

func parseGo14379(t *testing.T, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(repoRoot14379(t), rel), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", rel, err)
	}
	return f
}

func findFunc14379(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("找不到函数 %s", name)
	return nil
}

// stringLit14379 收集一个函数体内出现的全部字符串字面量。
func stringLit14379(n ast.Node) []string {
	var out []string
	ast.Inspect(n, func(x ast.Node) bool {
		if bl, ok := x.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			out = append(out, strings.Trim(bl.Value, "`\""))
		}
		return true
	})
	return out
}

// sqlText14379 把函数体内所有字符串字面量拼起来（★ SQL 在 Go 里是多行反引号串）。
func sqlText14379(n ast.Node) string {
	return strings.Join(stringLit14379(n), "\n")
}

// TestSection14379ColdStartChainIsFourSegments（★ 判据①）
//
// ★★ 冷启动读链的【四段瀑布】（★ 顺序有语义）：
//
//	loadFromDB:
//	  if c.turnReader != nil {
//	     LoadLatestOutbound(...)  ← ① 快照腿（+ ①b 回退腿，在它内部）
//	     if 空 → LoadChain(...,10) ← ② delta 链腿
//	  }
//	  return c.loadFromLegacyDB(...)  ← ③ L3 兜底腿
//
//	LoadLatestOutbound 内部：
//	  ①  kind='final_full' … ORDER BY ts DESC LIMIT 1
//	  ①b … ORDER BY turn_no DESC, ts DESC LIMIT 1   ← ★ 回退腿（无 kind 条件）
//
// ★★ 判据一律用【AST 调用】查，★ 不用字符串字面量拼接
//
//	（★ 第一版写的是「把函数体里的字符串字面量拼起来再找 `LoadLatestOutbound`」
//	⇒ ⇒ 那是【方法名不是字面量】⇒ ⇒ 判据恒假 ⇒ ⇒ 门自己红了 ⇒ ★ 本轮自抓）。
func TestSection14379ColdStartChainIsFourSegments(t *testing.T) {
	cache := parseGo14379(t, "domains/hooks/compression/session_cache.go")
	loadFromDB := findFunc14379(t, cache, "loadFromDB")

	// ★ 三段必须都以【方法调用】出现（★ 顺序另判）
	for _, want := range []string{"LoadLatestOutbound", "LoadChain", "loadFromLegacyDB"} {
		if !callNamed14379(loadFromDB, want) {
			t.Errorf("★ loadFromDB 里没有对 %s 的调用 —— 冷启动链的这一段不见了", want)
		}
	}
	// ★★ 对照形态：v2 主路径的两段必须真的存在（★ 否则「L3 是兜底」这句话就空了）
	hasSnap, hasChain := false, false
	ast.Inspect(loadFromDB, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.SelectorExpr); ok {
				switch id.Sel.Name {
				case "LoadLatestOutbound":
					hasSnap = true
				case "LoadChain":
					hasChain = true
				}
			}
		}
		return true
	})
	if !hasSnap || !hasChain {
		t.Error("★ loadFromDB 里 v2 主路径的两段没有都以【方法调用】出现 —— " +
			"判据退化成字符串匹配，失去结构意义")
	}
	// ★★ 顺序：快照腿必须【在】delta 链腿之前（★ 读方契约：快照优先）
	//   ★ token.Pos 本身可比较（★ 它就是带方法的一个 int）——★ 别去取 .Offset
	//   ★（★ token.Pos 没有 Offset 字段 ⇒ 第一版编译不过 ⇒ 门自己抓到了）
	var snapPos, chainPos token.Pos
	ast.Inspect(loadFromDB, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.SelectorExpr); ok {
				switch id.Sel.Name {
				case "LoadLatestOutbound":
					if snapPos == 0 {
						snapPos = ce.Pos()
					}
				case "LoadChain":
					if chainPos == 0 {
						chainPos = ce.Pos()
					}
				}
			}
		}
		return true
	})
	if snapPos != 0 && chainPos != 0 && snapPos > chainPos {
		t.Error("★ 快照腿 LoadLatestOutbound 出现在 delta 链 LoadChain 之后 —— " +
			"★ 「快照优先」这个语义变了（★ 读方契约要求快照优先）")
	}
}

// callNamed14379 判断一个表达式里有没有对某个具名函数的调用
// （★ 同时匹配裸函数名 `f(...)` 与方法名 `x.f(...)`）。
func callNamed14379(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if x == nil {
			return false
		}
		if ce, ok := x.(*ast.CallExpr); ok {
			switch fn := ce.Fun.(type) {
			case *ast.Ident:
				if fn.Name == name {
					found = true
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == name {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// TestSection14379FallbackLegHasNoKindFilter（★ 判据②③）
//
// ★★★★ 回退腿（`ORDER BY turn_no DESC`）是本轮新发现的成本面（29,269 次 /
// 213.1 ms / 均值 7.28 ms）。★ 它的成因正是【过滤里没有 kind 条件】：
// 于是它会取到【任意轮】的最新出站 ⇒ ⇒ 而 E1 门下那些行都是 jsonb 'null'。
func TestSection14379FallbackLegHasNoKindFilter(t *testing.T) {
	f := parseGo14379(t, "domains/session/v2/turn_reader.go")
	fd := findFunc14379(t, f, "LoadLatestOutbound")

	sql := sqlText14379(fd)

	// ① 回退腿必须存在（★ 判据的落点）
	if !strings.Contains(sql, "ORDER BY turn_no DESC") {
		t.Fatal("★ LoadLatestOutbound 里没有回退腿（ORDER BY turn_no DESC）—— " +
			"★ 本轮发现的这条成本面（29,269 次 / 213.1 ms）不再存在，" +
			"⇒ ⇒ §143.79 的「四段瀑布」变回三段 ⇒ ⇒ 结论要重新称量")
	}
	// ② 回退腿【不能】带 kind 条件（★ 这正是它的成因）
	idx := strings.Index(sql, "ORDER BY turn_no DESC")
	head := sql[:idx]
	// ★ 只看回退腿【自己】那段 SQL（★ 从上一个 SELECT 起算）
	if last := strings.LastIndex(head, "SELECT outbound_body"); last >= 0 {
		leg := head[last:]
		if strings.Contains(leg, "kind =") {
			t.Error("★ 回退腿里出现了 kind 条件 —— " +
				"★ 它就不再是「取任意轮最新出站」，成本机制（恒真过滤 ⇒ 必然落空）改变")
		}
		if !strings.Contains(leg, "outbound_body IS NOT NULL") {
			t.Error("★ 回退腿的过滤不再是 `outbound_body IS NOT NULL` —— " +
				"★ 本轮的机制结论（该过滤在库里是【恒真】）的前提变了")
		}
	}
	// ③ 快照腿【必须】带 kind 条件（★ 对照：否则两条腿就没区别了）
	if !strings.Contains(sql, "kind = 'final_full'") {
		t.Error("★ 快照腿不再按 kind='final_full' 过滤 —— " +
			"★ 「快照腿 vs 回退腿」的对照失效")
	}
}

// TestSection14379RealBodyCheckIsJsonbCaliber（★ 判据④）
//
// ★★★★ 本轮的核心订正：
//
//	库里「没有正文」的行存的是 **jsonb `'null'`**，不是 SQL NULL
//	⇒ ⇒ **`outbound_body IS NOT NULL` 是恒真判据**
//	⇒ ⇒ 真正的「有正文」判据必须在 Go 侧（`string(raw) == "null"`）。
//
// ★ 一旦有人把它改回「用 IS NULL 判有无」⇒ ⇒ E1 门下的读侧成本面会重新被隐藏。
func TestSection14379RealBodyCheckIsJsonbCaliber(t *testing.T) {
	f := parseGo14379(t, "domains/session/v2/turn_reader.go")
	fd := findFunc14379(t, f, "LoadLatestOutbound")

	sql := sqlText14379(fd)
	// ★ SQL 侧不得出现「用 IS NULL 判有没有正文」
	if strings.Contains(sql, "outbound_body IS NULL") {
		t.Error("★ SQL 里出现了 `outbound_body IS NULL` —— " +
			"★ 库里「没有正文」的行存的是 jsonb 'null'，IS NULL 判据【恒假】⇒ ⇒ " +
			"★ 会把「所有会话都判成有正文」⇒ ⇒ E1 门下的读侧成本面被彻底隐藏")
	}

	// ★ Go 侧必须用 jsonb 口径判「空」
	body := sqlText14379(fd)
	_ = body
	found := false
	ast.Inspect(fd, func(n ast.Node) bool {
		if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.EQL {
			if lit, ok := be.Y.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if strings.Trim(lit.Value, "`\"") == "null" {
					found = true
				}
			}
		}
		return true
	})
	if !found {
		t.Error("★ LoadLatestOutbound 里没有 jsonb 口径的「空」判据（与字符串 \"null\" 比较）—— " +
			"★ §143.56 引入的 jsonb 口径被回退了 ⇒ ⇒ E1 门下的读侧成本面会重新被隐藏")
	}
}

// TestSection14379RecomputeScriptKeepsItsEvidence（★ 判据⑤）
func TestSection14379RecomputeScriptKeepsItsEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot14379(t), section14379AuditPath))
	if err != nil {
		t.Fatalf("★ 复算脚本缺失：%s（%v）", section14379AuditPath, err)
	}
	s := string(raw)
	for _, needle := range []string{
		// ★ 量具前提（★ 窗口长度 / 淘汰数 / 是否打满）
		"pg_stat_statements_info",
		"pg_stat_statements.max",
		// ★ 四段腿的指纹
		"kind = $3",
		"ORDER BY turn_no DESC",
		"SELECT b.turn_no",
		"COALESCE(ff.outbound_body",
		// ★ 形态口径（★ 必须用字节阈值，不许出现 detoast 的 jsonb_typeof）
		"pg_column_size(outbound_body) > 12",
		// ★ 现时速率（两次采样）
		"pg_sleep(60)",
		// ★ 落空瀑布自洽
		"pct_reaching_L3",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("★ 复算脚本缺 %q —— 它是本轮全部读数的唯一可复算依据", needle)
		}
	}
	// ★★★ 反向判据：脚本的【代码部分】里【不许】出现 jsonb_typeof
	//   （★ 它会 detoast ⇒ 全扫超时）
	//   ★★★ 判据必须【剥掉注释】后再查 —— ★ 第一版直接 `strings.Contains(整份脚本)`
	//   ⇒ ⇒ 命中了脚本【注释里】那句「jsonb_typeof() 会 detoast」⇒ ⇒ 门误报
	//   ⇒ ⇒ ★ 这正是 §143.73 的「剥注释后再查」纪律（★ 本轮又自踩一次）。
	code := stripSQLComments14379(s)
	if strings.Contains(code, "jsonb_typeof") {
		t.Error("★ 复算脚本的【可执行部分】里出现了 `jsonb_typeof` —— " +
			"★ 它会 detoast ⇒ 29 万行全扫必超时（★ 本轮已实测踩过一次）⇒ ⇒ " +
			"★ 形态判定必须用 `pg_column_size(outbound_body) > 12`")
	}
}

// TestSection14379ScriptMustCoverEveryLegShape（★ 判据⑦【本轮新增·最重要】）
//
// ★★★★★ 本轮最痛的一击：**复算脚本 v1 的腿指纹写窄了 ⇒ 少算 66%**。
//
//	v1 用 `query LIKE '%COALESCE(ff.outbound_body%'` 认「L3 兜底腿」
//	⇒ 它只认到 §143.55/§143.56 之后的那一版
//	⇒ 【完全漏掉】§143.55 之前的旧内 JOIN 形态
//	（★ 生产实测那条腿：calls=17,424 / total=2,619,553.7 ms / mean=150.342 ms
//	★ —— 它是【单条最贵的腿】，比 COALESCE 形态还贵 2.2 倍）
//	⇒ ⇒ v1 把 L3 读成 8,870 次，实际是 26,299 次 ⇒ 【少算 66%】
//	⇒ ⇒ v1 的「pct_v2_hit = 75.33%」是【错的】（真实是 27.41%）
//
// ★★★⇒ 通用式：**指纹写窄 ⇒ 读数系统性偏低 ⇒ 结论方向被带偏**。
// ⇒ ⇒ 修法不是「把指纹写宽一点」，而是【**逐形态点名认领 + 形态普查在先**】：
//
//	① 脚本必须先【普查】出全部候选形态（Q2）；
//	② 然后【逐条点名】认领（L3 的三个形态一个都不能少）；
//	③ ⇒ 且【第五腿】（写侧 getLatestBodies）必须在场 —— 它此前完全无人知晓。
//
// ★ 这道门钉的就是【认领清单的完整性】：少认一个形态 ⇒ 门必须红。
//
// ★★★★★★ 2026-10-23（§143.79 轮末）**订正**：★ 这道门的第一版【自己也恒绿了】，
//
//	★ 症状：变异 M1（删掉形态 α 的 queryid）/ M2（改名）/ M3（删第五腿）/ M6
//	  （把 queryid 换进注释）—— ★★ **全部 GREEN** ⇒ ⇒ 门没牙。
//	★ 根因：★ 本门查的是【剥注释后的代码】，★ 而三个 queryid 在脚本里
//	  **只出现在注释中的「映射表」里** ⇒ ⇒ 剥掉注释后**一个都不剩**
//	  ⇒ ⇒ 判据变成【恒假】（永远查不到 ⇒ 也永远红不了）。
//	⇒ ★★ 通用式（★ 与「剥注释后再查」那条纪律**同源但相反**）：
//	  ★ 「剥注释」是为了【不让注释里的引用冒充代码】⇒ 但它也会
//	  ★ **把「文档里必须存在的登记」一起剥掉**。
//	  ⇒ ⇒ **判据必须【分面】：代码面用剥注释后的文本、登记面用原文**。
//	  ⇒ ⇒ 且★ **必须有一条变异专打这个面**（★ M1/M2/M3/M6 就是）⇒
//	  ⇒ ⇒ **门自己恒绿时，只有变异台能发现** ⇒ ⇒ 变异台是【门的元门】。
//
// ★★ 修法：★ 本门现在查【两个面】：
//
//	① 【代码面】（剥注释后）：形态必须在【可执行部分】被点名（真在算它）；
//	② 【登记面】（原文）：形态必须在【映射表】里被登记（复核用的依据）。
//	⇒ ⇒ 两个面都要在 ⇒ ⇒ 任何一面被删都会红。
func TestSection14379ScriptMustCoverEveryLegShape(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot14379(t), section14379AuditPath))
	if err != nil {
		t.Fatalf("★ 复算脚本缺失：%s（%v）", section14379AuditPath, err)
	}
	full := string(raw)
	code := stripSQLComments14379(full)

	// ★★★ 判据①：**L3 的三个形态必须逐个点名**（★ 用 queryid 点名，★ 比 LIKE 精确）
	//   ★ 少一个 ⇒ 那个形态的成本就整段消失（★ v1 就是这么丢掉 2.62M ms 的）
	//   ★ ★ 两个面都要查：★ 代码面（真在算）+ 登记面（映射表里的复核依据）
	for _, qid := range []string{
		"8794675561828039885",  // L3 旧内 JOIN 形态（★ 最贵的那条）
		"3955058327330889760",  // L3 COALESCE(ff,sb) 形态
		"-7776241007544382269", // L3 COALESCE 的另一个参数形态
	} {
		if !strings.Contains(code, qid) {
			t.Errorf("★ 复算脚本的【可执行部分】没有点名 L3 形态 %s —— "+
				"★ 本轮实测：漏掉它 ⇒ L3 从 26,299 次被读成 8,870 次（少算 66%%）⇒ "+
				"★ 且「v2 命中率 75.33%%」这个结论方向直接被带偏（真实 27.41%%）", qid)
		}
		if !strings.Contains(full, qid) {
			t.Errorf("★ 复算脚本的【映射表】没有登记 L3 形态 %s —— "+
				"★ 映射表是【形态 → queryid → 仓内归属】的唯一复核依据 ⇒ ⇒ 缺一条就没人能复核", qid)
		}
	}
	// ★★★ 判据②：**第五腿必须在场**（★ 写侧 getLatestBodies）
	//   ★ 它不在冷启动读链上，但它读的是同一张表 ⇒ ⇒ 成本面必须被登记
	if !strings.Contains(code, "5347544359269580322") {
		t.Error("★ 复算脚本的【可执行部分】没有点名第五腿（写侧 getLatestBodies，queryid 5347544359269580322）—— " +
			"★ 它生产实测 63,105 次 / 435.7 s / 命中率仅 11.3% ⇒ ⇒ " +
			"★ 它此前完全无人知晓 ⇒ ⇒ 漏掉它 ⇒ 成本面被低估")
	}
	if !strings.Contains(full, "5347544359269580322") {
		t.Error("★ 复算脚本的【映射表】没有登记第五腿 —— " +
			"★ 映射表是复核用的唯一依据 ⇒ ⇒ 缺一条就没人能复核这条新成本面")
	}
	// ★★★ 判据③：**形态普查必须在【认领】之前**（★ 顺序有语义）
	//   ★ 普查在前 ⇒ 「还有没有别的形态」这个问题会被先问出来
	//   ★ 反过来（先认领后普查）⇒ 认领清单永远是【自己写的那几条】
	idxCensus := strings.Index(code, "形态普查")
	idxClaim := strings.Index(code, "8794675561828039885")
	if idxCensus < 0 {
		t.Error("★ 复算脚本里没有「形态普查」这一步 —— " +
			"★ 本轮教训：只【认领】自己知道的形态 ⇒ 漏掉的形态永远不会浮出来 ⇒ ⇒ " +
			"★ 必须先普查出全部候选形态，再逐条点名认领")
	}
	if idxCensus > 0 && idxClaim > 0 && idxCensus > idxClaim {
		t.Error("★ 复算脚本里「形态普查」排在「认领」之后 —— " +
			"★ 顺序反了 ⇒ ⇒ 认领清单依然是【自己写的那几条】⇒ ⇒ 漏腿的机制没被去掉")
	}
	// ★★★ 判据④：**全部读数必须在同一条 SELECT 里**（★ 跨快照相加会算出矛盾的数）
	//   ★ v1 把腿①/腿①b/腿②/L3 分四条 SELECT 取 ⇒ ⇒ 加出来的总和与分项不自洽
	//   ⇒ v2 把它们放进【同一条 SELECT】（CTE + JOIN）
	if !strings.Contains(code, "pct_of_total_calls") {
		t.Error("★ 复算脚本里没有「同一条 SELECT 内的占比」列 —— " +
			"★ 本轮教训：跨快照分条取数 ⇒ ⇒ 相加得到的数与分项不自洽 ⇒ ⇒ " +
			"★ 必须在【同一条 SELECT】里同时给出各腿与它们的占比")
	}
}

// TestSection14379SelfConsistencyEquationsFollowCodeOrder（★ 判据⑧【本轮新增】）
//
// ★★★ 本轮的另一处自踩：**自洽等式按「直觉」写，而不是按【代码里的分支顺序】写**。
//
//	v1 写的等式：`腿①calls - 腿②calls == 腿①b 的 rows`
//	⇒ ⇒ 生产实测 **差 4,426**（不成立）⇒ ⇒ 脚本自己报出一个假红
//
//	★ 正确的等式（按 `loadFromDB` 的分支顺序推）：
//	  腿① 未命中 ⇒ 走腿①b ⇒ 腿①b 未命中 ⇒ 走腿② ⇒ 腿② 落空 ⇒ 走 L3
//	⇒ 所以：**腿①b 的未命中数 == 腿② 的调用数**
//	⇒ 生产实测：26,316 == 26,316 ✔ **完全相等（差 0）**
//	⇒ 且：**腿② 的调用数 == L3 的调用数**（26,316 == 26,299 ✔ 差 17，在采样窗口内）
//
// ★ 这道门钉的是：**等式必须与代码分支顺序一致**，且**必须给出对照等式**
// （★ 只给一条等式 ⇒ 它可能整体漂移而无人察觉）。
func TestSection14379SelfConsistencyEquationsFollowCodeOrder(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot14379(t), section14379AuditPath))
	if err != nil {
		t.Fatalf("★ 复算脚本缺失：%s（%v）", section14379AuditPath, err)
	}
	code := stripSQLComments14379(string(raw))

	// ★ 判据①：必须有「腿①b 未命中 == 腿② 调用数」这条实测成立的等式
	if !strings.Contains(code, "l1b_miss_eq_l2_calls") {
		t.Error("★ 复算脚本缺自洽等式 `l1b_miss_eq_l2_calls` —— " +
			"★ 它是本轮实测【成立】的那条（26,316 == 26,316）⇒ ⇒ " +
			"★ v1 写的那条（l1 - l2 == l1b.rows）实测差 4,426 ⇒ ⇒ 不成立")
	}
	// ★ 判据②：必须有第二条对照等式（★ 腿②calls == L3calls）
	if !strings.Contains(code, "l2_calls_eq_l3_calls") {
		t.Error("★ 复算脚本缺对照等式 `l2_calls_eq_l3_calls` —— " +
			"★ 只给一条等式 ⇒ ⇒ 它整体漂移时无人察觉 ⇒ ⇒ 必须有第二条独立等式")
	}
	// ★ 判据③：★ v1 那条【错的】等式不许再出现（★ 出现就说明又被改回去了）
	if strings.Contains(code, "l1_minus_l2_eq_l1b_rows") {
		t.Error("★ 复算脚本里又出现了 `l1_minus_l2_eq_l1b_rows` —— " +
			"★ 本轮实测它【不成立】（差 4,426）⇒ ⇒ 它是按直觉写的，不是按代码分支顺序写的")
	}
	// ★ 判据④：命中率口径必须用【腿①b 未命中数】而不是【腿① − 腿②】
	if !strings.Contains(code, "pct_three_legs_all_miss") {
		t.Error("★ 复算脚本缺 `pct_three_legs_all_miss` —— " +
			"★ 本轮实测：v2 主路径命中率 = 腿①b 未命中数 / 腿① 调用数 = 27.41%")
	}
}

// stripSQLComments14379 去掉 SQL 里的行注释（`--` 到行尾）与块注释（/* */）。
// ★ 用途：让「代码形态」类判据不被【注释里的引用】误伤（★ §143.73 纪律）。
func stripSQLComments14379(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	out := b.String()
	for {
		i := strings.Index(out, "/*")
		if i < 0 {
			break
		}
		j := strings.Index(out[i:], "*/")
		if j < 0 {
			out = out[:i]
			break
		}
		out = out[:i] + out[i+j+2:]
	}
	return out
}

// TestSection14379FixmeBlockPresent（★ 判据⑥）
func TestSection14379FixmeBlockPresent(t *testing.T) {
	src := readRepoFile14379(t, "cmd/gateway/main_v3_wiring.go")
	if !strings.Contains(src, "FIXME-14379（") {
		t.Error("★ cmd/gateway/main_v3_wiring.go 里没有 FIXME-14379（ 登记块 —— " +
			"本门全部判据的依据就写在那里，它不能被无声删掉")
	}
}
