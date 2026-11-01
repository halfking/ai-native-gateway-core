package promptinjection

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// 本文件是 2026-11-02 12h 审计轮 R4 的落地门：**热路径检测器的可空列
// 必须 COALESCE**。
//
// ── 为什么要有这道门 ──────────────────────────────────────────────────
//
// 2026-11-01 的 24h 审计轮（9d3b190de）修的是**管理面**那一侧的同一族缺陷：
// pgx v5 的 timestamptz 在 prepared-statement 路径下以 binary 格式返回，
// 扫进 Go 的非指针 string 必炸（`cannot scan timestamptz (OID 1184) in
// binary format into *string`）⇒ 5 个读端点自 06-29 起有行即 500。
//
// 同一族里还有另一半没修：**热路径检测器**（domains/promptinjection）读的
// 表有大量「可空列 + 无 NOT NULL + DEFAULT」，扫描目标是 Go 的非指针
// string/int/bool ⇒ **一行 NULL 就让整条读路径报错**：
//
//   · loadRules()      —— description（**无 DEFAULT** ⇒ 任何未显式赋值的新行
//                          都是 NULL）/ enabled / case_sensitive
//   · getPolicy()      —— 12 个可空列（enabled / detection_mode /
//                          enable_* / score_threshold_* / action_on_*）
//   · getSeverityAction() —— 9 个可空列
//
// 管理面那侧是 500（页面报错）；**检测器这侧更重**——`loadRules()` 返回
// err ⇒ **规则集加载失败** ⇒ 提示词注入检测**静默失效**（不是报错，是
// 不检测）。
//
// ⇒ 「同一语义只修了一半」（LESSONS §606：同一 fail-closed 语义在仓内出现
// 两次就只转了一次 ⇒ 另一条路径上仍是空白单）。
//
// ── 判据 ──────────────────────────────────────────────────────────────
//
// 修法与 admin 侧同源：**可空列一律 COALESCE 成该列的 schema DEFAULT**。
// 本门把「每张表的每个可空列 → 期望的 COALESCE 默认值」列成表，逐条断言
// 投影串里确实 COALESCE 了 ⇒ 以后加列忘了 COALESCE，门会红。
//
// ★ 门只管**可空列**；NOT NULL 列不需要 COALESCE，断言它们**没有**被
//   COALESCE 反而会挡住「顺手改一下」的噪声。

// nullableColumnExpectations：表 → (可空列 → 期望的 COALESCE 默认值)。
//
// 默认值取自各表的 schema DEFAULT（sql/objects/tables/）：
//   · prompt_injection_rules.sql      —— description **无 DEFAULT** ⇒ ''
//   · prompt_injection_policies.sql   —— enabled/detection_mode/…
//   · severity_action_matrix.sql      —— observe_action/enforce_action/…
//
// ★ description 的默认是 ''（空串）而不是 NULL：**列本身可空**，但本仓
//   读它时要求「有值」⇒ 用 '' 表达「没有描述」，与 category_new /
//   action_override 同族处置。
var nullableColumnExpectations = map[string]map[string]string{
	"prompt_injection_rules": {
		"category_new":      "category_new::text, ''",
		"description":       "description, ''",
		"enabled":           "enabled, true",
		"case_sensitive":    "case_sensitive, false",
		"action_override":   "action_override::text, ''",
		"tags":              "tags, '{}'",
	},
	"prompt_injection_policies": {
		"enabled":                    "enabled, true",
		"detection_mode":             "detection_mode, 'observe'",
		"enable_basic_rules":         "enable_basic_rules, true",
		"enable_advanced_rules":      "enable_advanced_rules, true",
		"enable_heuristics":          "enable_heuristics, true",
		"enable_ml_model":            "enable_ml_model, false",
		"enable_llm_detection":       "enable_llm_detection, true",
		"enable_canary_detection":    "enable_canary_detection, true",
		"enable_vector_similarity":   "enable_vector_similarity, false",
		"content_replacement_strategy": "content_replacement_strategy, 'llm_rewrite'",
		"max_input_length":           "max_input_length, 50000",
		"auto_learn_enabled":         "auto_learn_enabled, false",
		"detection_timeout_ms":       "detection_timeout_ms, 5000",
		"score_threshold_log":        "score_threshold_log, 3",
		"score_threshold_warn":       "score_threshold_warn, 6",
		"score_threshold_sanitize":   "score_threshold_sanitize, 8",
		"score_threshold_block":      "score_threshold_block, 10",
		"action_on_low_risk":         "action_on_low_risk, 'log'",
		"action_on_medium_risk":      "action_on_medium_risk, 'warn'",
		"action_on_high_risk":        "action_on_high_risk, 'block'",
	},
	"severity_action_matrix": {
		"observe_action":            "observe_action::text, 'log'",
		"enforce_action":            "enforce_action::text, 'block'",
		"require_approval":          "require_approval, false",
		"approval_timeout_minutes":  "approval_timeout_minutes, 0",
		"notify_on_detect":          "notify_on_detect, false",
		"notify_channels":           "notify_channels, '[]'",
		"affect_session_health":     "affect_session_health, true",
		"session_health_penalty":    "session_health_penalty, 10",
		"terminate_session_on_repeat": "terminate_session_on_repeat, false",
		"repeat_threshold":          "repeat_threshold, 3",
	},
}

// projections：表 → 它在代码里的投影串常量。
var projections = map[string]string{
	"prompt_injection_rules":    detectorRuleSelectCols,
	"prompt_injection_policies": detectorPolicySelectCols,
	"severity_action_matrix":    detectorSeverityActionSelectCols,
}

// TestDetectorQueriesCoalesceEveryNullableColumn 是本门的主体。
//
// 判据：每张表的每个可空列，在它的投影串里都必须以
// `COALESCE(<column>, <default>)` 的形态出现。
func TestDetectorQueriesCoalesceEveryNullableColumn(t *testing.T) {
	for table, cols := range nullableColumnExpectations {
		proj, ok := projections[table]
		if !ok {
			t.Fatalf("表 %s 没有登记投影串常量 ⇒ 门覆盖不到它", table)
		}
		for column, want := range cols {
			needle := "COALESCE(" + want + ")"
			if !strings.Contains(proj, needle) {
				t.Errorf("表 %s 的可空列 %s：投影串里找不到 %q ⇒ "+
					"一行 NULL 会让扫描报 \"cannot scan NULL into *string\" "+
					"（热路径检测器这侧是**静默失效**，不是 500）。\n投影串：%s",
					table, column, needle, proj)
			}
		}
	}
}

// TestDetectorQueriesDoNotCoalesceNotNullColumns 钉住「不误伤」：
// NOT NULL 列不需要 COALESCE ⇒ 对它们 COALESCE 是噪音（而且会让
// 「这列可空」的事实被稀释）。这条不是安全门，是**一致性**门。
func TestDetectorQueriesDoNotCoalesceNotNullColumns(t *testing.T) {
	notNullColumns := map[string][]string{
		"prompt_injection_rules":    {"id", "rule_name", "rule_type", "category", "pattern", "severity"},
		"prompt_injection_policies": {"tenant_id"},
		"severity_action_matrix":    {"severity_level"},
	}
	for table, cols := range notNullColumns {
		proj := projections[table]
		for _, column := range cols {
			if strings.Contains(proj, "COALESCE("+column+",") {
				t.Errorf("表 %s 的列 %s 是 NOT NULL，不该被 COALESCE ⇒ "+
					"去掉它（COALESCE 会掩盖「这列到底可不可空」这个事实）", table, column)
			}
		}
	}
}

// TestDetectorScanArityMatchesProjection 是本门里**真正有牙**的一条：
// 它把投影串的列数与 Scan 调用的实参个数**逐个函数**对上。
//
// 为什么必须是 AST 门而不是手写数字：手写的「期望 12 列」只是把我自己
// 抄的数再抄一遍 ⇒ 有人改了投影而忘了改 Scan（或反之），数字不变、
// 门照样绿 ⇒ 那正是本族缺陷的原始形态（query 与 struct 形状对不上）。
//
// 判据：对 loadRules / getPolicy / getSeverityAction 三个函数，
// 投影串的列数必须等于该函数里 Scan 的实参个数。
//
// ★ 这条门只查**个数**；列名与目标类型的对应关系由人读代码保证（仓内
//   对此没有更好的自动化手段——把 Scan 目标抽成数据会改动生产代码形状）。
func TestDetectorScanArityMatchesProjection(t *testing.T) {
	want := map[string]string{
		"loadRules":        detectorRuleSelectCols,
		"getPolicy":        detectorPolicySelectCols,
		"getSeverityAction": detectorSeverityActionSelectCols,
	}

	scanArity := scanCallArityByFunction(t)
	for fn, proj := range want {
		arity, ok := scanArity[fn]
		if !ok {
			t.Errorf("函数 %s 里没找到 Scan 调用 ⇒ 门覆盖不到它（函数改名/重构了？）", fn)
			continue
		}
		cols := countProjectedColumns(proj)
		if arity != cols {
			t.Errorf("函数 %s：Scan 实参 %d 个，而投影串有 %d 列 ⇒ "+
				"query 与 Scan 形状对不上（运行时会报 \"number of field descriptions "+
				"must equal number of destinations\"，或把 A 列的值扫进 B 字段）",
				fn, arity, cols)
		}
	}
}

// scanCallArityByFunction 解析本包源码，返回「函数名 → 该函数里第一个
// Scan 调用的实参个数」。
func scanCallArityByFunction(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("解析本包源码失败：%v", err)
	}
	out := map[string]int{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "Scan" {
						return true
					}
					if _, exists := out[fn.Name.Name]; !exists {
						out[fn.Name.Name] = len(call.Args)
					}
					return true
				})
			}
		}
	}
	return out
}

// countProjectedColumns 数投影里的顶层列（逗号分隔，忽略 COALESCE 内部
// 的逗号与括号深度）。
func countProjectedColumns(proj string) int {
	depth := 0
	cols := 0
	inCol := false
	for _, r := range proj {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				cols++
				inCol = false
				continue
			}
		}
		if r != ' ' && r != '\n' && r != '\t' && r != '\r' {
			inCol = true
		}
	}
	if inCol {
		cols++
	}
	return cols
}
