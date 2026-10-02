package db

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// columnarInsertOnlyParentsContract 双层防漂移（2026-10-01 R17 §九.6）：
//
//  1. 本文件钉住 Go 启动 ensure 体与 sql/objects/functions 正典文件逐字
//     同义（防两处漂移出第二个名单）；
//  2. sql/migrations/startup 包的
//     TestColumnarInsertOnlyParentsCanonicalSingleFamily 钉住正典文件本身
//     = 单族 routing_decision_log（防名单被"顺手"扩族——2026-10-01 252
//     生产 21 族列存事故的根因形态）。
//
// 名单变更的唯一合法路径：改正典 .sql + 本 ensure 常量，同步更新两处契约
// 测试期望，并在审计文档留痕（ON CONFLICT/UPDATE 族不可列存的约束见
// R16/R17 报告）。
func TestColumnarInsertOnlyParentsEnsureMatchesCanonical(t *testing.T) {
	const canonPath = "../sql/objects/functions/columnar_insert_only_parents.sql"
	raw, err := os.ReadFile(canonPath)
	if err != nil {
		t.Fatalf("read canonical file: %v", err)
	}
	extract := func(sql string) string {
		// 去注释、折叠空白，比较语义体。
		var b strings.Builder
		for _, line := range strings.Split(sql, "\n") {
			if idx := strings.Index(line, "--"); idx >= 0 {
				line = line[:idx]
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		ws := regexp.MustCompile(`\s+`)
		return strings.TrimSpace(ws.ReplaceAllString(b.String(), " "))
	}
	canon := extract(string(raw))
	ensure := extract(columnarInsertOnlyParentsCanonicalBody)
	// 正典文件用 CREATE FUNCTION（pg_dump 导出形态），ensure 用 CREATE OR
	// REPLACE——归一后比较。
	canon = strings.Replace(canon, "CREATE FUNCTION", "CREATE OR REPLACE FUNCTION", 1)
	if canon != ensure {
		t.Fatalf("ensure body drifted from canonical sql/objects/functions file:\n canonical: %s\n ensure:    %s", canon, ensure)
	}
	if !strings.Contains(canon, "ARRAY['routing_decision_log']") {
		t.Fatalf("canonical family list unexpectedly changed; incident reference: docs/audit/2026-10-01-252-sql-log-audit-round17.md — widening requires explicit audit sign-off")
	}
}
