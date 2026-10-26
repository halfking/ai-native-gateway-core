package startup

// migration_902_dedup_pkey_include_test.go —— 902 的契约门（R146 §143.102 / P5）。
//
// # 这道门守的是什么
//
// 902 把 stats_event_dedup 的 pkey 换体 UNIQUE(event_id) INCLUDE(occurred_at)，
// 并删除 845 所建的 (event_id, occurred_at) 复合索引（252 只读实测 151 MB）。
// 这是 §143.27.7 登记的 P5（存储与效率综合优化的零语义项）。
//
// 索引合并类迁移最容易出的错是**「换体换掉了承重语义」**——
//	① INCLUDE 列不参与唯一性 ⇒ 若有人把键列写成 (event_id, occurred_at)，
//	  ON CONFLICT (event_id) 会静默找不到推断目标（42P10）或更糟：允许同
//	  event_id 多行 ⇒ dedup 不变式被破坏；
//	② 换体一旦写成「先删 pkey 再建」的两步非原子形态 ⇒ 活写表的并发
//	  ON CONFLICT 会撞见「无唯一约束可匹配」的瞬时窗口（42P10）；
//	③ 幂等守卫缺失 ⇒ 重跑留下孤儿 tmp 索引（本地实验阶段 4 实测 ~20 MB）。
// 所以本门钉五件事：
//
//	① up 体确实建了 INCLUDE 体临时唯一索引（键列仅 event_id、INCLUDE 仅 occurred_at）；
//	② 换体在同一 DO 块（同事务）内完成，且有目录态幂等守卫（indnkeyatts=1 AND indnatts=2）；
//	③ 有兜底 DROP tmp（任何重跑路径不留孤儿）；
//	④ **必须自带 lock_timeout**（>870 的启动迁移的仓库规矩）；
//	⑤ down 会把 845 形态（复合索引 + 单列 pkey）恢复回来。
//
// 另外钉登记面（7 点）与两份副本的逐字节一致（881/882/890/891 同款门）。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真文件被判绿。
// 合成样本只在本门内部构造，不碰仓库里的真实迁移文件。

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	mig902Name     = "902_stats_event_dedup_pkey_include.sql"
	mig902DownName = "902_stats_event_dedup_pkey_include.down.sql"

	mig902TmpIdx     = "stats_event_dedup_pkey_include_tmp"
	mig902PkeyName   = "stats_event_dedup_pkey"
	mig902OldCompIdx = "idx_stats_event_dedup_event_id_occurred_at"
	mig902DedupTable = "stats_event_dedup"
	mig902UpSHA256   = "d09a79791edf6c5160e31b59cca6a5c3fff47d8dada5ded2255df63680260829"
	mig902DownSHA256 = "0798829325a28bab9be005bfc6137437e51229d8128a1fbf8bfc6bcf1e8ef00f"
)

func mig902Tree(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func mig902Read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// mig902StripComments 去行注释与块注释（891 同款纪律：不剥注释 ⇒ 文件头
// 的论证文字里的索引形态会被当成可执行体读进来 ⇒ 恒绿门）。
func mig902StripComments(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	line := regexp.MustCompile(`--[^\n]*`)
	return line.ReplaceAllString(s, " ")
}

// mig902CheckUpBody 校验 up 体形态，返回问题描述列表（空 = 合格）。
// ★ 抽成纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
func mig902CheckUpBody(clean string) []string {
	var problems []string

	// ① INCLUDE 体临时唯一索引：键列仅 event_id、INCLUDE 仅 occurred_at。
	tmpRE := regexp.MustCompile(`(?is)CREATE\s+UNIQUE\s+INDEX\s+CONCURRENTLY\s+IF\s+NOT\s+EXISTS\s+` +
		regexp.QuoteMeta(mig902TmpIdx) + `\s+ON\s+public\.` + mig902DedupTable +
		`\s*\(\s*event_id\s*\)\s*INCLUDE\s*\(\s*occurred_at\s*\)`)
	if !tmpRE.MatchString(clean) {
		problems = append(problems, "缺 INCLUDE 体临时唯一索引（CREATE UNIQUE INDEX CONCURRENTLY "+mig902TmpIdx+" ON "+mig902DedupTable+" (event_id) INCLUDE (occurred_at)）")
	}

	// ② 换体必须原子（同一 DO 块内 DROP CONSTRAINT + ADD CONSTRAINT USING INDEX）
	//    且带目录态幂等守卫（pkey 已带 INCLUDE ⇒ 跳过）。
	swapRE := regexp.MustCompile(`(?is)DO\s+\$swap\$[\s\S]*?DROP\s+CONSTRAINT[\s\S]*?ADD\s+CONSTRAINT\s+` +
		regexp.QuoteMeta(mig902PkeyName) + `\s+PRIMARY\s+KEY\s+USING\s+INDEX\s+` + regexp.QuoteMeta(mig902TmpIdx) + `[\s\S]*?\$swap\$`)
	if !swapRE.MatchString(clean) {
		problems = append(problems, "换体不在同一 DO 块内（DROP CONSTRAINT + ADD CONSTRAINT USING INDEX 必须同事务，否则活写表的并发 ON CONFLICT 会撞见无约束窗口）")
	}
	guardRE := regexp.MustCompile(`(?is)indnkeyatts\s*=\s*1[\s\S]{0,400}indnatts\s*=\s*2`)
	if !guardRE.MatchString(clean) {
		problems = append(problems, "缺目录态幂等守卫（indnkeyatts=1 AND indnatts=2 ⇒ pkey 已带 INCLUDE 时跳过）")
	}

	// ③ 删旧复合索引 + 兜底清 tmp。
	if !strings.Contains(clean, "DROP INDEX CONCURRENTLY IF EXISTS "+mig902OldCompIdx) {
		problems = append(problems, "缺 DROP INDEX CONCURRENTLY IF EXISTS "+mig902OldCompIdx+"（845 复合索引必须被合并掉）")
	}
	if !strings.Contains(clean, "DROP INDEX CONCURRENTLY IF EXISTS "+mig902TmpIdx) {
		problems = append(problems, "缺兜底 DROP INDEX CONCURRENTLY IF EXISTS "+mig902TmpIdx+"（重跑路径不留孤儿）")
	}
	return problems
}

// ── ① up 体形态 ────────────────────────────────────────────────────────────

func TestMigration902_UpBodyShape(t *testing.T) {
	up := mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902Name))
	if problems := mig902CheckUpBody(mig902StripComments(up)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("902 up 体：%s", p)
		}
	}
	// no-transaction 标记（CONCURRENTLY 依赖非事务通道）。
	if !strings.Contains(up, "dbinit:no-transaction") {
		t.Error("902 up 体缺 dbinit:no-transaction 标记 —— 全新安装会死在 CREATE INDEX CONCURRENTLY inside transaction block")
	}
}

// TestMigration902_DetectorRejectsBadShapes 阳性对照：三份坏样本都必须被判红。
func TestMigration902_DetectorRejectsBadShapes(t *testing.T) {
	good := mig902StripComments(mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902Name)))
	if len(mig902CheckUpBody(good)) != 0 {
		t.Fatal("阳性对照失败：真文件被判为不合格 —— 检测器坏了")
	}

	// 坏样本 ①：键列写成 (event_id, occurred_at)（INCLUDE 丢失 ⇒ 唯一性语义被改）。
	bad1 := strings.Replace(good,
		"(event_id) INCLUDE (occurred_at)", "(event_id, occurred_at)", 1)
	if len(mig902CheckUpBody(bad1)) == 0 {
		t.Error("坏样本①（键列含 occurred_at、无 INCLUDE）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本 ②：换体被拆出 DO 块（非原子）。
	bad2 := strings.Replace(good, "DO $swap$", "DO $nothing$", 1)
	if len(mig902CheckUpBody(bad2)) == 0 {
		t.Error("坏样本②（换体不在 $swap$ 块内）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本 ③：兜底 DROP tmp 被删（重跑留孤儿）。
	bad3 := strings.Replace(good,
		"DROP INDEX CONCURRENTLY IF EXISTS "+mig902TmpIdx, "DROP INDEX CONCURRENTLY IF EXISTS x_tmp_never", 1)
	if len(mig902CheckUpBody(bad3)) == 0 {
		t.Error("坏样本③（兜底 DROP tmp 缺失）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② lock_timeout（仓库规矩） ─────────────────────────────────────────────

func TestMigration902_CarriesLockTimeout(t *testing.T) {
	up := mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902Name))
	clean := strings.ToLower(mig902StripComments(up))
	if !strings.Contains(clean, "lock_timeout") {
		t.Error("902 的 up 体没有 lock_timeout —— 编号 >870 的启动迁移必须自带（startup_ddl_lock_guard_gate 规矩）")
	}
	lt := regexp.MustCompile(`(?i)lock_timeout\s*=\s*'(\d+)s'`)
	m := lt.FindStringSubmatch(up)
	if m == nil {
		t.Error("902 的 lock_timeout 不是 '<秒>s' 形态")
		return
	}
	if m[1] == "0" {
		t.Error("902 的 lock_timeout = 0s ⇒ 无限等待，与「快速失败」的语义相反")
	}
}

// ── ③ down 恢复 845 形态 ──────────────────────────────────────────────────

func TestMigration902_DownRestores845Shape(t *testing.T) {
	down := mig902StripComments(mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902DownName)))
	if !strings.Contains(down, "CREATE INDEX CONCURRENTLY IF NOT EXISTS "+mig902OldCompIdx) {
		t.Error("902 down 未重建 845 复合索引 " + mig902OldCompIdx)
	}
	if !strings.Contains(down, "DROP INDEX CONCURRENTLY IF EXISTS "+mig902OldCompIdx) &&
		!strings.Contains(down, "DROP INDEX CONCURRENTLY IF EXISTS stats_event_dedup_pkey_plain_tmp") {
		t.Error("902 down 缺兜底清理")
	}
	// down 的换回段也要有目录态守卫（pkey 不是 INCLUDE 形态 ⇒ 跳过）。
	if !strings.Contains(down, "indnkeyatts") {
		t.Error("902 down 的换回段缺目录态守卫 —— 幂等性缺失")
	}
}

// ── ④ 两份副本逐字节一致 ──────────────────────────────────────────────────

func TestMigration902_EmbeddataCopiesAreByteIdentical(t *testing.T) {
	root := mig902Tree(t)
	for _, name := range []string{mig902Name, mig902DownName} {
		canonical := mig902Read(t, root, filepath.Join("sql", "migrations", "startup", name))
		embedded := mig902Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", name))
		if canonical != embedded {
			t.Errorf("%s 与 installer embeddata 副本不一致 —— 只改一份会让装出来的库与仓库的库分叉", name)
		}
		sum := sha256.Sum256([]byte(canonical))
		if got := hex.EncodeToString(sum[:]); got != mig902UpSHA256 && got != mig902DownSHA256 {
			t.Errorf("%s 的 sha256 = %s 与台账不符 —— 文件被改动后必须同步 db-changelog/TSV", name, got)
		}
	}
}

// ── ⑥ 可重入性：invalid 残留清理（2026-10-24 12h 审计轮） ─────────────────
//
// ★ 这条腿守的是本轮**实测复现**的真缺陷：
//
//	PG 的既定行为 = CREATE INDEX CONCURRENTLY 失败后留下一个 invalid 索引。
//	而步骤 ① 是 `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS <tmp>`
//	⇒ `IF NOT EXISTS` **只按名字判存在、不看 validity**
//	⇒ ⇒ 遇到残留 ⇒ ① **跳过创建**（只打一条 NOTICE）
//	⇒ ⇒ ② 的形状守卫（要求 indisvalid AND indisready）**必然抛错**
//	⇒ ⇒ 而失败后每次重跑都走同一条路径 ⇒ ⇒ **永久卡死**
//	⇒ 902 是 installer StartupFiles 的链尾 ⇒ ⇒ 它失败 = 新装链与升级链都断。
//
// ★ 修法 = 在 ① 之前加步骤 ⓪：**只删无效的**残留 tmp 索引。
// ★ 两条约束必须同时成立：
//	(a) 清理**存在**，且它的删除条件是「索引无效」（NOT (indisvalid AND indisready)）；
//	(b) 清理**不是无条件删** —— 无条件 `DROP INDEX <tmp>` 会把步骤 ①→②
//	    的合法中间产物也删掉 ⇒ 白跑一遍 CONCURRENTLY 构建。

// mig902CheckInvalidCleanup 校验步骤 ⓪ 的形态，返回问题描述列表（空 = 合格）。
// ★ 抽成纯函数 ⇒ 阳性对照喂坏样本走同一把尺子。
func mig902CheckInvalidCleanup(clean string) []string {
	var problems []string

	// ⓪ 的清理块必须存在，且必须带「无效」判定（NOT (indisvalid AND indisready)）。
	cleanupRE := regexp.MustCompile(`(?is)DO\s+\$pre\$[\s\S]*?DROP\s+INDEX[\s\S]*?\$pre\$`)
	if !cleanupRE.MatchString(clean) {
		problems = append(problems, "缺步骤 ⓪（清理 invalid 残留 tmp 索引）—— 上一次 CONCURRENTLY 构建失败留下的 invalid 索引会让本迁移永久卡死")
		return problems
	}
	invalidGuardRE := regexp.MustCompile(`(?is)NOT\s*\(\s*ix\.indisvalid\s+AND\s+ix\.indisready\s*\)`)
	if !invalidGuardRE.MatchString(clean) {
		problems = append(problems, "步骤 ⓪ 缺「索引无效」判定（NOT (indisvalid AND indisready)）—— 无条件删会把合法的中间产物也删掉")
	}
	// ⓪ 必须出现在步骤 ① 之前。
	preAt := strings.Index(clean, "DO $pre$")
	idxAt := strings.Index(clean, "CREATE UNIQUE INDEX CONCURRENTLY")
	if preAt < 0 || idxAt < 0 || preAt > idxAt {
		problems = append(problems, "步骤 ⓪ 不在步骤 ① 之前 —— 清理必须前置于 CONCURRENTLY 构建")
	}
	return problems
}

// TestMigration902_InvalidLeftoverCleanup 钉住「步骤 ⓪ 只清无效残留」。
func TestMigration902_InvalidLeftoverCleanup(t *testing.T) {
	up := mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902Name))
	if problems := mig902CheckInvalidCleanup(mig902StripComments(up)); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("902 up 体：%s", p)
		}
	}
}

// TestMigration902_InvalidCleanupDetectorRejectsBadShapes 阳性对照：两份坏样本都必须被判红。
func TestMigration902_InvalidCleanupDetectorRejectsBadShapes(t *testing.T) {
	good := mig902StripComments(mig902Read(t, mig902Tree(t), filepath.Join("sql", "migrations", "startup", mig902Name)))
	if len(mig902CheckInvalidCleanup(good)) != 0 {
		t.Fatal("阳性对照失败：真文件被判为不合格 —— 检测器坏了")
	}

	// 坏样本 ①：⓪ 整段被删（⇒ 永久卡死回归）。
	bad1 := regexp.MustCompile(`(?is)DO\s+\$pre\$[\s\S]*?\$pre\$`).ReplaceAllString(good, " ")
	if len(mig902CheckInvalidCleanup(bad1)) == 0 {
		t.Error("坏样本①（步骤 ⓪ 缺失）被判合格 —— 检测器没有鉴别力")
	}

	// 坏样本 ②：⓪ 的「无效」判定被去掉（⇒ 变成无条件删，会误删合法中间产物）。
	bad2 := strings.Replace(good,
		"NOT (ix.indisvalid AND ix.indisready)", "true", 1)
	if len(mig902CheckInvalidCleanup(bad2)) == 0 {
		t.Error("坏样本②（⓪ 缺无效判定）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ⑤ 登记面（7 点） ──────────────────────────────────────────────────────

func TestMigration902_RegisteredAtAllDeliveryPoints(t *testing.T) {
	root := mig902Tree(t)

	runner := mig902Read(t, root, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runner, `"`+mig902Name+`"`) {
		t.Error("runner.go StartupFiles 未登记 902 —— 新装链会跳过它")
	}

	main := mig902Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(main, "//go:embed embeddata/startup/"+mig902Name) {
		t.Error("main.go 缺 902 的 go:embed —— embeddata 副本成死文件")
	}
	if !strings.Contains(main, `"startup/`+mig902Name+`"`) {
		t.Error("main.go 的 embed map 未登记 902")
	}

	seq := mig902Read(t, root, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, "sql/migrations/startup/"+mig902Name) {
		t.Error("生产升级序列 apply-db-revision-sequence.sh 未登记 902 —— 升级库会被静默跳过（投递通道缺口）")
	}

	tsv := mig902Read(t, root, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, mig902Name) {
		t.Error("installed_startup_migrations.tsv 镜像未登记 902")
	}

	changelog := mig902Read(t, root, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, "| 902 | `"+mig902Name+"`") {
		t.Error("docs/db-changelog.md 未登记 902")
	}
	if !strings.Contains(changelog, mig902UpSHA256) {
		t.Error("docs/db-changelog.md 里 902 的 sha256 与文件实测不符")
	}

	conc := mig902Read(t, root, filepath.Join("installer", "cmd", "llm-gw-installer", "startup_concurrency_gate_test.go"))
	if !strings.Contains(conc, `"`+mig902Name+`":`) {
		t.Error("startup_concurrency_gate_test.go 的豁免/计数表未登记 902 —— CONCURRENTLY 会被单事务通道判死")
	}
}
