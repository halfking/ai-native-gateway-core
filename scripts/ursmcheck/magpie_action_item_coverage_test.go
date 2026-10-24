package ursm_test

// magpie_action_item_coverage_test.go —— ★ **「计划项 → 门」完整性门**。
//
// # 这道门守住哪块缺口
//
// 计划书 §六（P0–P3）给每一项行动项都点了一条「验收量具（**去掉即红**）」。
// ⇒ ★ 而「**每一项都真的有门**」这句话，此前**只以散文形式**存在于
// 审计档的 §四 门禁表里 ⇒ ⇒ ★★ **没有任何机器能发现**：
// ① 计划书新增一项行动项（⇒ 它没有任何门）；
// ② 某个门文件被删掉 / 改名（⇒ 那一项的量具**消失了**，而门禁表还在）；
// ③ 审计档门禁表漏登记某个门（⇒ 「每项都有门」这句话已经不真了）。
//
// ★★ 这正是本轮「认证审计」要消除的那一类缺陷（★ §3.19.7.2 登记的
// ★★ 「**散文级断言失真**」⇒ ★ 当时只做「清残留 + 登记局限」，
// ★★ 本门把其中**最关键的一条散文**升级为**可机器核对的事实**）。
//
// # 本门的三条判据
//
//	① ★ **集合精确相等**：★ 计划书 §六 抽出的行动项 ID 集
//	   **必须**与门侧 `planItemGates` 的 key 集**完全一致**（★ 双向）
//	   ⇒ ⇒ 新增计划项无门 ⇒ **红**；★ 门侧留着已删的计划项 ⇒ **红**。
//	② ★ **门真实存在且有断言**：★ 映射到的每个门文件必须**在仓内存在**
//	   且**含 `func Test`** ⇒ ⇒ 门被删 / 改名 / 掏空 ⇒ **红**。
//	③ ★ **门禁表不许漏登记**：★ 每个门文件路径必须出现在审计档 §四 门禁表里
//	   ⇒ ⇒ 审计档的表格从**散文**变成**被核对的对象**。
//
// # ★ 为什么是「两份权威 + 对账」而不是单边检查
//
// ★ 计划书是**被审对象**（★ 它可以被改），审计档 §四 也是**被审对象**。
// ⇒ ⇒ 门侧必须留**自己的一份**（`planItemGates`）⇒ ⇒ 三方对账：
// ★ **计划书 §六**（项集）⇄ **门侧**（项 → 门文件）⇄ **审计档 §四**（登记）。
// ★ ⇒ 与 C8（认证表 ↔ 设计页）**同款**手法。

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// planItemGates 是**门侧**的「计划行动项 → 门文件」映射（★ 第二份权威）。
//
// ★★ 它住在**门文件**里、**不在**计划书或审计档里 ⇒ 这是关键：
// ★★ 计划书负责声明「**有哪些**行动项」，审计档负责登记「**门在哪**」，
// ★★ 门侧负责钉住「**这一项对应哪个门文件**」⇒ ⇒ 三方**互为对手**。
//
// ★★ 诚实边界（★ 登记在审计档 §五）：★ 三方里**门侧**这一份仍可被显式改写
// ★★ ⇒ 「同时改三处」仍能放行 ⇒ ⇒ 这属**流程性**论证（★ git diff 可见），
// ★★ **不是**技术互锁 ⇒ ⇒ 登记而不修。
var planItemGates = map[string][]string{
	"P0-1": {"scripts/ursmcheck/magpie_p0_extension_adjudication_test.go"},
	"P0-2": {"scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go"},
	"P0-3": {"errorsx/restclasses_test.go"},
	"P0-4": {"domains/credential/slowrest_test.go"},
	"P0-5": {"scripts/ursmcheck/magpie_p0_extension_onoff_test.go"},
	"P1-1": {"internal/requestobserver/requestobserver_test.go"},
	"P1-2": {"internal/routeorder/routeorder_test.go"},
	"P1-3": {
		"domains/credential/rpm_reservation_contract_test.go",
		"domains/authentication/budget_reservation_contract_test.go",
	},
	"P1-4": {"internal/rewrite/rewrite_test.go"},
	"P1-5": {"scripts/ursmcheck/magpie_p1_failure_policy_test.go"},
	"P2-1": {"scripts/ursmcheck/magpie_p2_v2_criteria_test.go"},
	"P2-2": {"scripts/ursmcheck/magpie_p2_extension_ledger_test.go"},
	"P2-3": {"scripts/ursmcheck/request_surface_doc_currency_test.go"},
}

// magpiePlanItemRowRe 匹配 §六 表格里「**首列恰为行动项 ID**」的行。
//
// ★★ 形状必须**精确**：★ 只认 `| **P0-1** |` 这种**表格首列**形态 ⇒
// ★★ ⇒ ① 散文里的 `§6-P0-3` **不**算；② ② `| P0 |`（§6.1 的汇总表）**不**算；
// ★★ ⇒ ③ `P0-3 / P0-4` 这种并列表述**不**算。
var magpiePlanItemRowRe = regexp.MustCompile(`^\|\s*\*\*(P[0-3]-[0-9]+)\*\*\s*\|`)

// magpieSectionScope 取出从指定标题起、到**下一个同级标题**为止的正文。
//
// ★★ **位置敏感**：★ 计划书里 P-ID 在 §六 之外**到处都有**（★ §三 / §四 / §六 横幅里）
// ★★ ⇒ ⇒ 抽取**必须**先切到 §六 ⇒ ⇒ 否则会把散文里的 P-ID 一起抽进来 ⇒ 假红。
func magpieSectionScope(text, headingPrefix string) string {
	idx := strings.Index(text, headingPrefix)
	if idx < 0 {
		return ""
	}
	rest := text[idx:]
	// ★ 下一个「\n## 」开头 = 下一个同级标题 ⇒ §六 的正文到那里为止。
	if n := strings.Index(rest[1:], "\n## "); n >= 0 {
		return rest[:n+1]
	}
	return rest
}

// magpiePlanItemIDs 从计划书 §六 抽出行动项 ID（★ 按出现顺序）。
func magpiePlanItemIDs(text string) []string {
	scope := magpieSectionScope(text, "## 六、")
	if scope == "" {
		return nil
	}
	var ids []string
	for _, line := range strings.Split(scope, "\n") {
		if m := magpiePlanItemRowRe.FindStringSubmatch(line); m != nil {
			ids = append(ids, m[1])
		}
	}
	return ids
}

// magpieRepoRoot 返回仓根目录。
func magpieRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败 ⇒ 无法定位仓根")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓根定位可疑：%s 下没有 go.mod", root)
	}
	return root
}

// TestMagpieActionItemsAllHaveGates 是「**计划项 → 门**」完整性门。
func TestMagpieActionItemsAllHaveGates(t *testing.T) {
	root := magpieRepoRoot(t)

	// ★★ 计划书的路径**不写死**：★ ★ 复用既有的 `planDocPath(t)`（★ 它用 **glob** 解析
	// ★★ 因为文件名含中文与括号 ⇒ 写死极易在改名 / 复制时失配 ⇒ ★ 那是既有门
	// ★★ `magpie_plan_currency_test.go` 的既定做法）★ ⇒ ★ 本门**同款**。
	plan := readRepoFile(t, planDocPath(t))

	// ① ★ 集合精确相等（双向）。
	ids := magpiePlanItemIDs(plan)
	if len(ids) == 0 {
		t.Fatalf("★ 从计划书 §六 **一个**行动项都没抽到 ⇒ 提取器坏了（★ 读数无效）")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("计划书 §六 里行动项 %s 出现了**多次** ⇒ 提取器或计划书有问题", id)
		}
		seen[id] = true
	}
	for id := range planItemGates {
		if !seen[id] {
			t.Errorf("★★ 门侧映射里有 %s，但计划书 §六 **没有**这一项 ⇒\n"+
				"★★ ⇒ 门侧与计划书**不同源** ⇒ 修法：计划书补上该项，或门侧删掉这条映射",
				id)
		}
	}
	for _, id := range ids {
		if _, ok := planItemGates[id]; !ok {
			t.Errorf("★★ 计划书 §六 的行动项 %s **没有**对应的门 ⇒\n"+
				"★★ ⇒ 「每条行动项都有『去掉即红』的验收量具」这句话**不成立**\n"+
				"★★ ⇒ 修法（三处都要改，★ 缺一仍会红）：\n"+
				"★★   ① 为 %s 建门；② 在门侧 `planItemGates` 里登记；③ 在审计档 §四 门禁表里补一行",
				id, id)
		}
	}

	// ② ★ 门真实存在且含断言。
	for id, files := range planItemGates {
		if len(files) == 0 {
			t.Errorf("★★ 行动项 %s 的门侧映射是**空的** ⇒ 它没有验收量具", id)
			continue
		}
		for _, rel := range files {
			abs := filepath.Join(root, rel)
			b, rerr := os.ReadFile(abs)
			if rerr != nil {
				t.Errorf("★★ 行动项 %s 的门文件 %s 在仓内**不存在** ⇒\n"+
					"★★ ⇒ 这一项的验收量具**消失了**（★ 门被删 / 改名 ⇒ 而门禁表还在）\n"+
					"★★ ⇒ 修法：恢复该门，或把行动项 %s 改挂到别的门",
					id, rel, id)
				continue
			}
			if !strings.Contains(string(b), "func Test") {
				t.Errorf("★★ 行动项 %s 的门文件 %s **不含任何 `func Test`** ⇒\n"+
					"★★ ⇒ 它在位但**没有断言** ⇒ 「去掉即红」是空的\n"+
					"★★ ⇒ 修法：把断言写回去", id, rel)
			}
		}
	}

	// ③ ★ 审计档 §四 门禁表不许漏登记。
	audit := readRepoFile(t, auditDocRelPath)
	gateTable := magpieSectionScope(audit, "## 四、门禁清单")
	if gateTable == "" {
		t.Fatal("★ 审计档里找不到「## 四、门禁清单」小节 ⇒ 锚点漂了 ⇒ 门无法对账")
	}
	for id, files := range planItemGates {
		for _, rel := range files {
			if !strings.Contains(gateTable, rel) {
				t.Errorf("★★ 行动项 %s 的门文件 %s **没有**登记在审计档 §四 门禁表里 ⇒\n"+
					"★★ ⇒ 审计档的「门禁清单」与门侧映射**对不上** ⇒ 「每项都有门」不可查\n"+
					"★★ ⇒ 修法：在 §四 表里补上这一行", id, rel)
			}
		}
	}
}

// TestMagpiePlanItemExtractorHasTeeth 是**提取器量具自检**。
//
// ★★ 为什么需要它：★ 提取器（★ 从计划书 §六 抽 ID）一旦**静默失效**
// ★★ （★ 例如切段切错、形状放宽）⇒ ⇒ ① 判据①会「集合为空 ⇒ t.Fatal」
// ★★   （★ 那会**假红**，不是假绿）⇒ 但 ② ③ 两条判据**照样跑** ⇒
// ★★ ⇒ 所以必须有**独立**的自检证明提取器**取对了**、且**只取该取的**。
//
// ★★ 这也是本仓库的既有纪律（★ `TestCertRowRangeExtractorHasTeeth` /
// ★★ `TestPendingCountExtractorsHaveTeeth` / `TestMagpieP2FuncCorpusIsPerFunction`）。
func TestMagpiePlanItemExtractorHasTeeth(t *testing.T) {
	const src = `# 计划书

## 〇、结论

正文里提到 P0-1，但这**不是**行动项表 ⇒ 不该被抽到。

## 六、优化方案与规划

| **P0-1** | 主题 | 动作 | 量具 |
| **P2-9** | 主题 | 动作 | 量具 |

### 6.1 工作量与顺序

| P0 | 汇总 | 8–12 | 无 |
| P1 | 汇总 | 10–15 | P0-1 |

## 七、不照搬清单

| **P3-1** | 别的表 | 动作 | 量具 |
`
	got := magpiePlanItemIDs(src)

	// ① ★ §六 表格里的两行必须被抽到。
	if len(got) != 2 || got[0] != "P0-1" || got[1] != "P2-9" {
		t.Errorf("★ 提取器必须只取 §六 表格首列的 ID（期望 [P0-1 P2-9]，实际 %v）", got)
	}
	// ② ★ §六 **之外**的 P-ID（★ 散文里的 P0-1、§七 表格里的 P3-1）不得被抽到。
	for _, forbidden := range []string{"P3-1"} {
		for _, id := range got {
			if id == forbidden {
				t.Errorf("★ 提取器**越界**了 ⇒ 把 §六 之外的 %s 抽进来了", forbidden)
			}
		}
	}
	// ③ ★ **形状**必须精确：★ `| P0 |` 与散文里的 `P0-1` 都不是行动项行。
	if strings.Contains(strings.Join(got, ","), "P0,") {
		t.Errorf("★ 提取器把 §6.1 的 `| P0 |` 汇总行当成了行动项")
	}
	// ④ ★ 提取器对「没有 §六」的文本必须**如实**返回空（★ 不许瞎猜）。
	if ids := magpiePlanItemIDs("# 别的文档\n\n正文 P0-1 P1-1\n"); len(ids) != 0 {
		t.Errorf("★ 提取器对没有 §六 的文本返回了 %v ⇒ 应当返回空", ids)
	}
}
