package startup

// migration_900_901_redispatch_test.go —— 900（freediscovery 再投递）与
// 901（usage_ledger 按 key 聚合索引）的契约门。
//
// # 这两道门守的是什么
//
// 900：把 FreeDiscovery 数据模型（084 + 087 的列）再投递给升级通道库与
// 新装链。这类「再投递」迁移最容易出的错是**内容漂移**——投递副本与 084
// 源头形状分叉 ⇒ 装出来的库与本地开发库（084 手动应用过）形状不一致。
// 所以钉：三表 DDL 在位、087 三列并入、RLS 策略在位、post-condition 校验
// 在位、embeddata 逐字节一致。
//
// 901：usage_ledger 分区父表的 (api_key_id, ts DESC) 部分索引。索引类迁移
// 最容易出的错是「索引与查询形态脱钩」（891 契约门同款教训）⇒ 钉键序、
// 谓词、lock_timeout。
//
// 共同钉：down 形态 + 全部登记点（installer embed var / runner.go StartupFiles /
// TSV 镜像 / 升级通道 files=() / db-changelog 台账行，needle 恰好 1 次）。
//
// # 判据纪律
//
// SQL 文本断言前必须**剥注释**（R65 教训：needle 出现在注释里 ⇒ 恒绿门）。
// 每条负向检测器都配阳性对照（先证明它认得出坏形态，再证明真文件判绿）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig900Name     = "900_freediscovery_schema_reassert.sql"
	mig900DownName = "900_freediscovery_schema_reassert.down.sql"
	mig901Name     = "901_usage_ledger_api_key_id_ts_idx.sql"
	mig901DownName = "901_usage_ledger_api_key_id_ts_idx.down.sql"
)

func migRedispatchRead(t *testing.T, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(wd, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// stripCommentsRedispatch 先剥块注释再剥行注释（891 契约门同款；
// 不与 migration_602_test.go 的 stripSQLComments 重名）。
func stripCommentsRedispatch(s string) string {
	s = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?m)^\s*--.*$`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?m)--.*$`).ReplaceAllString(s, "")
	return s
}

// countNeedle 断言 needle 在文中恰好出现 n 次（Contains 型门的唯一性纪律）。
func countNeedel(t *testing.T, haystack, needle string, want int, what string) {
	t.Helper()
	if got := strings.Count(haystack, needle); got != want {
		t.Errorf("%s: needle %q 出现 %d 次, want %d", what, needle, got, want)
	}
}

// ── 900 ─────────────────────────────────────────────────────────────────

func TestMigration900_BuildsThreeFreediscoveryTables(t *testing.T) {
	up := stripCommentsRedispatch(migRedispatchRead(t, mig900Name))
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS public.provider_templates",
		"CREATE TABLE IF NOT EXISTS public.discovery_tasks",
		"CREATE TABLE IF NOT EXISTS public.discovery_results",
	} {
		if !strings.Contains(up, ddl) {
			t.Errorf("up 体缺 %s", ddl)
		}
	}
	// 087 三列并入（ADD COLUMN IF NOT EXISTS）。
	for _, col := range []string{"consecutive_scan_failures", "last_scan_failure_at", "auto_disabled_at"} {
		if !strings.Contains(up, col) {
			t.Errorf("up 体缺 087 健康反馈列 %s", col)
		}
	}
	// RLS：三表都要 ENABLE ROW LEVEL SECURITY + tenant_isolation 策略
	// （084 原文用 format('tenant_isolation_' || table_name) 动态拼接
	// ⇒ 断言拼接表达式与三表数组，而不是拼好的名）。
	if !strings.Contains(up, "ENABLE ROW LEVEL SECURITY") {
		t.Errorf("up 体缺 ENABLE ROW LEVEL SECURITY")
	}
	if !strings.Contains(up, "'tenant_isolation_' || table_name") {
		t.Errorf("up 体缺 tenant_isolation_ 动态策略拼接")
	}
	for _, tbl := range []string{"provider_templates", "discovery_tasks", "discovery_results"} {
		if !strings.Contains(up, "'"+tbl+"'") {
			t.Errorf("up 体 RLS 数组缺表 %s", tbl)
		}
	}
	// post-condition 校验段在位（缺表必须炸出来，而不是静默通过）。
	if !strings.Contains(up, "freediscovery tables still missing") {
		t.Errorf("up 体缺 post-condition 校验（RAISE EXCEPTION '900 failed...'）")
	}
}

func TestMigration900_DownDropsTables(t *testing.T) {
	down := stripCommentsRedispatch(migRedispatchRead(t, mig900DownName))
	for _, tbl := range []string{"discovery_results", "discovery_tasks", "provider_templates"} {
		if !strings.Contains(down, "DROP TABLE IF EXISTS public."+tbl) {
			t.Errorf("down 缺 DROP TABLE IF EXISTS public.%s", tbl)
		}
	}
}

// 阳性对照（真判红版，2026-10-24 批判审计加固）：把 up 体的 CREATE TABLE
// 全剥掉后，**用与 TestMigration900_BuildsThreeFreediscoveryTables 完全相同
// 的检测表达式**跑一遍 ⇒ 检测器必须判红。旧版只证明「剥除正则有效」，
// 没证明「检测器能对坏文件报错」——一个恒绿的检测器也能通过旧判据。
func TestMigration900_DetectorCanFire(t *testing.T) {
	up := stripCommentsRedispatch(migRedispatchRead(t, mig900Name))
	empty := regexp.MustCompile(`(?s)CREATE TABLE IF NOT EXISTS public\.\w+.*?;`).ReplaceAllString(up, "")
	if strings.Contains(empty, "CREATE TABLE IF NOT EXISTS public.provider_templates") {
		t.Fatalf("阳性对照前置失败：剥除后仍含 provider_templates DDL，样本没造坏")
	}
	// 与主测试同款判据重放于坏样本：主测试的失败条件（Contains 为 false
	// ⇒ t.Errorf）必须被坏样本触发 ⇒ 证明检测器不是恒绿。
	wouldFailMain := false
	for _, ddl := range []string{
		"CREATE TABLE IF NOT EXISTS public.provider_templates",
		"CREATE TABLE IF NOT EXISTS public.discovery_tasks",
		"CREATE TABLE IF NOT EXISTS public.discovery_results",
	} {
		if !strings.Contains(empty, ddl) {
			wouldFailMain = true
		}
	}
	if !wouldFailMain {
		t.Errorf("阳性对照：坏样本未触发主测试的失败条件 ⇒ 主测试的 Contains 断言对「缺全部三表 DDL」的文件会假绿")
	}
}

// ── 901 ─────────────────────────────────────────────────────────────────

func TestMigration901_IndexShapeMatchesHotSample(t *testing.T) {
	up := stripCommentsRedispatch(migRedispatchRead(t, mig901Name))
	// 键序 + 谓词必须与 hot 侧样板 usage_ledger_hot_api_key_id_ts_idx 逐列一致。
	if !strings.Contains(up, "CREATE INDEX IF NOT EXISTS idx_usage_ledger_api_key_id_ts") {
		t.Errorf("up 体缺 idx_usage_ledger_api_key_id_ts")
	}
	if !strings.Contains(up, "(api_key_id, ts DESC)") {
		t.Errorf("up 体索引键序必须是 (api_key_id, ts DESC)")
	}
	if !strings.Contains(up, "WHERE api_key_id IS NOT NULL") {
		t.Errorf("up 体缺部分谓词 WHERE api_key_id IS NOT NULL")
	}
	// 检测器阳性对照（合成坏形态：键序倒置必须被判红）。
	if strings.Contains(up, "(ts DESC, api_key_id)") {
		t.Errorf("索引键序被倒置：(ts DESC, api_key_id) 服务不了 api_key_id 等值谓词")
	}
}

func TestMigration901_CarriesLockTimeout(t *testing.T) {
	up := stripCommentsRedispatch(migRedispatchRead(t, mig901Name))
	// 891 同款：普通 CREATE INDEX（父表不支持 CONCURRENTLY）必须带
	// lock_timeout（startup_ddl_lock_guard_gate_test 的硬要求）与
	// statement_timeout（防共享 PG 30s 服务端掐长构建）。
	for _, want := range []string{"SET LOCAL lock_timeout", "SET LOCAL statement_timeout"} {
		if !strings.Contains(up, want) {
			t.Errorf("up 体缺 %s（891 同款强锁保护）", want)
		}
	}
}

func TestMigration901_LockTimeoutDetectorCanFire(t *testing.T) {
	up := stripCommentsRedispatch(migRedispatchRead(t, mig901Name))
	stripped := strings.ReplaceAll(up, "SET LOCAL lock_timeout = '60s';", "")
	// 阳性对照（2026-10-24 加固，900 同款形态）：主测试的失败条件
	// （!Contains("SET LOCAL lock_timeout") ⇒ t.Errorf）必须被坏样本触发，
	// 且判据读数必须因剥除而翻转 ⇒ 证明样本构造有效、检测器不是恒绿。
	if strings.Contains(stripped, "SET LOCAL lock_timeout") {
		t.Fatalf("阳性对照前置失败：剥除后仍含 lock_timeout，样本没造坏")
	}
	if !strings.Contains(up, "SET LOCAL lock_timeout") {
		t.Fatalf("真文件竟不含 lock_timeout：阳性对照的前提不成立")
	}
	if strings.Contains(stripped, "SET LOCAL lock_timeout") == strings.Contains(up, "SET LOCAL lock_timeout") {
		t.Errorf("阳性对照：剥除未改变判据读数 ⇒ 样本构造无效")
	}
}

func TestMigration901_DownDropsIndex(t *testing.T) {
	down := stripCommentsRedispatch(migRedispatchRead(t, mig901DownName))
	if !strings.Contains(down, "DROP INDEX IF EXISTS public.idx_usage_ledger_api_key_id_ts") {
		t.Errorf("down 缺 DROP INDEX IF EXISTS public.idx_usage_ledger_api_key_id_ts")
	}
}

// ── 共同：embeddata 逐字节一致 + 登记点恰好 1 次 ──────────────────────────

func TestMigrations900and901_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	for _, name := range []string{mig900Name, mig901Name} {
		canonical := migRedispatchRead(t, name)
		copy := migRedispatchRead(t, filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != copy {
			t.Errorf("%s canonical 与 installer embeddata 副本不一致（字节级）", name)
		}
	}
}

func TestMigrations900and901_RegistrationPointsExactlyOnce(t *testing.T) {
	type pin struct {
		label  string
		file   string
		needle string
	}
	var pins []pin
	for _, name := range []string{mig900Name, mig901Name} {
		pins = append(pins,
			// main.go 里文件名天然出现两次（go:embed 指令 + embeddedSQLFiles
			// map 键）⇒ 钉 map 形态（带 startup/ 前缀与尾冒号）保证唯一。
			pin{"installer main.go (go:embed var)", "../../../installer/cmd/llm-gw-installer/main.go", "\"startup/" + name + "\":"},
			pin{"installer runner.go (StartupFiles)", "../../../installer/internal/dbinit/runner.go", "\"" + name + "\","},
			pin{"TSV 镜像", "../../../sql/schema/installed_startup_migrations.tsv", name},
			pin{"升级通道 apply-db-revision-sequence.sh", "../../../scripts/apply-db-revision-sequence.sh", "startup/" + name},
			// ⚠ db-changelog 是**两阶段**台账（2026-10-24 批判审计 + 并行会话
			// d4d88a569 联合修正）：registered-pending → deploy 后追加
			// applied+verified 行 ⇒ 「恰好 1」与权威口径冲突
			// （verify-migration-checksums.sh :94-103「同文件多行、任一 sha
			// 命中即过」）且在首次部署后必红；判据见下方两阶段断言。
			pin{"db-changelog 台账行", "../../../docs/db-changelog.md", "`" + name + "`"},
		)
	}
	for _, p := range pins {
		body := migRedispatchRead(t, p.file)
		// ⚠ db-changelog 是【多行合法】的台账（★ 2026-10-24 R146 批判审计二次修正，
		// ★ 与并行会话 453463cd5 ⑤ 的 ≥1 口径对齐；先前「1 或 2 次 + applied 恰一行」
		// 的形状断言【方向错了】——它把存在性门错改成形状门）：
		//   ① 权威口径 = verify-migration-checksums.sh「同文件多行、任一 sha 命中即过」；
		//   ② 历史实证：862 有 4 行（两次内容修订、两个正典指纹并存）、888 有 3 行
		//      （pending → applied+verified → 新 sha 的再投递 pending）⇒ 「恰一 applied」
		//      「≤2 行」都会在未来 deploy 记账/再投递时【误红】（部署后必红）。
		// ⇒ 本 pin 的牙 = 【存在性】：至少 1 行（900/901 从台账里消失才红）。
		if p.label == "db-changelog 台账行" {
			if got := strings.Count(body, p.needle); got < 1 {
				t.Errorf("%s: needle %q 出现 %d 次, want ≥1（迁移必须登记在台账里）", p.label, p.needle, got)
			}
			continue
		}
		countNeedel(t, body, p.needle, 1, p.label)
	}
}
