package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 「**已退役**」的迁移声明 —— 本门是它的契约门。
//
// ★ 背景（2026-10-26，ai-session-manager 第十七轮）：
//   619 的 canonical 文件在 2026-08-31（ee53680c4）被**刻意删掉** ——
//   审计判定 `session_turns_unified` 是孤儿视图（0 个生产读者，且与 526 的
//   `session_turns_with_current_month` 语义重复 ⇒ 双计陷阱）⇒ 改由 635 在
//   已部署环境里 DROP。但 619 **已经在环境里应用过** ⇒ 已应用的字节不在仓内
//   ⇒ 部署前的迁移台账对账门无法拿它做 checksum 比对 ⇒ 过去只能判成
//   「无法解析为唯一 canonical 文件」⇒ **拒绝部署** ⇒ 挡死所有环境的每次部署。
//
//   修法：仓里显式声明「已退役」（619 用 `.sql.retired`，830 用既有的
//   `.sql.skip`）⇒ 对账门大声记录并跳过该版本的 checksum 校验。
//
// ★ 本门钉住三条：
//   ① 每份退役声明**非空**且**含退役关键词** ⇒ 不许空壳占位（空壳会让
//      「删错了」与「刻意退役」长得一样）；
//   ② 退役声明**不得与规范件并存**（规范件在 ⇒ 它根本没退役，走老路即可；
//      两份并存会让对账门的优先级变得含糊）；
//   ③ **619 的规范件不许回来** —— 08-31 的裁决是「不再在新环境创建这个
//      孤儿视图」；把它恢复回来等于让孤儿视图在新装环境里又被创建。
//
// ★ 判据形态：扫**整个** startup 目录，而不是只钉 619 一个 ⇒ 以后再有人
//   加一份退役声明，这三条自动覆盖到 ⇒纪律不靠人记。

const (
	retiredSuffix = ".retired"
	retiredKeywordMarker = "退役"
)

// retirementDeclarations 列出目录里全部「退役声明」。
//
// ★ 两种形态都算：
//   · `<ver>_*.sql.retired`     —— 文件被整个删掉时的留痕（619 用它）
//   · `<ver>_*.sql.skip`         —— 文件改标留档（830 用它）
//   · `<ver>_*.sql.bak.skip`     —— 同上的备份形态（336 用它）
func retirementDeclarations(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读 startup 目录: %v", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(name, retiredSuffix) ||
			strings.HasSuffix(name, ".sql.skip") ||
			strings.HasSuffix(name, ".sql.bak.skip") {
			out = append(out, name)
		}
	}
	return out
}

// canonicalNameOf 退役声明的规范名（去掉退役后缀）。
func canonicalNameOf(decl string) string {
	switch {
	case strings.HasSuffix(decl, ".sql.bak.skip"):
		return strings.TrimSuffix(decl, ".bak.skip")
	case strings.HasSuffix(decl, ".sql.skip"):
		return strings.TrimSuffix(decl, ".skip")
	case strings.HasSuffix(decl, retiredSuffix):
		return strings.TrimSuffix(decl, retiredSuffix)
	default:
		return decl
	}
}

func TestRetirementDeclarationsAreSelfDescribing(t *testing.T) {
	decls := retirementDeclarations(t)
	// ★ 量具自审：扫不到东西 ⇒ 这道门会恒绿（看上去正常，实际扫了个寂寞）。
	if len(decls) == 0 {
		t.Fatalf("startup 目录里一份退役声明都没有 —— 枚举面失效？本门会恒绿")
	}
	for _, decl := range decls {
		body, err := os.ReadFile(decl)
		if err != nil {
			t.Errorf("读 %s: %v", decl, err)
			continue
		}
		if len(strings.TrimSpace(string(body))) == 0 {
			t.Errorf("退役声明 %s 是空文件 —— 退役必须写明为什么；"+
				"否则「刻意退役」与「删错了」在门眼里长得一模一样", decl)
			continue
		}
		// ★ 关键词要求**只**管 `.retired` 这一种形态。
		//   · `.retired` 是**写下来的退役声明**（正文就是声明）⇒ 必须自述。
		//   · `.sql.skip` 是**被改标的 SQL 文件**（正文仍是 SQL，退役理由写在
		//     它的文件头注释里）⇒ 它的「非空」即可，不该拿「退役」二字去卡它
		//     （仓里 318b / 336×3 / 338 / 350 / 830 这七份都是这一形态）。
		if strings.HasSuffix(decl, retiredSuffix) &&
			!strings.Contains(string(body), retiredKeywordMarker) {
			t.Errorf("退役声明 %s 里没有「%s」二字 —— 这份声明说不清自己为什么退役，"+
				"对账门与人都读不出它的意图", decl, retiredKeywordMarker)
		}
	}
}

func TestRetirementDeclarationsDoNotCoexistWithCanonicalFile(t *testing.T) {
	for _, decl := range retirementDeclarations(t) {
		canonical := canonicalNameOf(decl)
		if _, err := os.Stat(canonical); err == nil {
			t.Errorf("退役声明 %s 与它的规范件 %s **并存** —— "+
				"规范件在 ⇒ 这条迁移根本没退役（对账门会走老路校验它）；"+
				"退役声明与规范件并存 ⇒ 对账门的优先级变含糊（哪一份说了算？）",
				decl, canonical)
		}
	}
}

// Test619CanonicalViewStaysDeleted 钉住 08-31 的裁决：619 的 canonical 文件
// **不许回来**。
//
// ★ 为什么这条要单独钉：恢复它是「看起来最省事」的修法 —— 有了 canonical
//   文件，对账门就能照常校验，退役声明也就不必存在。但那等于让孤儿视图
//   `session_turns_unified` 在新装环境里又被创建出来（635 会随后 DROP，
//   等于「先造后拆」）⇒ 推翻 08-31 那次审计修正。
func Test619CanonicalViewStaysDeleted(t *testing.T) {
	const canonical = "619_session_turns_unified_view.sql"
	if _, err := os.Stat(canonical); err == nil {
		t.Fatalf("%s 存在 —— 08-31（ee53680c4）判定 session_turns_unified 是"+
			"孤儿视图（0 个生产读者 + 与 526 语义重复 ⇒ 双计陷阱）⇒ 刻意删掉它的"+
			"canonical 文件，改由 635 在已部署环境里 DROP。"+
			"恢复它 = 让这个孤儿视图在新装环境里又被创建出来。", canonical)
	}

	const decl = "619_session_turns_unified_view.sql" + retiredSuffix
	body, err := os.ReadFile(decl)
	if err != nil {
		t.Fatalf("读不到 %s：619 已应用过，它的 canonical 文件已被刻意删掉，"+
			"⇒ 必须有退役声明，否则部署前的台账对账门会拒掉所有部署。原始错误：%v",
			decl, err)
	}
	text := string(body)
	if !strings.Contains(text, retiredKeywordMarker) {
		t.Errorf("%s 里没有「%s」二字 —— 退役声明说不清自己为什么退役", decl, retiredKeywordMarker)
	}
	// ★ 可追溯性：退役声明必须指出「谁来收尾」（本例是 635 的 DROP）。
	if !strings.Contains(text, "635") {
		t.Errorf("%s 里没有提到 635 —— 退役声明必须指出【谁来收尾】"+
			"（619 的视图是由 635_drop_session_turns_unified.sql 在已部署环境里 DROP 的）", decl)
	}
	// ★ 反向锚：619 的规范名必须能从声明名推出来（对账门靠它对上账本记名）。
	if got := canonicalNameOf(decl); got != "619_session_turns_unified_view.sql" {
		t.Errorf("619退役声明的规范名推出来是 %q，不是 619_session_turns_unified_view.sql —— "+
			"对账门要拿它对上账本里的 migration_name，推错了就放行不了", got)
	}
}

// TestRetirementDeclarationFilesAreNotApplied钉住「退役声明**不是**迁移」。
//
// ★ 判据形态：它们**不以 `.sql` 结尾** ⇒ 部署通道的 pending 扫描
//   （`sql/migrations/startup/[0-9]*.sql`）、installer 的 embed 对账、
//   离线升级的迁移挑选，都不会把它们当迁移挑走。
//   —— 这条测试是那道「不是迁移」这件事的**可执行副本**：哪天有人把它改名成
//   `619_xxx.sql`（看着更正式），本门就红。
func TestRetirementDeclarationFilesAreNotApplied(t *testing.T) {
	for _, decl := range retirementDeclarations(t) {
		if strings.HasSuffix(decl, ".sql") {
			t.Errorf("退役声明 %s 以 .sql 结尾 —— 部署通道的 pending 扫描"+
				"（[0-9]*.sql）会把它当迁移挑走 ⇒ 退役声明必须保持非 .sql 后缀", decl)
		}
		if !strings.Contains(decl, "_") {
			t.Errorf("退役声明 %s 的文件名里没有版本号下划线 —— "+
				"对账门靠<ver>_ 前缀定位它", decl)
		}
	}
}

// ★ 目录里那几份退役声明是【仓的既有事实】，把它们逐个点名，是为了让
//   「本门扫到了什么」可核对（而不是扫到什么算什么）。
func TestKnownRetirementDeclarationsArePresent(t *testing.T) {
	for _, want := range []string{
		"619_session_turns_unified_view.sql" + retiredSuffix,
		"830_ursm_node_snapshot_min_partitioned.sql.skip",
	} {
		if _, err := os.Stat(filepath.Base(want)); err != nil {
			t.Errorf("预期存在的退役声明 %s 不存在：%v", want, err)
		}
	}
}
