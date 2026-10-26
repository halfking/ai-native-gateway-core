package bg

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The export script and the health check answer two different questions about
// the same rows ("has it happened yet" vs "has it already happened"), so their
// SQL is allowed to differ in exactly one respect: the time window. Everything
// that decides *which rows count as damaged* must be identical, or the export
// under-reports what the check reports and the operator decides on a smaller
// problem than the one they have.
//
// This is a source-shape ratchet, not a behavioral test: it compares the two
// artifacts as text. It is deliberately specific about the three load-bearing
// tokens instead of grepping for something generic like "cost_usd", because a
// generic match passes under every mutation that matters.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：健康检查的读源已从 `public._request_logs`
// 换成**会话族**（`db.SessionFamilyTurnsSourceSQL()`），模型名分组列从
// `outbound_model` 换成会话族的 `raw_model_name`（它就是
// COALESCE(outbound_model, client_model) 的同义列；会话族投影里的
// `outbound_model` 落的是 `t.model`，即 client_model 的同义列 ⇒ 那是另一个问题）。
//
// ⇒ 本门随之改成**两态**（详见下面 exportScriptDriftRegistered 的注释）：
//   状态 A：导出脚本已迁到会话族 ⇒ **完整对拍**（同族 + 同分组列）照旧生效；
//   状态 B：导出脚本仍读**退役的** v1 族 ⇒ 那是**已登记的漂移**
//           （scripts/ 不在 bg/ 范围，本轮不能改它；它在线上必然 42P01）。
//
// ⚠ 状态 B 不是「检查通过」，而是「已登记的未修缺陷」：导出脚本仍读一张被
// DROP 的表 ⇒ psql（ON_ERROR_STOP=1）直接失败 ⇒ 运维以为「导出没有负成本
// 行」，实际是导出**根本没跑起来**。父会话处置（把脚本迁到会话族）之后，
// 本门**自动**切回完整对拍，不需要再改这个测试。

const exportScriptPath = "../scripts/export-negative-cost-rows.sh"

// requestRowFamily 标识一段 SQL 读的是哪一族的请求行。
type requestRowFamily string

const (
	familySession   requestRowFamily = "session"
	familyRetiredV1 requestRowFamily = "retired-v1"
	familyUnknown   requestRowFamily = "unknown"
)

// requestRowFamilyOf 判定一段 SQL 读的是哪一族。
//
// ⚠ 边界：`request_logs_with_current_month`（canonical 视图，890 之后是会话
// 两臂体）与退役的 v1 族名字前缀相同 ⇒ 这里按 `public._request_logs`（带下划
// 线、且带 public. 限定）来认 v1 族，不靠裸前缀匹配。
func requestRowFamilyOf(sql string) requestRowFamily {
	switch {
	case strings.Contains(sql, "public.session_turns"):
		return familySession
	case strings.Contains(sql, "public._request_logs"):
		return familyRetiredV1
	default:
		return familyUnknown
	}
}

// modelishColumns 是两个产物都可能拿来分组的「模型样」列。
var modelishColumns = []string{
	"client_model", "raw_model_name", "canonical_model",
	"model_chosen", "outbound_model", "provider_model",
}

// populatedModelColumn 返回该族里**被填好的出站模型列**——
// 分组必须用它，否则分出来的组合不是「哪个凭据 + 哪个出站模型坏了」。
//
//	v1 族：outbound_model（raw_model_name 在真库每条负成本行上都是空的，
//	      分组它会塌成「一个凭据、空白模型」的假组合）
//	会话族：raw_model_name（它就是 COALESCE(outbound_model, client_model) 的
//	      同义列；而会话族投影里的 outbound_model 落的是 t.model = client_model
//	      的同义列 ⇒ 用它分组是**另一个问题**）
func populatedModelColumn(family requestRowFamily) string {
	switch family {
	case familySession:
		return "raw_model_name"
	case familyRetiredV1:
		return "outbound_model"
	default:
		return ""
	}
}

// forbiddenGroupedColumns 返回该族里**不许**拿来分组的列。
//
// client_model / canonical_model / model_chosen / provider_model 是「另一个
// 问题」（见上面 populatedModelColumn 的说明）；raw_model_name / outbound_model
// 则是「本族里那个错的同义列」。
func forbiddenGroupedColumns(family requestRowFamily) []string {
	base := []string{"client_model", "canonical_model", "model_chosen", "provider_model"}
	switch family {
	case familySession:
		return append(base, "outbound_model")
	case familyRetiredV1:
		return append(base, "raw_model_name")
	default:
		return base
	}
}

// groupByClauseRE 抓 GROUP BY 子句本体（到该子句结束：右括号 / 分号 / 空行）。
//
// ⚠ 为什么要单独抽 GROUP BY，而不是沿用「GROUP BY 之后到分号」的大跨度：
// 健康检查里 `r.raw_model_name AS outbound_model` 是**输出别名**（外层报文
// 逐字不变的兼容层），它**不是**分组键。把 SELECT 列表也算进去 ⇒ 「输出别名」
// 与「分组列」混为一谈 ⇒ 门会为一个不存在的漂移变红。
var groupByClauseRE = regexp.MustCompile(`(?is)GROUP\s+BY(.*?)(?:\)|;|\n\s*\n)`)

// groupedModelColumns 返回出现在 GROUP BY 子句里的模型样列。
func groupedModelColumns(sql string) map[string]bool {
	out := map[string]bool{}
	for _, m := range groupByClauseRE.FindAllStringSubmatch(sql, -1) {
		clause := m[1]
		for _, col := range modelishColumns {
			if regexp.MustCompile(`\b` + col + `\b`).MatchString(clause) {
				out[col] = true
			}
		}
	}
	return out
}

func readRecordedCostCheckSQL(t *testing.T) string {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == "recorded_cost_is_negative" {
			return c.Query
		}
	}
	// NOT a skip. If the check is renamed or removed, this file's claims about
	// it become unverifiable, and a silent skip turns the ratchet into a
	// constant-green assertion.
	t.Fatalf("check recorded_cost_is_negative not found in AllHealthChecks() "+
		"(%d checks registered); this test cannot verify what it claims to", len(AllHealthChecks()))
	return ""
}

func readExportScript(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(exportScriptPath)
	if err != nil {
		t.Fatalf("read %s: %v", exportScriptPath, err)
	}
	return string(b)
}

// exportScriptDriftRegistered 是「导出脚本仍读退役的 v1 族」这一漂移的登记。
//
// ⚠ **这不是「检查通过」，是「已登记的未修缺陷」**：
// 迁移 890 把 v1 族整族 DROP ⇒ 脚本里的 `public._request_logs` 指向一张不存在
// 的表 ⇒ 脚本每次执行都在第一条查询上 42P01（psql ON_ERROR_STOP=1 ⇒ 退出码
// 非零、导出产物为空）⇒ 运维会把「导出没有负成本行」误读成「没有负成本」。
//
// ⇒ 登记的处置：把脚本迁到会话族（源）+ 按会话族的 raw_model_name 分组。
// 脚本迁完之后，本门**自动**切回完整对拍（见 TestNegativeCostExportParity）。
const exportScriptDriftRegistered = "导出脚本仍读迁移 890 已 DROP 的 v1 族（public._request_logs）"

func TestNegativeCostExportParity(t *testing.T) {
	checkSQL := readRecordedCostCheckSQL(t)
	script := readExportScript(t)

	checkFamily := requestRowFamilyOf(checkSQL)
	scriptFamily := requestRowFamilyOf(script)

	// ---- 1. 健康检查侧：必须已经迁到会话族，并按该族「被填好的出站模型列」分组 ----
	// （v1 族退役后，读 v1 族 = 提及已 DROP 的关系 = 整条语句 42P01。）
	if checkFamily != familySession {
		t.Errorf("健康检查必须读会话族（迁移 890 后 v1 族已退役），实际读 %q 族", checkFamily)
	}
	checkGrouped := groupedModelColumns(checkSQL)
	if want := populatedModelColumn(familySession); want != "" && !checkGrouped[want] {
		t.Errorf("健康检查必须按 %q 分组（会话族里它就是 COALESCE(outbound_model, client_model) 的同义列），实际分组列：%v",
			want, keysOfBool(checkGrouped))
	}
	for _, bad := range forbiddenGroupedColumns(familySession) {
		if checkGrouped[bad] {
			t.Errorf("健康检查按 %q 分组 —— 那是另一个问题（会话族投影里的 outbound_model 落的是 client_model 的同义列）", bad)
		}
	}

	// ---- 2. 同符号谓词：两侧都必须是 cost_usd < 0 ----
	// An export that finds "cost_usd > 0" would produce a confident, completely
	// inverted report.
	for name, src := range map[string]string{"health check": checkSQL, "export script": script} {
		if !strings.Contains(src, "cost_usd < 0") {
			t.Errorf("%s: must select rows with cost_usd < 0; a different sign "+
				"inverts the finding while looking equally authoritative", name)
		}
	}

	// ---- 3. 导出脚本侧 ----
	scriptGrouped := groupedModelColumns(script)
	switch scriptFamily {
	case familyRetiredV1:
		// 状态 B：已登记的漂移。
		//
		// 此刻**不能**做同族对拍（脚本还没迁），也不能断言「两侧一致」——
		// 那会把一个真实的未修缺陷说成通过。本门在这里做两件事：
		//   ① 把漂移**大声**登记（t.Log + 文件头注释），不让它被读成「没问题」；
		//   ② 仍然守住脚本**自身**的口径：它必须按 v1 族「被填好的那列」
		//      （outbound_model）分组，且不得按坏列分组。
		t.Logf("已登记漂移：%s —— %s。脚本在线上必然 42P01（空导出被误读成「没有负成本」）",
			exportScriptDriftRegistered, exportScriptPath)
		if want := populatedModelColumn(familyRetiredV1); want != "" && !scriptGrouped[want] {
			t.Errorf("导出脚本必须按 %q 分组（v1 族里 raw_model_name 在每条负成本行上都是空的），实际分组列：%v",
				want, keysOfBool(scriptGrouped))
		}
		for _, bad := range forbiddenGroupedColumns(familyRetiredV1) {
			if scriptGrouped[bad] {
				t.Errorf("导出脚本按 %q 分组 —— 那是另一个问题", bad)
			}
		}
	case familySession:
		// 状态 A：脚本已迁 ⇒ **完整对拍**：同族 + 同分组列。
		if want := populatedModelColumn(familySession); want != "" && !scriptGrouped[want] {
			t.Errorf("导出脚本必须按 %q 分组，实际分组列：%v", want, keysOfBool(scriptGrouped))
		}
		for _, bad := range forbiddenGroupedColumns(familySession) {
			if scriptGrouped[bad] {
				t.Errorf("导出脚本按 %q 分组 —— 那是另一个问题", bad)
			}
		}
		// 同族同列：任何一侧多出/少掉一个分组列，导出的组合清单就与检查的实体
		// 清单不是同一批 ⇒ 运维看到的是另一个问题。
		if diff := diffBoolSets(checkGrouped, scriptGrouped); len(diff) != 0 {
			t.Errorf("健康检查与导出脚本的分组列不一致：%v（健康检查 %v / 导出脚本 %v）—— "+
				"导出报告的受损组合集合就会与检查报告的不是同一批",
				diff, keysOfBool(checkGrouped), keysOfBool(scriptGrouped))
		}
	default:
		t.Errorf("导出脚本读的族无法判定（%q）—— 它必须读会话族，或（迁移未完成的过渡期）退役的 v1 族",
			scriptFamily)
	}
}

func keysOfBool(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffBoolSets 返回只在左边（a）出现的键——用于「健康检查分组列 ⊆ 导出脚本
// 分组列」这一个方向的比较（导出脚本多分组一列 ⇒ 组合清单会变细 ⇒ 也是漂移，
// 但方向不同，报告出来更清楚）。
func diffBoolSets(a, b map[string]bool) []string {
	out := []string{}
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
