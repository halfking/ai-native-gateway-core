package db

import (
	"fmt"
	"strings"
	"testing"
)

// session_probe_arm_source_contract_test.go —— 探测臂（「包含探测」第三臂）的
// 纯 Go 契约门。防两类漂移：
//
// ① canonical 列序加列/换名时探测臂没跟上 ⇒ UNION ALL 位置匹型错位 ⇒
//
//	编译期发现不了（SQL 是字符串），运行期在真库上爆 42703/类型错 ⇒
//	这里用名字逐位对拍 + panic 兜底先红在 CI。
//
// ② usage_facts 费用 join 的口径（traffic_class <> 'business'、按 request_id
//
//	GROUP BY、ts 窗参数）被顺手改动 ⇒ 费用合计偏离供应商对账 ⇒
//	关键子串逐字钉住。
func TestProbeArmProjectionParity(t *testing.T) {
	exprs := sessionProbeProjectionExprs()
	if len(exprs) != len(canonicalColumnOrderV2) {
		t.Fatalf("probe arm projection length %d != canonical %d",
			len(exprs), len(canonicalColumnOrderV2))
	}
	for i, name := range canonicalColumnOrderV2 {
		if exprs[i] == "" {
			t.Errorf("probe arm expr for column %q (position %d) is empty", name, i)
		}
	}
}

func TestProbeArmSQLComposition(t *testing.T) {
	sql := SessionFamilyTurnsSourceWindowedAllSQL("$1", "$2", "$3", "$4")

	if !strings.Contains(sql, "public.session_turns_probe_hot") {
		t.Fatal("probe arm must read session_turns_probe_hot")
	}
	if !strings.Contains(sql, "public.session_turns_hot") ||
		!strings.Contains(sql, "public.session_turns ") {
		t.Fatal("three-arm union must keep both business arms")
	}
	// 费用 join 口径（逐字）：非业务 class、按 request_id 聚合、复用 ts 窗参数。
	for _, want := range []string{
		"traffic_class <> 'business'",
		"GROUP BY request_id",
		"SUM(cost_amount) AS probe_cost",
		"SUM(credits_charged) AS probe_credits",
		"MAX(cost_currency) AS probe_currency",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("probe arm cost join missing %q", want)
		}
	}
	// 118 列逐位以 `AS <name>` 出场（UNION ALL 按位置匹型，顺序即契约）。
	pos := 0
	for _, name := range canonicalColumnOrderV2 {
		needle := " AS " + name
		idx := strings.Index(sql[pos:], needle)
		if idx < 0 {
			t.Fatalf("probe arm projection lost column %q (expected at position %d)", name, pos)
		}
		pos += idx + len(needle)
	}
	// 探测臂 ts 窗与 usage_facts occurred_at 界复用同一对参数（$3/$4）。
	if strings.Count(sql, ">= $3") != 2 || strings.Count(sql, "<= $4") != 2 {
		t.Errorf("probe arm must bind ts and usage_facts windows to $3/$4, got >=$3:%d <=$4:%d",
			strings.Count(sql, ">= $3"), strings.Count(sql, "<= $4"))
	}
}

func TestProbeArmAttachmentCountNotNull(t *testing.T) {
	// requestLogRow.AttachmentCount 是非指针 int：NULL 扫描必炸（pgx 把
	// NULL 扫进 int 报错）⇒ 探测臂必须出字面量 0。
	sql := SessionFamilyProbeArmSourceSQL("$3", "$4")
	if !strings.Contains(sql, " AS attachment_count") ||
		!strings.Contains(sql, "0 AS attachment_count") {
		t.Fatalf("probe arm attachment_count must be literal 0, sql snippet around it:\n%s",
			func() string {
				i := strings.Index(sql, "attachment_count")
				if i < 0 {
					return "<absent>"
				}
				start := i - 40
				if start < 0 {
					start = 0
				}
				end := i + 40
				if end > len(sql) {
					end = len(sql)
				}
				return fmt.Sprintf("...%s...", sql[start:end])
			}())
	}
}
