package sqlguard

// 989（会话分析族记账连续性修复，L20 修红轮）投递门 + 自证门。
//
// ★ 为什么需要这道门：本轮实测发现 211 的 public.schema_migrations **独缺 '981'**
//   （有 '980' 与 '990'），而 981 的产物【全部在场】⇒ 它确实跑过、只是没记账
//   （与 950/951/957/958 同款断层，960 先例同款处置）。
//   ★ 修法不能是「给 981 补一句自登记」——981 在 211/252 都已 applied 且
//   252 台账 checksum 与磁盘逐字一致 ⇒ 改字节即 L23 同款分叉
//   ⇒ 只能另立守卫式回填迁移（989）。
//   ⇒ 本门把 989 的**九面投递链**钉死（canonical / down / embeddata 副本×2 /
//     installer go:embed×2 + map×2 / dbinit StartupFiles / tsv / docker 通道
//     files=() + ledger_artifact_probe / 离线升级副本×2 / 本台账行），
//     与 997/998/999 同构 ⇒ 防止「补一次漏三面」的循环。
//
// ★★ **为什么还要一道【自证门】**：守卫式回填的整个价值在「对象不在场就不记账」。
//   若有人把 WHERE 守卫删掉，本迁移会**在 981 根本没跑过的库上也写下 '981' 行**
//   ⇒ 写下假陈述 ⇒ 记账链比断层更坏（它会把「没做」记成「做了」）。
//   ⇒ 本门把「守卫在场」逐条钉死，并做**变异自证**（把守卫换成 true ⇒ 必须被抓）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	mig989UpFile   = "989_migration_ledger_session_analytics_981_repair.sql"
	mig989DownFile = "989_migration_ledger_session_analytics_981_repair.down.sql"
	mig989TSVIndex = "391"
)

// mig989Guards 是 989 的守卫判据要点的**全部**集合（回填行的 WHERE 侧）。
//
// ★ MV 用 to_regclass 而不是 information_schema：information_schema.columns 只收
//
//	relkind ∈ {'r','v'}，**物化视图（relkind='m'）不在里面** ⇒ 用它判 MV 恒假
//	（994 产物探针的 2026-11-20 修正：MV 的列一律走 pg_catalog）。
var mig989Guards = []string{
	"to_regclass('public.user_session_aggregates') IS NOT NULL",
	"to_regclass('public.project_task_aggregates') IS NOT NULL",
	"to_regclass('public.agent_usage_aggregates') IS NOT NULL",
	"to_regclass('public.model_usage_aggregates') IS NOT NULL",
	"to_regclass('public.session_dim') IS NOT NULL",
	"to_regprocedure('public.refresh_session_analytics_view(text)') IS NOT NULL",
	"to_regprocedure('public.refresh_all_session_analytics_views()') IS NOT NULL",
	"to_regprocedure('public.session_analytics_is_probe(text,text,text)') IS NOT NULL",
}

// mig989UpViolations 返回 989 正本的内容级违例（空 = 合规）。
func mig989UpViolations(body string) []string {
	var v []string
	for _, g := range mig989Guards {
		if !strings.Contains(body, g) {
			v = append(v, "缺守卫："+g)
		}
	}
	// 回填行必须带 WHERE 守卫（不是裸 INSERT）。
	if !strings.Contains(body, "SELECT '981',") {
		v = append(v, "缺 '981' 的守卫式回填行（SELECT '981', ... WHERE ...）")
	}
	if !strings.Contains(body, "ON CONFLICT (version) DO UPDATE") {
		v = append(v, "缺幂等 ON CONFLICT 守卫")
	}
	// 自身自登记（≥952 约定）——否则本迁移应用后运行时台账仍无 989 行。
	if !strings.Contains(body, "VALUES ('989',") {
		v = append(v, "缺 '989' 的自身自登记")
	}
	// ★ 反向：回填行**不能**是裸 INSERT（守卫被摘掉的最常见形态）。
	if strings.Contains(body, "INSERT INTO public.schema_migrations (version, description)\nVALUES ('981'") {
		v = append(v, "'981' 回填是裸 INSERT（守卫被摘掉 ⇒ 会写下假陈述）")
	}
	return v
}

func TestMigration989GuardedBackfillIsGuarded(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", mig989UpFile)
	raw, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 989 正本失败: %v", err)
	}
	body := string(raw)
	if v := mig989UpViolations(body); len(v) != 0 {
		t.Fatalf("989 正本内容违例（★ 守卫式回填的价值全在「对象不在场就不记账」）: %v", v)
	}
	// 时间预算守卫（995/996/997-999 同口径）：本文件是纯台账写，但仍在
	// 共享 PG 上跑 ⇒ 必须有等锁与超时上限。
	for _, g := range []string{"SET LOCAL lock_timeout = '5s';", "SET LOCAL statement_timeout = '10min';"} {
		if !strings.Contains(body, g) {
			t.Errorf("989 缺超时守卫: %s", g)
		}
	}
}

// TestMigration989GuardMutationIsCaught —— ★★ 变异自证：把回填行的守卫换成
// 恒真（`WHERE true`）⇒ 内容判据必须点名它（★ 门有牙）。
func TestMigration989GuardMutationIsCaught(t *testing.T) {
	up := filepath.Join("..", "..", "sql", "migrations", "startup", mig989UpFile)
	raw, err := os.ReadFile(up)
	if err != nil {
		t.Fatalf("读 989 正本失败: %v", err)
	}
	body := string(raw)

	// 变异形态①：把守卫合取整体换成恒真（★ 与目标不相交的改法）。
	mutated := body
	for _, g := range mig989Guards {
		mutated = strings.ReplaceAll(mutated, g, "true")
	}
	if mutated == body {
		t.Fatal("变异锚点缺失：守卫文本没在正本里找到")
	}
	v := mig989UpViolations(mutated)
	if len(v) == 0 {
		t.Fatalf("★ 变异臂：把守卫换成恒真后仍未被抓（门无牙）")
	}

	// 变异形态②：把守卫式回填改成裸 INSERT（★ 最常见的「摘守卫」写法）。
	mutated2 := strings.ReplaceAll(body,
		"INSERT INTO public.schema_migrations (version, description)\nSELECT '981',",
		"INSERT INTO public.schema_migrations (version, description)\nVALUES ('981',")
	if mutated2 == body {
		t.Fatal("变异锚点缺失：回填行形态没在正本里找到")
	}
	if v := mig989UpViolations(mutated2); len(v) == 0 {
		t.Fatalf("★ 变异臂：裸 INSERT 回填仍未被抓（门无牙）")
	}
}

// TestMigration989DeliveryChain —— 九面投递链（与 997/998/999 同构）。
func TestMigration989DeliveryChain(t *testing.T) {
	upPath := filepath.Join("..", "..", "sql", "migrations", "startup", mig989UpFile)
	downPath := filepath.Join("..", "..", "sql", "migrations", "startup", mig989DownFile)
	upRaw, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("读 989 正本失败: %v", err)
	}
	downRaw, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatalf("读 989 down 失败: %v", err)
	}
	exec, downExec := string(upRaw), string(downRaw)

	// ① down：台账 DELETE 对偶（自身 + 被回填的 981）。
	for _, want := range []string{
		"DELETE FROM public.schema_migrations WHERE version = '989'",
		"WHERE version = '981'",
	} {
		if !strings.Contains(downExec, want) {
			t.Fatalf("989 down 缺台账 DELETE（与 up 回填对偶）: %s", want)
		}
	}
	// ★ down 不许删对象（本迁移只动台账；删对象是 981 自己的职责）。
	if strings.Contains(strings.ToUpper(downExec), "DROP ") {
		t.Fatalf("989 down 不得 DROP 对象（纯记账修复）")
	}

	// ② embeddata 副本逐字一致。
	for pair, want := range map[string]string{
		filepath.Join("embeddata", "startup", mig989UpFile):   exec,
		filepath.Join("embeddata", "startup", mig989DownFile): downExec,
	} {
		cop, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", pair))
		if err != nil {
			t.Fatalf("读 %s embeddata 副本失败: %v", pair, err)
		}
		if string(cop) != want {
			t.Fatalf("989 正本与 embeddata 副本不一致（%s）", pair)
		}
	}

	// ③ installer go:embed + embeddedSQLFiles map 双登记。
	mainRaw, err := os.ReadFile(filepath.Join("..", "..", "installer", "cmd", "llm-gw-installer", "main.go"))
	if err != nil {
		t.Fatalf("读 installer main.go 失败: %v", err)
	}
	main := collapseSpaces(string(mainRaw))
	for _, frag := range []string{
		"//go:embed embeddata/startup/" + mig989UpFile,
		"//go:embed embeddata/startup/" + mig989DownFile,
		`"startup/` + mig989UpFile + `": migrationLedgerSessionAnalytics981Repair989,`,
		`"startup/` + mig989DownFile + `": migrationLedgerSessionAnalytics981Repair989Down,`,
	} {
		if !strings.Contains(main, collapseSpaces(frag)) {
			t.Fatalf("installer main.go 缺 989 登记: %s", frag)
		}
	}

	// ④ dbinit StartupFiles。
	runnerRaw, err := os.ReadFile(filepath.Join("..", "..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("读 dbinit runner.go 失败: %v", err)
	}
	if !strings.Contains(string(runnerRaw), `"`+mig989UpFile+`",`) {
		t.Fatalf("dbinit runner.go StartupFiles 缺 989（新装库不会应用它 ⇒ 新装库仍有 981 的记账缺口）")
	}

	// ⑤ tsv 台账（由 startup_manifest_test 从 StartupFiles 派生，禁止手改）。
	tsvRaw, err := os.ReadFile(filepath.Join("..", "..", "sql", "schema", "installed_startup_migrations.tsv"))
	if err != nil {
		t.Fatalf("读 installed_startup_migrations.tsv 失败: %v", err)
	}
	if !strings.Contains(string(tsvRaw), mig989TSVIndex+"\t"+mig989UpFile) {
		t.Fatalf("tsv 缺 989 登记行（%s\\t%s）", mig989TSVIndex, mig989UpFile)
	}

	// ⑥ docker 通道：files 列表 + 产物探针。
	deployRaw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "apply-db-revision-sequence.sh"))
	if err != nil {
		t.Fatalf("读 apply-db-revision-sequence.sh 失败: %v", err)
	}
	deployText := string(deployRaw)
	for _, frag := range []string{
		`"$ROOT_DIR/sql/migrations/startup/` + mig989UpFile + `"`,
		`ledger_artifact_probe "` + mig989UpFile + `"`,
	} {
		if !strings.Contains(deployText, frag) {
			t.Fatalf("apply-db-revision-sequence.sh 缺 989 登记: %s（docker 通道会静默跳过该迁移）", frag)
		}
	}

	// ⑦ 离线升级通道副本（installer/migrations/<name>/{up,down}）。
	offlineDir := filepath.Join("..", "..", "installer", "migrations",
		strings.TrimSuffix(mig989UpFile, ".sql"))
	for pair, want := range map[string]string{
		"up.sql":   exec,
		"down.sql": downExec,
	} {
		cop, err := os.ReadFile(filepath.Join(offlineDir, pair))
		if err != nil {
			t.Fatalf("读 989 installer/migrations 副本失败: %v", err)
		}
		if string(cop) != want {
			t.Fatalf("989 正本与 installer/migrations/%s 不一致", pair)
		}
	}

	// ⑧ 台账行（docs/db-changelog.md）+ ★ sha 必须与磁盘逐字一致。
	changelogRaw, err := os.ReadFile(filepath.Join("..", "..", "docs", "db-changelog.md"))
	if err != nil {
		t.Fatalf("读 db-changelog.md 失败: %v", err)
	}
	changelog := string(changelogRaw)
	if !strings.Contains(changelog, "`"+mig989UpFile+"`") {
		t.Fatalf("db-changelog.md 缺 989 的台账行（台账连续性门会红）")
	}
	if !strings.Contains(changelog, mig989SHA) {
		t.Fatalf("db-changelog.md 的 989 行 sha 与实测值不符（期望 %s）", mig989SHA)
	}
}

// mig989SHA ＝ 989 正本的实测 sha256（★ 仓内三份副本逐字节一致 ⇒ 一处即可）。
const mig989SHA = "a66a2eddc8cf323d1b785dd74ffdd25cc255fc8b799c161f09e09458ea009599"
