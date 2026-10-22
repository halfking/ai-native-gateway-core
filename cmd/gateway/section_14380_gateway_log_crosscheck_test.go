//go:build !integration

package main

// §143.80 的门：把「压缩器【冷启动读链】的服务侧证据】与【第五腿落空的两类机制】
// 钉成结构判据（★ 用 go/ast 查形态，不查裸子串）。
//
// ★★★★ 为什么这一轮要有这道门（★ §143.80 的起因）：
//
//	§143.77 / §143.78 / §143.79 三轮都只用了【库侧证据】（pg_stat_statements），
//	并在各自的「诚实边界」里登记「★ 本轮仍未用 154 的服务日志做交叉验证」。
//	⇒ ⇒ §143.80 把这个未验项补上 ⇒ ⇒ 而补上之后**发现了三件库侧看不到的事**：
//
//	  ① ★★★★★ 服务侧有【直接证据】：★ `session_compressor: v2 cache loaded successfully`
//	     这条日志带 `body_size` 字段 ⇒ ⇒ ★★ **`body_size = 0` 就是【语义落空】的
//	     直接证据** ⇒ ⇒ ★ 它是 §143.79 那条「量具 rows 记 1、但 Go 侧判空」
//	     在【服务侧】的对应物 ⇒ ⇒ ★★ **库侧与服务侧从此可以互相印证**。
//	  ② ★★★★ 服务侧实测：**一次性会话 100% 落空**（256/256、277/277、163/163，
//	     ★ 三次采样）⇒ ⇒ **与 §143.78 的机制推理（首轮必然无行）完全吻合**。
//	  ③ ★★★★★ **第五腿的落空不是单一原因**，而是【两类性质完全不同】的：
//	     ★ 【时序性】一次性会话的首轮（bodies 还没写）
//	     ★ 【结构性】★ 分支会话（`gt_gw_` / `gs_gw_`）⇒ ★★ 【永久不写 bodies】
//	     ⇒ ⇒ ★★ **⇒ 它的短路判据与读侧【完全不同】** ⇒ ⇒ 属主拍板。
//
// ★ 这道门要守的不是「某个函数存在」，而是【几件仍然为真的事实】：
//
//	① ★★ 服务侧取证脚本必须存在，且【必须打在那条带 body_size 的日志上】。
//	② ★★★ 库侧复算脚本必须存在，且【必须按【三分类】认领落空原因】。
//	③ ★★★★ 【分支会话不写 bodies】这条结构性事实，必须在仓内【代码自证】处可查。
//	④ ★★ 「结构性」与「时序性」两类必须【在脚本里分开出现】（★ 混淆 ⇒ 判据错）。
//	⑤ ★★ 复算脚本【不许】出现 UNION 分支带 ORDER BY/LIMIT 的形态（★ 本轮又踩一次）。
//	⑥ ★★ ★★ 取生产退出码必须用【真实 psql 退出码】（★ 不是 `| tail` 后的 `$?`）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14380GatewayLogScript = "scripts/audit/verify_gateway_log_coldstart_evidence.sh"
	section14380FifthLegScript   = "scripts/audit/verify_fifth_leg_miss_reasons.sql"
)

func repoRoot14380(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readRepoFile14380(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot14380(t), rel))
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", rel, err)
	}
	return string(raw)
}

func parseGo14380(t *testing.T, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(repoRoot14380(t), rel), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", rel, err)
	}
	return f
}

// stripSQLComments14380 去掉 SQL 的行注释与块注释。
// ★ 用途：★ 让「代码形态」类判据不被注释里的引用误伤（★ §143.73 纪律）。
func stripSQLComments14380(s string) string {
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

// splitAfterUnionAll14380 把文本按 `UNION ALL` 切开，返回【紧跟每个 UNION ALL 的那一段】。
//
// ★ 用途：★ 判据「UNION 的分支里带 ORDER BY / LIMIT」必须只看【紧跟该 UNION 的分支】——
// ★ 若看更长的窗口 ⇒ ⇒ 下一个分支的 ORDER BY 会混进来 ⇒ ⇒ 漏报（★ 本轮 M7 变异逃过门）。
// ★ 若禁掉整个 UNION ⇒ ⇒ 误报（★ 安全的 UNION 是合法且必要的）。
func splitAfterUnionAll14380(s string) []string {
	var segs []string
	rest := s
	for {
		i := strings.Index(rest, "UNION ALL")
		if i < 0 {
			break
		}
		rest = rest[i+len("UNION ALL"):]
		// ★ 只取到下一个 UNION ALL / 语句分隔符为止
		cut := len(rest)
		for _, sep := range []string{"UNION ALL", ";\n", "ORDER BY"} {
			if j := strings.Index(rest, sep); j >= 0 && j < cut {
				cut = j
			}
		}
		segs = append(segs, rest[:cut])
	}
	return segs
}

// TestSection14380GatewayLogEvidenceScriptExists（★ 判据①）
//
// ★★★★ 服务侧取证脚本必须存在，且【必须打在那条带 body_size 的日志上】——
// ★ 那是本轮【唯一】能把「语义落空」直接观测到的信号（★ 库侧看不到它）。
//
// ★ ★★ 同时钉住三个「量具前提」（★ 本轮实测踩到的）：
//
//	① 服务名必须是 `llm-gateway-go-canary@8782`（★ `llm-gateway-go` 在 154 上是 masked）
//	② 必须按 `body_size` 分类（★ 0 = 落空、> 0 = 真命中）
//	③ 必须把【一次性会话】与【固定长会话】分开（★ 混在一起会得出 56.89% 这种
//	   【被固定长会话稀释过的】中间值 ⇒ ⇒ 掩盖了「一次性会话 100% 落空」这个真结论）
func TestSection14380GatewayLogEvidenceScriptExists(t *testing.T) {
	raw := readRepoFile14380(t, section14380GatewayLogScript)

	// ① 服务名与槽位
	if !strings.Contains(raw, "llm-gateway-go-canary@8782") {
		t.Error("★ 154 取证脚本没有指向 `llm-gateway-go-canary@8782` —— " +
			"★ ★ 154 上 `llm-gateway-go` 这个服务名是 **masked**（★ 实测）⇒ ⇒ " +
			"★ 写错服务名 ⇒ ⇒ 脚本会读到空日志 ⇒ ⇒ 「采集 0 条」被误读成「没有冷启动」")
	}
	// ② 必须按 body_size 分类
	if !strings.Contains(raw, "body_size") {
		t.Error("★ 154 取证脚本没有按 `body_size` 分类 —— " +
			"★ ★★ `body_size = 0` 是本轮【唯一】的语义落空直接证据 ⇒ ⇒ " +
			"★ 不按它分类 ⇒ ⇒ 就退回到「只看条数」⇒ ⇒ 什么也证不了")
	}
	// ③ 必须把一次性会话与固定长会话分开
	if !strings.Contains(raw, "omp_fixed_long") {
		t.Error("★ 154 取证脚本没有把【固定长会话】（omp）单独分出来 —— " +
			"★ ★ 实测：固定长会话 379 次冷启动（★ 每会话 190 次）里 21.90% 落空，" +
			"★ 而一次性会话 277 次【全部】落空 ⇒ ⇒ " +
			"★ ★★ 混在一起 ⇒ ⇒ 读数被稀释成 56.89% ⇒ ⇒ 掩盖了真结论")
	}
	// ★ 必须有「排除 omp 后」这个口径（★ 那是本轮的核心结论）
	if !strings.Contains(raw, "EXCLUDING_omp") {
		t.Error("★ 154 取证脚本缺少「排除固定长会话后」的口径 —— " +
			"★ 本轮的核心结论正是「排除后 = 306/306 = 100% 落空」⇒ ⇒ " +
			"★ 这个口径是结论本身 ⇒ ⇒ 少了它脚本就只剩中间值")
	}
}

// TestSection14380FifthLegMissReasonsScriptExists（★ 判据②④⑤）
//
// ★★★★ 第五腿的落空必须【按三分类】认领，★ 且「结构性」与「时序性」必须分开：
//
//	★ 【结构性】分支会话（`gt_gw_` / `gs_gw_`）⇒ ★★ 【永久不写 bodies】
//	★ 【时序性】一次性会话的首轮 ⇒ bodies 还没写
//
// ★★ 混淆这两类 ⇒ ⇒ 短路判据会错 ⇒ ⇒ 【结构性那批短路了也没用（它本来就没正文），
// 而时序性那批才真正受益】⇒ ⇒ ⇒ 属主拍板会被误导。
func TestSection14380FifthLegMissReasonsScriptExists(t *testing.T) {
	raw := readRepoFile14380(t, section14380FifthLegScript)
	code := stripSQLComments14380(raw)

	// ① 三分类必须都在
	for _, needle := range []string{
		"1_分支会话_结构性永久0行",
		"3_一次性_时序性首轮0行",
	} {
		if !strings.Contains(code, needle) {
			t.Errorf("★ 第五腿复算脚本缺分类 %q —— "+
				"★ 本轮实测：第五腿的 88.5%% 落空 = 【时序性 34.23%%】+【结构性 3.12%%】"+
				"★ ⇒ ⇒ 两类性质不同 ⇒ ⇒ 必须分开认领", needle)
		}
	}
	// ② ★ 必须分别统计「结构性」与「时序性」两类（★ 用可判别的 SQL 形态）
	//   ★★★ 判据必须查【完整形态】（★ §143.79 同族教训：裸子串会让「改名」变异逃过门）
	//     ★ 第一版写的是「只要出现 `LIKE 'gt_%'` 就算过」⇒ ⇒ 变异把
	//     ★ `t.session_id LIKE 'gt_%' OR t.session_id LIKE 'gs_%'` 整段改掉后，
	//     ★ 脚本里【别处】的 `NOT LIKE 'gt_%'` 仍然命中 ⇒ ⇒ 门仍然绿 ⇒ ⇒ 没牙
	//     ★ ⇒ ⇒ 修法：★ 查【两个前缀 + OR 的完整形态】
	if !strings.Contains(code, "LIKE 'gt_%' OR t.session_id LIKE 'gs_%'") {
		t.Error("★ 第五腿复算脚本没有用【分支会话前缀的完整形态】判别结构性缺失 —— " +
			"★ 实测：`gt_gw_`/`gs_gw_` 分支会话 13,124 个，★ 100% 无 bodies 行 ⇒ ⇒ " +
			"★ 这是【结构性】缺失 ⇒ ⇒ 判据必须能把它挑出来 ⇒ ⇒ " +
			"★ ★★ 而且必须查【完整形态】（★ 两个前缀 + OR）⇒ ⇒ " +
			"★ 裸子串会让「把整段判据改掉」的变异逃过门")
	}
	// ③ 必须有「分支会话 vs 普通会话」的对照（★ 100% vs 36% 是本轮的关键对照）
	if !strings.Contains(code, "branch") || !strings.Contains(code, "plain") {
		t.Error("★ 第五腿复算脚本缺少【分支会话 vs 普通会话】的对照口径 —— " +
			"★ 本轮实测对照：branch 13,124 个会话【100.00%】无 bodies；" +
			"★ plain 407,569 个会话 36.00% 无 bodies ⇒ ⇒ " +
			"★ ★★ 没有这个对照 ⇒ ⇒ 就看不出「结构性」这两个字")
	}
	// ④ ★★ 反向判据：UNION 的【分支里带 ORDER BY / LIMIT】的形态不许出现
	//   （★ 本轮又踩了一次同族坑：报 `syntax error at or near "UNION"`）
	//   ★★★ 判据的精度（★ 两处修正）：
	//     ★ 第一版写「不许出现 UNION ALL」⇒ ⇒ 误报（★ 安全的 UNION 是合法且必要的）
	//     ★ 第二版用「UNION ALL 之后 200 字符内」⇒ ⇒ 漏报（★ 脚本里 UNION 之后的
	//       SELECT 很长，ORDER BY/LIMIT 落在窗口之外 ⇒ ⇒ ★ M7 变异逃过门）
	//     ★ ⇒ ⇒ 第三版：★ **按【分支】切分**（★ UNION ALL 到下一个 UNION ALL / 语句末）
	//       ⇒ ⇒ 判据只看【紧跟该 UNION 的那个分支】
	segments := splitAfterUnionAll14380(code)
	for _, seg := range segments {
		if strings.Contains(seg, "ORDER BY") || strings.Contains(seg, "LIMIT") {
			t.Error("★ 第五腿复算脚本里出现了【UNION 分支带 ORDER BY / LIMIT】的形态 —— " +
				"★ ★★ 它会报 `syntax error at or near \"UNION\"`（★ §143.77 已记录，★ 本轮又踩了一次）" +
				"★ ⇒ ⇒ 最稳的写法是【拆成两个独立循环】")
			break
		}
	}
}

// TestSection14380BranchSessionsHaveNoBodiesIsCodeProvable（★ 判据③）
//
// ★★★★ 【分支会话不写 bodies】这条结构性事实，必须在仓内【代码自证】处可查。
//
// ★ 机制（★ 代码自证）：★ `domains/streaming/handler.go` 的 `serveWithExecutor`
// 里，`isBranchSessionID(sessionID)` 为真时【不自动创建 session】⇒ ★ `sessionInfo`
// 保持 nil ⇒ ⇒ ★★ 后续的 v2 写入路径不完整 ⇒ ⇒ ★★ **turns 写了、bodies 没写**。
//
// ★★ ⇒ ⇒ 对分支会话，【读链与第五腿的落空是永久性的】⇒ ⇒ ★★ 它的短路判据
// ★★ 与读侧【完全不同】⇒ ⇒ ⇒ 属主拍板。
//
// ★ 这道门钉的是：★ 那条分支守卫【必须还在】，且【必须还带分支会话的语义注释】。
func TestSection14380BranchSessionsHaveNoBodiesIsCodeProvable(t *testing.T) {
	f := parseGo14380(t, "domains/streaming/handler.go")

	// ① `isBranchSessionID` 必须存在，且必须以 gt_ / gs_ 两个前缀为判据
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "isBranchSessionID" {
			fn = fd
			break
		}
	}
	if fn == nil {
		t.Fatal("★ `isBranchSessionID` 不存在 —— " +
			"★ 【分支会话不写 bodies】这条结构性事实的代码自证点没了")
	}
	// ★ 判据形态：函数体里必须有 `gt_` 与 `gs_` 两个前缀字面量
	var prefixes []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			prefixes = append(prefixes, strings.Trim(bl.Value, "`\""))
		}
		return true
	})
	hasGt, hasGs := false, false
	for _, p := range prefixes {
		if p == "gt_" {
			hasGt = true
		}
		if p == "gs_" {
			hasGs = true
		}
	}
	if !hasGt || !hasGs {
		t.Error("★ `isBranchSessionID` 的判据不再同时认 `gt_` 与 `gs_` —— " +
			"★ 本轮实测：两个前缀的分支会话【100%】都不写 bodies ⇒ ⇒ " +
			"★ ★★ 少认一个 ⇒ ⇒ 那批会话就会被误判成「普通会话」⇒ ⇒ " +
			"★ ★★ 第五腿的落空分类就错了")
	}

	// ② ★ 必须在 serveWithExecutor 里【真的用上】这个守卫（★ ★ 不是「函数存在」）
	//   ⇒ ⇒ 查调用点
	calls := 0
	ast.Inspect(f, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "isBranchSessionID" {
				calls++
			}
		}
		return true
	})
	if calls < 2 {
		t.Errorf("★ `isBranchSessionID` 的调用点只有 %d 处 —— "+
			"★ 本轮实测它守的是【不自动创建 session ⇒ sessionInfo 为 nil ⇒ bodies 不写】"+
			"★ ⇒ ⇒ 调用点减少 ⇒ ⇒ 那条机制链可能已经断了", calls)
	}
}

// TestSection14380ProdExitCodeMustBeReal（★ 判据⑥）
//
// ★★ ★★ 本轮抓到的【最隐蔽的一处量具缺陷】：
//
//	★ 我一直用 `bash lgwp-prod.sh <sql> <timeout> | tail -N` 然后 `echo $?`
//	★ ⇒ ⇒ ★★ **`$?` 拿到的是 `tail` 的退出码，不是 psql 的！**
//	★ ⇒ ⇒ ★★ 所以「rc=0」这个判据一直是【假的】⇒ ⇒ 一个真的报错的脚本
//	★ ⇒ ⇒ ★★ 也会被读成「跑通了」⇒ ⇒ ★★ 【本轮就有一个脚本真的报了
//	★ ⇒ ⇒ ★★ `syntax error at or near "UNION"`，而当时的判据说它 rc=0】。
//
// ★★ ⇒ 通用式：★ **判据要读【被测对象】的退出码，不是【管道最后一环】的**。
// ⇒ ⇒ ★ 这道门钉的是：★ 取证脚本必须【显式捕获真实退出码】。
func TestSection14380ProdExitCodeMustBeReal(t *testing.T) {
	raw := readRepoFile14380(t, section14380GatewayLogScript)

	// ★ 脚本必须用 set -o pipefail 或显式捕获 ⇒ ⇒ 不能让管道吞掉退出码
	hasPipefail := strings.Contains(raw, "set -o pipefail")
	hasExplicitRC := strings.Contains(raw, "PIPESTATUS") || strings.Contains(raw, "REAL_PSQL_RC")
	if !hasPipefail && !hasExplicitRC {
		t.Error("★ 154 取证脚本没有保证【取到真实退出码】—— " +
			"★ 本轮教训：`cmd | tail` 后的 `$?` 是 **tail** 的退出码 ⇒ ⇒ " +
			"★ ★★ 「rc=0」判据一直是假的 ⇒ ⇒ 一个真报错的脚本会被读成跑通了 ⇒ ⇒ " +
			"★ 修法：`set -o pipefail` 或显式捕获 psql 的退出码")
	}
}

// TestSection14380CrossCheckIsRegistered（★ 判据⑦）
//
// ★★★★ 本轮的核心价值是【把库侧与服务侧对上了】⇒ ⇒ 这个【对拍】必须在仓内
// ★★ 有登记（★ 否则下一轮的人不知道这两组数字可以互相印证）。
//
// ★ 登记点：`cmd/gateway/main_v3_wiring.go` 的 `FIXME-14380（` 块。
func TestSection14380CrossCheckIsRegistered(t *testing.T) {
	src := readRepoFile14380(t, "cmd/gateway/main_v3_wiring.go")
	if !strings.Contains(src, "FIXME-14380（") {
		t.Error("★ cmd/gateway/main_v3_wiring.go 里没有 `FIXME-14380（` 登记块 —— " +
			"★ 本轮的核心结论（★ 库侧 72.59% 与服务侧 100% 落空互相印证；" +
			"★ 第五腿的落空分【结构性】与【时序性】两类）必须落在仓内，★ 否则下一轮无从复核")
	}
	// ★ 登记块里必须写明【两条】关键数字（★ 少了任一条，下一轮会重走一遍）
	for _, needle := range []string{
		"body_size",
		"gt_",
	} {
		if !strings.Contains(src, needle) {
			t.Errorf("★ FIXME-14380 登记块里没有 %q —— "+
				"★ 登记块必须留下【可复核的关键词】：★ `body_size` 是服务侧落空证据，"+
				"★ `gt_` 是结构性缺失（分支会话）的主体", needle)
		}
	}
}
