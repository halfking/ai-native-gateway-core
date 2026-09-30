package partguard

import (
	"os"
	"path/filepath"
	"testing"
)

// 本扫描器用 filepath.Walk **直接读磁盘**，因此 `go test -overlay` 对它
// 无效——用 overlay 做变异验证会得到"变异后仍绿"的假结论（R77 在
// metricguard 上已确认过同一件事）。故判别力必须固化成永久属性：
// 用 t.TempDir() 造合成仓库，把每种形态都跑一遍，判据写死在测试里。
//
// 合成夹具的写法纪律：每个 case 都要有一条**反向对照**（negative control），
// 否则"门对任何东西都绿"和"门只对正确的东西绿"在这个测试里长得一模一样。

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scanDir(t *testing.T, dir string) []Violation {
	t.Helper()
	vs, err := CollectViolations(dir)
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func hasV(vs []Violation, file, table string) bool {
	for _, v := range vs {
		if v.File == file && v.Table == table {
			return true
		}
	}
	return false
}

// 正控：真实违规形态必须被抓到，且不能把 _hot 后缀误判成父表。
func TestSelfTest_DetectsAndDoesNotOverreach(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "bad.go", `package x

func a() string {
	return `+"`UPDATE public.session_bodies SET x=1`"+`
}
func b() string {
	return `+"`DELETE FROM sessions WHERE id=$1`"+`
}
func c() string {
	return `+"`TRUNCATE stats_event_inbox`"+`
}
`)
	// 反向对照：下列每一条都必须**不**产生违规。
	writeFile(t, dir, "good.go", `package x

func d() string {
	// 对 hot 表写入是完全正确的做法，绝不能被判成父表违规
	return `+"`UPDATE request_logs_hot SET body='{}' WHERE id=$1`"+`
}
func e() string {
	return `+"`DELETE FROM candidate_failure_logs_hot WHERE ts < now()`"+`
}
func f() string {
	return `+"`UPDATE some_unpartitioned_table SET x=1`"+`
}
func g() string {
	return `+"`INSERT INTO public.sessions (id) VALUES ($1)`"+`
}
func h() string {
	return `+"`SELECT count(*) FROM session_bodies`"+`
}
`)
	// 反向对照二：注释里提到表名 + 提到 UPDATE，不能产生违规。
	writeFile(t, dir, "commented.go", `package x

// 这里说明为什么不能 UPDATE sessions：分区父表不接受 UPDATE。
// 参考 DELETE FROM request_logs 的 hot 侧写法。
func i() string { return "no sql here" }
`)
	// 反向对照三：_test.go 不在扫描范围。
	writeFile(t, dir, "bad_test.go", `package x

func j() string { return `+"`UPDATE sessions SET title=$1`"+` }
`)

	vs := scanDir(t, dir)

	for _, want := range []struct{ file, table string }{
		{"bad.go", "session_bodies"},
		{"bad.go", "sessions"},
		{"bad.go", "stats_event_inbox"},
	} {
		if !hasV(vs, want.file, want.table) {
			t.Errorf("正控失败：%s 对父表 %s 的写入未被检出", want.file, want.table)
		}
	}
	for _, bad := range []struct{ file, table string }{
		{"good.go", "request_logs"}, // _hot 后缀被贪婪捕获成 request_logs_hot
		{"good.go", "candidate_failure_logs"},
		{"good.go", "some_unpartitioned_table"},
		{"good.go", "sessions"}, // INSERT 不是 UPDATE/DELETE/TRUNCATE
		{"good.go", "session_bodies"},
		{"commented.go", "sessions"},
		{"commented.go", "request_logs"},
		{"bad_test.go", "sessions"}, // 测试文件不在范围
	} {
		if hasV(vs, bad.file, bad.table) {
			t.Errorf("反向对照失败：%s 不应对 %s 报违规", bad.file, bad.table)
		}
	}
}

// SQLite 路径排除在合成夹具里同样可验证：同名单文件表必须被排除。
func TestSelfTest_SQLitePathExcluded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "storage/sqlite/store.go", `package x

func a() string { return `+"`DELETE FROM sessions WHERE id=$1`"+` }
`)
	writeFile(t, dir, "pg/store.go", `package x

func b() string { return `+"`DELETE FROM sessions WHERE id=$1`"+` }
`)

	vs := scanDir(t, dir)
	if hasV(vs, "storage/sqlite/store.go", "sessions") {
		t.Error("SQLite 路径应被排除，却报出了违规")
	}
	if !hasV(vs, "pg/store.go", "sessions") {
		t.Error("非 SQLite 路径下的同名父表写入必须报出（对照组）")
	}
}

// 扫描器不许腐化：多行 SQL 字面量里、非首行的语句也必须被检出。
// 真实仓库的 SQL 几乎都是跨行模板，只测单行等于没测真实形态。
func TestSelfTest_MultilineSQL(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "multi.go", `package x

func a() string {
	return `+"`\n\t\tWITH c AS (\n\t\t\tUPDATE public.sessions\n\t\t\tSET title=$1\n\t\t)\n\t\tSELECT 1`"+`
}
`)
	vs := scanDir(t, dir)
	if !hasV(vs, "multi.go", "sessions") {
		t.Error("多行 SQL 中的 UPDATE 父表未被检出——扫描器只匹配首行，判据已退化为代理量")
	}
}
