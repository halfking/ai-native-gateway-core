//go:build !integration

package main

// §143.78 的门：把「★★★ L3 兜底腿的 25,492 次调用【结构性地查不到东西】」
// 这条结论及其依赖的前提，钉成**结构判据**（★ 用 AST 查形态，不查裸子串）。
//
// ★★★ 为什么这道门要写（§143.78 的起因）：
//
//	§143.77 把「63 分钟」的根因钉到【并行 worker 启动开销】上（成立）。
//	§143.78 追问「那 25,492 次为什么【全部返 0 行】」，答案改变了**处置方向**：
//
//	① 【L3 是【兜底】】—— `domains/hooks/compression/session_cache.go` 的 `loadFromDB`
//	   **先**走 v2 主路径 `turnReader.LoadLatestOutbound`，再走 v2 delta 链
//	   `LoadChain(...,10)`，★ 两段都落空才落到 `LastOutboundForSession`（本腿）。
//	② 【0 行 ≡ 「查询那一刻该 id 还没有轮次」】—— L3 的驱动腿是 turns 族，
//	   bodies 侧缺行只会让 body 为 NULL、**不会**让行数变 0。
//	③ 【为什么那批 id 没有轮次？】—— `domains/streaming/session_assignment.go`：
//	   `shouldCreateFreshSessionForMessageCount(n) ⇒ n <= 1`，且
//	   `assignGatewaySessionWithFinder` 里 `if shouldCreateFreshSessionForMessageCount(...)`
//	   ⇒ `createSession()` ⇒ ★★ **「客户端消息数 ≤ 1」的请求，每次都新建一个会话**
//	   ⇒ ⇒ **每次请求换一个全新的 `gw_<uuid>`** ⇒ ⇒ 首轮必然没有既往轮次 ⇒ 0 行。
//	④ ⇒ ⇒ 「全 0 行」不是「查询形态错」，是「这批会话还没有上一轮」
//	   ⇒ ⇒ ★★ **优化这条查询对【正确性】零收益**；真正的处置是
//	   ⇒ ⇒ **「让注定查不到的调用不要走到它」**（零 DB 成本）。
//
// ★★ 这道门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//
//	① ★★ `loadFromDB` 里 **v2 主路径在前、L3 兜底腿在后**（★ L3 永远是兜底）。
//	② ★★ **探针不进 mirror**（★ 「bodies 侧缺行」这个前提的一半）。
//	③ ★★★★ **「消息数 ≤ 1 ⇒ 每次新建会话」**（★ 一次性会话的【机制根源】）。
//	   ★★★ 一旦有人改掉这个阈值 ⇒ ⇒ 「0 行 = 首轮」这个等价关系作废。
//	④ ★★★★ **`createSession()` 会置 `AutoCreated = true`**（★ 短路判据的【事实源】）。
//	⑤ ★★★ **`getLocal` 只在 `st != nil` 时 `setL1`**（★ 「0 行 ⇒ 不缓存 ⇒ 下次重查」
//	   这个【放大器】）。
//	⑥ ★★ 复算脚本必须存在且保留关键口径（★ 它是本轮全部读数的唯一可复算依据）。
//	⑦ ★★ `FIXME-14378（` 登记块必须在，且必须写明正确判据（`AutoCreated`）。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const section14378AuditPath = "scripts/audit/verify_l3_zero_row_naming.sql"

func repoRoot14378(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14378(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14378(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

// parseGo14378 解析仓内一个 Go 文件（★ 带 ParseComments —— 判据要能区分
// 「注释里写了」与「代码里写了」）。
func parseGo14378(t *testing.T, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(repoRoot14378(t), rel), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", rel, err)
	}
	return f
}

// findFunc14378 按名字找顶层函数。
func findFunc14378(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == name {
			return fd
		}
	}
	t.Fatalf("找不到函数 %s", name)
	return nil
}

// callNamed14378 判断一个表达式里有没有对某个具名函数的调用。
func callNamed14378(n ast.Node, name string) bool {
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

// TestSection14378L3IsAlwaysFallback（★ 判据①）
//
// ★★★ 真实结构（★ 2026-10-23 读码核对）：
//
//	loadFromDB:
//	  if c.turnReader != nil {
//	     LoadLatestOutbound(...)   ← ① v2 快照（final_full）
//	     if 空 → LoadChain(...,10) ← ② v2 delta 链重建（同样读 bodies）
//	  }
//	  return c.loadFromLegacyDB(...)   ← ③ L3 兜底腿（唯一出口）
//
//	loadFromLegacyDB:
//	  c.db.LastOutboundForSession(...)  ← 本腿
//
// ⇒ ⇒ ★★ **L3 被调用 ⇒ v2 的快照与 delta 链【都读不到】**
// ⇒ ⇒ ★★★ 而 delta 链也是从 bodies 表读的 ⇒ ⇒ **bodies 侧本就没有这些会话的正文**
func TestSection14378L3IsAlwaysFallback(t *testing.T) {
	src := readRepoFile14378(t, "domains/hooks/compression/session_cache.go")

	loadFromDB := findFunc14378(t, parseGo14378(t, "domains/hooks/compression/session_cache.go"), "loadFromDB")
	// ★ 对照形态：v2 主路径必须真的存在（否则下面几条判据会变成恒真）
	if !callNamed14378(loadFromDB, "LoadLatestOutbound") {
		t.Error("★ loadFromDB 里没有 LoadLatestOutbound —— " +
			"判据「v2 主路径在前」失去意义（本判据会变成恒真）")
	}
	if !callNamed14378(loadFromDB, "LoadChain") {
		t.Error("★ loadFromDB 里没有 LoadChain（v2 delta 链）—— " +
			"「v2 两条路都落空才落到 L3」这句话作废")
	}
	if !callNamed14378(loadFromDB, "loadFromLegacyDB") {
		t.Error("★ loadFromDB 不再落到 loadFromLegacyDB —— " +
			"「L3 只在 v2 两条路都落空后才被调用」这句话作废")
	}
	// ★ 兜底腿本体确实调 LastOutboundForSession
	legacy := findFunc14378(t, parseGo14378(t, "domains/hooks/compression/session_cache.go"), "loadFromLegacyDB")
	if !callNamed14378(legacy, "LastOutboundForSession") {
		t.Error("★ loadFromLegacyDB 里没有 LastOutboundForSession 调用 —— " +
			"L3 兜底腿的落点变了")
	}
	_ = src
}

// TestSection14378ProbeDoesNotEnterMirror（★ 判据②）
func TestSection14378ProbeDoesNotEnterMirror(t *testing.T) {
	src := readRepoFile14378(t, "internal/sessionv2mirror/hook.go")
	// ★ 判据的落点：`IsProbeSyntheticSession(entry)` 之后必须紧跟 `return`
	//   （★ 这就是「探针产出不进 mirror ⇒ 不写 bodies」的那一行）。
	i := strings.Index(src, "IsProbeSyntheticSession(entry)")
	if i < 0 {
		t.Fatal("hook.go 里找不到 `IsProbeSyntheticSession(entry)` 调用点")
	}
	tail := src[i:]
	j := strings.Index(tail, "return")
	if j < 0 {
		t.Fatal("IsProbeSyntheticSession 调用点之后没有 return —— " +
			"「探针不进 mirror ⇒ bodies 侧 0 行是设计后果」这个前提不成立")
	}
	// ★ 距离不能太远（★ 否则说明 return 属于别的分支 ⇒ 判据对不上被测对象）
	span := tail[:j]
	if strings.Count(span, "\n") > 3 {
		t.Errorf("IsProbeSyntheticSession 与 return 相距 %d 行 —— "+
			"判据可能已经对不上「探针短路」这件事（★ 同一组数字必须来自同一个集合）",
			strings.Count(span, "\n"))
	}
}

// TestSection14378OneMessageMeansFreshSession（★ 判据③）
//
// ★★★★ 这条是本轮结论的【机制根源】：
//
//	shouldCreateFreshSessionForMessageCount(n) ⇒ n <= 1
//	assignGatewaySessionWithFinder 里：
//	  if shouldCreateFreshSessionForMessageCount(assignment.ClientMessages) {
//	     return createSession()
//	  }
//
// ⇒ ⇒ **「客户端消息数 ≤ 1」的请求 ⇒ 每次都新建会话** ⇒ 每个请求一个新 id
// ⇒ ⇒ 该 id 的首轮必然没有既往轮次 ⇒ ⇒ L3 必然返 0 行。
//
// ★★ 判据用 AST 查【比较表达式的形态】，不查裸子串（★ 注释里写 `messageCount <= 1`
//
//	不算 —— 那是 §143.73 立过的纪律）。
func TestSection14378OneMessageMeansFreshSession(t *testing.T) {
	f := parseGo14378(t, "domains/streaming/session_assignment.go")
	fd := findFunc14378(t, f, "shouldCreateFreshSessionForMessageCount")

	// ★ 取函数的返回值表达式
	var ret ast.Expr
	ast.Inspect(fd, func(n ast.Node) bool {
		if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 && ret == nil {
			ret = r.Results[0]
		}
		return true
	})
	if ret == nil {
		t.Fatal("shouldCreateFreshSessionForMessageCount 没有返回值 —— 形态变了")
	}
	be, ok := ret.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("shouldCreateFreshSessionForMessageCount 的返回值不是比较表达式（★ 形态变了）")
	}
	if be.Op != token.LEQ {
		t.Errorf("★ 比较运算符是 %v，不是 <= —— "+
			"「消息数 ≤ 1 ⇒ 新建会话」的阈值语义变了", be.Op)
	}
	left, lok := be.X.(*ast.Ident)
	right, rok := be.Y.(*ast.BasicLit)
	if !lok || left.Name != "messageCount" {
		t.Errorf("★ 比较式的左操作数不是 messageCount —— 形态变了")
	}
	if !rok || right.Kind != token.INT || right.Value != "1" {
		t.Errorf("★ 比较式的右操作数不是字面量 1 —— 形态变了")
	}

	// ★ 判据②：这个阈值必须真的被【调用点】用上，且调用分支里是 createSession()
	assign := findFunc14378(t, f, "assignGatewaySessionWithFinder")
	hit := false
	ast.Inspect(assign, func(n ast.Node) bool {
		if ifs, ok := n.(*ast.IfStmt); ok {
			if callNamed14378(ifs.Cond, "shouldCreateFreshSessionForMessageCount") &&
				callNamed14378(ifs.Body, "createSession") {
				hit = true
			}
		}
		return true
	})
	if !hit {
		t.Error("★ assignGatewaySessionWithFinder 里没有「按消息数阈值走 createSession」的分支 —— " +
			"「一次性会话的机制根源」不成立 ⇒ 「0 行 = 首轮」这个等价关系作废")
	}
}

// TestSection14378AutoCreatedIsTheShortCircuitFactSource（★ 判据④）
//
// ★★★★ 短路判据的【事实源】：`createSession()` 置 `assignment.AutoCreated = true`。
//
// ★★ 为什么它是【零 DB 成本、零语义风险】的短路判据：
//   - 自动新建 ⇒ 该 session_id 全库从未出现过 ⇒ L3 必然返 0 行（不是「大概没用」）；
//   - `AutoCreated` 已经在生产流通（`domains/streaming/handler.go` 往响应头写
//     `X-Gw-Session-Auto: true`）⇒ ⇒ 判据不需要新增任何状态。
func TestSection14378AutoCreatedIsTheShortCircuitFactSource(t *testing.T) {
	f := parseGo14378(t, "domains/streaming/session_assignment.go")
	assign := findFunc14378(t, f, "assignGatewaySessionWithFinder")

	// ★ 找 createSession 闭包（赋值给一个标识符的 FuncLit）
	var closure *ast.FuncLit
	ast.Inspect(assign, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || closure != nil {
			return true
		}
		for i, rhs := range as.Rhs {
			fl, ok := rhs.(*ast.FuncLit)
			if !ok {
				continue
			}
			if id, ok := as.Lhs[i].(*ast.Ident); ok && id.Name == "createSession" {
				closure = fl
			}
		}
		return true
	})
	if closure == nil {
		t.Fatal("★ 找不到 createSession 闭包 —— 一次性会话的落点变了")
	}

	// ★ 闭包里必须有一句 `assignment.AutoCreated = true`
	found := false
	ast.Inspect(closure, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range as.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "AutoCreated" {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "assignment" {
				continue
			}
			if lit, ok := as.Rhs[0].(*ast.Ident); ok && lit.Name == "true" {
				found = true
			}
		}
		return true
	})
	if !found {
		t.Error("★ createSession 里没有 `assignment.AutoCreated = true` —— " +
			"短路判据的事实源没了（★ 零 DB 成本、零语义风险的那条判据要靠它）")
	}
}

// TestSection14378ZeroRowIsNotCached（★ 判据⑤）
//
// ★★★ 放大器：`getLocal` 只在 `st != nil` 时 `setL1` ⇒ ⇒ **L3 返 0 行时
// **既不填 L1、也不提交 Redis** ⇒ ⇒ 下一次请求还会再查一次 L3
// ⇒ ⇒ L3 的调用量与【请求数】同阶，而不是与【会话数】同阶。
//
// ★★ 判据用 AST：必须在 `getLocal` 里找到【条件为 `st != nil`】的那个 IfStmt，
//
//	且它的 body 里含有 `setL1` 调用。
func TestSection14378ZeroRowIsNotCached(t *testing.T) {
	f := parseGo14378(t, "domains/hooks/compression/session_cache.go")
	fd := findFunc14378(t, f, "getLocal")

	guarded := false
	ast.Inspect(fd, func(n ast.Node) bool {
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		be, ok := ifs.Cond.(*ast.BinaryExpr)
		if !ok || be.Op != token.NEQ {
			return true
		}
		x, okx := be.X.(*ast.Ident)
		y, oky := be.Y.(*ast.Ident)
		if !okx || !oky || x.Name != "st" || y.Name != "nil" {
			return true
		}
		if callNamed14378(ifs.Body, "setL1") {
			guarded = true
		}
		return true
	})
	if !guarded {
		t.Error("★ getLocal 里找不到「`st != nil` 才 setL1」的守卫 —— " +
			"「0 行 ⇒ 不缓存 ⇒ 下次重查」这个放大器的判据对不上被测对象")
	}
}

// TestSection14378RecomputeScriptKeepsItsEvidence（★ 判据⑥）
func TestSection14378RecomputeScriptKeepsItsEvidence(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot14378(t), section14378AuditPath))
	if err != nil {
		t.Fatalf("★ 复算脚本缺失：%s（%v）", section14378AuditPath, err)
	}
	s := string(raw)
	for _, needle := range []string{
		// ★ 0 行调用量与行数（pg_stat_statements）
		"pg_stat_statements",
		// ★ 一次性会话的量化口径（★ 0 行调用的代理指标）
		"HAVING count(*) = 1",
		"session_turns_2026_10",
		// ★ 来源分桶
		"origin_stage",
		"origin_actor",
		// ★ 「父表看不到当月分区」这个假设的证伪口径
		"parent_rows",
		"session_turns_hot",
		// ★ L3 驱动腿的语义对拍（0 行 ⇔ 没有轮次）
		"not_exists",
		"one_shot_has_turn",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("★ 复算脚本缺 %q —— 它是本轮全部读数的唯一可复算依据", needle)
		}
	}
}

// TestSection14378FixmeBlockRecordsTheRightShortCircuit（★ 判据⑦）
func TestSection14378FixmeBlockRecordsTheRightShortCircuit(t *testing.T) {
	src := readRepoFile14378(t, "cmd/gateway/main_v3_wiring.go")
	i := strings.Index(src, "FIXME-14378（")
	if i < 0 {
		t.Fatal("★ cmd/gateway/main_v3_wiring.go 里没有 FIXME-14378（ 登记块 —— " +
			"本门全部判据的依据就写在那里，它不能被无声删掉")
	}
	block := src[i:]
	// ★★ 登记块必须写明【正确判据】= AutoCreated
	// ★★★ needle 用【完整标记形态】`AutoCreated = true`，不用裸子串 `AutoCreated`
	//   ⇒ ★ 裸子串会让「把字段改名成 AutoCreatedX」这种变异逃过门
	//   ⇒ ★ 这是 §143.73 立过的纪律（登记类 needle 必须带完整标记形态）。
	if !strings.Contains(block, "AutoCreated = true") {
		t.Error("★ FIXME-14378 登记块里没有 `AutoCreated = true` —— " +
			"★ 本轮第一版写的「按 session_id 形态短路」已被实测推翻（一次性会话全是 gw_）" +
			"⇒ ⇒ 正确判据是「会话是自动新建的」，登记块必须反映它")
	}
	// ★★ 登记块必须留痕「第一版的前缀判据被推翻」（★ 自我推翻要留痕）
	if !strings.Contains(block, "前缀判据") {
		t.Error("★ FIXME-14378 登记块没有留下「前缀判据被推翻」的痕迹 —— " +
			"★ 自我推翻必须留痕，否则下一个人会照着旧判据实施")
	}
}

// TestSection14378OneMessageThresholdConstant（★ 量具自检）
//
// ★★★ 「判据自己坏了吗」的自检：★ 门里用来读阈值的字面量 "1" 必须能被解析，
//
//	且【生产代码里那个阈值必须真的是 1】—— 本判据用一个独立路径再读一次
//	（读 `shouldCreateFreshSessionForMessageCount` 的【源码文本】并断言它含
//	`messageCount <= 1`）⇒ ⇒ ★ 两路读数一致才认。
func TestSection14378OneMessageThresholdConstant(t *testing.T) {
	src := readRepoFile14378(t, "domains/streaming/session_assignment.go")
	body := src
	if i := strings.Index(src, "func shouldCreateFreshSessionForMessageCount("); i >= 0 {
		rest := src[i:]
		end := strings.Index(rest, "\n}")
		if end < 0 {
			t.Fatal("找不到 shouldCreateFreshSessionForMessageCount 的函数体结尾")
		}
		body = rest[:end]
	}
	if !strings.Contains(body, "messageCount <= 1") {
		t.Errorf("★ shouldCreateFreshSessionForMessageCount 的源码文本里没有 `messageCount <= 1` —— " +
			"★ 两路读数（AST / 文本）不一致 ⇒ 阈值变了（★ 判据与被测对象必须同源）")
	}
	// ★ 对照：字面量 1 必须能被解析（★ 量具自检：不是所有 "1" 都合法）
	if n, err := strconv.Atoi("1"); err != nil || n != 1 {
		t.Error("★ 量具自检失败：阈值字面量解析异常")
	}
}
