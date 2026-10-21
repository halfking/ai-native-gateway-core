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

// r87DestructiveOf 判定一个 SQL 文本是不是硬破坏性，并取出被破坏的对象。
func r87DestructiveOf(sql string) (bool, []string) {
	if !r87ReDestructiveSQL.MatchString(sql) {
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
		if r87IsStringInspection(call) || r87IsMessageCall(call) {
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
		if r87IsStringInspection(call) || r87IsMessageCall(call) {
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
func r87BuildPkgFacts(t *testing.T, root string) map[string]*r87PkgFacts {
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
			facts = &r87PkgFacts{funcsWithCheck: map[string]bool{}, declaredGuards: map[string]bool{}}
			out[dir] = facts
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
	return out
}

// ── 单文件分析 ─────────────────────────────────────────────────────────────

// r87AnalyzeFile 判定一个测试文件是不是「破坏性且无目标库守卫」的候选腿。
func r87AnalyzeFile(t *testing.T, path, rel, src string, exported map[string]bool, pkgs map[string]*r87PkgFacts, modulePath, root string) []r87Pair {
	t.Helper()
	if !opensPostgresConn(src) {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil
	}
	consts := r87ConstStrings(f)

	// ① 真读事实源导出的 DSN 名。
	var names []string
	for _, n := range r71ScanFile([]byte(src)) {
		if exported[n] {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil
	}

	// ② 破坏性语句（排除自己建的对象）。
	self, createsDB, createsSchema := r87SelfCreatedObjects(f, fset, consts)
	probes := r87CollectProbes(fset, f)
	var live []r87DestructiveStmt
	for _, s := range r87DestructiveStmts(f, fset, consts) {
		skip := false
		for _, tg := range s.targets {
			if self[tg] {
				skip = true
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
	legs := r88AnalyzeLegs(f, fset, pkgs, filepath.Dir(path), modulePath, root, live)
	if len(legs) == 0 {
		return nil
	}
	var out []r87Pair
	seen := map[string]bool{}
	for _, leg := range legs {
		for _, n := range names {
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
}

// r88Leg 一条「没有被守卫支配」的破坏性腿。
type r88Leg struct {
	Stmt string
	Root string
}

// r88CollectFuncs 收集一个文件里的函数及其判定证据与调用关系。
func r88CollectFuncs(f *ast.File, fset *token.FileSet, pkgs map[string]*r87PkgFacts, dir, modulePath, root string) map[string]*r88FuncInfo {
	assigned := r87AssignedNodes(f)
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
		// 调用关系 + 跨包闸门。
		ast.Inspect(fd, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				fi.calls[fun.Name] = true
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
				facts := pkgs[d]
				if facts == nil {
					return true
				}
				// ★ 跨包闸门：**优先按包自己声明的名单**；包没声明才退回包级粗口径。
				guarded := false
				if len(facts.declaredGuards) > 0 {
					guarded = facts.declaredGuards[fun.Sel.Name]
				} else {
					guarded = facts.hasCheck
				}
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

// r88Reach 返回从 root 可达的同文件函数（含 root 自身）。
func r88Reach(root *r88FuncInfo, funcs map[string]*r88FuncInfo) map[string]*r88FuncInfo {
	seen := map[string]*r88FuncInfo{}
	queue := []*r88FuncInfo{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if _, dup := seen[cur.name]; dup {
			continue
		}
		seen[cur.name] = cur
		for name := range cur.calls {
			if g, ok := funcs[name]; ok {
				if _, dup := seen[g.name]; !dup {
					queue = append(queue, g)
				}
			}
		}
	}
	return seen
}

// r88Guarded 判断腿根是否被目标库判定支配。
func r88Guarded(root *r88FuncInfo, funcs map[string]*r88FuncInfo) bool {
	for _, f := range r88Reach(root, funcs) {
		if f.guarded || f.callsGate {
			return true
		}
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

// r88AnalyzeLegs 返回「没有被守卫支配」的破坏性腿。
func r88AnalyzeLegs(f *ast.File, fset *token.FileSet, pkgs map[string]*r87PkgFacts, dir, modulePath, root string, live []r87DestructiveStmt) []r88Leg {
	funcs := r88CollectFuncs(f, fset, pkgs, dir, modulePath, root)
	var out []r88Leg
	seen := map[string]bool{}
	for _, d := range live {
		g := r88Enclosing(d.pos, funcs)
		if g == nil {
			// 不在任何函数体里（包级 var 初始化）⇒ 本身就是一条腿。
			key := "<包级>\t" + d.sql
			if !seen[key] {
				seen[key] = true
				out = append(out, r88Leg{Stmt: d.sql, Root: "<包级>"})
			}
			continue
		}
		// 腿根 = 所有能到达该语句的测试入口。
		var roots []*r88FuncInfo
		for _, cand := range funcs {
			if !cand.isTest {
				continue
			}
			if _, ok := r88Reach(cand, funcs)[g.name]; ok {
				roots = append(roots, cand)
			}
		}
		if len(roots) == 0 {
			// 退化形态：没有任何测试入口能到达它 ⇒ 以所在函数为腿根。
			roots = []*r88FuncInfo{g}
		}
		for _, r := range roots {
			if r88Guarded(r, funcs) {
				continue
			}
			key := r.name + "\t" + d.sql
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r88Leg{Stmt: d.sql, Root: r.name})
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
	pkgs := r87BuildPkgFacts(t, root)

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
		candidates = append(candidates, r87AnalyzeFile(t, path, rel, src, exported, pkgs, modulePath, root)...)
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

	write := func(fn, body string) string {
		p := filepath.Join(dir, fn)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return p
	}

	// 夹具 ①：真读 + 破坏性 + **无守卫** ⇒ 必须被判成候选。
	f1 := write("unguarded_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ②：真读 + 破坏性 + **有守卫**（current_database() 在前）⇒ 不得被判成候选。
	f2 := write("guarded_dbname_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = \"SELECT current_database()\"\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ③：真读 + 破坏性 + **禁用串断言**（切片字面量，不是实参）⇒ 不得被判成候选。
	f3 := write("forbidden_only_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\tfor _, forbidden := range []string{\"TRUNCATE public.some_table\"} {\n\t\t_ = forbidden\n\t}\n}\n")
	// 夹具 ④：真读 + 破坏性 + **自己建的对象** ⇒ 不得被判成候选。
	f4 := write("self_created_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_, _ = os.ReadFile(\"CREATE TABLE public.some_table (id int)\")\n"+
		"\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ⑤：真读 + **SQL 文本断言**（字面量在 strings.Index 里）⇒ 不得被判成候选。
	f5 := write("sql_text_assertion_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"strings\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = strings.Index(\"down sql\", \"DROP TABLE IF EXISTS some_table\")\n}\n")
	// 夹具 ⑥：真读 + 破坏性写在**切片字面量**里、range 之后送进 Exec ⇒ **必须**被判成候选。
	f6 := write("slice_exec_test.go", "package fx\n\nimport (\n\t\"context\"\n\t\"os\"\n\t\"testing\"\n\n"+
		"\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f(t *testing.T, pool *pgxpool.Pool) {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\tfor _, stmt := range []string{\"TRUNCATE public.some_table\"} {\n"+
		"\t\t_, _ = pool.Exec(context.Background(), stmt)\n\t}\n}\n")
	// 夹具 ⑧：真读 + **散文里的关键字**（错误提示语里提到 DROP TABLE）⇒ 不得被判成候选。
	f8 := write("prose_only_test.go", "package fx\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = fmt.Errorf(\"本测试会 DROP TABLE public.some_table CASCADE，只允许对可丢弃的测试库运行\")\n}\n")
	// 夹具 ⑦：真读 + 破坏性 + **先探后建**（已存在即跳过）⇒ 不得被判成候选。
	f7 := write("probe_then_skip_test.go", "package fx\n\nimport (\n\t\"os\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
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
	pkgs = r87BuildPkgFacts(t, dir)

	// 夹具 ⑨：调用**名单里的闸门** ⇒ 不得被判成候选。
	f9 := write("declared_gate_test.go", "package fx\n\nimport (\n\t\"os\"\n\n"+
		"\"example.com/x/gatepkg\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = gatepkg.Gate()\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")
	// 夹具 ⑩：只调用**不在名单里**的核对函数 ⇒ **必须**被判成候选。
	f10 := write("verify_only_test.go", "package fx\n\nimport (\n\t\"os\"\n\n"+
		"\"example.com/x/gatepkg\"\n\t\"github.com/jackc/pgx/v5/pgxpool\"\n)\n\n"+
		"func f() {\n\t_ = os.Getenv(\""+name+"\")\n\t_ = pgxpool.New\n"+
		"\t_ = gatepkg.Verify()\n\t_, _ = os.ReadFile(\"TRUNCATE public.some_table\")\n}\n")

	if got := r87AnalyzeFile(t, f1, "unguarded_test.go", mustRead(t, f1), exported, pkgs, "example.com/x", dir); len(got) != 1 || got[0].Name != name {
		t.Fatalf("探测器没认出「真读 + 破坏性 + 无守卫」的夹具（应判 1 条，实得 %d 条：%v）⇒ 探测器失效（门会恒绿）", len(got), got)
	}
	if got := r87AnalyzeFile(t, f2, "guarded_dbname_test.go", mustRead(t, f2), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「有 current_database() 守卫」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f3, "forbidden_only_test.go", mustRead(t, f3), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「禁用串断言（切片字面量）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f4, "self_created_test.go", mustRead(t, f4), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「自己建的对象」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f5, "sql_text_assertion_test.go", mustRead(t, f5), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「SQL 文本断言（strings.Index）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f6, "slice_exec_test.go", mustRead(t, f6), exported, pkgs, "example.com/x", dir); len(got) != 1 {
		t.Fatalf("探测器没认出「切片字面量 + range + Exec」的破坏性形态（实得 %d 条：%v）⇒ 探测器有盲区（会漏掉真高危腿）", len(got), got)
	}
	if got := r87AnalyzeFile(t, f7, "probe_then_skip_test.go", mustRead(t, f7), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「先探后建（已存在即跳过）」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f8, "prose_only_test.go", mustRead(t, f8), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「散文里提到 DROP TABLE」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f9, "declared_gate_test.go", mustRead(t, f9), exported, pkgs, "example.com/x", dir); len(got) != 0 {
		t.Fatalf("探测器把「调用了包自己声明的闸门」的夹具也判成候选（%v）⇒ 判据过宽", got)
	}
	if got := r87AnalyzeFile(t, f10, "verify_only_test.go", mustRead(t, f10), exported, pkgs, "example.com/x", dir); len(got) != 1 {
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
