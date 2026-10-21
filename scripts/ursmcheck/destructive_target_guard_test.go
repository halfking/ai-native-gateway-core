package ursm_test

// R87 轮的门：**「事实源承载的 DSN 名 × 硬破坏性 SQL × 无目标库安全守卫」**。
//
// # 这道门补的是哪块缺口
//
// R86 轮的门（`datasource_env_target_guard_test.go`）问的是
// 「事实源给的这个值，这条腿到底会不会用」⇒ 判据 = **按库名做决定**。
// ⇒⇒ 于是有**一整类腿落在它的口径之外**：
//
//	**破坏性腿**：读事实源导出的 DSN 名 ⇒ 直接 TRUNCATE / DROP 产品表，
//	而且**从不做任何目标库判定** ⇒⇒ 事实源把产品库给它 ⇒ 它就把产品库清空。
//
// R87 轮量到这类腿 **3 条**（全部读 `TEST_AUDIT_ISOLATED_DB_URL`），
// 而那个名字的含义是「**隔离**审计库」（`scripts/audit/verify-outbox-reaper.sh`
// 给它的是隔离容器 `127.0.0.1:15433`）⇒⇒ **事实源却把它指向产品库**
// ⇒⇒ 名字与值互相矛盾 ⇒ 没有任何门会问「这条腿要打的库对不对」。
//
// # 判据（三条，缺一不可）
//
//	① 该腿**真读**一个事实源导出的 DSN 名（os.Getenv/os.LookupEnv 实参，含 const 追链）；
//	② 该腿含**硬破坏性 SQL**（TRUNCATE / DROP TABLE / DROP SCHEMA / DROP DATABASE），
//	   且目标**不是**本文件自己建的（CREATE SCHEMA / CREATE TABLE / CREATE DATABASE 出来的）；
//	③ 该腿在**第一条破坏性语句之前**没有任何**目标库判定**证据。
//
// # 判据 ③ 的四种证据（机器可判，全部 AST 口径）
//
//	(a) 字符串字面量里出现 `current_database(` —— 读形态库名；
//	(b) AST 读 `.Database` 字段（**不是**赋值左值 ⇒ 赋值是「改指」，不是判定）；
//	(c) 对 DSN 目标做比较（strings.Contains/HasPrefix/HasSuffix/EqualFold
//	    且其中一个实参是 host/port 标记字面量）；
//	(d) 调用了一个**含上述判定的函数**（同包内的 helper，或 import 进来的包）。
//
// ★ (d) 是必需的：仓内的守卫形态既有「同包 helper」（R86 的 `guardScratchDBName`）
//   也有「独立包」（R87 新增的 `internal/testguard`）⇒ 只认同包 ⇒ 跨包守卫会被误判成「无守卫」。
//   ⇒ ⇒ 本门**不靠函数名猜测**，而是去**被调用的那个包里**找判定证据（同一个仓内）。
//
// ★★ **判据 ② 的「实参口径」**：破坏性 SQL 必须在**调用实参**里
//   （`reset(t, pool, "TRUNCATE public.x")` / `pool.Exec(ctx, "TRUNCATE …")`）。
//   ⇒ 切片字面量里的禁用串（`[]string{"TRUNCATE …"}` + `contains(text, forbidden)`）
//   **不是实参** ⇒ 天然排除 ⇒ 不会把「断言某 SQL 不许出现」误判成「执行了破坏性 SQL」。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	r87LedgerPath = "destructive_target_ledger.tsv"
)

// r87ReDestructiveSQL 认「硬破坏性 SQL」的关键字。
var r87ReDestructiveSQL = regexp.MustCompile(`(?i)\bTRUNCATE\b|\bDROP\s+TABLE\b|\bDROP\s+SCHEMA\b|\bDROP\s+DATABASE\b`)

// r87ReCreateObject 认「本文件自己建的对象」。
var r87ReCreateObject = regexp.MustCompile(`(?i)\bCREATE\s+(SCHEMA|TABLE|DATABASE)\s+(?:IF\s+NOT\s+EXISTS\s+|IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)*)`)

// r87ReCreateKind 认「本文件建过某类对象」（**不要求名字**）。
//
// ★ 与 r87ReCreateObject 的区别就是「名字要不要能解析出来」：
//   `"CREATE DATABASE " + name`（name 是运行时变量）⇒ 名字解析不出，
//   但「建过库」这件事是确定的 ⇒ ⇒ 判据 ③(R1) 用这一条。
var r87ReCreateKind = regexp.MustCompile(`(?i)\bCREATE\s+(SCHEMA|TABLE|DATABASE)\b`)

// r87ReHostMarker 认「DSN 目标标记」字面量（host / port / 库名标记）。
//
// ★ 库名标记这一组是为 `bg/ursm_830_realdb_test.go` 那类形态准备的：
//   它读 DSN 的库名（`dsnDatabaseName(dsn)`）并要求「库名含 test」⇒ 那是**读形态守卫**。
var r87ReHostMarker = regexp.MustCompile(`(?i)^(?:127\.0\.0\.1|localhost|0\.0\.0\.0|\[::1\]|:[0-9]{2,5}|[0-9]{4,5}|test|scratch|tmp|bench|e2e|itgate)$`)

// r87ReIdent 认一个（可带 schema 前缀的）对象名。
var r87ReIdent = regexp.MustCompile(`^"?([A-Za-z_][A-Za-z0-9_$]*(?:\.[A-Za-z_][A-Za-z0-9_$]*)*)"?`)

// r87Pair 一条「事实源承载的 DSN 名 × 破坏性腿」的候选对。
type r87Pair struct {
	Name string
	File string
	Stmt string
	// Root 是**腿根**：能到达这条破坏性语句的测试入口函数名（R88 轮新增）。
	//
	// ★ 为什么要有它：R88 轮把判定单位从「文件」降到「腿」⇒⇒ 报错必须点名
	//   **是哪条腿**（哪个测试入口）没守卫，否则同一个文件里多条腿时无从下手。
	Root string
}

func (p r87Pair) key() string { return p.Name + "\t" + p.File }

// r87PkgFacts 一个包（目录）的目标库判定能力。
type r87PkgFacts struct {
	// funcsWithCheck：本包里**体内**含目标库判定的函数名。
	funcsWithCheck map[string]bool
	// hasCheck：本包里是否存在任何目标库判定（跨包识别的兜底）。
	hasCheck bool
	// declaredGuards：本包**自己声明**的闸门函数名单（`GuardFunctions` 变量）。
	//
	// ★ 为什么要有这一层（R87 轮实测的洞）：
	//   跨包识别若只问「这个包有没有做过目标库判定」，那么
	//   `internal/testguard.RequireDatabaseMatchesDSN`（它**只**核对连接落点）
	//   也会被当成守卫 ⇒ ⇒ 变异「摘掉真正的闸门 RequireIsolatedTarget、
	//   留下这个核对函数」时，门仍然绿 ⇒ ⇒ **门有洞**。
	//   ⇒ ⇒ 闸门名单由**守卫包自己声明**，本门按名单解析跨包调用。
	declaredGuards map[string]bool
	// pkgFuncs：本包里**每个函数**的「守卫状态 + 调用边」（R89 轮新增）。
	//
	// ★ 为什么要有这一层（R89 轮补 R88 轮留下的边界）：
	//
	//	R88 轮的可达性是**同文件**的 ⇒⇒ 「守卫在同包的另一个文件里」判不出来
	//	⇒⇒ 方向是**假阳性**（把有守卫的腿判成没守卫）。
	//	⇒⇒ 本层把「这个包里每个函数的体内判定证据 / 跨包闸门调用 / 它调了谁」
	//	**按函数名**建索引 ⇒⇒ 跨文件同包的调用链就接上了。
	//
	// ★ 为什么用**函数名**做键：同一个包里函数名唯一 ⇒⇒ 名即实体；
	//   且当前文件的本地定义与包索引指向**同一个函数** ⇒⇒ 本地优先、包索引兜底。
	pkgFuncs map[string]r89PkgFunc
	// pkgScopeDSN：**包作用域**（顶层 var/const 的初始化表达式）读到的
	// **事实源导出名**（R90 轮新增）。
	//
	// ★ 为什么单列一层：包级 `var dsn = os.Getenv("TEST_DATABASE_URL")`
	//   **不在任何函数体内** ⇒⇒ 函数级扫描看不见它
	//   ⇒⇒ 而它流进的是**本包所有**测试 ⇒⇒ 必须整包算「消费」。
	pkgScopeDSN map[string]bool
	// pkgScopeOpensPG：包作用域是否打开 PG 连接（R90 轮新增）。
	pkgScopeOpensPG bool
	// pkgTestMainDSN：**`TestMain` 里**读到的事实源导出名（R90 轮新增）。
	//
	// ★ 为什么单列 `TestMain`：Go 的 `TestMain` 在**本包所有测试之前**运行
	//   ⇒⇒ 它读到的 DSN 会流进本包**每一条**腿
	//   ⇒⇒ 而 `TestMain` 既不是 `Test` 前缀（不是腿根），也常常不在别的腿的可达集里
	//   ⇒⇒ 不单列就会漏掉「TestMain 建池 + 各测试各清各的表」这一整个形态。
	pkgTestMainDSN map[string]bool
	// pkgTestMainOpensPG：`TestMain` 是否打开 PG 连接（R90 轮新增）。
	pkgTestMainOpensPG bool
}

// r89PkgFunc 一个包内某个函数的守卫状态与调用边。
type r89PkgFunc struct {
	// guarded：函数体内含目标库判定证据。
	guarded bool
	// callsGuard：函数调用了跨包闸门（按包自己声明的 `GuardFunctions` 名单）。
	callsGuard bool
	// calls：这个函数调用到的**同包**函数名。
	calls []string
	// isTest：是不是**测试入口**（`Test` 前缀）。
	//
	// ★ R89 轮新增：腿根（测试入口）**可能住在另一个文件**里
	//   （Go 的测试惯例：共享 helper 放兄弟 `_test.go`，测试函数在另一个文件）
	//   ⇒⇒ 腿根发现必须**按包**搜，不能只搜当前文件。
	isTest bool
	// dsnNames：这个函数体内读到的**事实源导出 DSN 名**（R90 轮新增）。
	//
	// ★ 为什么要有这一层：R89 轮登记的「候选集盲区」是——
	//   「破坏性 SQL 在 A 文件、读 DSN 在 B 文件」的腿**进不了候选集**
	//   ⇒⇒ 门连看都没看见它 ⇒⇒ 106 个破坏性文件在视野之外。
	//   ⇒⇒ 本层把「这个函数读了哪些事实源导出的 DSN 名」**按函数名**建索引
	//   ⇒⇒ 配合已有的包级可达图 ⇒⇒ 「腿能不能流到事实源的值」就判得出来了。
	dsnNames []string
	// opensPG：这个函数体内是否打开 PG 连接（R90 轮新增）。
	//
	// ★ 为什么要单列：读了 DSN 名 ≠ 那个值进了连接。
	//   （例：`dsn := os.Getenv("TEST_DATABASE_URL")` 之后拿它去连 SQLite）
	//   ⇒⇒ 必须要求「腿的可达范围里确实有 PG 连接被打开」。
	opensPG bool
	// cross：这个函数**跨包**调用到的目标（R91 轮新增）。
	//
	// ★ 为什么要有这一层：R87~R90 轮的可达性是**同包**的
	//   ⇒⇒ 「事实源的 DSN 在另一个包里被读取并连上」的形态**判不出来**
	//   ⇒⇒ 方向是**漏放**（数据事故级）。
	//   ⇒⇒ 本层把跨包调用边也记下来 ⇒⇒ 全局调用图才建得起来。
	cross []r91CrossRef
}

// r91CrossRef 一个**跨包**调用的目标（R91 轮）。
type r91CrossRef struct {
	// Dir 是被调函数所在包的**绝对目录**（仓内包才非空）。
	Dir string
	// Name 是被调函数名。
	Name string
}

// r87GuardHit 一次守卫证据。
type r87GuardHit struct {
	pos  int
	kind string
}

// ── 目标库判定的四种证据（纯 AST 口径）────────────────────────────────────

// r87AssignedNodes 标出所有赋值左值（它们是「改指」，不是「读」）。
func r87AssignedNodes(f *ast.File) map[ast.Node]bool {
	assigned := map[ast.Node]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || as.Tok != token.ASSIGN {
			return true
		}
		for _, lhs := range as.Lhs {
			assigned[lhs] = true
		}
		return true
	})
	return assigned
}

// r87IsCompareFunc 认「字符串比较」类函数（用于判据 ③(c)）。
func r87IsCompareFunc(name string) bool {
	switch name {
	case "Contains", "HasPrefix", "HasSuffix", "EqualFold":
		return true
	}
	return false
}

// r87IsSkipFunc 认「跳过」类调用（用于判据 ③(e)）。
func r87IsSkipFunc(name string) bool {
	return strings.HasPrefix(name, "Skip")
}

// r87CollectHits 收集一段 AST 里的目标库判定证据。
//
// ★ assigned 由**整份文件**算出来 ⇒ 跨函数也正确
//   （一个文件里 `cfg.Database = x` 的赋值不会把别处的读判成「改指」）。
func r87CollectHits(fset *token.FileSet, root ast.Node, assigned map[ast.Node]bool) []r87GuardHit {
	var hits []r87GuardHit
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			// (a) current_database() 字面量。
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil && strings.Contains(v, "current_database(") {
					hits = append(hits, r87GuardHit{pos: fset.Position(x.Pos()).Offset, kind: "current_database()"})
				}
			}
		case *ast.SelectorExpr:
			// (b) 读 .Database（**不是**赋值左值 ⇒ 赋值是「改指」，不是判定）。
			if x.Sel.Name == "Database" && !assigned[n] {
				hits = append(hits, r87GuardHit{pos: fset.Position(x.Pos()).Offset, kind: "读 .Database"})
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// (e) **先探后建**：跳过消息里明说「目标已存在」。
			//     形态：`t.Skipf("%d of the fixture tables already exist — this test drops them", n)`
			//     ⇒ 目标已存在 ⇒ 这条腿不跑 ⇒ 破坏性 SQL 不会落到有数据的库上。
			//
			//     ★★ 必须放在 (c) 的早退**之前**：早退条件是「不是比较函数」，
			//     而 Skipf 恰好不是比较函数 ⇒ 放在后面这段代码永远不执行
			//     ⇒⇒ 「规则写了但从未生效」= 恒绿门的一种。
			if r87IsSkipFunc(sel.Sel.Name) {
				for _, a := range x.Args {
					v, ok := r87ResolveString(a, nil)
					if !ok {
						continue
					}
					if strings.Contains(strings.ToLower(v), "already exist") || strings.Contains(v, "已存在") {
						hits = append(hits, r87GuardHit{pos: fset.Position(x.Pos()).Offset, kind: "先探后建（已存在即跳过）"})
					}
				}
			}
			// (c) 对 DSN 目标做比较。
			if !r87IsCompareFunc(sel.Sel.Name) {
				return true
			}
			for _, a := range x.Args {
				bl, ok := a.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(bl.Value)
				if err == nil && r87ReHostMarker.MatchString(v) {
					hits = append(hits, r87GuardHit{pos: fset.Position(x.Pos()).Offset, kind: "DSN 目标比较 " + sel.Sel.Name})
				}
			}
		}
		return true
	})
	return hits
}

// ── 破坏性语句定位 ─────────────────────────────────────────────────────────

// r87ConstStrings 收集本文件里的字符串常量（供 ident 回查）。
//
// ★★ **必须覆盖函数内的局部 const/var**，不只包级声明。
//   实例：db/request_status_projection_realdb_test.go
//     `const schema = "rs160"`（**函数内**）
//     `pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)`
//   ⇒ 只扫包级声明 ⇒ 变量名解析不出来 ⇒ SQL 变成
//     "DROP SCHEMA IF EXISTS  CASCADE"（中间空了）⇒ 目标名丢失
//   ⇒ ⇒ 自建集合与破坏性目标**都对不上** ⇒ 把一条自建 schema 的腿误报成高危。
func r87ConstStrings(f *ast.File) map[string]string {
	out := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		gd, ok := n.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
			return true
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if v, ok := r87ResolveString(vs.Values[i], out); ok {
					out[ident.Name] = v
				}
			}
		}
		return true
	})
	return out
}

// r87ReDestructiveTail 匹配「破坏性关键字**后面还有至少一个字符**」的形态。
//
// ★ R90 轮加这一层的理由（实测逼出来的）：探测器原来只认「**出现过**关键字」
//   ⇒⇒ **非 SQL 的字符串**也会被当成破坏性语句：
//     · `Strategy: "truncate"`（压缩策略名）
//       —— domains/session/v2/cache_v2_integration_test.go
//     · `ValidateOrderByColumn("session_summaries", "DROP TABLE")`
//       —— admin/session_analytics_helpers_test.go
//     · `require.Contains(t, hint, "TRUNCATE")`
//       —— admin/data_lifecycle_hot_partition_reclaim_hint_test.go
//   ⇒⇒ 读数被灌水（「有活破坏性语句的文件 38 / 语句 62」里有若干条根本不是 SQL）。
//
// ★ 为什么这条判据**不会漏掉真腿**：
//   「恰好结束于关键字」的字面量**不可执行**（没有对象可删可截断）
//   ⇒⇒ 而真腿的写法**至少带一个字符**：
//     · `fmt.Sprintf("DROP TABLE IF EXISTS %s", pname)` ⇒ 解析出 "DROP TABLE IF EXISTS %s"
//     · `"DROP TABLE " + tbl` ⇒ 解析出 "DROP TABLE "（末尾是**空格**，不是空）
//   ⇒⇒ 判据是「关键字后面至少还有**一个字符**（空格也算）」，
//     **不是**「能不能解析出对象名」——后者会把运行时变量形态的真腿漏掉
//     （`"DROP DATABASE IF EXISTS " + name` 的对象名静态解析不出来）。
//
// ★ 方向说明：这一层只收紧**假阳性**（把不是 SQL 的串剔掉）
//   ⇒⇒ 它让读数变准，但**不会**让门变松 ⇒ 真腿的可见性不变。
var r87ReDestructiveTail = regexp.MustCompile(`(?is)\b(TRUNCATE|DROP\s+TABLE|DROP\s+SCHEMA|DROP\s+DATABASE).`)

// r87DestructiveOf 判定一个 SQL 文本是不是硬破坏性，并取出被破坏的对象。
func r87DestructiveOf(sql string) (bool, []string) {
	if !r87ReDestructiveSQL.MatchString(sql) {
		return false, nil
	}
	// ★ R90 轮：关键字**恰好结束于字面量末尾** ⇒ 不是可执行 SQL ⇒ 不算破坏性语句。
	if !r87ReDestructiveTail.MatchString(sql) {
		return false, nil
	}
	var targets []string
	up := strings.ToUpper(sql)

	// TRUNCATE [TABLE] a, b
	if i := strings.Index(up, "TRUNCATE"); i >= 0 {
		rest := sql[i+len("TRUNCATE"):]
		if j := strings.Index(rest, ";"); j >= 0 {
			rest = rest[:j]
		}
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(strings.ToUpper(rest), "TABLE ") {
			rest = strings.TrimSpace(rest[len("TABLE"):])
		}
		for _, part := range strings.Split(rest, ",") {
			if t := r87FirstIdent(part); t != "" {
				targets = append(targets, t)
			}
		}
	}
	// DROP TABLE / DROP SCHEMA / DROP DATABASE
	for _, kw := range []string{"DROP TABLE", "DROP SCHEMA", "DROP DATABASE"} {
		upKW := strings.ToUpper(kw)
		idx := 0
		for {
			i := strings.Index(up[idx:], upKW)
			if i < 0 {
				break
			}
			pos := idx + i
			rest := sql[pos+len(kw):]
			if j := strings.Index(rest, ";"); j >= 0 {
				rest = rest[:j]
			}
			if t := r87FirstIdent(rest); t != "" {
				targets = append(targets, t)
			}
			idx = pos + len(kw)
		}
	}
	return true, targets
}

// r87FirstIdent 取一段文本里的第一个对象名。
func r87FirstIdent(s string) string {
	s = strings.TrimSpace(s)
	up := strings.ToUpper(s)
	if strings.HasPrefix(up, "IF EXISTS") {
		s = strings.TrimSpace(s[len("IF EXISTS"):])
	}
	m := r87ReIdent.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1])
}


// r87SelfCreatePositions 返回「本文件自己建的每个对象」的**最早 CREATE 位置**。
//
// ★★ R92 轮新增：R87 轮的「自建对象 ⇒ 删它安全」排除**只问「文件建不建」**，
//   **不问「建在前还是落在后」** ⇒⇒ 「先 DROP 后 CREATE」的形态被误判成安全
//   ⇒⇒ 漏放方向（数据事故级）。
func r87SelfCreatePositions(f *ast.File, fset *token.FileSet, consts map[string]string) map[string]int {
	out := map[string]int{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if r87IsStringInspection(call) || r87IsMessageCall(call) || r87IsAssertionCarrier(call) {
			return true
		}
		for _, a := range call.Args {
			sql, ok := r87ResolveString(a, consts)
			if !ok {
				continue
			}
			for _, m := range r87ReCreateObject.FindAllStringSubmatch(sql, -1) {
				name := strings.ToLower(m[2])
				pos := fset.Position(a.Pos()).Offset
				if p, ok := out[name]; !ok || pos < p {
					out[name] = pos
				}
			}
		}
		return true
	})
	return out
}

// r87SelfCreatedObjects 收集本文件自己建的对象名（小写）。
//
// ★ 与「破坏性语句」走**同一套实参解析**（拼接、const 都能解析）⇒
//   `"CREATE SCHEMA " + schema`（schema 是 const）能被认出来，
//   而 `"DROP SCHEMA " + schema + " CASCADE"` 也能 ⇒ ⇒ 两边口径一致，
//   ⇒ ⇒ 不会「认得出删、认不出建」⇒ ⇒ 不会把自建 schema 误报成破坏产品表。
//
// ★ 返回值里的 createsDB / createsSchema 用于判据 ③(R1)：
//   「文件自己建了库/schema ⇒ 删的只能是它自己建的那个名字」。
func r87SelfCreatedObjects(f *ast.File, fset *token.FileSet, consts map[string]string) (map[string]bool, bool, bool) {
	out := map[string]bool{}
	createsDB := false
	createsSchema := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if r87IsStringInspection(call) || r87IsMessageCall(call) || r87IsAssertionCarrier(call) {
			return true
		}
		for _, a := range call.Args {
			sql, ok := r87ResolveString(a, consts)
			if !ok {
				continue
			}
			// ★ 建过「库/schema」这件事**不需要名字** ⇒ 用宽松正则判。
			//   名字是运行时变量时（`"CREATE DATABASE " + name`）严格正则匹配不到，
			//   但「它建过库」这件事是确定的 ⇒ ⇒ R1 必须用它判，否则
			//   ⇒ ⇒ 「自建库 ⇒ 自隔离」这条例外对运行时变量名完全失效。
			for _, m := range r87ReCreateKind.FindAllStringSubmatch(sql, -1) {
				switch strings.ToUpper(m[1]) {
				case "DATABASE":
					createsDB = true
				case "SCHEMA":
					createsSchema = true
				}
			}
			for _, m := range r87ReCreateObject.FindAllStringSubmatch(sql, -1) {
				out[strings.ToLower(m[2])] = true
			}
		}
		return true
	})
	return out, createsDB, createsSchema
}

// r87ReToReg 认「探测某个关系是否存在」的 SQL（to_regclass / to_regprocedure）。
//
// ★ 这是第三种守卫形态（R2）：**目标已存在 ⇒ 复用/返回 ⇒ 不动它**。
//   实例：bg/analyze_throttle_840_realdb_test.go
//     `SELECT to_regclass('public.llm_gateway_task_state') IS NOT NULL`
//     ⇒ 已存在 ⇒ 复用并 return（清理函数**没有**注册）
//     ⇒ ⇒ 那个 DROP TABLE 只在「表本来不存在、本测试自己建的」情况下才会跑。
var r87ReToReg = regexp.MustCompile(`(?i)to_reg(class|procedure)\s*\(\s*'([^']+)'`)

// r87Probe 一次「目标是否已存在」的探测。
type r87Probe struct {
	pos  int
	name string // 小写
}

// r87CollectProbes 收集文件里的 to_regclass / to_regprocedure 探测。
func r87CollectProbes(fset *token.FileSet, f *ast.File) []r87Probe {
	var out []r87Probe
	ast.Inspect(f, func(n ast.Node) bool {
		bl, ok := n.(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return true
		}
		v, err := strconv.Unquote(bl.Value)
		if err != nil {
			return true
		}
		for _, m := range r87ReToReg.FindAllStringSubmatch(v, -1) {
			out = append(out, r87Probe{pos: fset.Position(bl.Pos()).Offset, name: strings.ToLower(m[2])})
		}
		return true
	})
	return out
}

// r87TargetMatches 判断探测到的名字是不是这条破坏性语句的目标。
func (p r87Probe) matches(target string) bool {
	if p.name == "" || target == "" {
		return false
	}
	base := p.name
	if i := strings.LastIndex(base, "."); i >= 0 {
		base = base[i+1:]
	}
	tbase := target
	if i := strings.LastIndex(tbase, "."); i >= 0 {
		tbase = tbase[i+1:]
	}
	return base == tbase
}

// r87DestructiveStmts 收集一个文件里的破坏性语句（实参口径）。
func r87DestructiveStmts(f *ast.File, fset *token.FileSet, consts map[string]string) []r87DestructiveStmt {
	var out []r87DestructiveStmt
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// ★★ (A1) **字符串检查类调用**里的字面量是「对 SQL 文本的断言」，
		//   不是「被执行的 SQL」⇒ 不算破坏性语句。
		//   实例：sql/migrations/startup/migration_711_test.go:95
		//     `strings.Index(downText, "DROP TABLE IF EXISTS hosted_task_callbacks")`
		//   ⇒ 那条腿只是断言 down 迁移的语句顺序，**它一行 SQL 都没执行**。
		//
		// ★★★ (A1b) **消息类调用**里的字面量是**散文**，也不是 SQL。
		//   实例：bg/ursm_830_realdb_test.go:85
		//     `fmt.Errorf("…本测试会 DROP TABLE ... CASCADE，只允许对可丢弃的测试库运行…")`
		//   ⇒ 那句是在**说明**这条腿会做什么，不是在做。
		//   ★★ 这一条是本轮最反直觉的一处：**把解析做得更彻底（支持字符串拼接）
		//     反而让假阳性变多** —— 因为散文里的关键字被认成了语句。
		//     ⇒ ⇒ 修法不是「少解析」，而是**按实参的用途分类**：
		//        执行类/本地 helper ⇒ 可能是 SQL；消息类 ⇒ 一定是散文。
		if r87IsStringInspection(call) || r87IsMessageCall(call) || r87IsAssertionCarrier(call) {
			return true
		}
		for _, a := range call.Args {
			sql, ok := r87ResolveString(a, consts)
			if !ok {
				continue
			}
			if hit, targets := r87DestructiveOf(sql); hit {
				out = append(out, r87DestructiveStmt{
					pos:     fset.Position(a.Pos()).Offset,
					sql:     sql,
					targets: targets,
				})
			}
		}
		return true
	})
	out = append(out, r87RangeDestructive(f, fset, consts)...)
	sort.Slice(out, func(i, j int) bool { return out[i].pos < out[j].pos })
	return out
}

// r87ExecLike 认「可能真的执行 SQL」的调用。
var r87ExecLike = map[string]bool{
	"Exec": true, "Query": true, "QueryRow": true, "ExecBatch": true,
	"CopyFrom": true, "SendBatch": true, "Prepare": true,
}

// r87IsStringInspection 认「字符串检查」类调用（strings.* / bytes.*）。
// r87IsAssertionCarrier 认「断言 / 正则」类调用 —— 它们的实参是**被检查的文本**，
// 不是**被执行的 SQL**。
//
// ★ R90 轮加这一层的理由（实测）：探测器原来只排除「字符串检查类」与「消息类」，
//   ⇒⇒ 下面这几种**根本不是 SQL**的字面量仍被算成破坏性语句：
//     · `require.Contains(t, hint, "TRUNCATE")`
//       —— admin/data_lifecycle_hot_partition_reclaim_hint_test.go（断言needle）
//     · `regexp.MustCompile(`…TRUNCATE…`)` / `MustCompile(`…DROP\s+TABLE…`)`
//       —— sql/schema/handrun_sql_inventory_test.go、internal/sqlguard/…、
//         installer/cmd/llm-gw-installer/…（**正则模式**）
//   ⇒⇒ 读数被灌水（⇒⇒ 「有活破坏性语句的文件 38 / 语句 62」里有若干条不是 SQL）。
//
// ★★ 为什么**排除整个调用**是安全的：ast.Inspect 会**逐个**访问调用节点
//   ⇒⇒ `require.NoError(t, pool.Exec(ctx, "TRUNCATE x"))` 里
//   **内层** `pool.Exec(...)` 是**另一个** CallExpr 节点 ⇒⇒ 照样算数
//   ⇒⇒ 排除的只是「把 SQL 文本当断言材料」的那一层，不影响真腿。
var r87AssertionCarriers = map[string]bool{
	"regexp": true, "require": true, "assert": true,
}

func r87IsAssertionCarrier(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return r87AssertionCarriers[x.Name]
}

// r87IsStringInspection 认「字符串检查类」调用 —— 实参是**被检查的文本**，不是 SQL。
func r87IsStringInspection(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return x.Name == "strings" || x.Name == "bytes"
}

// r87IsMessageCall 认「消息类」调用 —— 它们的实参是**给人看的散文**，不是 SQL。
//
// ★ `fmt.Sprintf` **不在**这个名单里：它是「拼 SQL」的常用手法
//   （`fmt.Sprintf("DROP TABLE IF EXISTS %s", pname)`）⇒ 必须仍然算数。
var r87MessageCalls = map[string]bool{
	"Errorf": true, "Error": true, "New": true, "Fatal": true, "Fatalf": true,
	"Skip": true, "Skipf": true, "Log": true, "Logf": true, "Print": true,
	"Printf": true, "Panic": true, "Panicf": true,
}

func r87IsMessageCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return r87MessageCalls[sel.Sel.Name]
}

// r87RangeDestructive 收集「切片字面量 → range → 送进 Exec 类调用」的破坏性语句。
//
// ★ 实例：admin/data_lifecycle_attachments_integration_test.go
//
//	for _, stmt := range []string{
//	    "TRUNCATE public._request_logs_hot",
//	    ...
//	} { pool.Exec(ctx, stmt) }
//
// ⇒ 字面量在**复合字面量**里、不是调用实参 ⇒ 纯「实参口径」会漏掉它
// ⇒ ⇒ 这里补一次小数据流：range 的值变量被送进 Exec 类调用 ⇒ 该切片里的
// ⇒ 破坏性字面量**算数**。
//
// ★ 反例（必须仍然不算）：domains/requestjourney/observation_outbox_integration_test.go
//
//	for _, forbidden := range []string{"TRUNCATE request_journey_observation_outbox"} {
//	    if contains(text, forbidden) { t.Fatalf(...) }
//	}
//
// ⇒ 值变量进的是 `contains`（字符串检查）⇒ 不算 ⇒ 不误判。
func r87RangeDestructive(f *ast.File, fset *token.FileSet, consts map[string]string) []r87DestructiveStmt {
	var out []r87DestructiveStmt
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok || rs.Tok != token.DEFINE || rs.Body == nil {
			return true
		}
		valName := ""
		switch v := rs.Value.(type) {
		case *ast.Ident:
			valName = v.Name
		default:
			return true
		}
		lit, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		execs := false
		ast.Inspect(rs.Body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !r87ExecLike[sel.Sel.Name] {
				return true
			}
			for _, a := range call.Args {
				if id, ok := a.(*ast.Ident); ok && id.Name == valName {
					execs = true
				}
			}
			return true
		})
		if !execs {
			return true
		}
		for _, elt := range lit.Elts {
			s, ok := r87ResolveString(elt, consts)
			if !ok {
				continue
			}
			if hit, targets := r87DestructiveOf(s); hit {
				out = append(out, r87DestructiveStmt{
					pos:     fset.Position(elt.Pos()).Offset,
					sql:     s,
					targets: targets,
				})
			}
		}
		return true
	})
	return out
}

// r87DestructiveStmt 一条破坏性语句。
type r87DestructiveStmt struct {
	pos     int
	sql     string
	targets []string
}

// r87ResolveString 把一个实参解析成字符串（字面量 / const ident / Sprintf 内嵌字面量 / 字符串拼接）。
//
// ★ **拼接**这一支是为判据 ③(e) 加的：
//
//	t.Skipf("public.models_canonical / model_modality_verification already exist (%d) — " +
//	    "…the test drops them", existing)
//
// ⇒ 实参是 BinaryExpr 而不是 BasicLit ⇒ 只认 BasicLit 会漏掉「已存在即跳过」这条证据
// ⇒ ⇒ 漏判的后果是**把有守卫的腿误报成高危**（假阳性）。
func r87ResolveString(a ast.Expr, consts map[string]string) (string, bool) {
	switch x := a.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			return "", false
		}
		return v, true
	case *ast.Ident:
		v, ok := consts[x.Name]
		return v, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		// ★ **部分解析**：两侧各自独立解析，能解析出来的部分拼起来。
		//   原因：`"DROP SCHEMA " + schema + " CASCADE"` 这种写法里，
		//   变量名不是字面量 ⇒ 整串解析失败 ⇒ **破坏性语句被漏掉**。
		//   ⇒ 拼出来的串里关键字仍在 ⇒ 破坏性这件事被认出来
		//   ⇒ 至于「删的是哪个对象」解析不出来 ⇒ 交给人工判词与台账。
		l, lok := r87ResolveString(x.X, consts)
		r, rok := r87ResolveString(x.Y, consts)
		switch {
		case lok && rok:
			return l + r, true
		case lok:
			return l, true
		case rok:
			return r, true
		}
		return "", false
	case *ast.CallExpr:
		// fmt.Sprintf("TRUNCATE %s", x) 之类 ⇒ 取内嵌字面量。
		for _, sub := range x.Args {
			if v, ok := r87ResolveString(sub, consts); ok {
				return v, true
			}
		}
	}
	return "", false
}

// ── 包索引（跨包守卫识别）──────────────────────────────────────────────────

// r87BuildPkgFacts 建全仓包索引：每个目录里哪些函数体内含目标库判定。
//
// ★ R90 轮新增参数 `exported`（事实源导出的 DSN 名集合）⇒⇒ 因为「这条腿
//   消费不消费事实源的 DSN」必须按**函数粒度**回答 ⇒⇒ 逐函数记它读了哪些名。
func r87BuildPkgFacts(t *testing.T, root, modulePath string, exported map[string]bool) (map[string]*r87PkgFacts, map[string]*r91Node) {
	t.Helper()
	out := map[string]*r87PkgFacts{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, raw, parser.ParseComments)
		if perr != nil {
			return nil
		}
		dir := filepath.Dir(path)
		facts := out[dir]
		if facts == nil {
			facts = &r87PkgFacts{
				funcsWithCheck: map[string]bool{},
				declaredGuards: map[string]bool{},
				pkgFuncs:       map[string]r89PkgFunc{},
				pkgScopeDSN:    map[string]bool{},
				pkgTestMainDSN: map[string]bool{},
			}
			out[dir] = facts
		}
		// ★ R90 轮：**包作用域**读到的导出名 + 包作用域是否打开 PG 连接。
		//
		// ★ 为什么必须单独算：包级 `var dsn = os.Getenv("TEST_DATABASE_URL")`
		//   **不在任何函数体内** ⇒⇒ 逐函数扫描看不见它
		//   ⇒⇒ 而它流进的是**本包所有**腿 ⇒⇒ 必须整包算「消费」。
		consts := r87ConstStrings(f)
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.VAR && gd.Tok != token.CONST) {
				continue
			}
			for _, n := range r90ScopeDSNNames(gd, consts, exported) {
				facts.pkgScopeDSN[n] = true
			}
			if opensPostgresConn(r90SrcSlice(string(raw), fset, gd.Pos(), gd.End())) {
				facts.pkgScopeOpensPG = true
			}
		}
		// ★ R90 轮：**`TestMain`** 读到的导出名 / 是否开 PG 连接。
		//
		// ★ Go 语义：`TestMain` 在本包**所有**测试之前运行 ⇒⇒ 它读的 DSN
		//   流进本包每一条腿 ⇒⇒ 包内每条腿都算「消费」。
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Name.Name != "TestMain" {
				continue
			}
			for _, n := range r90FuncDSNNames(fd, consts, exported) {
				facts.pkgTestMainDSN[n] = true
			}
			if opensPostgresConn(r90SrcSlice(string(raw), fset, fd.Pos(), fd.End())) {
				facts.pkgTestMainOpensPG = true
			}
		}
		// ★ 读这个包自己声明的闸门名单（`var GuardFunctions = []string{...}`）。
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) == 0 || vs.Names[0].Name != "GuardFunctions" {
					continue
				}
				for _, v := range vs.Values {
					cl, ok := v.(*ast.CompositeLit)
					if !ok {
						continue
					}
					for _, elt := range cl.Elts {
						bl, ok := elt.(*ast.BasicLit)
						if !ok || bl.Kind != token.STRING {
							continue
						}
						if s, err := strconv.Unquote(bl.Value); err == nil {
							facts.declaredGuards[s] = true
						}
					}
				}
			}
		}
		assigned := r87AssignedNodes(f)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if len(r87CollectHits(fset, fd.Body, assigned)) > 0 {
				facts.funcsWithCheck[fd.Name.Name] = true
				facts.hasCheck = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// ── 第二遍：按**函数名**给每个包建「守卫状态 + 调用边」索引（R89 轮）────
	//
	// ★ 为什么必须单独一遍：第一遍算 `declaredGuards` 时，被 import 的那个包
	//   可能还没被走到 ⇒⇒ 跨包闸门的判定必须等**所有包的名单都齐了**才能算。
	//
	// ★ 口径与单文件分析**同款**（同一个 `r88CollectFuncs`）⇒⇒ 不会出现
	//   「索引与单文件分析两套口径」的漂移。
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, raw, parser.ParseComments)
		if perr != nil {
			return nil
		}
		dir := filepath.Dir(path)
		facts := out[dir]
		if facts == nil {
			return nil
		}
		for name, fi := range r88CollectFuncs(f, fset, out, dir, modulePath, root, string(raw), exported) {
			cur := facts.pkgFuncs[name]
			cur.guarded = cur.guarded || fi.guarded
			cur.callsGuard = cur.callsGuard || fi.callsGate
			cur.isTest = cur.isTest || fi.isTest
			// ★ R90 轮：把「读了哪些事实源名 / 开不开 PG 连接」也并进包索引。
			cur.opensPG = cur.opensPG || fi.opensPG
			for _, c := range fi.cross {
				if !r91HasCross(cur.cross, c) {
					cur.cross = append(cur.cross, c)
				}
			}
			for _, d := range fi.dsnNames {
				if !r89Contains(cur.dsnNames, d) {
					cur.dsnNames = append(cur.dsnNames, d)
				}
			}
			for c := range fi.calls {
				if c == name {
					continue
				}
				if !r89Contains(cur.calls, c) {
					cur.calls = append(cur.calls, c)
				}
			}
			facts.pkgFuncs[name] = cur
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s (pkg index): %v", root, walkErr)
	}
	// ── R91 轮：把包索引拼成**全局**调用图并做不动点传播（跨包可达）。────
	gidx := r91BuildGlobal(out)
	r91Propagate(gidx, out)
	return out, gidx
}

// r89Contains 判断名字是否已在列表里。
func r89Contains(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}

// ── 单文件分析 ─────────────────────────────────────────────────────────────

// r87AnalyzeFile 判定一个测试文件是不是「破坏性且无目标库守卫」的候选腿。
//
// ★★★★★ R90 轮：**候选集从「文件级」放宽到「包级（腿级）」** ───────────────
//
// R87~R89 轮的候选口径是**文件级**的：**同一个文件**必须同时满足
//
//	① 自己读事实源导出的 DSN 名 ② 自己开 PG 连接 ③ 自己含硬破坏性 SQL
//
// ⇒⇒ ★★ 于是「破坏性 SQL 在 A 文件、读 DSN 在 B 文件」的腿
//   **压根进不了候选集** ⇒⇒ 门**连看都没看见它**（R89 轮实测：106 个文件）。
//
// ⇒⇒ R90 轮把①②都改成**腿级**问法：
//
//	这条腿的**包级可达集**里，有没有函数读了事实源导出的 DSN 名？
//	这条腿的**包级可达集**里，有没有函数打开了 PG 连接？
//	这条腿**够不到**任何目标库判定（守卫）？
//
// ⇒⇒ 三条同时成立 ⇒⇒ 它才是候选（⇒⇒ 事实源给什么值它都会清空那张表）。
//
// ★ 为什么「读 DSN 名」与「开 PG 连接」要**分别**问：
//
//	读了 DSN 名 ≠ 那个值进了连接（例：读出来拿去连 SQLite / 只做断言）。
//	⇒⇒ 两个条件都要，才不会把「读了名但没用」的腿误报成高危。
func r87AnalyzeFile(t *testing.T, path, rel, src string, exported map[string]bool, pkgs map[string]*r87PkgFacts, gidx map[string]*r91Node, modulePath, root string) []r87Pair {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}
	consts := r87ConstStrings(f)

	// ① 破坏性语句（排除自己建的对象）。
	self, createsDB, createsSchema := r87SelfCreatedObjects(f, fset, consts)
	createPos := r87SelfCreatePositions(f, fset, consts)
	probes := r87CollectProbes(fset, f)
	var live []r87DestructiveStmt
	for _, s := range r87DestructiveStmts(f, fset, consts) {
		skip := false
		for _, tg := range s.targets {
			if self[tg] {
				// ★★ R92 轮：**位置敏感** —— 「自建 ⇒ 删它安全」只在
				//   「**建在前**」时成立 ⇒⇒ 「先 DROP 后 CREATE」的形态
				//   删掉的是**落点上已存在的那个对象**（产品表）⇒⇒ 不能排除。
				if cp, ok := createPos[tg]; !ok || cp <= s.pos {
					skip = true
				}
			}
			if createsDB && strings.Contains(strings.ToUpper(s.sql), "DROP DATABASE") {
				skip = true
			}
			// ★ (R2) 「探测过目标 ⇒ 已存在就不动它」⇒ 这条语句只会在
			//   「目标本来不存在」时才跑 ⇒ 它删的是自己刚建的那个东西。
			for _, pr := range probes {
				if pr.pos < s.pos && pr.matches(tg) {
					skip = true
				}
			}
		}
		if !skip {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}
	// ★ (R1) 「自建库/自建 schema ⇒ 完全自隔离」：
	//   文件自己 CREATE 了 DATABASE/SCHEMA，而它的破坏性语句**只有**
	//   DROP DATABASE / DROP SCHEMA ⇒ ⇒ 删的只能是它自己建的那个名字
	//   ⇒ ⇒ 静态解析不出那个名字（运行时变量）也不构成风险。
	if createsDB || createsSchema {
		allSelfScoped := true
		for _, s := range live {
			up := strings.ToUpper(s.sql)
			if !strings.Contains(up, "DROP DATABASE") && !strings.Contains(up, "DROP SCHEMA") {
				allSelfScoped = false
				break
			}
		}
		if allSelfScoped {
			return nil
		}
	}
	// ③ ★★★★★ R88 轮：**判定单位从「文件」降到「腿」**。
	//
	// R87 轮的口径是「**这个文件里**在第一条破坏性语句之前有没有目标库判定」，
	// 那是一条**文件级**的断言 ⇒⇒ 而危害的单位是**腿（一条会执行破坏性 SQL 的
	// 测试路径）** ⇒⇒ 于是「一个文件里有一条腿带守卫 ⇒ 整个文件（含别的腿）
	// 都被判成有守卫」成了恒真推论。
	//
	// ★ R88 轮实测出来的洞（R88 变异 M1）：给一条**已带守卫**的文件追加一个
	// **不带守卫**的第二入口（自己建池、自己 TRUNCATE 产品表）⇒ 文件级口径
	// 看到「判定在前面」⇒ **门仍然绿** ⇒ 事实源给什么值它都会清空产品表。
	//
	// ⇒⇒ 所以改成：**每条破坏性语句，按它所在函数能被哪些测试入口到达来定腿根，
	// 再看那个腿根能不能到达一个目标库判定**（同文件可达 + 跨包闸门名单）。
	//
	// ★ R90 轮追加：腿根还要**消费事实源的 DSN**（包级可达）
	//   ⇒⇒ 「读 DSN 在别的文件」的腿这次进得来候选集了。
	legs := r88AnalyzeLegs(f, fset, pkgs, gidx, filepath.Dir(path), modulePath, root, src, exported, live)
	if len(legs) == 0 {
		return nil
	}
	var out []r87Pair
	seen := map[string]bool{}
	for _, leg := range legs {
		// ★ R90 轮：名字来自**这条腿**消费的事实源名（不再是「这个文件读到的名」）。
		for _, n := range leg.DSNNames {
			k := n + "\t" + rel
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, r87Pair{Name: n, File: rel, Stmt: leg.Stmt, Root: leg.Root})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].File < out[j].File
	})
	return out
}

// ── R88 轮：腿级判定（同文件可达 + 跨包闸门名单）───────────────────────────
//
// ★★★ 为什么必须换掉 R87 的「文件级位置序」口径 ───────────────────────────
//
//	R87 轮的门问的是：「**这个文件里**在第一条破坏性语句之前，有没有目标库判定证据」。
//	那是一条**文件级**断言。而危害的单位是**腿**（一条会真的执行破坏性 SQL 的
//	测试路径）⇒⇒ 于是下面这条恒真推论成立：
//
//	    「一个文件里有一条腿带守卫 ⇒ 整个文件（含别的腿）都被判成有守卫」
//
//	⇒⇒ R88 变异 M1 实测：给一条**已带守卫**的文件追加一个**不带守卫**的第二入口
//	    （自己建池、自己 TRUNCATE 产品表）⇒ 文件级口径看到「判定在前面」
//	    ⇒ **门仍然绿** ⇒ 事实源给什么值它都会清空产品表。
//
//	⇒⇒ 换口径：**按腿判**。
//	  · 腿根 = 能到达该破坏性语句的**测试入口**（`Test` 前缀的函数）；
//	    若没有任何测试入口能到达它（退化形态，例如整个文件只有一个非 Test 函数），
//	    则以**该语句所在函数**本身为腿根。
//	  · 腿根**被守卫支配** ⇔ 它的同文件可达集合里，存在一个「体内含目标库判定」
//	    或「调用了跨包闸门」的函数。
//
//	★ 为什么不是拿「语句所在函数」当腿根：仓内四条真腿的破坏性字面量住在**兄弟
//	  helper** 里（`reset` / `resetOutboxAndSessions` / `attachFixtureReset` /
//	  `bootstrapSupplierSchema`），而守卫住在**另一个** helper 里（`openPool` /
//	  `openAuditPool` / `openAttachPool` / `openSupplierPG`）⇒⇒ 两者是**兄弟**，
//	  不是祖先 ⇒⇒ 只看「语句所在函数」会把四条真腿全判成没守卫（假阳性）。
//	  ⇒⇒ 真正的支配关系在**调用者**那一侧：测试函数同时调了那两个 helper。
//
//	★ 口径边界（登记，不掩饰）：可达性是**同文件**的。跨文件同包的 helper 调用
//	  **判不出来** ⇒ 若守卫与破坏性语句分居同包的两个文件，本门会**判成无守卫**
//	  （假阳性方向）。当前仓内四条真腿的守卫与破坏性语句同文件、台账三条也同文件
//	  ⇒ 实测无影响；跨文件形态已登记为已知缺口（同 R87 台账里「跨文件自建库」
//	  那条的缺口性质）。

// r88FuncInfo 一个文件内的函数：判定证据 + 调用关系。
type r88FuncInfo struct {
	name      string
	start     int
	end       int
	guarded   bool            // 体内含目标库判定证据
	callsGate bool            // 调用了跨包闸门函数（按包自己声明的名单）
	calls     map[string]bool // 同文件调用的函数名
	isTest    bool            // 测试入口（Test 前缀）
	// dsnNames：体内读到的**事实源导出 DSN 名**（R90 轮新增）。
	dsnNames []string
	// opensPG：体内是否打开 PG 连接（R90 轮新增）。
	opensPG bool
	// cross：体内**跨包**调用到的目标（R91 轮新增）。
	cross []r91CrossRef
}

// r88Leg 一条「没有被守卫支配」的破坏性腿。
type r88Leg struct {
	Stmt string
	Root string
	// DSNNames：这条腿**消费**的事实源导出 DSN 名（R90 轮新增）。
	//
	// ★ 为什么挂在**腿**上而不是文件上：候选集包级化之后，
	//   同一个文件里的不同腿可能消费**不同**的 DSN 名
	//   ⇒⇒ 台账的键（name + file）必须按腿实际消费的名字来登记。
	DSNNames []string
}

// r88CollectFuncs 收集一个文件里的函数及其判定证据与调用关系。
//
// ★ R90 轮新增两个参数（`src` / `exported`）与两个产物（`dsnNames` / `opensPG`）：
//   候选集从**文件级**放宽到**包级** ⇒⇒ 「这条腿消费不消费事实源的 DSN」
//   必须能在**函数粒度**上回答 ⇒⇒ 逐函数记「读了哪些导出名 / 开不打开 PG」。
func r88CollectFuncs(f *ast.File, fset *token.FileSet, pkgs map[string]*r87PkgFacts, dir, modulePath, root string, src string, exported map[string]bool) map[string]*r88FuncInfo {
	assigned := r87AssignedNodes(f)
	consts := r87ConstStrings(f)
	out := map[string]*r88FuncInfo{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		fi := &r88FuncInfo{
			name:   fd.Name.Name,
			start:  fset.Position(fd.Pos()).Offset,
			end:    fset.Position(fd.End()).Offset,
			calls:  map[string]bool{},
			isTest: strings.HasPrefix(fd.Name.Name, "Test"),
		}
		// 体内判定证据：(a)current_database() /(b)读 .Database /(c)DSN 目标比较 /(e)先探后建。
		fi.guarded = len(r87CollectHits(fset, fd, assigned)) > 0
		// ★ R90 轮：体内读到的**事实源导出名**（AST 口径，与 `r71ScanFile` 同款：
		//   `os.Getenv` / `os.LookupEnv` + 字面量或包级 const 名字）。
		fi.dsnNames = r90FuncDSNNames(fd, consts, exported)
		// ★ R90 轮：体内是否打开 PG 连接（文本口径，与仓内 `opensPostgresConn` 同款）。
		fi.opensPG = opensPostgresConn(r90SrcSlice(src, fset, fd.Pos(), fd.End()))
		// 调用关系 + 跨包闸门。
		ast.Inspect(fd, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				fi.calls[fun.Name] = true
				// ★ R91 轮：**同包**调用本包自己声明的闸门 ⇒ 记为「调用了闸门」。
				//
				// ★ 为什么必须加：原先 `callsGate` 只在**限定名调用**
				//   （`otherpkg.Gate()`）上设置 ⇒⇒ 同包内写成 `Gate()` 时**不算**
				//   ⇒⇒ 跨包链式闸门（腿 → P.helper → Q.Helper → `Q.Gate()`）
				//   ⇒⇒ 中间那一环**接不上** ⇒⇒ 腿被误判成「没守卫」。
				//
				// ★ 为什么安全：同包调用本包**声明**的闸门，在同包口径下
				//   本来就被「闸门函数自身的内联证据」覆盖（`Gate` 的体内有
				//   `current_database()`）⇒⇒ 加这一条**不改变**同包读数
				//   ⇒⇒ 只是让**跨包**那一侧接得上。
				if facts := pkgs[dir]; facts != nil && facts.declaredGuards[fun.Name] {
					fi.callsGate = true
				}
			case *ast.SelectorExpr:
				pkgName, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				imp, ok := r87ImportPath(f, pkgName.Name)
				if !ok {
					return true
				}
				d := r87ImportDir(root, modulePath, imp)
				if d == "" {
					return true
				}
				// ★ R91 轮：记录**跨包**调用边（同包内的调用走 `calls` 即可）。
				if d != dir {
					fi.cross = append(fi.cross, r91CrossRef{Dir: d, Name: fun.Sel.Name})
				}
				facts := pkgs[d]
				if facts == nil {
					return true
				}
				// ★★ R91 轮：**跨包闸门只认「包自己声明的名单」**，
				//   **不再退回包级粗口径**（`facts.hasCheck`）。
				//
				// ★★★ 为什么必须去掉那个兜底（实测逼出来的）：
				//   仓内 **18 个包**「体内有目标库判定证据、但**没**声明 `GuardFunctions`」
				//   ⇒⇒ 旧口径下，**任何**跨包调用进这 18 个包都会被当成「调用了闸门」
				//   ⇒⇒ 一条腿只要调了 `bg`/`admin`/`db` 里的任意一个函数
				//   ⇒⇒ 就被判成「有守卫」⇒⇒ ⇒ **漏放**（数据事故级）。
				//
				//   而「这个包**恰好**含有目标库判定」**不是**「这个函数是闸门」：
				//   前者是**包级事实**，后者是**函数级承诺**
				//   ⇒⇒ 只有**声明名单**才是承诺。
				//
				// ★ 与 R87 轮的用意一致（那轮引入 `GuardFunctions` 正是为了摆脱
				//   包级粗口径）⇒⇒ 本轮把残留的兜底也收掉。
				guarded := facts.declaredGuards[fun.Sel.Name]
				if guarded {
					fi.callsGate = true
				}
			}
			return true
		})
		out[fi.name] = fi
	}
	return out
}

// r89Merged 把「当前文件的函数」与「同包索引」合成一张**按函数名**的调用图。
//
// ★ 本地优先：同一个包里函数名唯一 ⇒⇒ 本地定义与包索引指向**同一个函数**
//   ⇒⇒ 本地那份带位置与体内证据，更细 ⇒⇒ 覆盖包索引那份不会丢信息。
func r89Merged(funcs map[string]*r88FuncInfo, pkg map[string]r89PkgFunc) map[string]r89PkgFunc {
	out := make(map[string]r89PkgFunc, len(funcs)+len(pkg))
	for name, pf := range pkg {
		out[name] = pf
	}
	for name, f := range funcs {
		cur := out[name]
		cur.guarded = cur.guarded || f.guarded
		cur.callsGuard = cur.callsGuard || f.callsGate
		cur.isTest = cur.isTest || f.isTest
		// ★ R90 轮：合并「读了哪些事实源名」与「开不开 PG 连接」。
		cur.opensPG = cur.opensPG || f.opensPG
		for _, c := range f.cross {
			if !r91HasCross(cur.cross, c) {
				cur.cross = append(cur.cross, c)
			}
		}
		for _, d := range f.dsnNames {
			if !r89Contains(cur.dsnNames, d) {
				cur.dsnNames = append(cur.dsnNames, d)
			}
		}
		for c := range f.calls {
			if !r89Contains(cur.calls, c) {
				cur.calls = append(cur.calls, c)
			}
		}
		out[name] = cur
	}
	return out
}

// r89ReachNames 从 rootName 出发可达的函数名集合（含 rootName 自身）。
//
// ★ R89 轮：可达域从**同文件**扩到**同包跨文件** ⇒⇒
//   · 包索引里有、本文件里也有的 ⇒ 用合并后的那份；
//   · 只有包索引里有的 ⇒ 用包索引里的调用边；
//   · 两边都没有 ⇒ 名字不解析（那多半是外部函数、方法或变量）。
func r89ReachNames(rootName string, g map[string]r89PkgFunc) map[string]bool {
	seen := map[string]bool{}
	queue := []string{rootName}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		if node, ok := g[name]; ok {
			queue = append(queue, node.calls...)
		}
	}
	return seen
}

// ── R91 轮：全局调用图（跨包可达）──────────────────────────────────────────
//
// ★★★ 为什么要做这一层 ─────────────────────────────────────────────────────
//
//	R87~R90 轮的可达性是**同包**的 ⇒⇒ 于是三件事判不出来：
//
//	① **跨包消费事实源 DSN**（★ **漏放**方向，数据事故级）
//	   —— 腿在包 A、DSN 在包 B 被读取并连上 ⇒⇒ 门**看不见**这条腿。
//	② **跨包链式闸门**：腿 → 包 B 的 helper → `internal/testguard` 的**声明闸门**
//	   ⇒⇒ 直接跨包调用早被 `declaredGuards` 覆盖，**跨包的一跳间接**没有。
//	③ **跨包 PG 连接打开点**：腿在包 A，连库在包 B。
//
// ★★ 而**跨包内联判定**（目标库判定写在另一个包里）**故意不闭合** ——
//
//	`internal/testguard.RequireDatabaseMatchesDSN` 的**体内**就有目标库判定证据，
//	但 R87 轮刻意**不**把它列入 `GuardFunctions` 名单：
//	它只核对「连接落在 DSN 指的那个库」，**不决定这条腿跑不跑**
//	⇒⇒ DSN 指产品库时它照样通过 ⇒⇒ 破坏性 SQL 照样落在产品库上
//	⇒⇒ ⇒ 它**不是**安全闸门 ⇒⇒ 跨包传播内联判定 ⇒⇒ 会把真腿误判成「有守卫」
//	⇒⇒ ⇒ **跨包只传播「声明闸门调用」，不传播内联判定。**
//
// ★★ 仓内实测（R91 轮诊断）：跨包被调函数**自己读了事实源 DSN** 的有 **11** 处
//   （`config.Load` × 8 读 `DATABASE_URL`/`LLM_GATEWAY_DATABASE_URL`；
//   `pkg/identity.ShadowDatabase` × 3 读 `IDENTITY_SHADOW_DSN`）
//   ⇒⇒ 形态**真实存在**，只是当前没有和破坏性语句落在同一条腿上
//   ⇒⇒ ⇒ 这是**结构上开着**的洞 ⇒ 本轮闭合。

// r91HasCross 判断跨包目标是否已在列表里。
func r91HasCross(list []r91CrossRef, c r91CrossRef) bool {
	for _, x := range list {
		if x.Dir == c.Dir && x.Name == c.Name {
			return true
		}
	}
	return false
}

// r91Key 节点的全局键（包目录 + 函数名）。
//
// ★ 为什么必须带包目录：同一个包里函数名唯一 ⇒ 名即实体；
//   **跨包就不唯一了**（两个包都可能有 `reset`）⇒⇒ 只用函数名做键会串包。
func r91Key(dir, name string) string { return dir + "\x00" + name }

// r91Node 全局调用图的一个节点（= 某个包里的某个函数）。
type r91Node struct {
	dir  string
	name string
	// same：包内被调（函数名）。
	same []string
	// cross：跨包被调。
	cross []r91CrossRef
	// predsSame：同包内谁调用它（**反向边**，汇总性质靠它回流）。
	predsSame []*r91Node
	// predsCross：跨包谁调用它（**反向边**）。
	//
	// ★ 与 predsSame 分开：R91 轮的传播规则对两类边**同规则**，
	//   但分开放是为了让变异（M15）能**只**掐断跨包那一半。
	predsCross []*r91Node
	// 自身性质。
	dsnNames   []string
	openPG     bool
	callsGuard bool
	guarded    bool
	isTest     bool
	// 汇总（沿调用边传播后的结果）。
	reachDSN    map[string]bool
	reachOpenPG bool
	reachGate   bool
}

// r91BuildGlobal 把所有包的包索引拼成一张**全局**调用图。
func r91BuildGlobal(pkgs map[string]*r87PkgFacts) map[string]*r91Node {
	out := map[string]*r91Node{}
	for dir, facts := range pkgs {
		for name, fn := range facts.pkgFuncs {
			key := r91Key(dir, name)
			n := out[key]
			if n == nil {
				n = &r91Node{dir: dir, name: name, reachDSN: map[string]bool{}}
				out[key] = n
			}
			n.same = append(n.same, fn.calls...)
			n.cross = append(n.cross, fn.cross...)
			n.dsnNames = append(n.dsnNames, fn.dsnNames...)
			n.openPG = n.openPG || fn.opensPG
			n.callsGuard = n.callsGuard || fn.callsGuard
			n.guarded = n.guarded || fn.guarded
			n.isTest = n.isTest || fn.isTest
		}
	}
	return out
}

// r91Succ 取一个节点的全部后继（同包 + 跨包）。
func r91Succ(nodes map[string]*r91Node, n *r91Node) []*r91Node {
	var out []*r91Node
	for _, c := range n.same {
		if s := nodes[r91Key(n.dir, c)]; s != nil {
			out = append(out, s)
		}
	}
	for _, c := range n.cross {
		if s := nodes[r91Key(c.Dir, c.Name)]; s != nil {
			out = append(out, s)
		}
	}
	return out
}

// ★★ 汇总性质的语义是「**从**这个节点出发**可达**的东西」，
//   即 `reach(X) = 自身 ∪ ⋃ reach(后继)` ⇒⇒ 汇总要**从后继流回前驱**
//   ⇒⇒ 传播必须沿**反向边**做。
//   ⇒⇒ ★ 第一版写成「把自己的汇总推给后继」⇒ **方向反了**
//   ⇒⇒ 后果：调用者**拿不到**被调用者读到的 DSN 名 ⇒⇒ 跨包消费判不出来
//   ⇒⇒ （R91 轮实测：R90 夹具 ⑲ 因此变红 ⇒ 当场暴露）

// r91Propagate 沿调用边传播三种汇总性质（不动点）。
//
// ★ 传播规则（R91 轮的关键判据）：
//
//	· `reachDSN`（读到了哪些事实源名）/ `reachOpenPG`（开不开 PG）
//	  / `reachGate`（调用了**声明闸门**）⇒ **沿全部边**（同包 + 跨包）传播；
//	· **内联判定（`guarded`）不跨包传播** ⇒ 见本节头的说明。
func r91Propagate(nodes map[string]*r91Node, pkgs map[string]*r87PkgFacts) {
	for _, n := range nodes {
		for _, d := range n.dsnNames {
			n.reachDSN[d] = true
		}
		if n.openPG {
			n.reachOpenPG = true
		}
		if n.callsGuard {
			n.reachGate = true
		}
		// 包作用域 / TestMain 的 DSN 读取（**本包**内算 ⇒ R90 轮的规则）。
		if f := pkgs[n.dir]; f != nil {
			for d := range f.pkgScopeDSN {
				n.reachDSN[d] = true
			}
			for d := range f.pkgTestMainDSN {
				n.reachDSN[d] = true
			}
		}
	}
	// ── 建**反向边**（谁调谁）⇒ 汇总沿它回流。
	for _, n := range nodes {
		for _, c := range n.same {
			if succ := nodes[r91Key(n.dir, c)]; succ != nil {
				succ.predsSame = append(succ.predsSame, n)
			}
		}
		for _, c := range n.cross {
			if succ := nodes[r91Key(c.Dir, c.Name)]; succ != nil {
				succ.predsCross = append(succ.predsCross, n)
			}
		}
	}
	queue := make([]*r91Node, 0, len(nodes))
	for _, n := range nodes {
		queue = append(queue, n)
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		// ★ 沿**反向边**把 n 的汇总回流给它的调用者。
		//
		// ★ 分成两段（同包 / 跨包）是有意的：传播规则对两类边**目前相同**，
		//   但分开写才能让变异**只**掐断跨包那一半（M15）。
		for _, pred := range n.predsSame {
			r91Flow(pred, n, true, &queue)
		}
		for _, pred := range n.predsCross {
			// ★ `withGate=true`：「调用了**声明**闸门」是**函数级承诺**，
			//   与包无关 ⇒⇒ 必须跨包传。
			//   ⇒⇒ ★ **不**跨包传的是「体内写了判定」（`guarded`）——
			//     它从来不进这套传播（见本节头：跨包内联判定不是闸门）。
			r91Flow(pred, n, true, &queue)
		}
	}
}

// r91Flow 把 n 的汇总回流给它的调用者 pred。
//
// ★ `withGate` = 这一段边**要不要**传递「调用了声明闸门」。
//   ⇒ 同包边：传（`callsGate` 是函数级承诺，与包无关）。
//   ⇒ 跨包边：传（同样是函数级承诺）。
//   ⇒ ★ **不传**的是「体内写了判定」那个性质（`guarded`）——
//     它**从来不跨包传播**（见本节头：跨包内联判定不是闸门）。
func r91Flow(pred, n *r91Node, withGate bool, queue *[]*r91Node) {
	changed := false
	for d := range n.reachDSN {
		if !pred.reachDSN[d] {
			pred.reachDSN[d] = true
			changed = true
		}
	}
	if n.reachOpenPG && !pred.reachOpenPG {
		pred.reachOpenPG = true
		changed = true
	}
	if withGate && n.reachGate && !pred.reachGate {
		pred.reachGate = true
		changed = true
	}
	if changed {
		*queue = append(*queue, pred)
	}
}

// r91LegDSNNames 这条腿（连同**跨包**可达范围）读到的全部事实源导出名。
func r91LegDSNNames(rootName, dir string, gidx map[string]*r91Node) []string {
	n := gidx[r91Key(dir, rootName)]
	if n == nil {
		return nil
	}
	var out []string
	for d := range n.reachDSN {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// r91LegOpensPG 这条腿（连同**跨包**可达范围）里有没有打开 PG 连接。
func r91LegOpensPG(rootName, dir string, gidx map[string]*r91Node) bool {
	n := gidx[r91Key(dir, rootName)]
	return n != nil && n.reachOpenPG
}

// r91LegCallsGate 这条腿（连同**跨包**可达范围）里有没有调用**声明闸门**。
//
// ★ 注意：这里**只**认「调用了声明闸门」，**不**认跨包的内联判定
//   ⇒⇒ `RequireDatabaseMatchesDSN`（R87 刻意不列入名单的那个核对函数）不会被当成闸门。
func r91LegCallsGate(rootName, dir string, gidx map[string]*r91Node) bool {
	n := gidx[r91Key(dir, rootName)]
	return n != nil && n.reachGate
}

// r89NodeGuarded：这张图里，从 rootName 可达的范围内有没有目标库判定。
func r89NodeGuarded(rootName string, g map[string]r89PkgFunc) bool {
	for name := range r89ReachNames(rootName, g) {
		if node, ok := g[name]; ok {
			if node.guarded || node.callsGuard {
				return true
			}
		}
	}
	return false
}

// ── R90 轮：候选集包级化（腿的「事实源 DSN 消费」判定）─────────────────────
//
// ★★★ 本节要闭合的洞（R89 轮登记的「候选集盲区」）──────────────────────────
//
//	R87~R89 轮的候选集是**文件级**的 ⇒⇒ 「破坏性 SQL 在 A 文件、读 DSN 在 B 文件」
//	的腿**进不了候选集** ⇒⇒ 门**连看都没看见它**（实测 106 个破坏性文件）。
//
//	⇒⇒ 本节把「这条腿消不消费事实源的 DSN」拆成两个**可判**的子问题：
//
//	  ① **消费名**：腿的**包级可达集**里，有没有函数读了事实源导出的 DSN 名？
//	     （外加两条「不在函数体内」的来源：包作用域初始化、`TestMain`）
//	  ② **真的连了 PG**：腿的可达范围（或包作用域 / `TestMain`）里，有没有打开 PG 连接？
//
//	⇒⇒ ①②都成立 ⇒⇒ 这条腿连的就是「事实源给什么就连什么」的库。
//
// ★ 为什么 ①② 要**分别**判：读了 DSN 名 ≠ 那个值进了连接
//   （例：读出来只为打印，或拿它去连 SQLite）⇒⇒ 少一个条件就会误报。

// r90SrcSlice 取出源码里 [from, to) 这一段文本（按字节偏移）。
func r90SrcSlice(src string, fset *token.FileSet, from, to token.Pos) string {
	if !from.IsValid() || !to.IsValid() {
		return ""
	}
	s := fset.Position(from).Offset
	e := fset.Position(to).Offset
	if s < 0 || e > len(src) || s >= e {
		return ""
	}
	return src[s:e]
}

// r90FuncDSNNames 返回这个函数体内读到的**事实源导出名**。
//
// ★ 口径与仓内 `r71ScanFile` 同款：`os.Getenv` / `os.LookupEnv` + 字面量
//   或包级 const 名字 ⇒⇒ 两处口径不会漂移。
func r90FuncDSNNames(fd *ast.FuncDecl, consts map[string]string, exported map[string]bool) []string {
	seen := map[string]bool{}
	var names []string
	ast.Inspect(fd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "os" {
			return true
		}
		var name string
		switch a := call.Args[0].(type) {
		case *ast.BasicLit:
			if a.Kind != token.STRING {
				return true
			}
			name, _ = strconv.Unquote(a.Value)
		case *ast.Ident:
			name = consts[a.Name]
		default:
			return true
		}
		if name == "" || seen[name] || !exported[name] {
			return true
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	sort.Strings(names)
	return names
}

// r90ScopeDSNNames 返回**包作用域**（顶层 var/const 初始化）读到的导出名。
//
// ★ 为什么必须单独算：包级 `var dsn = os.Getenv("TEST_DATABASE_URL")`
//   **不在任何函数体内** ⇒⇒ 函数级扫描看不见它
//   ⇒⇒ 而它流进的是**本包所有**测试 ⇒⇒ 必须整包算「消费」。
func r90ScopeDSNNames(gd *ast.GenDecl, consts map[string]string, exported map[string]bool) []string {
	seen := map[string]bool{}
	var names []string
	ast.Inspect(gd, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "Getenv" && sel.Sel.Name != "LookupEnv" {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "os" {
			return true
		}
		var name string
		switch a := call.Args[0].(type) {
		case *ast.BasicLit:
			if a.Kind != token.STRING {
				return true
			}
			name, _ = strconv.Unquote(a.Value)
		case *ast.Ident:
			name = consts[a.Name]
		default:
			return true
		}
		if name == "" || seen[name] || !exported[name] {
			return true
		}
		seen[name] = true
		names = append(names, name)
		return true
	})
	sort.Strings(names)
	return names
}

// r90LegDSNNames 返回这条腿**消费**的事实源导出名。
//
// ★ 三条来源（缺一不可地都要）：
//   ① 腿的**包级可达集**里**函数体内**读到的（覆盖「读 DSN 在另一个文件」）；
//   ② **包作用域**读到的（包级 var/const 初始化）⇒⇒ 整包所有腿都算；
//   ③ **`TestMain`** 里读到的 ⇒⇒ Go 的 `TestMain` 跑在本包所有测试之前
//      ⇒⇒ 它读的 DSN 流进本包每一条腿。
func r90LegDSNNames(rootName string, g map[string]r89PkgFunc, facts *r87PkgFacts) []string {
	seen := map[string]bool{}
	var names []string
	add := func(n string) {
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		names = append(names, n)
	}
	for n := range r89ReachNames(rootName, g) {
		if node, ok := g[n]; ok {
			for _, d := range node.dsnNames {
				add(d)
			}
		}
	}
	if facts != nil {
		for n := range facts.pkgScopeDSN {
			add(n)
		}
		for n := range facts.pkgTestMainDSN {
			add(n)
		}
	}
	sort.Strings(names)
	return names
}

// r90LegOpensPG：这条腿的可达范围（或包作用域 / `TestMain`）里有没有打开 PG 连接。
//
// ★ 为什么要单列：读了 DSN 名 ≠ 那个值进了连接
//   ⇒⇒ 少了这一条，「读了名但拿它连 SQLite / 只做断言」的腿会被误报成高危。
func r90LegOpensPG(rootName string, g map[string]r89PkgFunc, facts *r87PkgFacts) bool {
	for n := range r89ReachNames(rootName, g) {
		if node, ok := g[n]; ok && node.opensPG {
			return true
		}
	}
	if facts != nil && (facts.pkgScopeOpensPG || facts.pkgTestMainOpensPG) {
		return true
	}
	return false
}

// r88Enclosing 找位置 pos 所在的最内层函数。
func r88Enclosing(pos int, funcs map[string]*r88FuncInfo) *r88FuncInfo {
	var best *r88FuncInfo
	for _, f := range funcs {
		if pos < f.start || pos >= f.end {
			continue
		}
		if best == nil || (f.end-f.start) < (best.end-best.start) {
			best = f
		}
	}
	return best
}

// r88AnalyzeLegs 返回「没有被守卫支配、且消费事实源 DSN」的破坏性腿。
//
// ★ R90 轮：新增**消费判定**（`dsnNames` 非空 且 腿的可达范围里确实开了 PG 连接）
//   ⇒⇒ 候选集由**文件级**放宽到**包级（腿级）**。
func r88AnalyzeLegs(f *ast.File, fset *token.FileSet, pkgs map[string]*r87PkgFacts, gidx map[string]*r91Node, dir, modulePath, root string, src string, exported map[string]bool, live []r87DestructiveStmt) []r88Leg {
	funcs := r88CollectFuncs(f, fset, pkgs, dir, modulePath, root, src, exported)
	// ★ R89 轮：同包**跨文件**的函数索引（守卫状态 + 调用边 + 是不是测试入口）。
	var pkg map[string]r89PkgFunc
	if f := pkgs[dir]; f != nil {
		pkg = f.pkgFuncs
	}
	g := r89Merged(funcs, pkg)
	var out []r88Leg
	seen := map[string]bool{}
	for _, d := range live {
		owner := r88Enclosing(d.pos, funcs)
		ownerName := "<包级>"
		if owner != nil {
			ownerName = owner.name
		}
		// 腿根 = **包里**所有能到达该语句的测试入口。
		//
		// ★ R89 轮：腿根搜索从**当前文件**扩到**整个包** ——
		//   Go 的测试惯例就是「共享 helper 放兄弟 `_test.go`、测试函数在另一个文件」
		//   （仓内现成例子：`domains/session/v2/session_request_status_backfill_test.go`
		//   的 `statusBackfillFixtureDB` 被 `session_duration_ms_realdb_test.go` 跨文件调用）。
		var roots []string
		for name, node := range g {
			if !node.isTest {
				continue
			}
			if r89ReachNames(name, g)[ownerName] {
				roots = append(roots, name)
			}
		}
		if len(roots) == 0 {
			// 退化形态：没有任何测试入口能到达它 ⇒ 以所在函数为腿根。
			roots = []string{ownerName}
		}
		for _, r := range roots {
			// ★ R91 轮：守卫判定**拆成两半**（跨包只传播「声明闸门调用」）。
			//
			// ① **内联判定**（函数体内自己写的目标库判定）⇒ **只限同包**。
			//    ⇒⇒ 因为 `internal/testguard.RequireDatabaseMatchesDSN` 的体内
			//    就有目标库判定证据，但 R87 刻意**不**把它当闸门
			//    ⇒⇒ 跨包传播它会把真腿误判成「有守卫」。
			inlineGuard := false
			for name := range r89ReachNames(r, g) {
				if node, ok := g[name]; ok && node.guarded {
					inlineGuard = true
					break
				}
			}
			// ② **声明闸门调用** ⇒ **全局**可达范围（跨包链式也覆盖）。
			if inlineGuard || r91LegCallsGate(r, dir, gidx) {
				continue
			}
			// ★★★★★ R90 轮：**消费判定** —— 这条腿会不会真的把事实源给的
			//   DSN 值连到 PG 上？
			//
			//   ① 腿的**包级可达集**里有没有函数读了事实源导出的 DSN 名；
			//   ② 同上可达范围（或包作用域 / TestMain）里有没有**打开 PG 连接**。
			//
			//   ⇒⇒ ①②都成立 ⇒⇒ 这条腿连的是「事实源给什么就连什么」的库
			//   ⇒⇒ 而它没守卫 ⇒⇒ 事实源给产品库的值，它就会清空产品表。
			dsnNames := r91LegDSNNames(r, dir, gidx)
			if len(dsnNames) == 0 {
				continue
			}
			if !r91LegOpensPG(r, dir, gidx) {
				continue
			}
			key := r + "\t" + d.sql
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r88Leg{Stmt: d.sql, Root: r, DSNNames: dsnNames})
		}
	}
	return out
}

// r87ImportPath 取 import 别名对应的导入路径。
func r87ImportPath(f *ast.File, alias string) (string, bool) {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := alias
		if imp.Name != nil {
			name = imp.Name.Name
		} else {
			parts := strings.Split(p, "/")
			name = parts[len(parts)-1]
		}
		if name == alias {
			return p, true
		}
	}
	return "", false
}

// r87ImportDir 把导入路径解析成仓内**绝对目录**（外部依赖返回空串）。
func r87ImportDir(root, modulePath, importPath string) string {
	if modulePath == "" || !strings.HasPrefix(importPath, modulePath+"/") {
		return ""
	}
	rel := strings.TrimPrefix(importPath, modulePath+"/")
	if strings.Contains(rel, "..") {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

// ── 台账 ───────────────────────────────────────────────────────────────────

// r87ParseLedger 解析台账：每行 `name<TAB>file<TAB>verdict`。
func r87ParseLedger(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out
		}
		t.Fatalf("read %s: %v", path, err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			t.Errorf("%s:%d 格式不对（要 name<TAB>file<TAB>verdict）：%q", path, i+1, line)
			continue
		}
		if strings.TrimSpace(parts[2]) == "" {
			t.Errorf("%s:%d 的判词是空的 —— 台账必须逐条写明为什么可以放行", path, i+1)
		}
		out[parts[0]+"\t"+parts[1]] = parts[2]
	}
	return out
}

// ── 门 ①：未被登记的破坏性腿 ⇒ 红 ──────────────────────────────────────────

func TestDestructiveTargetGuardNoUnguardedLeg(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}
	exported := r71ParseDatasourceExports(t, r86DSPath)
	if len(exported) < 30 {
		t.Fatalf("事实源 %s 只解析出 %d 个 export ⇒ 扫描口径失效（门会恒绿）", r86DSPath, len(exported))
	}
	modulePath := r87ModulePath(t, root)
	pkgs, gidx := r87BuildPkgFacts(t, root, modulePath, exported)

	var candidates []r87Pair
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(raw)
		rel, _ := filepath.Rel(root, path)
		candidates = append(candidates, r87AnalyzeFile(t, path, rel, src, exported, pkgs, gidx, modulePath, root)...)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk %s: %v", root, walkErr)
	}

	ledger := r87ParseLedger(t, r87LedgerPath)
	var unregistered []r87Pair
	for _, c := range candidates {
		if _, ok := ledger[c.key()]; !ok {
			unregistered = append(unregistered, c)
		}
	}
	sort.Slice(unregistered, func(i, j int) bool {
		if unregistered[i].Name != unregistered[j].Name {
			return unregistered[i].Name < unregistered[j].Name
		}
		return unregistered[i].File < unregistered[j].File
	})
	for _, c := range unregistered {
		t.Errorf("事实源承载的 %q 被 %s 消费，而**腿 %q**（能到达该破坏性 SQL 的测试入口）"+
			"够不到任何目标库判定（破坏性 SQL：%s）⇒ "+
			"事实源给什么值这条腿都会清空那张表（数据事故级）⇒ "+
			"请给这条腿补目标库守卫（库名/host:port 判定），或登记进 %s 并写明判词",
			c.Name, c.File, c.Root, c.Stmt, r87LedgerPath)
	}

	var stale []string
	for key := range ledger {
		found := false
		for _, c := range candidates {
			if c.key() == key {
				found = true
				break
			}
		}
		if !found {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Errorf("台账 %s 里的 %q 现在探测不到了 ⇒ 台账腐化（请删行；该风险已消失）", r87LedgerPath, s)
	}

	t.Logf("事实源导出 %d 个名字；探测到 %d 条「破坏性且无目标库守卫」的腿，台账登记 %d 条（未登记 %d、腐化 %d）",
		len(exported), len(candidates), len(ledger), len(unregistered), len(stale))
}

// r87ModulePath 读 go.mod 的 module 行。
func r87ModulePath(t *testing.T, root string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// ── 门 ②：探测器自检（防恒绿门）─────────────────────────────────────────────

func TestR87DestructiveTargetDetectorSelfCheck(t *testing.T) {
	exported := r71ParseDatasourceExports(t, r86DSPath)
	if len(exported) < 30 {
		t.Fatalf("事实源只解析出 %d 个 export ⇒ 自检前提不成立", len(exported))
	}
	var name string
	for n := range exported {
		name = n
		break
	}
	dir := t.TempDir()
	pkgs := map[string]*r87PkgFacts{}
	var gidx map[string]*r91Node

	// ★ R89 轮：**每个夹具一个独立子目录**（= 独立包）。
	//
	// ★ 为什么必须这样：判据从 R88 轮起是**包级**的 ⇒⇒ 包索引按**函数名**建
	//   ⇒⇒ 若多个夹具共用一个目录且**函数重名**（本文件的老夹具全都叫 `f`），
	//   它们会被**合并成同一个函数** ⇒⇒ 一个夹具的守卫会污染另一个夹具
	//   ⇒⇒ 实测：夹具 ①（无守卫）被夹具 ②（有 current_database()）的同名 `f`
	//   合并 ⇒⇒ 判成有守卫 ⇒⇒ 夹具 ① 红。
	//   ⇒ ⇒ 每个夹具自成一个包 ⇒ 互不干扰。
	write := func(fixture, fn, body string) string {
		p := filepath.Join(dir, fixture, fn)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatalf("mkdir fixture dir: %v", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}

	// 夹具 ①：真读 + 破坏性 + **无守卫** ⇒ 必须被判成候选。
	f1 := write("fx1", "unguarded_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ②：真读 + 破坏性 + **有守卫**（current_database() 在前）⇒ 不得被判成候选。
	f2 := write("fx2", "guarded_dbname_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = \"SELECT current_database()\"\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ③：真读 + 破坏性 + **禁用串断言**（切片字面量，不是实参）⇒ 不得被判成候选。
	f3 := write("fx3", "forbidden_only_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\tfor _, forbidden := range []string{\"TRUNCATE public.some_table\"} {\n\t\t_ = forbidden\n\t}\n}\n")
	// 夹具 ④：真读 + 破坏性 + **自己建的对象** ⇒ 不得被判成候选。
	f4 := write("fx4", "self_created_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_, _ = os.ReadFile(\"CREATE TABLE public.some_table (id int)\")\n"+
		"\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ⑤：真读 + **SQL 文本断言**（字面量在 strings.Index 里）⇒ 不得被判成候选。
	f5 := write("fx5", "sql_text_assertion_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = strings.Index(\"down sql\", \"DROP TABLE IF EXISTS some_table\")\n}\n")
	// 夹具 ⑥：真读 + 破坏性写在**切片字面量**里、range 之后送进 Exec ⇒ **必须**被判成候选。
	f6 := write("fx6", "slice_exec_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f(t *testing.T, pool *pgxpool.Pool) {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\tfor _, stmt := range []string{\"TRUNCATE public.some_table\"} {\n"+
		"\t\t_, _ = pool.Exec(context.Background(), stmt)\n\t}\n}\n")
	// 夹具 ⑧：真读 + **散文里的关键字**（错误提示语里提到 DROP TABLE）⇒ 不得被判成候选。
	f8 := write("fx7", "prose_only_test.go", "package fx\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = fmt.Errorf(\"本测试会 DROP TABLE public.some_table CASCADE，只允许对可丢弃的测试库运行\")\n}\n")
	// 夹具 ⑦：真读 + 破坏性 + **先探后建**（已存在即跳过）⇒ 不得被判成候选。
	f7 := write("fx8", "probe_then_skip_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\tt.Skipf(\"%d of the fixture tables already exist — this test drops them\", 1)\n"+
		"\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")

	// ── 夹具 ⑨⑩：跨包守卫必须按**包自己声明的闸门名单**判定 ─────────────────
	//
	// ★ 这对夹具是 R87 轮变异 M1 的教训固化：
	//   变异「摘掉真正的闸门、只留核对函数」时，早期版本的门**仍然绿**
	//   ⇒ ⇒ 「这个包做过目标库判定」这种**包级**口径太粗 ⇒ 必须函数级。
	//
	//   造一个声明了闸门名单的包：
	//     gatepkg.Gate()   → 名单里的闸门（会决定腿能不能跑）
	//     gatepkg.Verify() → 不在名单里（只核对连接落点，不决定跑不跑）
	gateDir := filepath.Join(dir, "gatepkg")
	if err := os.MkdirAll(gateDir, 0o750); err != nil {
		t.Fatalf("mkdir gatepkg: %v", err)
	}
	gateSrc := "package gatepkg\n\n" +
		"var GuardFunctions = []string{\"Gate\"}\n\n" +
		"func Gate() string { return \"SELECT current_database()\" }\n\n" +
		"func Verify() string { return \"SELECT current_database()\" }\n"
	if err := os.WriteFile(filepath.Join(gateDir, "gate.go"), []byte(gateSrc), 0o600); err != nil {
		t.Fatalf("write gatepkg: %v", err)
	}
	// ★ R91 轮：包索引 + 全局图必须在**所有夹具写完之后**才建。
	//
	// ★ 为什么现在才变成硬要求：R87~R90 轮的判据只用**同包**可达
	//   ⇒⇒ 「当前文件自己的函数」由 `r89Merged` 的**本地优先**兜住
	//   ⇒⇒ 索引建得早一点也不影响读数。
	//   ⇒⇒ R91 轮改成**全局**可达后，判据要问「全局图里有没有这个节点」
	//   ⇒⇒ 索引必须**包含**所有夹具文件 ⇒⇒ 建早了 ⇒ 节点缺失 ⇒ **夹具恒绿**。
	//   ⇒⇒ ★ 与 R88 夹具同款纪律（「pkgs 在**所有夹具写完之后**才建」）。

	// 夹具 ⑨：调用**名单里的闸门** ⇒ 不得被判成候选。
	f9 := write("fx9", "declared_gate_test.go", "package fx\n\nimport (\n\t\"os\"\n\n"+
		"\"example.com/x/gatepkg\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = gatepkg.Gate()\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ⑩：只调用**不在名单里**的核对函数 ⇒ **必须**被判成候选。
	f10 := write("fx10", "verify_only_test.go", "package fx\n\nimport (\n\t\"os\"\n\n"+
		"\"example.com/x/gatepkg\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = gatepkg.Verify()\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")

	// ★ 包索引 + 全局图：**所有夹具写完之后**才建（见上面的说明）。
	pkgs, gidx = r87BuildPkgFacts(t, dir, "example.com/x", exported)

	if got := r87AnalyzeFile(t, f1, "unguarded_test.go", mustRead(t, f1), exported, pkgs, gidx, "example.com/x", dir); len(got) != 1 || got[0].Name != name {
		t.Fatalf("探测器没认出「真读 + 破坏性 + 无守卫」的夹具（应判 1 条，实得 %d 条：%v）⇒ 探测器失效（门会恒绿）", len(got), got)
	}
	if got := r87AnalyzeFile(t, f2, "guarded_dbname_test.go", mustRead(t, f2), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「有 current_database() 守卫」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f3, "forbidden_only_test.go", mustRead(t, f3), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「禁用串断言（切片字面量）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f4, "self_created_test.go", mustRead(t, f4), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「自己建的对象」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f5, "sql_text_assertion_test.go", mustRead(t, f5), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「SQL 文本断言（strings.Index）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f6, "slice_exec_test.go", mustRead(t, f6), exported, pkgs, gidx, "example.com/x", dir); len(got) != 1 {
		t.Fatalf("探测器没认出「切片字面量 + range + Exec」的破坏性形态（实得 %d 条：%v）⇒ 探测器有盲区（会漏掉真高危腿）", len(got), got)
	}
	if got := r87AnalyzeFile(t, f7, "probe_then_skip_test.go", mustRead(t, f7), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「先探后建（已存在即跳过）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f8, "prose_only_test.go", mustRead(t, f8), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「散文里提到 DROP TABLE」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f9, "declared_gate_test.go", mustRead(t, f9), exported, pkgs, gidx, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「调用了包自己声明的闸门」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f10, "verify_only_test.go", mustRead(t, f10), exported, pkgs, gidx, "example.com/x", dir); len(got) != 1 {
		t.Fatalf("探测器把「只调用核对函数（不在闸门名单里）」的夹具判成有守卫（实得 %d 条：%v）⇒ "+
			"跨包守卫口径太粗（包级 ⇒ 函数级）⇒ 变异 M1 那种「摘掉真闸门」就抓不到", len(got), got)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}
