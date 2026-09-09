package bg

// auto_index_refresher_sql_test.go — rollup SQL 形状回归测试。
//
// 背景（2026-09-10）：commit 251e751df 给 half-2 pressure_ratio 引入一个多余
// 的 ")"，DELETE 包裹语句在 PG 上每次 tick 都报
//   syntax error at or near ")" at character 6356
// 且 DELETE 失败会短路 INSERT，导致 credential_model_index_hot 五分钟一败、
// autoroute 候选池持续陈旧。修复过程中还暴露出 half-1 的 peak 子查询引用了
// 仅作为 GROUP BY 表达式（非裸列）的 COALESCE(rl.outbound_model, ...)，PG 报
//   subquery uses ungrouped column "rl.outbound_model"
// 语义类问题只有真库能发现（TEST_DATABASE_URL 存在时跑
// TestRollupCredentialModelIndex_NoDuplicateKey），本文件负责把"括号不配对"
// 这类纯形状错误挡在无 PG 的 CI 里。
//
// 判定规则：剥掉 -- 注释与单引号字面量后，括号深度不允许为负、且必须归零。

import (
	"strings"
	"testing"
)

func stripSQLCommentsAndLiterals(sql string) string {
	var b strings.Builder
	inLiteral := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		switch {
		case inLiteral:
			if c == '\'' {
				inLiteral = false
			}
		case c == '\'':
			inLiteral = true
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			// line comment: skip to end of line
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			if i < len(sql) {
				b.WriteByte('\n')
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func parenDepthProfile(sql string) (minDepth, finalDepth int) {
	clean := stripSQLCommentsAndLiterals(sql)
	depth := 0
	minDepth = 0
	for _, c := range clean {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth < minDepth {
				minDepth = depth
			}
		}
	}
	return minDepth, depth
}

func TestRollupSQL_BalancedParens(t *testing.T) {
	deleteSQL, insertSQL := credentialModelIndexRollupSQLs()
	cases := map[string]string{
		"delete(rollup wrapped in derived table)": deleteSQL,
		"insert(rollup as select source)":         insertSQL,
	}
	for name, sql := range cases {
		minDepth, finalDepth := parenDepthProfile(sql)
		if minDepth < 0 {
			t.Errorf("%s: paren depth went negative (%d) — unbalanced ')'", name, minDepth)
		}
		if finalDepth != 0 {
			t.Errorf("%s: paren depth ends at %d, want 0", name, finalDepth)
		}
	}
}

// TestRollupSQL_Half2PressureRatioShape pins the exact half-2 pressure_ratio
// expression that carried the 2026-09-10 extra-paren bug, so a refactor can't
// silently reintroduce it.
func TestRollupSQL_Half2PressureRatioShape(t *testing.T) {
	_, insertSQL := credentialModelIndexRollupSQLs()
	want := `), 0) / c.concurrency_limit)::numeric(5,4)
    END AS pressure_ratio,`
	if !strings.Contains(insertSQL, want) {
		t.Errorf("half-2 pressure_ratio expression drifted; expected balanced form:\n%s", want)
	}
}
