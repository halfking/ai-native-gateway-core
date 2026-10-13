package startup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 858 的判据有一条是**反向**的，值得单独说：
//
// 这条迁移存在的全部理由，是「部分谓词里那个 `gw_session_id <> ''` 字面量
// 让索引对参数化查询不可用」。如果哪天有人「照 hot 那条索引补齐谓词」，
// 把 `<> ''` 加回来，测试必须变红 —— 因为那会**静默地让索引重新不可用**，
// 而 CREATE INDEX IF NOT EXISTS 不会报错、测试也不会报，降级会一路跑到生产。
//
// 也就是说：这条判据守的不是「索引存在」，而是「索引还能被用」。

const idx858File = "858_request_logs_final_success_gsid_idx.sql"

// stmt858 剥掉 SQL 注释行，只留下**可执行语句**。
//
// 这不是洁癖：858 的注释里正面讨论了 `CONCURRENTLY` 与 `gw_session_id <> ”`
// 这两个词（它们是设计决定的**理由**）。判据若对整份文件做子串匹配，
// 会被自己的注释误伤——本轮实测两条判据就是这样假红的。
func stmt858(t *testing.T, name string) string {
	t.Helper()
	var kept []string
	for _, ln := range strings.Split(repoFile(t, "sql/migrations/startup/"+name), "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "--") {
			continue
		}
		if strings.TrimSpace(ln) == "" {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

// Test858MigrationCreatesPartialExistenceIndexOnParent：索引必须建在分区**父表**上。
// 建在单个分区上会漏掉下一个月度分区，而这条查询恰恰要遍历所有分区。
func Test858MigrationCreatesPartialExistenceIndexOnParent(t *testing.T) {
	up := stmt858(t, idx858File)

	if !strings.Contains(up, "ON public.request_logs (gw_session_id)") {
		t.Fatalf("858 必须在**父表** public.request_logs 上建索引，且索引列只有 gw_session_id；" +
			"建在单个分区上会漏掉下一个月度分区")
	}
	if strings.Contains(up, "CONCURRENTLY") {
		t.Error("858 不加 CONCURRENTLY —— 沿用 842 的实测结论：" +
			"CONCURRENTLY 在分区父表上被拒，由 PostgreSQL 级联")
	}
	for _, m := range []string{
		"CREATE INDEX IF NOT EXISTS request_logs_final_success_gsid_idx",
		"WHERE is_final_success AND gw_session_id IS NOT NULL",
	} {
		if !strings.Contains(up, m) {
			t.Errorf("858 缺 %q —— 索引形状不对就退不出「每次摸 5.1 万条目」这个形态", m)
		}
	}
}

// Test858PartialPredicateStaysProvable：★ 本迁移最关键的一条反向判据。
//
// 生产查询写的是 `gw_session_id <> $12`（**参数**）。PostgreSQL 只能证明
// 「参数不等式」蕴含「IS NOT NULL」，**无法**证明它蕴含「<> ” 这个字面量不等式」。
// 所以部分谓词里一旦出现 `gw_session_id <> ”`，索引就对本查询不可用 ——
// 实测该形态的索引 62 小时只有 316 次扫描，而同表另一条查询跑了 9,439 次。
func Test858PartialPredicateStaysProvable(t *testing.T) {
	up := stmt858(t, idx858File)

	// 只看 WHERE 子句，别被注释里讨论这个缺陷的文字误伤（stmt858 已剥注释）。
	where := ""
	if i := strings.Index(up, "WHERE is_final_success"); i >= 0 {
		where = up[i:]
	}
	if where == "" {
		t.Fatal("858 找不到部分谓词")
	}
	if strings.Contains(where, "gw_session_id <> ''") ||
		strings.Contains(where, "gw_session_id <> ''::text") {
		t.Fatalf("858 的部分谓词里不得出现 `gw_session_id <> ''` 字面量 —— " +
			"查询侧是参数 `gw_session_id <> $n`，PG 无法证明参数不等式蕴含字面量不等式，" +
			"加回去会让索引对本查询静默失效（实测：该形态 62h 仅 316 次扫描）")
	}
}

// Test858DownDropsTheIndex：down 必须真的删掉索引。
func Test858DownDropsTheIndex(t *testing.T) {
	down := repoFile(t, "sql/migrations/startup/858_request_logs_final_success_gsid_idx.down.sql")
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.request_logs_final_success_gsid_idx") {
		t.Fatalf("down 缺 `DROP INDEX IF EXISTS public.request_logs_final_success_gsid_idx` —— " +
			"回滚删不掉索引比不回滚更坏")
	}
}

// Test858EmbeddataCopyIsByteIdenticalToCanonical：embeddata 副本必须与正本逐字一致。
func Test858EmbeddataCopyIsByteIdenticalToCanonical(t *testing.T) {
	for _, name := range []string{
		"858_request_logs_final_success_gsid_idx.sql",
		"858_request_logs_final_success_gsid_idx.down.sql",
	} {
		canonical := repoFile(t, "sql/migrations/startup/"+name)
		embed := repoFile(t, "installer/cmd/llm-gw-installer/embeddata/startup/"+name)
		if canonical != embed {
			t.Errorf("%s 的 embeddata 副本与正本不一致 —— 全新安装会装出另一份", name)
		}
	}
}

// Test858IsRegisteredEverywhere：七处登记一处都不能漏。
// 历史上「文件存在但没登记」导致全新安装建不出正确形态，是本仓反复出现的形态。
func Test858IsRegisteredEverywhere(t *testing.T) {
	const name = "858_request_logs_final_success_gsid_idx.sql"

	checks := []struct{ rel, needle, why string }{
		{"installer/internal/dbinit/runner.go", `"` + name + `"`,
			"必须登记进 Runner.StartupFiles，否则安装器不会执行它"},
		{"installer/cmd/llm-gw-installer/main.go", "//go:embed embeddata/startup/" + name,
			"缺 go:embed 声明，文件根本进不了二进制"},
		{"installer/cmd/llm-gw-installer/main.go", `"startup/` + name + `"`,
			"缺 embeddedSQLFiles 映射条目"},
		{"sql/schema/installed_startup_migrations.tsv", name,
			"缺台账登记（该文件是 -update 标志生成的产物，不要手改）"},
		{"scripts/apply-db-revision-sequence.sh", name,
			"缺升级腿：存量库不会应用它，而这条索引对存量库同样必要"},
	}
	for _, c := range checks {
		src := repoFile(t, c.rel)
		if !strings.Contains(src, c.needle) {
			t.Errorf("%s 缺 %q —— %s", c.rel, c.needle, c.why)
		}
	}

	// 文件本体必须真的在磁盘上。
	if _, err := os.Stat(filepath.Join("..", "..", "..",
		"installer/cmd/llm-gw-installer/embeddata/startup", name)); err != nil {
		t.Fatalf("embeddata 里没有 858 的副本: %v", err)
	}
}
