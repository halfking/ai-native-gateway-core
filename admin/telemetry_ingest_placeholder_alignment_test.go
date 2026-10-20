package admin

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPersistRequestLogPlaceholderAlignment guards telemetryIngester.persistRequestLog's
// column-list / VALUES / Go-arg triple alignment.
//
// R51 audit (2026-09-21): the INSERT column list carried 37 columns while the
// VALUES segment stopped at $33 (three stream columns missing) — every ingest
// write died with "INSERT has more target columns than expressions" since the
// stream-telemetry columns were introduced (2026-07). This test parses
// telemetry.go itself and fails on any positional drift, mirroring the
// telemetry-package guard that was installed after the 2026-08-25 client.go
// incident.
//
// ★ 2026-10-20（迁移 890，v1 族退役）：被守的那条 INSERT **换了对象**。
//
//	原来：INSERT INTO _request_logs_hot (37 列)  —— v1 宽元数据表
//	现在：INSERT INTO usage_ledger_hot (19 列) —— 计费行（v1 表已 DROP）
//
// 主体（占位符与列/参数的三方对齐）**还在**，只是落到了本路径剩下的唯一
// 一条 INSERT 上 ⇒ 这是「主体还在、形状变了」的**反转**：判据跟着走，
// 不是把门删掉。
//
// ⚠ 为什么必须跟着走（而不是删掉这条门）：v1 表被 DROP 之后，这条 INSERT
// 是 ingest 路径上**唯一**的 PG 写入。它一旦列数与 VALUES 不齐，症状是
// "INSERT has more target columns than expressions" ⇒ 整条 ingest 事务回滚，
// **连同同事务已写的 usage_ledger 计费行**一起丢 ⇒ 计费数据静默丢失。
// 门在，症状就在被拦下。
func TestPersistRequestLogPlaceholderAlignment(t *testing.T) {
	src, err := os.ReadFile("telemetry.go")
	if err != nil {
		t.Fatalf("read telemetry.go: %v", err)
	}
	sig := "func (t *telemetryIngester) persistRequestLog"
	i := strings.Index(string(src), sig)
	if i < 0 {
		t.Fatalf("persistRequestLog not found")
	}
	rest := string(src)[i:]
	if j := strings.Index(rest[1:], "\nfunc "); j >= 0 {
		rest = rest[:j+1]
	}

	// ★ 被守的 INSERT 已从 v1 宽表换成 usage_ledger 计费行（890）。
	sqlStart := strings.Index(rest, "INSERT INTO usage_ledger_hot (")
	if sqlStart < 0 {
		t.Fatalf("INSERT INTO usage_ledger_hot not found —— " +
			"persistRequestLog 里已无别的 INSERT；若目标又变了，先确认它仍与「列/VALUES/参数」三方对齐")
	}
	sqlEnd := strings.Index(rest[sqlStart:], "`") + sqlStart
	sql := rest[sqlStart:sqlEnd]

	colStart := strings.Index(sql, "(") + 1
	colEnd := strings.Index(sql, ") VALUES")
	cols := splitTopLevel(sql[colStart:colEnd])

	valStart := strings.Index(sql, ") VALUES (") + len(") VALUES (")
	// ⚠ usage_ledger_hot 这条 INSERT **没有** ON CONFLICT 子句 ⇒ 找不到时
	// 取到串尾，而不是 -1（-1 会让切片 panic，把「形状变了」报成「测试崩了」）。
	valEnd := strings.Index(sql, "ON CONFLICT")
	if valEnd < 0 {
		valEnd = len(sql)
	}
	valsTxt := regexp.MustCompile(`--[^\n]*`).ReplaceAllString(sql[valStart:valEnd], "")
	valsTxt = strings.TrimSuffix(strings.TrimRight(valsTxt, " \t\n"), ")")
	exprs := splitTopLevel(valsTxt)

	if len(cols) == 0 || len(exprs) == 0 {
		t.Fatalf("parse failed: cols=%d exprs=%d", len(cols), len(exprs))
	}
	if len(exprs) != len(cols) {
		t.Fatalf("VALUES expression count %d != column count %d", len(exprs), len(cols))
	}

	// Bound args: everything after the closing backtick up to the error check
	// that terminates this Exec call.
	argTxt := rest[sqlEnd+1:]
	if i := strings.Index(argTxt, "if err != nil"); i >= 0 {
		argTxt = argTxt[:i]
	}
	args := splitTopLevel(strings.TrimSuffix(strings.TrimRight(argTxt, " \t\n"), ")"))
	if len(args) != len(cols)-1 { // ts is now(); every other column binds one arg
		t.Fatalf("Go arg count %d != columns-1 %d", len(args), len(cols)-1)
	}

	phRe := regexp.MustCompile(`^\$(\d+)(:.*)?$`)
	used := map[int]bool{}
	for k, expr := range exprs {
		if k == 1 && expr == "now()" { // ts
			continue
		}
		m := phRe.FindStringSubmatch(expr)
		if m == nil && strings.HasPrefix(expr, "COALESCE($") {
			inner := regexp.MustCompile(`\$(\d+)`).FindStringSubmatch(expr)
			if inner != nil {
				m = inner
			}
		}
		if m == nil {
			t.Fatalf("expression #%d unexpected form %q", k+1, expr)
		}
		n, _ := strconv.Atoi(m[1])
		if used[n] {
			t.Fatalf("placeholder $%d used more than once", n)
		}
		used[n] = true
		if n > len(args) {
			t.Fatalf("placeholder $%d beyond arg count %d", n, len(args))
		}
	}
	for n := 1; n <= len(args); n++ {
		if !used[n] {
			t.Fatalf("Go arg #%d (%s) never referenced by any placeholder", n, args[n-1])
		}
	}

	// R52 (semantic alignment): the count/uniqueness checks above cannot see
	// a slot-level mixup — COALESCE on the wrong placeholder still uses each
	// placeholder exactly once. Two invariants close that class:
	//
	// (1) positional: exprs is 0-based over the column list; slot 0 binds $1,
	// slot 1 is ts=now(), slot k (k>=2) binds $k — strictly positional.
	for k, expr := range exprs {
		col := cols[k]
		if k == 1 {
			if expr != "now()" {
				t.Fatalf("column #2 (%s) must bind now(), got %q", col, expr)
			}
			continue
		}
		inner := regexp.MustCompile(`\$(\d+)`).FindStringSubmatch(expr)
		if inner == nil {
			t.Fatalf("column %s expr %q has no placeholder", col, expr)
		}
		n, _ := strconv.Atoi(inner[1])
		want := k // slot k>=2 binds $k
		if want == 0 {
			want = 1 // slot 0 (request_id) binds $1
		}
		if n != want {
			t.Fatalf("column %s (slot %d) binds $%s, want $%d — positional drift", col, k+1, inner[1], want)
		}
	}

	// (2) NOT NULL columns must not bind a nil-able ingest-event pointer
	// bare: omitted JSON field → nil → 23502 → whole ingest tx (usage_ledger
	// included) rolls back. The known NOT NULL + pointer-bindable column is
	// stream_chunks_sent (migration 320); it must be COALESCE-wrapped, same
	// semantics as telemetry.streamChunksSentArg on the client side.
	notNullCols := map[string]bool{"stream_chunks_sent": true}
	for k, col := range cols {
		if !notNullCols[col] {
			continue
		}
		if !strings.Contains(exprs[k], "COALESCE") {
			t.Fatalf("NOT NULL column %s binds %q without COALESCE — nil pointer would 23502 the ingest tx", col, exprs[k])
		}
	}
}

// splitTopLevel splits on commas not nested inside parentheses.
func splitTopLevel(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, ch := range s {
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
		}
		if ch == ',' && depth == 0 {
			if id := strings.TrimSpace(cur.String()); id != "" {
				out = append(out, id)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(ch)
	}
	if id := strings.TrimSpace(cur.String()); id != "" {
		out = append(out, id)
	}
	return out
}
