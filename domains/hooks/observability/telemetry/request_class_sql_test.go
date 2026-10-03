package telemetry

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Offline contract tests for migration 610 (V6-W1.6 R8): request_class /
// due_at must be wired through EVERY write statement of request_logs_hot and
// keep the column ↔ placeholder ↔ arg alignment. Reads client.go source
// only — same offline pattern as TestRequestLogMainTableExcludesBodyColumns.

func readClientSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatalf("read client.go: %v", err)
	}
	return string(src)
}

func functionBody(t *testing.T, src, signature string) string {
	t.Helper()
	start := strings.Index(src, signature)
	if start < 0 {
		t.Fatalf("signature not found: %s", signature)
	}
	end := strings.Index(src[start+1:], "\nfunc ")
	if end < 0 {
		t.Fatalf("function end not found after %s", signature)
	}
	return src[start : start+1+end]
}

func TestInsertRequestLogCarriesRequestClass(t *testing.T) {
	body := functionBody(t, readClientSource(t), "func (c *Client) insertRequestLog")
	for _, want := range []string{
		"request_class, due_at",               // column list tail
		"requestClassArg(entry.RequestClass)", // arg-side immediate default
		"$102",                                // due_at placeholder
		"entry.RequestClass",                  // $101 arg
		"entry.DueAt",                         // $102 arg
		"request_class        = COALESCE(EXCLUDED.request_class, request_logs_hot.request_class)",
		"due_at               = COALESCE(EXCLUDED.due_at, request_logs_hot.due_at)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("insertRequestLog missing 608 contract piece %q", want)
		}
	}
	// Arg ordering: RequestClass/DueAt must come AFTER CustomerID ($100 ↔
	// customer_id, "the LAST column" per the 507 comment, now second-to-last).
	cust := strings.Index(body, "entry.CustomerID,")
	cls := strings.Index(body, "requestClassArg(entry.RequestClass),")
	if cust < 0 || cls < 0 || cls < cust {
		t.Fatalf("608 args must follow CustomerID: cust=%d class=%d", cust, cls)
	}
	// Highest placeholder must be 103 (608's $101/$102 plus Wave 3 B1's
	// $103 credits_rate_multiplier, migration 736).
	re := regexp.MustCompile(`\$(\d+)`)
	max := 0
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n > max {
			max = n
		}
	}
	if max != 103 {
		t.Fatalf("max placeholder = %d, want 103", max)
	}
}

func TestUpdateRequestLogCarriesRequestClass(t *testing.T) {
	src := readClientSource(t)
	idx := strings.Index(src, "UPDATE request_logs_hot\n")
	if idx < 0 {
		t.Fatalf("UPDATE statement not found")
	}
	seg := src[idx : idx+20000]
	// 2026-10-02（§9.74.2）：$98 的判据由 `$98 IS NULL` 改为 `$98::text IS NULL`。
	// 语义完全不变 —— 这个门断言的三件事（$98→request_class、$99→due_at、
	// 判据为 NULL 时保留旧值）都还在。改动的原因是**类型推断**，不是语义：
	// 裸参数出现在 CASE 的 WHEN 判据里没有类型上下文，PG 拒绝整条语句
	// （SQLSTATE 42P08），于是这条生产写入路径上的 request_class/due_at
	// 永远不落库。根因与最小复现见 client.go 该处注释，以及
	// TestCaseWhenPlaceholderHasTypeContext（离线门，真库测试没 DSN 时会 skip）。
	//
	// 仍然按字面量钉：既有的「due_at 判据复用 $98」这个耦合（class 为 NULL
	// 时 due_at 也不写）尚未裁决，钉住字面量意味着任何改动都必须先想清楚。
	for _, want := range []string{
		"request_class = CASE WHEN $98::text IS NULL THEN request_class ELSE $98 END",
		"due_at = CASE WHEN $98::text IS NULL THEN due_at ELSE $99 END",
	} {
		if !strings.Contains(seg, want) {
			t.Fatalf("UPDATE missing 608 assignment %q", want)
		}
	}
}

func TestRequestLogEntryHasClassFields(t *testing.T) {
	e := &RequestLogEntry{}
	c := "scheduled"
	d := time.Unix(1800000000, 0).UTC()
	e.RequestClass = &c
	e.DueAt = &d
	if *e.RequestClass != "scheduled" || !e.DueAt.Equal(d) {
		t.Fatalf("entry class fields not settable")
	}
}
