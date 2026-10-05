package apihub

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// mustReadSource 读同目录下的源码文件。
// 用源码文本而不是运行时变量，是为了让门在「常量被改名/搬走」时
// 给出可读的失败信息，而不是在断言处崩掉。
func mustReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", name, err)
	}
	return string(b)
}

// sqlConst 抽出源码里 `const <name> = ` + 反引号 + 块 + 反引号。
func sqlConst(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile("const " + name + " = `([\\s\\S]*?)`")
	m := re.FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("源码里找不到 const %s —— 常量被改名或搬走了，门失效", name)
	}
	return m[1]
}

// ── §10.27：List 的分页接线门 ───────────────────────────────────────────
//
// 为什么这道门必需：bg 侧那 7 条行为门跑在**假 store** 上，它们证明
// 「探针会请求 offset=500」，但证明不了「PG 真的会跳过前 500 行」。
// 如果 Offset 字段加了、listAssetsSQL 的 OFFSET 子句忘了加（或参数个数对不上），
// pgx 会把多余的参数丢掉/报错，而**行为门一条都不会红** —— 静默忽略。
// 这正是 §10.27 那个 bug 的形状（「offset 是死变量」），所以要有独立的一道门。
//
// 风格对齐 apihub/upsert_heartbeat_contract_test.go（源码文本门 + 取值钉门）。


func TestListAssetsSQLHasRealOffsetClause(t *testing.T) {
	sql := sqlConst(t, mustReadSource(t, "pg_store.go"), "listAssetsSQL")

	// ① 必须真的有 OFFSET 子句。没有它，Filter.Offset 就是死字段。
	if !regexp.MustCompile(`(?i)OFFSET\s+\$5`).MatchString(sql) {
		t.Fatalf("listAssetsSQL 没有 `OFFSET $5`。\n"+
			"  这会让 Filter.Offset 变成死字段：Go 侧算好了 offset，PG 收不到，\n"+
			"  「分页」变成静默无效。§10.27 的 bug 正是这个形状。\n"+
			"  当前 SQL:\n%s", sql)
	}

	// ② OFFSET 必须排在 LIMIT 之后（顺序反了是语法错，虽会报错但要在门里说清）。
	li := strings.Index(strings.ToUpper(sql), "LIMIT ")
	oi := strings.Index(strings.ToUpper(sql), "OFFSET ")
	if li < 0 || oi < 0 || li > oi {
		t.Errorf("OFFSET 必须在 LIMIT 之后：LIMIT@%d OFFSET@%d", li, oi)
	}

	// ③ 占位符最大编号必须是 5，且 $1..$5 一个不多一个不少。
	//    少传一个 ⇒ pgx 运行时报错（可见）；多传一个 ⇒ 取决于 pgx 版本，
	//    可能报错也可能被丢弃（不可见）—— 所以两头都要钉。
	max := 0
	seen := map[int]bool{}
	for _, mm := range regexp.MustCompile(`\$(\d+)`).FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(mm[1])
		if err != nil {
			t.Fatalf("占位符解析失败: %q", mm[0])
		}
		seen[n] = true
		if n > max {
			max = n
		}
	}
	if max != 5 {
		t.Errorf("占位符最大编号 = $%d，期望 $5。pgStore.List 必须传 5 个参数：\n"+
			"  (tenant, kindFilter, healthFilter, limit, offset)", max)
	}
	for i := 1; i <= 5; i++ {
		if !seen[i] {
			t.Errorf("占位符 $%d 缺失（跳号了）", i)
		}
	}
}

// TestFilterOffsetFieldExists 钉住结构体字段本身。
// 只钉名字和类型：它是 Service 接口的一部分，删掉会连带编译失败，
// 但**改类型**（比如误改成 string）也需要有人拦一下。
func TestFilterOffsetFieldExists(t *testing.T) {
	// 编译期断言：能取到、且是 int。
	f := Filter{}
	_ = f.Offset
	var want int = f.Offset
	if want != 0 {
		t.Fatalf("零值 Filter 的 Offset = %d，期望 0", want)
	}
}

// TestListClampsOffsetToZero 钉住负数保护。
// 负 OFFSET 在 PG 里是**语法错误**（不是返回空），一个来自 HTTP query
// 的 offset=-1 就能把整条查询打挂。
func TestListClampsOffsetToZero(t *testing.T) {
	src := mustReadSource(t, "pg_store.go")
	if !strings.Contains(src, "offset := f.Offset") ||
		!strings.Contains(src, "if offset < 0 {") {
		t.Errorf("pgStore.List 必须把负 Offset 归零。\n"+
			"  PG 对负 OFFSET 报语法错误，而 Filter.Offset 可能来自 HTTP query 参数。\n"+
			"  期望看到:\n\t\toffset := f.Offset\n\t\tif offset < 0 {\n\t\t\toffset = 0\n\t\t}")
	}
}
