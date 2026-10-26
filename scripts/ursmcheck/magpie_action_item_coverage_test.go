package ursm_test

// magpie_action_item_coverage_test.go —— ★ **「计划项 → 门」完整性门**。
//
// # 这道门守住哪块缺口
//
// 计划书 §六（P0–P2）给每一项行动项都点了一条「验收量具（**去掉即红**）」。
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
// # 本门的四条判据
//
//	① ★ **集合精确相等**：★ 计划书 §六 抽出的行动项 ID 集
//	   **必须**与门侧 `planItemGates` 的 key 集**完全一致**（★ 双向）
//	   ⇒ ⇒ 新增计划项无门 ⇒ **红**；★ 门侧留着已删的计划项 ⇒ **红**。
//	② ★ **门真实存在且有断言（★ AST 事实）**：★ 映射到的每个门文件必须**在仓内存在**
//	   且**至少含一个 `func TestXxx` 的函数声明**
//	   （★ 用 go/parser 判 ⇒ ★ **不是**字面子串 ⇒ ★ 一行注释骗不过它）
//	   ⇒ ⇒ 门被删 / 改名 / **掏空** ⇒ **红**。
//	③ ★ **审计档 §四 行级对账（★ 多重集精确相等）**：★ 审计档 §四 门禁表里
//	   「**行动项 ID → 门文件路径**」的登记，必须与门侧 `planItemGates`
//	   **逐项精确相等**（★ 比的是**多重集** ⇒ ★ 允许同一个门登记多行）
//	   ⇒ ⇒ 路径被挪到散文里 ⇒ **红**；★ 删掉一行登记 ⇒ **红**；★ 改挂 ⇒ **红**。
//	④ ★ **§四 登记的每个门文件都必须真实存在**：★ ⇒ 审计档登记的量具不能已经消失
//	   ⇒ ⇒ ★ **抓别的**门文件被删 / 改名（★ 实测 M102 红）。
//	   ⇒ ⇒ ⚠ ★★ **抓不到它自己被删**：★ 判据④ **住在本文件里** ⇒ ★ 删掉本文件 ⇒ ★ 判据④
//	   ⇒ ⇒   也不存在 ⇒ ★ 那个测试根本不跑 ⇒ **无人抓**（★ 通用式：★ 一道门永远抓不到
//	   ⇒ ⇒   「自己被删」⇒ ★ 要有牙必须**另一个**门也核同一份登记 ⇒ ★★ **互为对偶**）。
//	   ⇒ ⇒ ★ 详见审计档 §3.19.11.5 第 8 条（★ 已登记为「跨文件自证 ⇒ 独立一轮」）。
//
//	★ ★★ **就地订正（2026-10-25，§3.19.12）**：★ 上面那段「**无人抓**」是**当时**的读数
//	   ⇒ ⇒ ★★ 上面那几句话**仍然成立**（★ 判据④ **独自**就是抓不到自己的删除）
//	   ⇒ ⇒ ★★ 但现已由**另一个**文件（★ `scripts/ursmcheck/magpie_guard_presence_test.go`
//	   ⇒ ⇒ ★★ 「门在位自证」**对偶门**）核本门在不在 ⇒ ⇒ ★★ **F9 已闭**
//	   ⇒ ⇒ ★★ （★ 变异 M109 = 删掉**本文件** ⇒ 对偶门红；★ M110 = 删掉对偶门 ⇒ **本门**判据④ 红）
//	   ⇒ ⇒ ★★ ⇒ ★★ **两侧互为对偶** ⇒ ★★ **同时删两个**仍无人抓 ⇒ ★★ 残余已登记。
//
// # ★ 为什么是「两份权威 + 对账」而不是单边检查
//
// ★ 计划书是**被审对象**（★ 它可以被改），审计档 §四 也是**被审对象**。
// ⇒ ⇒ 门侧必须留**自己的一份**（`planItemGates`）⇒ ⇒ 三方对账：
// ★ **计划书 §六**（项集）⇄ **门侧**（项 → 门文件）⇄ **审计档 §四**（登记）。
// ★ ⇒ 与 C8（认证表 ↔ 设计页）**同款**手法。
//
// ★★ 诚实边界（★ 登记在审计档 §五）：★ 三方里**门侧**这一份仍可被显式改写
// ★★ ⇒ 「同时改三处」仍能放行 ⇒ ⇒ 这属**流程性**论证（★ git diff 可见），
// ★★ **不是**技术互锁 ⇒ ⇒ 登记而不修。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// planItemGates 是**门侧**的「计划行动项 → 审计档 §四 登记的门文件」映射（★ 第二份权威）。
//
// ★★ 它住在**门文件**里、**不在**计划书或审计档里 ⇒ 这是关键：
// ★★ 计划书负责声明「**有哪些**行动项」，审计档负责登记「**门在哪**」，
// ★★ 门侧负责钉住「**这一项对应哪个门文件**」⇒ ⇒ 三方**互为对手**。
//
// ★★★ ★★ **登记的是「§四 里的行」，不是「去重的路径集合」**（★ 第五轮 verifier 的 F2 逼出来的）：
// ★★★ ★★ 同一个门文件**可以**在 §四 里被登记**多行**（★ 例：P0-1 有「P0-1 裁决表」
// ★★★ ★★ 与「★ P0-1 订正 真调用复核」**两行**，登记的是**同一个**门文件）
// ★★★ ★★ ⇒ ★★ 门侧因此**按行**登记（★ 允许重复）⇒ ★★ 判据③ 比的是**多重集**
// ★★★ ★★ ⇒ ⇒ 删掉 §四 里的**其中一行** ⇒ 门侧与审计档**对不上** ⇒ **门红**。
var planItemGates = map[string][]string{
	"P0-1": {
		"scripts/ursmcheck/magpie_p0_extension_adjudication_test.go",
		// ★ §四 里 P0-1 有**两行**登记同一个门文件（★「P0-1 裁决表」+「★ P0-1 订正 真调用复核」）
		// ★ ⇒ 这里**按行**登记（★ 允许重复）⇒ ⇒ 删掉其中一行 ⇒ 门红（★ 判据③ 的多重集对账）。
		"scripts/ursmcheck/magpie_p0_extension_adjudication_test.go",
	},
	"P0-2": {"scripts/ursmcheck/magpie_p0_plugin_capability_holes_test.go"},
	"P0-3": {"errorsx/restclasses_test.go"},
	"P0-4": {"domains/credential/slowrest_test.go"},
	"P0-5": {"scripts/ursmcheck/magpie_p0_extension_onoff_test.go"},
	"P1-1": {
		"internal/requestobserver/requestobserver_test.go",
		"domains/streaming/request_observer_bridge_test.go",
	},
	"P1-2": {
		"internal/routeorder/routeorder_test.go",
		"domains/streaming/executors/routeorder_adapter_test.go",
	},
	"P1-3": {
		"domains/credential/rpm_reservation_contract_test.go",
		"domains/authentication/budget_reservation_contract_test.go",
	},
	"P1-4": {"internal/rewrite/rewrite_test.go"},
	"P1-5": {"scripts/ursmcheck/magpie_p1_failure_policy_test.go"},
	"P2-1": {
		"scripts/ursmcheck/magpie_p2_v2_criteria_test.go",
		"cmd/gateway/main_pipeline_requestid_test.go",
		"scripts/ursmcheck/magpie_p2_v2_criteria_test.go",
	},
	"P2-2": {
		"internal/extpointledger/extpointledger_test.go",
		"internal/extpointledger/table_test.go",
		"domains/hooks/response/chain_ledger_test.go",
		"scripts/ursmcheck/magpie_p2_extension_ledger_test.go",
		"admin/extension_points_test.go",
		"middleware/ledger_mw_test.go",
		"scripts/ursmcheck/magpie_p2_extension_ledger_test.go",
		"internal/extpointledger/table_test.go",
	},
	"P2-3": {"scripts/ursmcheck/request_surface_doc_currency_test.go"},
}

// ───────────────────────────── 提取器（★ 可被自检量一遍） ─────────────────────────────

// magpiePlanScopeRe 定位计划书的 §六。
//
// ★★ **行首**锚定（★ `(?m)^`）⇒ ★ 文档正文里出现的「## 六、」不算
// ★★ ⇒ ★★ 但**顶部目录**里**单独一行**的 `## 六、` 仍会命中 ⇒ ★★ 所以
// ★★ `magpiePlanItemIDs` 会**逐个**尝试所有命中，**取第一个真能抽出行动项的**。
var magpiePlanScopeRe = regexp.MustCompile(`(?m)^## 六、`)

// magpieAuditScopeRe 定位审计档的 §四 门禁清单。
var magpieAuditScopeRe = regexp.MustCompile(`(?m)^## 四、门禁清单`)

// magpiePlanGroupRe 识别 §六 里的**行动项分组小节**（★ `### P0` / `### P1` / `### P2`）。
//
// ★★★ 只认这三组 ⇒ ★★★ 「### P3（明确暂缓…）」与「### 6.1 工作量与顺序…」
// ★★★ **不在**范围里 ⇒ ⇒ ★ **P3 显式豁免**（★ 计划书自己写明它「明确暂缓」
// ★★★ ⇒ ⇒ 它**没有**可验收的行动项 ⇒ ⇒ 不该被要求配门）。
// ★★★ ★★ 注意：★ Go 的 RE2 **不支持**负向先行（★ `(?![0-9])` 会 panic）
// ★★★ ★★ ⇒ ★★ 这里用「后一位**不是**数字**或**到此结束」来表达同一件事。
var magpiePlanGroupRe = regexp.MustCompile(`^P[0-2]([^0-9]|$)`)

// magpiePlanItemIDRe 匹配「行动项 ID」。
//
// ★★ **只认 P0 / P1 / P2**（★ `P[0-2]`）⇒ ★★ 这是「P3 显式豁免」的**兜底**：
// ★★ 即使 P3 被列表化成表格，它的 ID 形态是 `P3-N` ⇒ **抽不出来**。
var magpiePlanItemIDRe = regexp.MustCompile(`P[0-2]-[0-9]+`)

// magpieBacktickTokenRe 取出表格单元格里**反引号**包起来的 token。
var magpieBacktickTokenRe = regexp.MustCompile("`([^`]+)`")

// magpieRepoPathRe 识别「**看起来是仓内文件路径**」的 token。
//
// ★★ 只收**像路径**的（★ 含 `/` 且以 `.go` / `.sh` 结尾）⇒ ★★ 于是
// ★★ `make guards`、`ursmcheck`、`cmd/gateway` 这类**被反引号包着的非路径**
// ★★ **不会**被误当成门文件 ⇒ ⇒ 判据④ 不会假红。
// ★★ 扩展名集合 = ★ `go` / `sh` / `py`（★ 第六轮 verifier 的 **S8**：★ 旧集合只收
// ★★ `.go` / `.sh` ⇒ ⇒ §四 里登一个 `.py` 门路径 ⇒ **判据④ 不核它** ⇒ ⇒ 死路径放行）。
var magpieRepoPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*\.(go|sh|py)$`)

// magpieSectionScopes 取出所有「行首命中 headRe」的同名小节正文。
//
// ★★ **位置敏感**：★ 小节正文 = 从标题起、到**下一个同级标题**（★ 行首 `## `）为止。
func magpieSectionScopes(text string, headRe *regexp.Regexp) []string {
	var scopes []string
	for _, loc := range headRe.FindAllStringIndex(text, -1) {
		rest := text[loc[0]:]
		// ★ 下一个「行首 `\n## `」= 下一个同级标题（★ `\n### ` 不会被误判）。
		if n := strings.Index(rest[1:], "\n## "); n >= 0 {
			scopes = append(scopes, rest[:n+1])
			continue
		}
		scopes = append(scopes, rest)
	}
	return scopes
}

// magpiePlanItemIDsInScope 从一个 §六 正文里抽出行动项 ID（★ 按出现顺序）。
//
// ★★★ **形状放宽**（★ 第五轮 verifier 的 A5 / A7 实测撞出来的假红）：
// ★★★ ① `| **P0-1** | …` 与 `| P0-1 | …`（★ **去粗体**）**都**算；
// ★★★ ② ID **不在首列**（★ **换列序**）**也**算（★ 取行内**第一个** ID）。
// ★★★ ⇒ ⇒ 代价：★ §6.1 的依赖列里写着 `P0-1` / `P1-4` ⇒ ★ 必须**排除非行动项小节**
// ★★★ ⇒ ⇒ 本函数**只**认 `### P0` / `### P1` / `### P2` 三组小节里的表格行。
func magpiePlanItemIDsInScope(scope string) []string {
	var ids []string
	seen := map[string]bool{}
	inGroup := false
	haveGroup := false
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "### "))
			inGroup = magpiePlanGroupRe.MatchString(title)
			if inGroup {
				haveGroup = true
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		// ★ 在 §六 里、但**不在** P0/P1/P2 小节内 ⇒ ★ 跳过
		// ★ （★ 例：§6.1 的依赖列里也有 `P0-1` ⇒ ★ 抽出来就是**重复项** ⇒ 假红）。
		if !inGroup && haveGroup {
			continue
		}
		m := magpiePlanItemIDRe.FindString(trimmed)
		if m == "" {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		ids = append(ids, m)
	}
	return ids
}

// magpieP3GroupRe 识别「**明确暂缓**」的那一组（★ `### P3`）。
//
// ★★ 判据① 的**豁免**只认这一组（★ 第六轮 verifier 的 **S4** 的修法）：
// ★★ 旧口径只认 `### P0 / P1 / P2` 三组 ⇒ ★★ 计划书若把新行动项放进 §六 的
// ★★ **其它**小节（★ 例 `### 6.1 工作量与顺序`）⇒ ★★ 那一项**逃出**对账 ⇒ ★★ 门有牙缺口。
// ★★ ⇒ 新口径：**只有 `### P3` 这一组被豁免**，其余小节的表格行一律参与对账。
var magpieP3GroupRe = regexp.MustCompile(`^P3([^0-9]|$)`)

// magpiePlanFirstCellHasID 判断**首列**里有没有行动项 ID 形态。
//
// ★★ 为什么只看**首列**：★ §四/§六 这类表格的「登记单位」是**首列**（★ 行动项 ID 列）
// ★★ ⇒ ★★ 而「ID 出现在**别的列**」的行（★ 例：汇总表的**依赖列**里写着 `P0-9`）
// ★★ ⇒ ★★ **不是**一条新行动项 ⇒ ★★ 若把它们也算进去 ⇒ ⇒ 假红（★ 第六轮 verifier 的 A5 / A7
// ★★ ⇒ ★★ 「换列序也认」的容忍度必须保留）⇒ ⇒ ★★ 所以：**首列有 ID** 才算候选。
func magpiePlanFirstCellHasID(row string) bool {
	cells := strings.Split(strings.TrimSpace(row), "|")
	if len(cells) < 2 {
		return false
	}
	return magpiePlanItemIDRe.MatchString(cells[1])
}

// magpiePlanBroadCandidates 扫 §六（★ **除 `### P3` 组外**）的**全部**表格行
// ⇒ 取出「**首列写着 ID**」的那些行里的候选行动项 ID。
//
// ★★ 这是判据① 的**覆盖面补丁**：★ 它问的是「有没有一条**首列是 ID** 的行
// ★★ 落在了判据① **不认**的小节里」⇒ ⇒ 答「有」⇒ ⇒ **门红**。
func magpiePlanBroadCandidates(scope string) []string {
	var out []string
	seen := map[string]bool{}
	skipP3 := false
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			title := strings.TrimSpace(strings.TrimPrefix(trimmed, "### "))
			skipP3 = magpieP3GroupRe.MatchString(title)
			continue
		}
		if skipP3 || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		if !magpiePlanFirstCellHasID(trimmed) {
			continue
		}
		for _, id := range magpiePlanItemIDRe.FindAllString(trimmed, -1) {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// magpiePlanScope 取出计划书 §六 的正文（★ **逐个尝试所有命中**，取第一个能抽出行动项的）。
func magpiePlanScope(text string) (string, bool) {
	for _, scope := range magpieSectionScopes(text, magpiePlanScopeRe) {
		if ids := magpiePlanItemIDsInScope(scope); len(ids) > 0 {
			return scope, true
		}
	}
	return "", false
}

// magpiePlanItemIDs 从计划书里抽出行动项 ID（★ 按出现顺序）。
//
// ★★★ **逐个尝试所有 §六 命中**：★ 顶部目录里**单独一行**的 `## 六、` 会先命中
// ★★★ ⇒ ★★ 那一段里**抽不出**行动项 ⇒ ⇒ **跳过**，继续找下一段 ⇒ ★★ 门不会误报
// ★★★ 「提取器坏了」（★ 第五轮 verifier 的 F6）。
func magpiePlanItemIDs(text string) []string {
	scope, ok := magpiePlanScope(text)
	if !ok {
		return nil
	}
	return magpiePlanItemIDsInScope(scope)
}

// magpieAuditRegColIndex 定位 §四 门禁表里「**登记**」所在的列（★ 「位置」列）。
//
// ★★★★ **为什么必须钉死到列**（★ 第六轮 verifier 的 FR1 假红实测出来的）：
// ★★ 旧实现把**整行**所有反引号路径都当成登记 ⇒ ⇒ 于是「在**变异列**里顺口写一句
// ★★ 「（另见 `xxx_test.go`）」」就**多出一条登记** ⇒ ⇒ 判据③ 多重集变大 ⇒ ★★ **假红**
// ★★ ⇒ ⇒ 而**假红比假绿更费**（★ 改方向）⇒ ⇒ ★★ 登记单位必须是**文档自己的登记单位**
// ★★ ⇒ ★★ 而 §四 的登记单位**就是「位置」列**（★ 表头：★ 门 / **位置** / 条数 / CI / 变异）
// ⇒ ⇒ ★★ 只认「位置」列 ⇒ ⇒ 别的列里提一句路径**不算登记** ⇒ ⇒ 不假红。
//
// ★★ 表头行（★ 首列恰为「门」的那行）里必须**恰好有一列**叫「位置」；
// ★★ 找不到 ⇒ ★★ 返回 false ⇒ ★★ 门**响亮失败**（★ 不许静默退化成「零条登记」⇒ 那是假绿）。
func magpieAuditRegColIndex(scope string) (int, bool) {
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 2 || strings.TrimSpace(cells[1]) != "门" {
			continue
		}
		idx := -1
		for i, c := range cells {
			if strings.TrimSpace(c) == "位置" {
				if idx >= 0 {
					return 0, false // ★ 两列都叫「位置」⇒ 口径不明 ⇒ 响亮失败。
				}
				idx = i
			}
		}
		if idx < 0 {
			return 0, false
		}
		return idx, true
	}
	return 0, false
}

// magpieRowGatePaths 取出**一个表格行**的「位置」列里登记的门文件路径。
//
// ★★ 只认**登记列**（★ 「位置」列）⇒ ⇒ 别的列里的路径**不算登记**（★ 见
// ★★ `magpieAuditRegColIndex` 的说明）⇒ ⇒ 不假红。
func magpieRowGatePaths(row string, regCol int) []string {
	cells := strings.Split(strings.TrimSpace(row), "|")
	if regCol < 0 || regCol >= len(cells) {
		return nil
	}
	var paths []string
	for _, tok := range magpieBacktickTokenRe.FindAllStringSubmatch(cells[regCol], -1) {
		if magpieRepoPathRe.MatchString(tok[1]) {
			paths = append(paths, tok[1])
		}
	}
	return paths
}

// magpieAuditGateRows 解析审计档 §四 门禁表 ⇒ 「行动项 ID → 门文件路径（★ 多重集）」。
//
// ★★ 行的「行动项 ID」取**行内第一个** `P[0-2]-N`（★ 不限首列 ⇒ ★ 换列序不误伤）。
// ★★ 行里**没有**行动项 ID 的（★ 例：「★ 门禁接线」「★★★ CI 载体真值门」）
// ★★ ⇒ ★ **不进**这个映射（★ 它们不属于任何计划行动项）。
//
// ★★ 登记路径只取**登记列**（★ 「位置」列）⇒ ⇒ 别的列里提一句路径**不算登记**。
// ★★ 解析不出登记列 ⇒ ★★ 返回 error ⇒ ★★ 门**响亮失败**（★ 不许静默变成「零条」）。
func magpieAuditGateRows(scope string) (map[string][]string, error) {
	regCol, ok := magpieAuditRegColIndex(scope)
	if !ok {
		return nil, fmt.Errorf("★ §四 表头里找不到唯一的「位置」列 ⇒ 登记口径不明")
	}
	out := map[string][]string{}
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		id := magpiePlanItemIDRe.FindString(trimmed)
		if id == "" {
			continue
		}
		out[id] = append(out[id], magpieRowGatePaths(trimmed, regCol)...)
	}
	return out, nil
}

// magpieAuditAllGatePaths 取出 §四 门禁表里登记的**全部**门文件路径
// （★ 含**没有**行动项 ID 的行 ⇒ ★ 判据④ 用它）。
//
// ★★ 同样只认**登记列**（★ 「位置」列）⇒ ⇒ 别的列里的路径**不算登记**。
func magpieAuditAllGatePaths(scope string) ([]string, error) {
	regCol, ok := magpieAuditRegColIndex(scope)
	if !ok {
		return nil, fmt.Errorf("★ §四 表头里找不到唯一的「位置」列 ⇒ 登记口径不明")
	}
	var paths []string
	for _, line := range strings.Split(scope, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}
		paths = append(paths, magpieRowGatePaths(trimmed, regCol)...)
	}
	return paths, nil
}

// magpieMultisetEqual 判断两个字符串**多重集**是否**完全相等**（★ 逐项计数相等）。
//
// ★★ 与「集合相等」的差别**正是** F2 的要害：★ §四 里 P0-1 的**两行**登记
// ★★ **同一个**门文件 ⇒ ★★ 集合看是 1 个、**多重集**看是 2 个 ⇒ ★★ 删掉一行 ⇒ 不等 ⇒ 红。
func magpieMultisetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ca := map[string]int{}
	cb := map[string]int{}
	for _, v := range a {
		ca[v]++
	}
	for _, v := range b {
		cb[v]++
	}
	for k, n := range ca {
		if cb[k] != n {
			return false
		}
	}
	return true
}

// magpieMultisetDiff 报告两个多重集的差集（★ 缺失 / 多余），★ 给人看。
func magpieMultisetDiff(want, got []string) (missing, extra []string) {
	cw := map[string]int{}
	cg := map[string]int{}
	for _, v := range want {
		cw[v]++
	}
	for _, v := range got {
		cg[v]++
	}
	for k, n := range cw {
		for i := 0; i < n-cg[k]; i++ {
			missing = append(missing, k)
		}
	}
	for k, n := range cg {
		for i := 0; i < n-cw[k]; i++ {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

// magpieHasTestFunc 判断一个 Go 源文件里**是否真的有** `func TestXxx` 的**函数声明**。
//
// ★★★ 判据必须是 **AST 事实**，不是**字面子串**（★ 第五轮 verifier 的 F4）：
// ★★★ 字面检查 `strings.Contains(src, "func Test")` ⇒ ★ 一行**注释**
// ★★★ `// func TestXxx` 就能骗过它 ⇒ ⇒ 「门被掏空」这种缺陷**完全**抓不到
// ★★★ ⇒ ⇒ 必须走 go/parser ⇒ ★★ 注释不是 AST 节点 ⇒ ★★ 骗不过。
func magpieHasTestFunc(src []byte, rel string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		return false, err
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "Test") {
			return true, nil
		}
	}
	return false, nil
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
	scope, hasScope := magpiePlanScope(plan)
	if !hasScope {
		t.Fatalf("★ 从计划书 §六 **一个**行动项都没抽到 ⇒ 提取器坏了（★ 读数无效）")
	}
	ids := magpiePlanItemIDsInScope(scope)
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

	// ★★ 判据① 的**覆盖面补丁**（★ 第六轮 verifier 的 **S4** 实测撞出来的）：
	// ★★ 判据① 只认 `### P0 / P1 / P2` 三组 ⇒ ★★ 计划书若把新行动项放进 §六 的
	// ★★ **其它**小节（★ 例 `### 6.1 工作量与顺序`）⇒ ★★ 那一项**逃出**对账
	// ★★ ⇒ ⇒ 「每项都有门」这句话就有一个**绕路** ⇒ ⇒ 本段把那个绕路**堵掉**：
	// ★★ 「**首列写着 ID** 的行」若落在判据① **不认**的小节里 ⇒ ⇒ **门红**。
	// ★★ （★ 「ID 出现在**别的列**」的行**不算** ⇒ ★★ 汇总表的依赖列里写着 `P0-9`
	// ★★ ⇒ ★★ **不**是假红 ⇒ ★★ 也保住了 A5 / A7 的「换列序也认」容忍度。）
	for _, cand := range magpiePlanBroadCandidates(scope) {
		if seen[cand] {
			continue
		}
		t.Errorf("★★ 计划书 §六 里有一行**首列**写着行动项 %s，但它落在**判据① 不认的小节**里 ⇒\n"+
			"★★ ⇒ 判据① 只认 `### P0 / P1 / P2` 三组 ⇒ ⇒ 这一项**逃出了**对账\n"+
			"★★ ⇒ 「每条行动项都有『去掉即红』的验收量具」这句话有**绕路**\n"+
			"★★ ⇒ 修法：把它挪进那三组之一，或更新本门的分组口径（★ `magpiePlanGroupRe`）",
			cand)
	}

	// ② ★ 门真实存在且**真的有断言**（★ AST 事实）。
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
			has, perr := magpieHasTestFunc(b, rel)
			if perr != nil {
				t.Errorf("★★ 行动项 %s 的门文件 %s **解析失败** ⇒\n"+
					"★★ ⇒ 门无法核对它到底有没有断言 ⇒ 修法：修好这个文件",
					id, rel)
				continue
			}
			if !has {
				t.Errorf("★★ 行动项 %s 的门文件 %s **没有任何 `func TestXxx` 的函数声明** ⇒\n"+
					"★★ ⇒ 它在位但**一个测试函数都没有** ⇒ 本门**核不到**它到底断言了什么\n"+
					"★★ ⇒ ★ 判据是 **AST 事实**：★ 一行 `// func TestXxx` 的**注释**骗不过它\n"+
					"★★ ⇒ ★★ **口径提示**（★ 第六轮 verifier 的 S7）：★ 本判据只保证「**有测试函数声明**」\n"+
					"★★ ⇒ ★★ ⇒ **不**保证「函数体里有断言」（★ 空壳 `func TestStub(t *testing.T){}` 也能过）\n"+
					"★★ ⇒ 修法：把真正的断言写进那个测试函数", id, rel)
			}
		}
	}

	// ③ ★ 审计档 §四 门禁表的**行级**登记，必须与门侧**逐项精确相等**。
	audit := readRepoFile(t, auditDocRelPath)
	auditScopes := magpieSectionScopes(audit, magpieAuditScopeRe)
	if len(auditScopes) == 0 {
		t.Fatal("★ 审计档里找不到「## 四、门禁清单」小节 ⇒ 锚点漂了 ⇒ 门无法对账")
	}
	auditRows, aerr := magpieAuditGateRows(auditScopes[0])
	if aerr != nil {
		t.Fatalf("★ 解析 §四 的**登记口径**失败 ⇒ %v ⇒ 门无法对账", aerr)
	}

	// ★★ 两个方向的项集都要**精确相等**（★ 「覆盖型声明」的项集不允许增减）。
	for id := range planItemGates {
		if _, ok := auditRows[id]; !ok {
			t.Errorf("★★ 门侧登记了行动项 %s，但审计档 §四 门禁表里**没有**它的任何一行 ⇒\n"+
				"★★ ⇒ 审计档与门侧**对不上** ⇒ 「每项都有门」不可查\n"+
				"★★ ⇒ 修法：在 §四 表里补上这一行", id)
		}
	}
	for id := range auditRows {
		if _, ok := planItemGates[id]; !ok {
			t.Errorf("★★ 审计档 §四 门禁表里有行动项 %s 的登记，但门侧**没有**它 ⇒\n"+
				"★★ ⇒ 两份权威**对不上** ⇒ 修法：门侧补上 %s，或把 §四 那几行删掉",
				id, id)
		}
	}
	for id, want := range planItemGates {
		got := auditRows[id]
		if magpieMultisetEqual(want, got) {
			continue
		}
		missing, extra := magpieMultisetDiff(want, got)
		t.Errorf("★★ 行动项 %s 的门文件登记与审计档 §四 门禁表**对不上** ⇒\n"+
			"★★ ⇒ 门侧登记 %d 条、审计档登记 %d 条 ⇒ ★ 登记是**逐行**的（★ 允许同一个门登记多行）\n"+
			"★★ ⇒ 门侧缺：%v；审计档多：%v\n"+
			"★★ ⇒ 修法：把两处改成一致（★ 删一行、挪到散文、改挂 ⇒ 都会被抓）",
			id, len(want), len(got), missing, extra)
	}

	// ④ ★ §四 登记的**每一个**门文件都必须真实存在（★ 含没有行动项 ID 的行）。
	allGatePaths, perr := magpieAuditAllGatePaths(auditScopes[0])
	if perr != nil {
		t.Fatalf("★ 解析 §四 的**登记口径**失败 ⇒ %v ⇒ 判据④ 无法对账", perr)
	}
	for _, rel := range allGatePaths {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("★★ 审计档 §四 门禁表登记的门文件 %s 在仓内**不存在** ⇒\n"+
				"★★ ⇒ 审计档登记的验收量具**已经消失** ⇒ 读者照着它去找会扑空\n"+
				"★★ ⇒ 修法：改成本仓真实存在的路径，或把这一行删掉",
				rel)
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

### P0

| **P0-1** | 主题 | 动作 | 量具 |
| **P2-9** | 主题 | 动作 | 量具 |

### P1

| P1-4 | 主题 | 动作 | 量具 |

### P3（明确暂缓）

| **P3-1** | 别的表 | 动作 | 量具 |

### 6.1 工作量与顺序

| P0 | 汇总 | 8–12 | P0-9 |
| P1 | 汇总 | 10–15 | P1-4 |

## 七、不照搬清单

| **P3-1** | 别的表 | 动作 | 量具 |
`
	got := magpiePlanItemIDs(src)

	// ① ★ §六 表格里的三行必须被抽到（★ P0/P1/P2 三组都在范围里）。
	if len(got) != 3 || got[0] != "P0-1" || got[1] != "P2-9" || got[2] != "P1-4" {
		t.Errorf("★ 提取器必须只取 §六 里 P0/P1/P2 小节的 ID（期望 [P0-1 P2-9 P1-4]，实际 %v）", got)
	}
	// ② ★ **不该抽的**必须抽不到：
	//    ★ `P3-1` = ★ **P3 小节**里的行动项（★ 计划书写明它「明确暂缓」⇒ **显式豁免**）；
	//    ★ `P0-9` = ★ **§6.1 汇总表**依赖列里写的 ID（★ 它**不在** P0/P1/P2 小节里）；
	//    ★ 两者任一被抽到 ⇒ ★「小节作用域」或「P3 豁免」被破坏 ⇒ ★ 门有假牙。
	for _, forbidden := range []string{"P3-1", "P0-9"} {
		for _, id := range got {
			if id == forbidden {
				t.Errorf("★ 提取器**越界**了 ⇒ 把不该抽的 %s 抽进来了", forbidden)
			}
		}
	}
	// ③ ★ **形状**必须容得下「去粗体」与「换列序」（★ A5 / A7）。
	//    ⇒ ★ 上面的 `| P1-4 | 主题 | …`（★ **无粗体**）已被抽到（见 ①）。
	// ④ ★ 提取器对「没有 §六」的文本必须**如实**返回空（★ 不许瞎猜）。
	if ids := magpiePlanItemIDs("# 别的文档\n\n正文 P0-1 P1-1\n"); len(ids) != 0 {
		t.Errorf("★ 提取器对没有 §六 的文本返回了 %v ⇒ 应当返回空", ids)
	}
}

// TestMagpiePlanBroadCandidatesHasTeeth 是「**广域候选**」这一步的量具自检
// （★ 第六轮 verifier 的 **S4** 的牙）。
//
// ★★ 为什么需要它：★ 判据① 只认 `### P0 / P1 / P2` 三组 ⇒ ★★ 计划书把新行动项
// ★★ 放进 §六 的**其它**小节 ⇒ ★★ 那一项**逃出**对账 ⇒ ★★ 门有绕路。
// ★★ ⇒ 「广域候选」这一步把那个绕路堵掉 ⇒ ★★ ⇒ 必须有**独立**的自检钉住它。
func TestMagpiePlanBroadCandidatesHasTeeth(t *testing.T) {
	const scope = `## 六、优化方案与规划

### P0

| **P0-1** | 主题 | 动作 | 量具 |

### P3（明确暂缓）

| **P3-1** | 别的表 | 动作 | 量具 |

### 6.1 工作量与顺序

| **P2-4** | 追加行动项 | 做某事 | 量具 |
| P0 | 汇总 | 8–12 | P0-9 |
`
	got := magpiePlanBroadCandidates(scope)

	// ① ★★ **首列是 ID** 的行（★ 在 `### 6.1` 下面）⇒ 必须进候选（★ S4 的牙）。
	found := false
	for _, id := range got {
		if id == "P2-4" {
			found = true
		}
	}
	if !found {
		t.Errorf("★★ `### 6.1` 下**首列**写着 `P2-4` 的行动项必须进候选 ⇒ 它逃出了对账（★ 实际 %v）", got)
	}
	// ② ★★ **别的列**里写着 ID（★ 汇总表的依赖列 `P0-9`）⇒ **不得**进候选
	//    （★ 否则汇总表会假红 ⇒ ★ 也保住了 A5 / A7 的「换列序也认」容忍度）。
	for _, id := range got {
		if id == "P0-9" {
			t.Errorf("★★ `P0-9` 出现在**依赖列**里 ⇒ 它**不是**一条新行动项 ⇒ 不得进候选（★ 实际 %v）", got)
		}
	}
	// ③ ★★ `### P3` 组（★ 明确暂缓）⇒ **不得**进候选。
	for _, id := range got {
		if id == "P3-1" {
			t.Errorf("★★ `### P3` 是**明确暂缓** ⇒ 不得进候选（★ 实际 %v）", got)
		}
	}
}

// TestMagpiePlanScopeIgnoresTopDirectory 是「**顶部目录**里的假 §六」这一形态的自检。
//
// ★★ 第五轮 verifier 的 F6：★ 在文档顶部加**单独一行**的 `## 六、` ⇒
// ★★ ⇒ 旧的「取第一个匹配」会把范围切到目录那一段 ⇒ ⇒ 抽不出行动项 ⇒
// ★★ ⇒ 门误报「提取器坏了」（★ **假红**）⇒ ⇒ 本门必须**跳过**它、继续找真 §六。
func TestMagpiePlanScopeIgnoresTopDirectory(t *testing.T) {
	const src = `## 六、

（目录项）

## 六、优化方案与规划

### P0

| **P0-1** | 主题 | 动作 | 量具 |
`
	got := magpiePlanItemIDs(src)
	if len(got) != 1 || got[0] != "P0-1" {
		t.Errorf("★ 顶部目录里的假 §六 不得干扰抽取（期望 [P0-1]，实际 %v）", got)
	}
}

// TestMagpieAuditGateRowExtractorHasTeeth 是「**审计档 §四 行级解析**」的自检。
//
// ★★ 为什么需要它：★ 判据③ 的全部牙齿都建立在「**一行** = 一个行动项 + 若干门文件」
// ★★ 这件事上 ⇒ ★★ 解析器一旦**放宽**（★ 例如把整行的路径都算进去、★ 或把
// ★★ 非路径 token 也算进去）⇒ ⇒ 「**逐行**登记」这个语义就没了 ⇒ ⇒ 判据③ 恒绿。
func TestMagpieAuditGateRowExtractorHasTeeth(t *testing.T) {
	const scope = `| 门 | 位置 | 条数 | CI | 变异 |
|---|---|---|---|---|
| P0-1 裁决表 | ` + "`a/gate_test.go`" + ` | 6 | ✅ ` + "`make guards`" + ` | M1–M4 全红 |
| ★ **P0-1 订正** 真调用复核 | ` + "`a/gate_test.go`" + `（G6 重写） | 1 | ✅ ` + "`make guards`" + ` | M11 全红 |
| ★ 门禁接线 | ` + "`b/wiring_test.go`" + ` | 3 | ✅ ` + "`make guards`" + ` | 拿掉登记 ⇒ 红 |
| P2-2 台账 | ` + "`c/ledger_test.go`" + ` + ` + "`d/table_test.go`" + ` | 8 | 随包 | M1 全红（另见 ` + "`e/prose_test.go`" + `） |
`
	rows, err := magpieAuditGateRows(scope)
	if err != nil {
		t.Fatalf("★ 解析 §四 的登记口径失败 ⇒ %v", err)
	}

	// ① ★ P0-1 的**两行**登记同一个门 ⇒ 必须是**两条**（★ 不是去重后的 1 条）。
	if len(rows["P0-1"]) != 2 {
		t.Errorf("★ P0-1 登记了两行同一个门 ⇒ 必须抽成 2 条，实际 %v", rows["P0-1"])
	}
	// ② ★ P2-2 那一行登记**两个**门 ⇒ 两条都要抽到。
	if len(rows["P2-2"]) != 2 {
		t.Errorf("★ P2-2 那一行登记了两个门 ⇒ 必须抽成 2 条，实际 %v", rows["P2-2"])
	}
	// ③ ★ **没有**行动项 ID 的行（★「★ 门禁接线」）**不得**进映射。
	if _, ok := rows["门禁接线"]; ok {
		t.Errorf("★ 没有行动项 ID 的行**不得**进映射")
	}
	if len(rows) != 2 {
		t.Errorf("★ 映射里只该有 P0-1 与 P2-2 两项，实际 %v", rows)
	}
	// ④ ★ **非路径**的 token（★ `make guards`）**不得**被当成门文件。
	for _, id := range []string{"P0-1", "P2-2"} {
		for _, p := range rows[id] {
			if !magpieRepoPathRe.MatchString(p) {
				t.Errorf("★ 非路径 token %q 被当成了门文件", p)
			}
		}
	}
	// ⑤ ★ 多重集相等：★ 少一条 ⇒ 不等；★ 多一条 ⇒ 不等；★ 一致 ⇒ 相等。
	if magpieMultisetEqual([]string{"a", "a"}, []string{"a"}) {
		t.Errorf("★ 多重集：少一条也判成相等了 ⇒ 判据③ 会放行「删掉一行」的打法")
	}
	if magpieMultisetEqual([]string{"a"}, []string{"a", "b"}) {
		t.Errorf("★ 多重集：多一条也判成相等了 ⇒ 判据③ 会放行「多加一行」的打法")
	}
	if !magpieMultisetEqual([]string{"a", "b", "a"}, []string{"a", "a", "b"}) {
		t.Errorf("★ 多重集：同样的集合应当判成相等（★ 顺序无关）")
	}
	// ⑥ ★★★ **登记单位 = 「位置」列**：★ 其它列（★ 变异列）里顺口提一句路径
	//    ⇒ **不算登记** ⇒ ⇒ 不假红（★ 第六轮 verifier 的 FR1）。
	if len(rows["P2-2"]) != 2 {
		t.Errorf("★ 变异列里的路径被当成了登记 ⇒ 判据③ 会**假红**（★ 实际 %v）", rows["P2-2"])
	}
	for _, p := range rows["P2-2"] {
		if p == "e/prose_test.go" {
			t.Errorf("★ 变异列里的路径 `e/prose_test.go` 被当成了登记 ⇒ 判据③ 会**假红**")
		}
	}
	// ⑦ ★★ 登记列解析不出来 ⇒ ★★ 必须**响亮失败**（★ 不许静默变成「零条登记」⇒ 那是假绿）。
	if _, err := magpieAuditGateRows("| 门 | 说明 |\n|---|---|\n| P0-1 | `a/gate_test.go` |\n"); err == nil {
		t.Errorf("★ 表头里没有「位置」列时**没有**报错 ⇒ 口径漂了会静默退化成假绿")
	}
}
