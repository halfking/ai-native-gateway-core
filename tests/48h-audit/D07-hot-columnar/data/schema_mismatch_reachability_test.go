// Package data - D07 数据测试：schema 不匹配类发现的**可达性**登记。
//
// ## 这道门解决什么
//
// 本轮连续两次踩同一个坑：**看到一段 SQL 与真库 schema 不匹配，就按「它挂在主链路上」
// 定级**。两次的结局都是「代码里写着，但它从来不会被执行」：
//
//   - `tool_usage_stats` 的 INSERT 有 12/15 个列名在真库不存在，按形态它挂在
//     `main.go:3265` 注册的 Hook 上，结论 P1；实际 `AggregateDaily` / `SaveStats` /
//     `ListToolNamesWithActivity` **各 0 个生产调用点**，那些列名从未被 PostgreSQL
//     执行过。
//   - `model_aliases.alias` 应为 `raw_name`，同理定 P1；实际 `reasoncap.NewPGSource`
//     零调用方，而唯一在用的 `paramguard/guard.go:386` 传的是
//     `reasoncap.Resolve(ctx, model, nil)`——第三个参数 `db DBSource` 是 **nil**，
//     `Resolve` 里 `if db != nil` 才走 `LookupReasoningCaps`，那条 SQL 根本到不了。
//
// 两者都是**潜伏陷阱**而不是当前故障：真接上线的那天会静默劣化。
// 这道门守的就是那一刻。
//
// ## 判据顺序（与「定级看实测」同源）
//
// ① 有无生产调用方（代码事实）→ ② 失败是否被静默吞掉 → ③ 表/库实际状态。
// **本门只管 ①**，②③ 是人工判断。凭 ③ 定级是错的：「表是空的」在无流量的
// 开发库上不是发现。
//
// ## 防的是「同名误判」
//
// 登记里的 symbol 一律**带包限定**。`reasoncap.NewPGSource` 在本仓有两个同名
// 函数（`pending.NewPGSource`、`credentialquota.NewPGSource`），不带限定就会被
// 误判成有调用方——这正是本轮的实际经历。`AggregateDaily` 同理：grep 命中的
// 全是 providerprofile 的 `AggregateDailyProfiles`。
//
// 跑测（无库即可，本门只读源码，不连库）：
//
//	go test -timeout 120s -run Reachability ./tests/48h-audit/D07-hot-columnar/data/...
package data

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"

	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// schemaMismatchFindings 登记「代码 SQL 与真库 schema 不匹配」的全部发现项，
// 每条带可达性判定与证据。**每条都必须能用 symbol 机械复核**——不能写只有人读得懂的
// 描述，那样这道门会在几年后变成一份没人验证的静态贴纸。
var schemaMismatchFindings = map[string]struct {
	symbol   string // 带包限定的符号，用作 grep 锚点
	wantHits int    // 期望的生产调用点数（排除 _test.go）
	evidence string // 人读证据：调用点位置，或零调用方的说明
}{
	"toolexecution.tool_usage_stats 12/15 列名不存在": {
		// 刻意用 `.AggregateDaily(` 而不是 `toolexecution.StatsAggregator`：后者在
		// 生产代码里**根本不存在**（main.go 用 import 别名 `te.`，写成
		// `te.StatsAggregator`），拿它当锚点会让 wantHits=0 被巧合满足而非真验证。
		// 前导点让它只匹配**调用**不匹配定义（定义是 `func (a *StatsAggregator)
		// AggregateDaily(`），且 _test.go 已被排除 ⇒ 生产零调用。
		symbol:   "*.AggregateDaily",
		wantHits: 0,
		evidence: "AggregateDaily / SaveStats / ListToolNamesWithActivity 各 0 个生产调用点。" +
			"grep AggregateDaily 命中的全是 providerprofile 的 AggregateDailyProfiles（另一个同名方法）。" +
			"失败模式：每个工具失败只 slog.Error 计数，函数返回 nil——补上定时任务后调用方会以为聚合成功",
	},
	"reasoncap.model_aliases.alias 应为 raw_name": {
		symbol:   "reasoncap.NewPGSource",
		wantHits: 0,
		evidence: "零调用方。唯一在用的 paramguard/guard.go:386 传 reasoncap.Resolve(ctx, model, nil)，" +
			"第三个参数 db DBSource 为 nil，Resolve 里 if db != nil 才走 LookupReasoningCaps。" +
			"注意 pending.NewPGSource 与 credentialquota.NewPGSource 是同名函数，别混。" +
			"失败模式：LookupReasoningCaps 出错时 Resolve 不返回 error，直接降级到 Tier 2 静态表——DB 覆盖永远不生效且不报错",
	},
	// 这两条原本分开登记，但门立刻指出它们共用一个 symbol 无法分别验证。核实后确认
	// 它们本来就是同一条路径：persistRunInTx 一次调用同时写 diagnostic_runs 与
	// routing_audit_log。**合并成一条比假精度更诚实**——分成两条只会让人误以为
	// 可以各自独立修复。
	"routeincident 同时写 diagnostic_runs.route_key 与 routing_audit_log.reason 两处缺列": {
		symbol:   "routeincident.NewObserver",
		wantHits: 1,
		evidence: "cmd/gateway/main.go:3554 telemetryClient.AddOnRequestLogPersisted(incidentObserver.AsHook())，" +
			"每条落库请求日志都触发；Transition → writeAudit → persistRunInTx 一次写两张表。" +
			"真库 routing_audit_log 实测 **21 列**（我先前误记 22，由迁移 758 的回滚复核暴露）里有 failure_reason 但没有 reason——两张表都缺。" +
			"失败模式：MaxRetries=4 ⇒ 42703（永久性错误）也会重试满 5 次，耗尽后只 slog.Warn 不升级",
	},
	"sessionsummary.session_turns 视图漏投影 origin_actor": {
		symbol:   "sessionsummary.NewPerTurnDigestSource",
		wantHits: 1,
		evidence: "cmd/gateway/main_pipeline.go:1416 SetMessageSource(NewPerTurnDigestSource(pool))，" +
			"且 main_pipeline.go:1185 挂了 SessionMetadataCloseHook——两条路径都引用 t.origin_actor",
	},
}

// expectedSchemaMismatchFindings 是棘轮：新增或移除登记项都要同步它。
const expectedSchemaMismatchFindings = 4

// TestData_SchemaMismatch_ReachabilityIsStillAccurate 复核每条登记的可达性判定
// 是否与今天代码里的事实一致。
//
// 三种红法，都指名到具体条目：
//   - 登记 ORPHAN 但现在找得到调用点 ⇒ 有人把它接上线了，**那 12 个错列会立刻生效**，
//     这是本门存在的理由，必须红。
//   - 登记 LIVE 但现在找不到调用点 ⇒ 登记过期/被摘掉，红以免它悄悄变成潜伏陷阱。
//   - 条数与棘轮不符 ⇒ 有人增删登记没同步常量。
func TestData_SchemaMismatch_ReachabilityIsStillAccurate(t *testing.T) {
	if n := len(schemaMismatchFindings); n != expectedSchemaMismatchFindings {
		t.Errorf("schemaMismatchFindings 有 %d 条，但棘轮 expectedSchemaMismatchFindings = %d。"+
			"新增发现要登记并同步棘轮；已修复的发现要删掉登记并调小棘轮。", n, expectedSchemaMismatchFindings)
	}

	// 按 symbol 分组：同一个 symbol 可能登记多条（如 routeincident 的两个表）。
	needles := map[string]string{}
	for name, f := range schemaMismatchFindings {
		if prev, dup := needles[f.symbol]; dup {
			t.Fatalf("登记「%s」与「%s」用了同一个 symbol %q，无法分别验证可达性——"+
				"请给其中一条换一个更精确的锚点", name, prev, f.symbol)
		}
		needles[f.symbol] = name
	}

	hits := countProductionCallSites(t, needles)

	names := make([]string, 0, len(schemaMismatchFindings))
	for name := range schemaMismatchFindings {
		names = append(names, name)
	}
	sort.Strings(names)

	orphans, live := 0, 0
	for _, name := range names {
		f := schemaMismatchFindings[name]
		where := hits[f.symbol]
		got := len(where)
		if got != f.wantHits {
			if f.wantHits == 0 && got > 0 {
				t.Errorf("登记「%s」为无生产调用方，但今天找到 %d 处调用：%s\n"+
					"    这意味着那段与真库不匹配的 SQL 现在会执行了，**这是本门存在的理由**。\n"+
					"    请先修 schema 契约，再把 wantHits 与 evidence 更新为已接线的事实。",
					name, got, strings.Join(where, ", "))
			} else if f.wantHits > 0 && got == 0 {
				t.Errorf("登记「%s」为有活接线（期望 %d 处），但今天一处也找不到。\n"+
					"    它可能已被摘掉或改名——**别让它悄悄退化成潜伏陷阱**，"+
					"请改判为无调用方并重估定级。", name, f.wantHits)
			} else {
				t.Errorf("登记「%s」的可达性判定与实测不符：期望 %d 处调用，实测 %d 处（%s）",
					name, f.wantHits, got, strings.Join(where, ", "))
			}
			continue
		}
		if f.wantHits == 0 {
			orphans++
		} else {
			live++
		}
	}
	t.Logf("schema 不匹配登记 %d 条：活接线 %d，潜伏 %d（本门只在潜伏项被接线时转红）",
		len(schemaMismatchFindings), live, orphans)
}

// countProductionCallSites 对每个 needle 统计**生产**调用点，排除 _test.go。
//
// 用 go/parser 在 **AST 层面**找 SelectorExpr（`pkg.Symbol`），不用文本匹配。
// 两次踩坑都指向同一个根因——文本匹配不区分代码、注释和字符串：
//
//  1. 不剥注释时，仓内大量注释提到函数名（credentialquota 的
//     `// NewPGSource wraps a *pgxpool.Pool...`）→ 假阳性。
//  2. 用正则剥注释时，`//` 与 `/*` 会在字符串字面量里出现（`"https://..."`、
//     SQL 注释），正则会从那里一路吞到行尾/下一个 `*/`，**把真实代码一起删掉** →
//     假阴性。剥离注释后 `routeincident.NewObserver` 从 1 变 0 就是这么来的。
//
// AST 层面找 SelectorExpr 两个问题一起消失：注释和字符串根本不会进 AST。
func countProductionCallSites(t *testing.T, needles map[string]string) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	out := map[string][]string{}
	var conflicted []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", ".git", "web", "installer", "docs", "sql", "deploy", "scripts", "skills":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		// 本门自己的源码必须排除：注释里写了这些 symbol，会把自己当证据。
		if strings.HasPrefix(rel, "tests/48h-audit") {
			return nil
		}
		fset := token.NewFileSet()
		raw, rerr2 := os.ReadFile(path)
		if rerr2 != nil {
			return rerr2
		}
		// 冲突中间态（并发会话遗留）与代码缺陷是两种红因，必须分开报：
		// 前者是工作区瞬时状态，后者是仓库真问题。合成一条会让人分不清该找谁。
		if bytes.Contains(raw, []byte("<<<<<<<")) {
			conflicted = append(conflicted, rel)
			return nil
		}
		file, perr := parser.ParseFile(fset, path, raw, 0)
		if perr != nil {
			// 解析失败必须让它显形，不能静默跳过——那会让「0 调用点」变成
			// 「我没能读懂这个文件」，两者后果完全不同。
			t.Errorf("解析 %s 失败（%v）：可达性结论不可信，不能当作零调用点", rel, perr)
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			qualified := ident.Name + "." + sel.Sel.Name
			if _, want := needles[qualified]; want {
				out[qualified] = append(out[qualified], rel)
			}
			// 无包前缀的方法调用（`x.AggregateDaily(...)` 的 AST 形态是
			// SelectorExpr{X: Ident(x), Sel: AggregateDaily}，上面已覆盖）。
			// `*.AggregateDaily` 用通配记号表示「任意接收者上的同名方法」。
			qualified = "*." + sel.Sel.Name
			if _, want := needles[qualified]; want {
				out[qualified] = append(out[qualified], rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}
	if len(conflicted) > 0 {
		// 这些文件当前处于 git 冲突中间态，解析不了，本次结论对它们无效。
		// 报出来而不是静默跳过——覆盖率缺口必须可见。
		t.Errorf("工作区有 %d 个文件处于冲突中间态，本次可达性结论对它们无效：\n  %s\n"+
			"这是并发会话/合并留下的瞬时状态，不是代码缺陷；冲突解决后本门会自动覆盖它们。",
			len(conflicted), strings.Join(conflicted, "\n  "))
	}
	return out
}
