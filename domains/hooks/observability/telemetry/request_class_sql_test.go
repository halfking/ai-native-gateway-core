package telemetry

import (
	"os"
	"regexp"
	"strconv"
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
	// 2026-10-03（§9.90.1，用户拍板解耦）：due_at 的判据由复用 $98 改为
	// 自己的 $99。那是 608 迁移「class 与 due_at 一起下发」的历史遗留耦合，
	// class 为 NULL 时 due_at 被连带跳过。
	//
	// ⚠ **只把字面量换成新的不够**——那只是让门跟着实现走，将来谁把它们
	// 绑回去，重新钉一次字面量就又绿了。所以这里断言的是**各自判据**这个
	// 语义：每列的 WHEN 判据必须用**它自己**的那个占位符。
	// 判据形如 `<col> = CASE WHEN $<n>::<type> IS NULL THEN <col> ELSE $<m> END`，
	// 这里要求 n == m（判据与写入值同参），并额外钉住 $98/$99 的归属。
	// ⚠ RE2 **不支持反向引用**（`\1` 在 Compile 期就报 invalid escape sequence），
	// 所以 THEN 分支也捕获一次、再在 Go 里比对列名相等，而不是写进正则。
	assignRe := regexp.MustCompile(
		`(?m)^\s*,\s*(request_class|due_at)\s*=\s*CASE WHEN \$(\d+)::([a-z ]+) IS NULL THEN (request_class|due_at) ELSE \$(\d+) END`)

	got := map[string][2]int{}
	for _, m := range assignRe.FindAllStringSubmatch(seg, -1) {
		if m[1] != m[4] {
			t.Fatalf("assignment 形状异常：写入 %s 却把 %s 当保留旧值的列", m[1], m[4])
		}
		n1, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatalf("%s: 判据占位符 %q 解析失败: %v", m[1], m[2], err)
		}
		n2, err := strconv.Atoi(m[5])
		if err != nil {
			t.Fatalf("%s: 写入值占位符 %q 解析失败: %v", m[1], m[5], err)
		}
		got[m[1]] = [2]int{n1, n2}
	}
	for _, col := range []string{"request_class", "due_at"} {
		ns, ok := got[col]
		if !ok {
			t.Fatalf("UPDATE missing 608 assignment for %s (pattern no longer matches; "+
				"if the SQL shape changed on purpose, update this gate deliberately)", col)
		}
		if ns[0] != ns[1] {
			t.Fatalf("%s: 判据用 $%d 而写入值用 $%d —— 两列的判据必须各自用"+
				"自己的占位符；共用一个是 608 迁移的历史耦合，已于 §9.90.1 裁决解耦", col, ns[0], ns[1])
		}
	}
	// 归属钉死：换了占位符编号而不改这里，说明有人在重排参数。
	if got["request_class"] != [2]int{98, 98} {
		t.Fatalf("request_class 占位符 = %v, want [98 98]", got["request_class"])
	}
	if got["due_at"] != [2]int{99, 99} {
		t.Fatalf("due_at 占位符 = %v, want [99 99]", got["due_at"])
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
