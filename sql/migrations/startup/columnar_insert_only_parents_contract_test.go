package startup

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestColumnarInsertOnlyParentsCanonicalSingleFamily 钉住列存触发器名单正典
// = 单族 routing_decision_log（2026-10-01 252 生产列存事故防复发，R17 §九.6）。
//
// 事故形态：库内该函数曾被手改成 21 族大名单，enforce_columnar_trigger
// 在每次 CREATE TABLE 后把清单内所有父表的 heap 分区批量转列存，打断全部
// ON CONFLICT/UPDATE 写路径（sessions upsert 4,284 连败、写入链冻结 1.5h）。
// 本测试让「顺手扩名单」在 CI 红：任何扩族必须显式改本测试期望 + 审计
// 留痕，并确认目标族真的是 insert-only（无 ON CONFLICT/UPDATE/FOR UPDATE
// 路径），否则 columnar_tuple_insert_speculative / tuple_lock / CTID scan
// 三类错误必复发（详见 docs/audit/2026-10-01-252-sql-log-audit-round17.md
// §〇/§一伤亡清单）。
func TestColumnarInsertOnlyParentsCanonicalSingleFamily(t *testing.T) {
	raw, err := os.ReadFile("../../objects/functions/columnar_insert_only_parents.sql")
	if err != nil {
		t.Fatalf("read canonical file: %v", err)
	}
	// 提取 ARRAY[...] 名单（去注释后匹配，防止注释里的示例名单误入）。
	var body strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	m := regexp.MustCompile(`ARRAY\[(.*?)\]`).FindStringSubmatch(body.String())
	if m == nil {
		t.Fatalf("canonical file has no ARRAY[...] family list")
	}
	items := regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(m[1], -1)
	got := make([]string, 0, len(items))
	for _, it := range items {
		got = append(got, it[1])
	}
	want := []string{"routing_decision_log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("columnar insert-only family list drifted: got %v, want %v.\n"+
			"Widening requires: (1) target family is truly insert-only (no ON CONFLICT/UPDATE/FOR UPDATE),"+
			" (2) update db/columnar_insert_only_parents_ensure.go in lockstep,"+
			" (3) audit doc trail. Incident: docs/audit/2026-10-01-252-sql-log-audit-round17.md", got, want)
	}
}
