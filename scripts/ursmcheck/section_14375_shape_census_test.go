package ursm_test

// §143.75 的门：把「形态普查 + 读方受害面 + 工具能力边界」这几件事钉成静态判据
// （★ 只读仓内源码 ⇒ 不依赖生产状态、不含任何生产读数）。
//
// ★ 为什么这道门要写（§143.75 的起因）：
//
//	§143.73/§143.74 定位并实测了「形态 A ⇒ 每轮整份落盘 ⇒ 二次方放大」，
//	★ 本轮把它【校准成决策可用的量】：
//	  ① 全库形态普查（10 月分区，只读）：
//	     形态 A = 188,696 会话 / 192,087 轮 / 972 MB；
//	     形态 B = 63,947 会话 / 86,215 轮 / 1.66 GB；
//	  ② ⇒ 真正【可回收】（形态 A 的多轮会话只留最后一轮）= ★★ **501 MB**，
//	     其中 ★★ **96.7% 来自 15 个 >50 轮的会话**；
//	  ③ ★★ 读方受害面**收敛**：`sessionsummary` 读侧早在注释里就写明
//	     「request_delta holds only the messages new to that turn」，并用
//	     「只取每轮最后一条」的 workaround ⇒ ⇒ **它对形态 A 本来就是对的**；
//	     ⇒ ⇒ **唯一受害的读路径 = 压缩器冷启动的 `TurnReader.LoadChain`**（累加所有轮）；
//	  ④ ★★ 而回填工具**自己知道**「拿增量再拆增量是恒等操作」⇒ ⇒ **它修不了形态 A**。
//
// ★★ 为什么判据全是【能力边界 / 反向断言】（★ 关键）：
//	★★ 本轮的门要守的不是「某个函数存在」，而是【几件仍然为真/仍然不成立的事实】：
//	★★ ① 差分内核**仍是前缀差分** ⇒ ⇒ ⇒ **形态 A 的历史就用它修不了**（★ 反向断言：
//	★★   一旦有人把它改成「直接返回全量」，形态 A 就有修法了 ⇒ ⇒ FIXME 与本门都要撤）；
//	★★ ② 回填工具**自述的恒等操作**仍在（★ 那是「为何历史没被修好」的证据原文）；
//	★★ ③ `sessionsummary` 的「只取最后一条」workaround 仍在（★ 读侧受害面收敛的锚点；
//	★★   一旦改成取全部 ⇒ 形态 A 会在摘要侧累加 ⇒ ⇒ 受害面扩大 ⇒ 需同步 FIXME）；
//	★★ ④ 复算脚本的【两个口径】都在（★ 形态标记 + 可回收量）。
//
// ★ 本门只读仓内源码 ⇒ **不依赖生产状态、不含任何生产读数**。

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	section14375DerivePath = "cmd/tools/backfill_session_bodies/derive.go"
	section14375ToolPath   = "cmd/tools/backfill_session_bodies/main.go"
	section14375SumPath    = "domains/sessionsummary/message_source_v2.go"
	section14375AuditPath  = "scripts/audit/verify_session_bodies_shape_census.sql"
)

func repoRoot14375(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func parseGo14375(t *testing.T, path string) (*ast.File, *token.FileSet) {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	fset := token.NewFileSet()
	// ★ 必须带 parser.ParseComments：★ 不带的话注释【根本不会进 AST】
	//   （f.Doc 与 f.Comments 恒为空）⇒ ⇒ 任何「判某条登记是否还在」的判据
	//   都会【恒假】⇒ ⇒ 门会一直红（假红）⇒ ★ 解析器与判据要配套。
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	return f, fset
}

func nodeText14375(fset *token.FileSet, n ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, n); err != nil {
		return ""
	}
	return buf.String()
}

func funcDecl14375(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if ok && d.Name != nil && d.Name.Name == name {
			return d
		}
	}
	t.Fatalf("找不到函数 %s", name)
	return nil
}

// commentGroupHasAll14375 报告是否存在某个注释组同时含有全部 needle。
func commentGroupHasAll14375(f *ast.File, needles ...string) bool {
	groups := make([]*ast.CommentGroup, 0, len(f.Comments)+1)
	if f.Doc != nil {
		groups = append(groups, f.Doc)
	}
	groups = append(groups, f.Comments...)
	for _, cg := range groups {
		txt := cg.Text()
		all := true
		for _, n := range needles {
			if !strings.Contains(txt, n) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestSection14375DeriveIsStillPrefixDiff pins 判据 ①：
// 差分内核必须仍是【前缀差分】（`DeriveTurnDeltas` 调用 `subtractMessages`，
// 且 `subtractMessages` 的身份比较基于 role + content）。
//
// ★ 反向断言：★ 一旦有人把它改成「直接返回全量」，★ **形态 A 的历史就有了修法**
// ★ ⇒ ⇒ 届时 FIXME-14375 与本门都必须撤 ⇒ ⇒ 门红。
func TestSection14375DeriveIsStillPrefixDiff(t *testing.T) {
	root := repoRoot14375(t)
	f, fset := parseGo14375(t, filepath.Join(root, section14375DerivePath))

	derive := nodeText14375(fset, funcDecl14375(t, f, "DeriveTurnDeltas"))
	if !strings.Contains(derive, "subtractMessages(") {
		t.Fatal("★ DeriveTurnDeltas 不再调用 subtractMessages ⇒ ★ 差分语义已被换掉 ⇒ " +
			"⇒ （★ 若这是有意的修复 ⇒ 形态 A 的历史就可用本工具修 ⇒ 请同步撤掉 FIXME-14375 与本门）")
	}

	// ★ 身份比较在 msgKey 里（★ 早期版本写成在 subtractMessages 内联比较 ⇒
	// ★ 判据要认【结构】而不是行号/文本位置）。
	sub := nodeText14375(fset, funcDecl14375(t, f, "subtractMessages"))
	if !strings.Contains(sub, "msgKey(") {
		t.Fatal("★ subtractMessages 不再用 msgKey 做身份比较 ⇒ 差分的「一对一消耗」语义变了 ⇒ 请同步复核 FIXME-14375")
	}
	key := nodeText14375(fset, funcDecl14375(t, f, "msgKey"))
	for _, needle := range []string{"Role", "Content"} {
		if !strings.Contains(key, needle) {
			t.Fatalf("msgKey 里缺少身份字段 %q（★ 身份 = (role, content) 是前缀差分的定义）", needle)
		}
	}
}

// TestSection14375ToolSelfKnowsItIsIdempotent pins 判据 ②：
// 回填工具的自述「拿增量再拆增量是【恒等操作】」必须仍在（★ 那是「为何历史没被修好」的证据原文）。
func TestSection14375ToolSelfKnowsItIsIdempotent(t *testing.T) {
	root := repoRoot14375(t)
	f, _ := parseGo14375(t, filepath.Join(root, section14375ToolPath))
	// ★★ needle 必须选【只有原文自述才有】的那句（★ 「等于把已有数据原样写回」）：
	// ★★ 若用「恒等操作」当 needle ⇒ ★★ 登记块 FIXME-14375 里也有这个词 ⇒ ⇒ 判据恒真。
	if !commentGroupHasAll14375(f, "等于把已有数据原样写回", "session_bodies") {
		t.Fatal("★ 回填工具的「恒等操作」自述不在了 ⇒ " +
			"⇒ 若它已能真正拆增量 ⇒ 形态 A 的历史就有修法了 ⇒ 请同步撤掉 FIXME-14375 与本门")
	}
}

// TestSection14375DefectStillRegistered pins 判据 ③：
// 「形态 A 的历史用本工具修不了」必须仍被登记（FIXME-14375 注释组，
// 且必须同时点明「恒等操作」与差分内核的名字 ⇒ 能识破「已作废 / 改名」的写法）。
//
// ★★ needle 用【完整登记标记形态】（带全角左括号）：★ 裸子串会让「把登记改名」
// ★★ 这种最常见的退役动作逃过门（★ §143.73 变异 M4 的教训）。
func TestSection14375DefectStillRegistered(t *testing.T) {
	root := repoRoot14375(t)
	f, _ := parseGo14375(t, filepath.Join(root, section14375ToolPath))
	if !commentGroupHasAll14375(f, "FIXME-14375（", "恒等操作", "DeriveTurnDeltas") {
		t.Fatal("★ 登记块 FIXME-14375 不在了，或它没有同时点明「恒等操作」与「DeriveTurnDeltas」⇒ " +
			"⇒ （★ 若差分语义已改 ⇒ 请同步撤掉 FIXME-14375 与本门）")
	}
}

// TestSection14375SummarizerStillTakesLastMessage pins 判据 ④：
// `sessionsummary` 的「只取每轮 delta 最后一条」workaround 必须仍在。
//
// ★ 这是【读侧受害面收敛】的结构锚点：★ 该 workaround 对形态 A 本来就是对的
// ★ ⇒ ⇒ 唯一受害的读路径是压缩器冷启动的 `LoadChain`。
// ★ 反向断言：★ 一旦改成取全部 ⇒ ⇒ 形态 A 会在摘要侧累加 ⇒ ⇒ 受害面扩大 ⇒ 需同步 FIXME。
func TestSection14375SummarizerStillTakesLastMessage(t *testing.T) {
	root := repoRoot14375(t)
	f, fset := parseGo14375(t, filepath.Join(root, section14375SumPath))
	text := nodeText14375(fset, funcDecl14375(t, f, "collapseToLastRequestMessage"))
	if !strings.Contains(text, "msgs[len(msgs)-1]") {
		t.Fatal("★ sessionsummary 的「只取最后一条」workaround 不在了 ⇒ " +
			"⇒ 形态 A 会在摘要侧累加 ⇒ 请同步复核 FIXME-14375 与本门")
	}
}

// TestSection14375CensusScriptPinsBothCalibers pins 判据 ⑤：
// 复算脚本必须同时钉住【两个口径】：形态标记（不 detoast 的字节判据）与可回收量。
//
// ★★ 必须【剥掉注释】再查：★ 否则注释里提到的锚点会让判据恒真（★ §143.73 变异 M6 的教训）。
func TestSection14375CensusScriptPinsBothCalibers(t *testing.T) {
	root := repoRoot14375(t)
	b, err := os.ReadFile(filepath.Join(root, section14375AuditPath))
	if err != nil {
		t.Fatalf("读取复算脚本 %s 失败：%v", section14375AuditPath, err)
	}
	code := stripSQLComments14373(string(b))
	for _, needle := range []string{
		"pg_column_size(outbound_body) > 12", // 形态标记（★ 不 detoast ⇒ 可全量）
		"keep_one_bytes",                     // 可回收量口径
		"kind='turn_delta'",                  // 只统计逐轮行
	} {
		if !strings.Contains(code, needle) {
			t.Fatalf("复算脚本 %s 的【可执行代码】里缺少口径锚点 %q（★ 两个口径缺一个 ⇒ 不能复算本轮结论）", section14375AuditPath, needle)
		}
	}
}

// TestSection14375JudgesAreNotConstant 自证判据不是常量：
// ① 在【合成源码】上，差分调用检测必须给出与输入一致的结论；
// ② 真实文件里必须确实检出。
func TestSection14375JudgesAreNotConstant(t *testing.T) {
	withDiff := `package p
func f() { diff := subtractMessages(full, prev) }`
	withoutDiff := `package p
func f() { return full }`
	for _, tc := range []struct {
		src  string
		want bool
	}{{withDiff, true}, {withoutDiff, false}} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "synthetic.go", tc.src, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			t.Fatalf("解析合成源码失败：%v", err)
		}
		got := strings.Contains(nodeText14375(fset, f), "subtractMessages(")
		if got != tc.want {
			t.Fatalf("★ 差分检测不自证：合成源码期望 %v，实际 %v ⇒ 判据可能是写死的", tc.want, got)
		}
	}

	root := repoRoot14375(t)
	f, fset := parseGo14375(t, filepath.Join(root, section14375DerivePath))
	if !strings.Contains(nodeText14375(fset, funcDecl14375(t, f, "DeriveTurnDeltas")), "subtractMessages(") {
		t.Fatal("★ 真实文件里没检出差分调用 ⇒ 判据 ① 现在是恒假的空壳 ⇒ 门没有牙")
	}
}
