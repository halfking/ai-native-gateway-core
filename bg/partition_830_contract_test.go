package bg

// 830 跨文件契约门（2026-10-04）// ★ 2026-10-06：本文件原名 `partition_825_contract_test.go`、函数叫 `Test825*`、
//   常量叫 `migration825Path`，而它从建起来那天起断言的**全是 830** ——
//   连那个常量的值都写着 `830_ursm_node_snapshot_min_partitioned.sql`。
//   之所以没人发现，是因为**本仓真的有一个 825**：
//   `825_modality_graded_verification.sql`（多模态分级核实），主题完全不同。
//   ⇒ 「825」在这仓里指两件事，于是「测试名对不上内容」被完全掩护。
//
//   ⚠ 同批改名时**必须保留**上游当天给 readMigration830 加的 `.sql.skip`
//     回退：830 已被改标成 `….sql.skip`（manual-by-design 迁移，部署通道
//     `_deploy_pending_startup_migrations` 只认 .down.sql/.skip/.bak.skip）。
//     丢了它这三道门会因 no such file 全红 —— 上游在同文件的注释里记过
//     「第一次改名时我只跑了两道门就说『不红任何门』」。
//
//   ★ 唯一**不能**跟着改的是表名后缀 `_post825`：那是 830 的 down 脚本
//     自己留下的对象名。

//
// ★ 这道门防的是 473 同族的病：**迁移建了分区，却忘了接进 ensureSpecs()**。
// bg/partition_manager.go:1367-1371 的注释记录了那次故障——473 一次性补了
// 2026_09 + 2026_10 分区，但没接 tick，到 2026-11-01 全线
// `no partition of relation found`。
//
// ursm_node_snapshot_min 比 473 更脆：它**没有 DEFAULT 分区**
// （830 文件头硬约束 2），所以缺分区不是"写入降级"而是"写入全败"。
// 而 ensure 接线散在三个地方 —— SQL 里的函数定义、partition_manager 的
// ensureSpecs、db.go 的 boot ensure —— 任何一处漏掉，症状都要等到
// 跨日 0 点才在生产上显现。跨文件契约必须有门盯着。

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

const (
	migration830Path = "../sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql"
	ensureURSMFunc   = "ensure_ursm_node_snapshot_min_daily_partition"
)

// readMigration830 读 830 迁移的内容。
//
// ★ 2026-10-06：加 `.sql.skip` 回退。该文件被改标成
// `830_ursm_node_snapshot_min_partitioned.sql.skip`——因为它是一条 manual-by-design
// 迁移（Test830IsDeliberatelyNotInTheAutoStartupSequence 禁止它进 installer 的
// StartupFiles），而**部署通道** `_deploy_pending_startup_migrations`
// (deploy-lib/db-changelog.sh) 只认 `.down.sql` / `.skip` / `.bak.skip` /
// 头部 SUPERSEDED 四类，不认那条豁免 ⇒ 不改标就挡住一切部署。
//
// 文件**内容没变**，这三道门断言的东西也一字未动——只是解析路径要容得下
// 两种命名。（第一次改名时我只跑了两道门就说「不红任何门」，实际上这三道
// 会因 no such file 而红；是全量回归抓出来的。）
func readMigration830(t *testing.T) (raw []byte, path string) {
	t.Helper()
	candidates := []string{
		migration830Path,
		migration830Path + ".skip",
	}
	var errs []string
	for _, p := range candidates {
		raw, err := os.ReadFile(p)
		if err == nil {
			return raw, p
		}
		errs = append(errs, err.Error())
	}
	t.Fatalf("cannot read the 830 migration under any known name:\n  %s",
		strings.Join(errs, "\n  "))
	return nil, ""
}

// ensureURSMFuncPattern 抓 SQL 里 CREATE OR REPLACE FUNCTION 的函数名。
// 故意不写死 830 的函数名，而是从 SQL 反查 —— 这样有人改了 SQL 里的函数名
// 而忘了改 Go 侧时，本门会立刻红，而不是等到生产跨日才炸。
// (?i) + [^)]* 是必要的：参数写成 `p_date DATE` 时，参数名本身也含
// "date"，只匹配 `(date)` 会漏掉参数名 —— 判据自己漏匹配比没判据更糟，
// 因为它看起来是绿的。
var ensureURSMFuncPattern = regexp.MustCompile(
	`(?i)CREATE OR REPLACE FUNCTION\s+public\.([a-z0-9_]+)\s*\([^)]*\bdate\b[^)]*\)`)

func Test830MigrationDefinesTheEnsureFunction(t *testing.T) {
	raw, path := readMigration830(t)
	if !ensureURSMFuncPattern.Match(raw) {
		t.Fatalf("%s does not define a public.<fn>(date) ensure function — "+
			"the regex in this gate no longer matches the file (gate is stale, or the migration was rewritten)",
			path)
	}
	m := ensureURSMFuncPattern.FindSubmatch(raw)
	if got := string(m[1]); got != ensureURSMFunc {
		t.Fatalf("830 defines %q but this gate (and bg/db wiring) expect %q", got, ensureURSMFunc)
	}
}

// 核心反 473 门：SQL 里的 ensure 函数名必须真的出现在 ensureSpecs() 中。
func Test830EnsureFunctionIsWiredIntoEnsureSpecs(t *testing.T) {
	raw, err := os.ReadFile("partition_manager.go")
	if err != nil {
		t.Fatalf("read partition_manager.go: %v", err)
	}
	specs := ensureSpecs()
	var found bool
	for _, s := range specs {
		if s.fnName == ensureURSMFunc {
			found = true
			if s.partitionUnit != "day" {
				t.Fatalf("%s partitionUnit = %q, want \"day\" — "+
					"a month unit would prebuild wrong boundaries and the table is partitioned by DAY",
					ensureURSMFunc, s.partitionUnit)
			}
			// pgx 把 string 传成 text，而函数签名是 date：没有 ::date 会被
			// PG 判 42883。partition_manager.go:1377 的注释记了这个坑。
			if s.argExpr != "$1::date" {
				t.Fatalf("%s argExpr = %q, want \"$1::date\" (pgx sends a string; "+
					"timestamptz->date is not an implicit cast)", ensureURSMFunc, s.argExpr)
			}
		}
	}
	if !found {
		t.Fatalf("ensureSpecs() has no entry for %s — this is the 473 failure mode: "+
			"the migration created the partition helper but the 24h tick never calls it, "+
			"so no current-day partition exists and every snapshot write fails",
			ensureURSMFunc)
	}
	// 反向自证：门不能因为"文件读到了"就恒真。确认函数名确实是我们期望的那个
	// 拼写，而不是被某处宽泛匹配蒙对。
	if !strings.Contains(string(raw), `fnName: "`+ensureURSMFunc+`"`) {
		t.Fatalf("partition_manager.go does not literally contain fnName: %q", ensureURSMFunc)
	}
}

// db.go 的 boot ensure 是双通道之一。缺了它，实例在两次 tick 之间启动时
// 没有当日分区。
func Test830BootEnsureIsWired(t *testing.T) {
	raw, err := os.ReadFile("../db/db.go")
	if err != nil {
		t.Fatalf("read db/db.go: %v", err)
	}
	src := string(raw)
	if !strings.Contains(src, "db.ensureURSMNodeSnapshotMinDailyPartition(migCtx)") {
		t.Fatal("db.go does not call ensureURSMNodeSnapshotMinDailyPartition during migration ensure — " +
			"a gateway starting between two 24h ticks would find no current-day partition")
	}
	if !strings.Contains(src, "func (d *DB) ensureURSMNodeSnapshotMinDailyPartition") {
		t.Fatal("db.go does not define ensureURSMNodeSnapshotMinDailyPartition")
	}
	// ★ 顺序无关性：830 是手工迁移、刻意不进 installer 启动序列，所以
	// 「二进制先上、830 后跑」是常态。boot ensure 必须容忍函数不存在，
	// 否则错误会冒到 db.Open，进程进 no-DB 模式并触发部署自动回滚
	// （750 的注释记的就是这个失败模式）。这条断言钉住"探针而非报错"。
	if !strings.Contains(src, "to_regprocedure('public."+ensureURSMFunc+"(date)')") {
		t.Fatalf("db.go boot ensure must probe to_regprocedure before calling %s — "+
			"without the probe, a missing function propagates out of db.Open and takes the gateway down "+
			"on every boot until someone runs the manual 830", ensureURSMFunc)
	}
	if !strings.Contains(src, "to_regprocedure('public."+ensureURSMFunc+"(date)')") {
		t.Fatalf("db.go boot ensure must probe to_regprocedure before calling %s — "+
			"without the probe, a missing function propagates out of db.Open and takes the gateway down "+
			"on every boot until someone runs the manual 830", ensureURSMFunc)
	}
	// ★ 反向守卫：探针必须**先**判父表形态，再判函数。
	//   只判函数存在会漏掉回滚方向——830.down 之后父表回到普通表、函数却可能
	//   还在；此时 ensure 会对非分区父表执行 PARTITION OF 而报错，冒到
	//   db.Open 就是 no-DB 模式。写 830 的 down 脚本时才发现这个洞。
	if !strings.Contains(src, "relkind = 'p'") {
		t.Fatal("db.go boot ensure must check the parent table's relkind before calling " +
			ensureURSMFunc + " — after a 830.down rollback the function may survive while the " +
			"parent is no longer partitioned, and the resulting error takes the gateway down")
	}
}

// 830 刻意**不**进 installer 自动启动序列。这条门把该决定钉成显式不变量：
// 有人日后要注册它，必须先改这条门并在 runbook 里写清执行窗口。
func Test830IsDeliberatelyNotInTheAutoStartupSequence(t *testing.T) {
	runner, err := os.ReadFile("../installer/internal/dbinit/runner.go")
	if err != nil {
		t.Fatalf("read runner.go: %v", err)
	}
	if strings.Contains(string(runner), "830_ursm_node_snapshot_min_partitioned.sql") {
		t.Fatal("830 is registered in the installer startup sequence. It was a deliberate " +
			"manual-only migration: registering it makes any unattended installer upgrade " +
			"RENAME a 10 GB live table with no human confirmation point. If this is now " +
			"intentional, update the runbook's manual-execution instructions and this gate together.")
	}
	install, err := os.ReadFile("../installer/cmd/llm-gw-installer/main.go")
	if err != nil {
		t.Fatalf("read installer main.go: %v", err)
	}
	if strings.Contains(string(install), "830_ursm_node_snapshot_min_partitioned.sql") {
		t.Fatal("830 is embedded in the installer's embeddedSQLFiles map — same reasoning as above")
	}
}

// DROP 型留存按**分区名里的日期**判过期。这是 830 与
// domains/ursm/v2/persist/retention_partition.go 之间的契约：
// SQL 用 to_char(p_date,'YYYYMMDD') 命名，留存用 `_([0-9]{8})$` 解析。
// 两边漂移 ⇒ 分区永远不被清理（空间不回收），且没有任何报错。
func TestPartitionNameContractMatchesRetentionParser(t *testing.T) {
	raw, _ := readMigration830(t)
	sqlSrc := string(raw)
	if !strings.Contains(sqlSrc, "format('ursm_node_snapshot_min_%s', to_char(p_date, 'YYYYMMDD'))") {
		t.Fatal("830 no longer names partitions ursm_node_snapshot_min_YYYYMMDD — " +
			"retention_partition.go parses that exact shape and would stop reclaiming anything")
	}
	ret, err := os.ReadFile("../domains/ursm/v2/persist/retention_partition.go")
	if err != nil {
		t.Fatalf("read retention_partition.go: %v", err)
	}
	if !strings.Contains(string(ret), `_([0-9]{8})$`) {
		t.Fatal("retention_partition.go no longer parses the _YYYYMMDD suffix — " +
			"the partition-name contract is now unfulfilled on one side")
	}
}

// 本表无 DEFAULT 分区，所以「今天的分区不存在」= 全量写入失败。
// 这条门守着那个决定：有人日后「顺手加个 DEFAULT 分区兜底」时，
// 留存和 ensure 的语义都要跟着改。
func Test830StillDeclaresNoDefaultPartition(t *testing.T) {
	raw, _ := readMigration830(t)
	// ★ 断言的是**DDL 构造**，不是 "default" 这个词。
	//   初版写成"正文里出现 default 就红"，结果把文件头硬约束 2 里那句
	//   「不建 DEFAULT 分区」的说明、以及步骤注释里的 DEFAULT 全算成了
	//   违规 —— 判据红了，代码却是对的。教训同 §5.8：先确认是判据错。
	//   PG 建 DEFAULT 分区只有一种写法：PARTITION OF <父表> DEFAULT
	//   （或 CREATE TABLE ... PARTITION OF ... DEFAULT）。
	for _, pat := range []*regexp.Regexp{
		regexp.MustCompile(`(?i)PARTITION\s+OF\s+public\.ursm_node_snapshot_min\s+DEFAULT`),
		regexp.MustCompile(`(?i)CREATE\s+TABLE\s+public\.[a-z0-9_]+\s+PARTITION\s+OF\s+public\.ursm_node_snapshot_min\s+DEFAULT`),
	} {
		if pat.Match(raw) {
			t.Fatalf("830 declares a DEFAULT partition (%q). This table has none on purpose "+
				"(no snapshot_ts single-column read path; a DEFAULT would be a permanently-scanned "+
				"junk pile). Adding one changes both the ensure and the retention semantics.",
				pat.String())
		}
	}
	// 第二条更贴近现实：有人"顺手加个 DEFAULT 分区兜底"时，写出来的是
	// 真实的 CREATE TABLE ... PARTITION OF ... DEFAULT（★ `PARTITION BY ...
	// DEFAULT` 根本不是合法 PG 语法，初版变异就栽在这里：它造出了门抓不到的
	//  非法 SQL，于是 M33 绿了 —— 门没坏，是变异不成立）。
	if regexp.MustCompile(`(?i)CREATE\s+TABLE\s+public\.ursm_node_snapshot_min_default`).Match(raw) {
		t.Fatal("830 creates a ursm_node_snapshot_min_default table — see the no-DEFAULT rationale above")
	}
	// 正向自证：父表确实声明了分区键，否则上面几条否定断言是恒真的。
	if !regexp.MustCompile(`(?i)PARTITION\s+BY\s+RANGE\s*\(\s*snapshot_ts\s*\)`).Match(raw) {
		t.Fatal("830 does not declare PARTITION BY RANGE (snapshot_ts) — the two negative " +
			"DEFAULT assertions above would then be vacuous")
	}
}

// ⚠️ 2026-10-06 deploy-scanner 门（合并轮自 feat 侧 Test830LivesInManualDirNotStartupDir
// 适配到 main 的 .sql.skip 布局）。上面 Test830IsDeliberatelyNotInTheAutoStartupSequence
// 只钉住了 **installer** 的两条投递路径，而 deploy 走的是**另一个**自动扫描器 ——
//
//	scripts/deploy-lib.legacy/db-changelog.sh _deploy_pending_startup_migrations()
//	    for f in sql/migrations/startup/[0-9]*.sql
//
// 它按目录扫文件，不看任何注册表。实测回归就是这样发生的：830 明明
// 「刻意不进自动启动序列」，却曾以裸 .sql 躺在 sql/migrations/startup/ 里，
// 于是**每次 deploy-245.sh 都把它当 pending 硬跑**，在 245 上稳定炸掉：
//
//	830_ursm_node_snapshot_min_partitioned.sql:75:
//	  ERROR: public.ursm_node_snapshot_min 不存在 —— 期望它已由 01-schema.sql + 818 建立。
//
// 而 installer 侧那条门全程**绿**：它查的两处都不含这个目录 ⇒ 门没有牙，
// 回归从缺口溜过去。main 的 R47 修法是把文件改标成 `.sql.skip`
// （glob 不匹配 + 部署通道认 .skip；`.down.sql` 会被通道识别为回滚脚本、
// 不当 pending 跑，所以 down 允许留在本目录）；本门把「up 永不匹配部署
// glob」连同「.sql.skip 仍存在」一起钉死。
func Test830NeverMatchesDeployPendingScannerGlob(t *testing.T) {
	const base = "830_ursm_node_snapshot_min_partitioned"
	entries, err := os.ReadDir("../sql/migrations/startup")
	if err != nil {
		t.Fatalf("read sql/migrations/startup: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// 裸 .sql（up 形态）才是 deploy 扫描器的盘中餐；.sql.skip 不匹配
		// [0-9]*.sql，.down.sql 被通道识别为回滚脚本。
		if strings.HasSuffix(name, ".sql") && !strings.HasSuffix(name, ".down.sql") {
			t.Fatalf("830 的 up 迁移在 sql/migrations/startup/ 里有裸 .sql 形态（%s）。\n"+
				"该目录被 _deploy_pending_startup_migrations() 无条件扫 [0-9]*.sql、不看任何注册表 ——\n"+
				"830 的 RENAME + CREATE PARENT TABLE 会被每次 deploy 硬跑一遍。\n"+
				"2026-10-06 实测：它已在 245 上稳定炸掉整个部署（符号链接未切换）。\n"+
				"manual-by-design 的 up 迁移在该目录只能以 .sql.skip 形态存在。", name)
		}
	}
	// 反向锚：光断言「不匹配 glob」在文件被整个删掉时也成立，
	// 那样 830 就此静默消失而门仍然绿。必须同时钉住 .sql.skip 真实存在。
	if _, err := os.Stat("../sql/migrations/startup/" + base + ".sql.skip"); err != nil {
		t.Fatal("sql/migrations/startup/830_ursm_node_snapshot_min_partitioned.sql.skip 不存在 — " +
			"文件被删了？manual-by-design 迁移仍需以 .sql.skip 形态留档。")
	}
}
