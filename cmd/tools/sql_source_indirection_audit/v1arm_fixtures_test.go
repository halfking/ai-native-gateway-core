package main

// v1arm_fixtures_test.go — 「部署时点 v1 臂」门的合成夹具（§19.10 跨会话订正）
//
// # 为什么要有这组夹具
//
// 门 17 的主断言是「**部署时点**不得有 v1 臂」，而「部署时点」这个概念
// 本身是由**声明 + 切换迁移**推出来的 ⇒ 光用仓库的真实状态测它，
// 会出现一个危险的空洞：**豁免机制本身坏掉时，门可能照样绿**。
//
// 例：解析声明的正则写坏了 ⇒ 声明集合为空 ⇒ 5 个视图被当成「未声明」
// ⇒ 它们进部署桶 ⇒ ① 会红（方向是安全的）；但反过来：
// 解析器**过度豁免**（任何视图都被当成已声明）⇒ 部署桶恒空 ⇒ ① 永远绿。
// ⇒ 必须有合成夹具证明「豁免不是空白支票」。
//
// 这组夹具因此覆盖四条腿：
//
//	C 切换迁移的体里还有 v1 臂      ⇒ 必须被报出来
//	D 切换迁移不在启动链里          ⇒ 必须被报出来
//	E 切换迁移排在 v1 退役之后      ⇒ 必须被报出来
//	反向：未声明却含 v1 臂          ⇒ 必须被算成部署形态
//
// 每个夹具都构造一个**完整的假仓库根**（视图 DDL + 启动迁移 + 启动链 TSV），
// 让 AuditV1Arms 走**与真实仓库完全相同**的那条代码路径。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRepoRoot 构造一个「假仓库根」，布局与真仓库一致。
type fakeRepoRoot struct {
	dir string
}

// newFakeRepoRoot 建出目录骨架。
func newFakeRepoRoot(t *testing.T) fakeRepoRoot {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{
		filepath.Join("sql", "objects", "views"),
		filepath.Join("sql", "migrations", "startup"),
		filepath.Join("sql", "schema"),
		filepath.Join("db"),
	} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return fakeRepoRoot{dir: dir}
}

func (f fakeRepoRoot) write(rel, content string) {
	p := filepath.Join(f.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		panic(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		panic(err)
	}
}

// writeView 写一个视图 DDL 到 sql/objects/views/。
func (f fakeRepoRoot) writeView(name, body string) {
	f.write("sql/objects/views/"+name, body)
}

// writeMigration 写一个启动迁移到 sql/migrations/startup/。
func (f fakeRepoRoot) writeMigration(name, body string) {
	f.write("sql/migrations/startup/"+name, body)
}

// writeChain 写启动链有序清单（sql/schema/installed_startup_migrations.tsv）。
//
// 形制与真仓库一致：`<序号>\t<文件名>`，序号从 1 起，顺序 = 应用顺序。
func (f fakeRepoRoot) writeChain(names ...string) {
	var sb strings.Builder
	sb.WriteString("# installed_startup_migrations.tsv — the installer's ORDERED startup migration list.\n")
	for i, n := range names {
		sb.WriteString(itoa(i+1) + "\t" + n + "\n")
	}
	f.write("sql/schema/installed_startup_migrations.tsv", sb.String())
}

// itoa 是 strconv.Itoa 的本地小抄（避免为夹具引入一个 import）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// baselineFallbackViewDDL 造一个「基线形态 fallback 体」的视图 DDL：
// 体里读 v1 基表 + 带两行机器可读声明。
func baselineFallbackViewDDL(view string) string {
	return "-- dbinit:baseline-v1-fallback —— 合成夹具：基线形态 fallback 体。\n" +
		"-- dbinit:baseline-v1-fallback-switch: 900_switch.sql —— 合成夹具：切换迁移。\n" +
		"CREATE VIEW public." + view + " AS\n" +
		" SELECT request_id FROM public._request_logs;\n"
}

// sessionFamilySwitchDDL 造一个「切换迁移」的体：部署形态读会话族（**没有** v1 臂）。
func sessionFamilySwitchDDL(view string) string {
	return "CREATE OR REPLACE VIEW public." + view + " AS\n" +
		" SELECT request_id FROM public.session_turns;\n"
}

// v1DropMigrationDDL 造一个「退役 v1 族」的迁移体。
//
// ★ 形制要满足 v1DropMigrationIndex 的推导判据：含 "DROP TABLE"
//
//	**且同时引用两代拼写**的 v1 基表名（带 `_` 与不带）。
func v1DropMigrationDDL() string {
	return "DO $$\nDECLARE\n  v_name text;\nBEGIN\n" +
		"  FOREACH v_name IN ARRAY ARRAY[\n" +
		"      'request_logs_bodies_hot', '_request_logs_bodies_hot',\n" +
		"      'request_logs_hot', '_request_logs_hot',\n" +
		"      'request_logs_bodies', '_request_logs_bodies',\n" +
		"      'request_logs', '_request_logs'\n" +
		"  ] LOOP\n" +
		"    EXECUTE format('DROP TABLE IF EXISTS public.%I', v_name);\n" +
		"  END LOOP;\nEND $$;\n"
}

// containsSubstring 报告列表里有没有含该子串的条目。
func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
