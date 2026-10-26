package startup

// migration_895_columns_reassert_test.go —— 896（把 375/376 给
// offline_activation_requests 补的三列 + 两索引重新交付到**只靠升级通道维护的存量库**）
// 的契约门。
//
// ★★ 文件名沿用 db-changelog.md 里已登记的引用名（★ 那条登记指向的就是本文件）；
//   门里管的迁移是 **896**。
//
// # 这道门防什么
//
// 缺陷机制（2026-10-21 252 PG 日志审计轮实测）：
//
//	**生产升级通道有一道 412 的地板，375/376 在地板下面。**
//
//	· 生产 154/245 的部署走 `scripts/deploy-lib/db-changelog.sh`，它只把
//	  `version >= DB_LEDGER_RECONCILE_FROM`（默认 412）且不在 `schema_migrations`
//	  的 startup 文件当 pending ⇒ 375 / 376（< 412）对该通道**永远不可见**。
//	· 2026-10-12 把它们补登进 `scripts/apply-db-revision-sequence.sh` 的
//	  `files=()` 只修了 docker 路径（`deploy-seamless-docker.sh`）与新装；
//	  252 上该通道的账本 `gateway_db_revision_sequences` 停在 2026-09-12
//	  ⇒ 生产从未收到。
//	· 结果（252 只读实测）：`public.offline_activation_requests` 只有 374 的 9 列
//	  ⇒ `licensing/store_pgx.go` 的离线激活管理 SQL（列出 / 审批 / 驳回）
//	  在生产恒 `42703 column "activation_code" does not exist`。
//
// # 为什么是「重新交付」而不是「下调地板」
//
// 412 以下有 100+ 个被 baseline 吸收、从未在 `schema_migrations` 记账的文件，
// 整体放开会让通道在生产重放上百个历史文件（含非幂等者）⇒ 爆炸半径不可控。
// ⇒ 「新号重申旧内容」是本仓既有定式（683b / 803-806）。
//
// # 判据纪律（本门自身）
//
//   · **剥注释后再比对**（R65 的教训）：896 的文件头正文里**逐字提到**了
//     375/376、`idx_oar_status` 等 ⇒ 不剥注释的比对会**恒红**。
//   · **阳性对照**：比对前先断言「抽出来的列/索引集合非空且正好 3 列 + 2 索引」
//     ⇒ 否则「两边都抽空」也会「相等」⇒ 门恒绿（R62/R65 记过的恒绿门陷阱）。
//   · **真库腿纯只读**：只 `SELECT information_schema` / `pg_indexes` ⇒
//     **不建对象、不写行** ⇒ 可以指向任何库（含生产）⇒ 指向 252 时
//     它就是那个缺陷的**探测器**（缺列 ⇒ 红）。
//
// # 变异可证
//
// 见文件末「变异记录」：M1 改坏列类型 ⇒ 红；M2 摘掉 runner.go 登记 ⇒ 红；
// M3 动 embeddata 副本 ⇒ 红；M4 让 down 删列 ⇒ 红；M5 去掉自证断言 ⇒ 红。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// mig896File 是本门唯一关心的迁移（up）。
	mig896File = "896_offline_activation_requests_columns_reassert.sql"
	// mig896DownFile 是它的 down（只删账本行）。
	mig896DownFile = "896_offline_activation_requests_columns_reassert.down.sql"
	// mig896Table 是本门唯一关心的表（★ 896 显式只碰 public 这一张）。
	mig896Table = "public.offline_activation_requests"
)

// ─────────────────────────────────────────────────────────────────────────
// 读文件 / 剥注释 / 归一化
// ─────────────────────────────────────────────────────────────────────────

// mig896Read 读仓内文件（相对本包目录）。
func mig896Read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", rel))
	if err != nil {
		t.Fatalf("读 %s 失败: %v", rel, err)
	}
	return string(b)
}

// mig896StripComments 剥掉 SQL 注释（-- 行注释 与 /* */ 块注释）。
func mig896StripComments(s string) string {
	s = regexp.MustCompile(`--[^\n]*`).ReplaceAllString(s, " ")
	s = regexp.MustCompile(`/\*.*?\*/`).ReplaceAllString(s, " ")
	return s
}

// mig896Normalize 归一化：压空白 + 去首尾空白。
func mig896Normalize(s string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(s, " "))
}

// mig896Unqualify 去掉表名的 `public.` 限定。
//
// ★ 为什么必须去：375/376 是**裸表名**（`ALTER TABLE offline_activation_requests`），
// 而 896 显式限定 `public.offline_activation_requests`（★ 这是 896 的**有意改进**：
// 252 上另有 42 张 `maintain.*` 副本 ⇒ 不限定会打到错的表）。
// ⇒ 所以「逐字等价」指的是**语义等价**，不是**字面等价**。
func mig896Unqualify(s string) string {
	return strings.ReplaceAll(s, "public.offline_activation_requests", "offline_activation_requests")
}

// mig896AddColumnRE 匹配一条 `ADD COLUMN [IF NOT EXISTS] <name> <type...>`。
var mig896AddColumnRE = regexp.MustCompile(`(?is)ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+([^;]+);`)

// mig896CreateIndexRE 匹配一条 `CREATE INDEX [IF NOT EXISTS] <name> ON <table> (...) [WHERE ...]`。
var mig896CreateIndexRE = regexp.MustCompile(`(?is)CREATE\s+INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?(\w+)\s+ON\s+([\w.]+)\s*\(([^)]*)\)\s*(?:WHERE\s+([^\n;]+))?;`)

// mig896Column 是抽出来的一列的**语义事实**。
type mig896Column struct {
	Name    string
	Type    string
	NotNull bool
	Default string
}

// mig896Index 是抽出来的一条索引的**语义事实**。
type mig896Index struct {
	Name      string
	Table     string
	Columns   string
	Predicate string
}

// mig896ExtractColumns 抽出文件里的全部列定义（剥注释后）。
func mig896ExtractColumns(body string) []mig896Column {
	var out []mig896Column
	for _, m := range mig896AddColumnRE.FindAllStringSubmatch(body, -1) {
		rest := mig896Normalize(mig896Unqualify(m[2]))
		c := mig896Column{Name: m[1], Type: rest}
		if strings.Contains(rest, "NOT NULL") {
			c.NotNull = true
		}
		if i := strings.Index(rest, "DEFAULT "); i >= 0 {
			c.Default = mig896Normalize(rest[i+len("DEFAULT "):])
			c.Type = mig896Normalize(rest[:i])
		}
		out = append(out, c)
	}
	return out
}

// mig896ExtractIndexes 抽出文件里的全部索引定义（剥注释后）。
func mig896ExtractIndexes(body string) []mig896Index {
	var out []mig896Index
	for _, m := range mig896CreateIndexRE.FindAllStringSubmatch(body, -1) {
		out = append(out, mig896Index{
			Name:      m[1],
			Table:     mig896Unqualify(m[2]),
			Columns:   mig896Normalize(m[3]),
			Predicate: mig896Normalize(m[4]),
		})
	}
	return out
}

// mig896Body 剥注释后的正文。
func mig896Body(t *testing.T, rel string) string {
	t.Helper()
	return mig896StripComments(mig896Read(t, rel))
}

// ─────────────────────────────────────────────────────────────────────────
// ① DDL 与 375/376 语义等价（★ 逐字等价指的是列类型 / 默认值 / 索引名与谓词）
// ─────────────────────────────────────────────────────────────────────────

func Test896DDLEquivalentTo375And376(t *testing.T) {
	base := mig896Body(t, filepath.Join("sql", "migrations", "startup", "375_offline_requests_status.sql")) +
		"\n" +
		mig896Body(t, filepath.Join("sql", "migrations", "startup", "376_offline_activation_code.sql"))
	mig := mig896Body(t, filepath.Join("sql", "migrations", "startup", mig896File))

	baseCols := mig896ExtractColumns(base)
	baseIdx := mig896ExtractIndexes(base)
	migCols := mig896ExtractColumns(mig)
	migIdx := mig896ExtractIndexes(mig)

	// ── 阳性对照：两边都必须是【3 列 + 2 索引】⇒ 否则「都抽空」也会「相等」──
	if len(baseCols) != 3 || len(baseIdx) != 2 {
		t.Fatalf("375/376 里解析出的列/索引数不对（列 %d / 索引 %d，期望 3 / 2）⇒ 下面的比对不可信（提取器坏了或源文件变了）",
			len(baseCols), len(baseIdx))
	}
	if len(migCols) != 3 || len(migIdx) != 2 {
		t.Fatalf("★ 896 里解析出的列/索引数不对（列 %d / 索引 %d，期望 3 / 2）⇒ 896 没有逐字重申 375/376 的全部产物",
			len(migCols), len(migIdx))
	}

	// ── 逐列比（类型 / NOT NULL / 默认值）──
	for _, want := range baseCols {
		found := false
		for _, got := range migCols {
			if got.Name != want.Name {
				continue
			}
			found = true
			if got.Type != want.Type {
				t.Errorf("★ 列 %s 的类型与 375/376 不一致：896 = %q，源 = %q ⇒ 存量库会拿到不同的列类型",
					want.Name, got.Type, want.Type)
			}
			if got.NotNull != want.NotNull {
				t.Errorf("★ 列 %s 的 NOT NULL 与 375/376 不一致：896 = %v，源 = %v", want.Name, got.NotNull, want.NotNull)
			}
			if got.Default != want.Default {
				t.Errorf("★ 列 %s 的默认值与 375/376 不一致：896 = %q，源 = %q", want.Name, got.Default, want.Default)
			}
		}
		if !found {
			t.Errorf("★ 896 里没有重申 375/376 的列 %s ⇒ 重新交付不完整", want.Name)
		}
	}

	// ── 逐索引比（名字 / 表 / 列 / 谓词）──
	for _, want := range baseIdx {
		found := false
		for _, got := range migIdx {
			if got.Name != want.Name {
				continue
			}
			found = true
			if got.Table != want.Table {
				t.Errorf("★ 索引 %s 的表与 375/376 不一致：896 = %q，源 = %q", want.Name, got.Table, want.Table)
			}
			if got.Columns != want.Columns {
				t.Errorf("★ 索引 %s 的列与 375/376 不一致：896 = %q，源 = %q", want.Name, got.Columns, want.Columns)
			}
			if got.Predicate != want.Predicate {
				t.Errorf("★ 索引 %s 的谓词与 375/376 不一致：896 = %q，源 = %q ⇒ 索引语义不同（部分索引）",
					want.Name, got.Predicate, want.Predicate)
			}
		}
		if !found {
			t.Errorf("★ 896 里没有重申 375/376 的索引 %s ⇒ 重新交付不完整", want.Name)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ② 交付面登记（7 点）
// ─────────────────────────────────────────────────────────────────────────

func Test896RegisteredInEveryDeliverySurface(t *testing.T) {
	const name = mig896File

	// ① embeddata 副本
	if _, err := os.Stat(filepath.Join("..", "..", "..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup", name)); err != nil {
		t.Errorf("installer embeddata/startup/%s 不存在：%v", name, err)
	}

	// ② main.go：go:embed 指令
	mainGo := mig896Read(t, filepath.Join("installer", "cmd", "llm-gw-installer", "main.go"))
	if !strings.Contains(mainGo, "//go:embed embeddata/startup/"+name) {
		t.Errorf("installer main.go 缺少 896 的 go:embed 指令")
	}

	// ③ main.go：embeddedSQLFiles 映射
	if !strings.Contains(mainGo, `"startup/`+name+`":`) {
		t.Errorf("installer main.go 的 embeddedSQLFiles 映射缺 896")
	}

	// ④ runner.go：StartupFiles
	runnerGo := mig896Read(t, filepath.Join("installer", "internal", "dbinit", "runner.go"))
	if !strings.Contains(runnerGo, `"`+name+`"`) {
		t.Errorf("installer/internal/dbinit/runner.go 的 StartupFiles 缺 896 ⇒ 新装库不会应用它")
	}

	// ⑤ 启动链镜像 tsv
	tsv := mig896Read(t, filepath.Join("sql", "schema", "installed_startup_migrations.tsv"))
	if !strings.Contains(tsv, name) {
		t.Errorf("sql/schema/installed_startup_migrations.tsv 缺 896 ⇒ 仓内多道以该 TSV 为准的门会读不到它")
	}

	// ⑥ 生产升级序列
	seq := mig896Read(t, filepath.Join("scripts", "apply-db-revision-sequence.sh"))
	if !strings.Contains(seq, name) {
		t.Errorf("scripts/apply-db-revision-sequence.sh 缺 896 ⇒ 生产升级链不会应用它")
	}

	// ⑦ sql 变更台账
	changelog := mig896Read(t, filepath.Join("docs", "db-changelog.md"))
	if !strings.Contains(changelog, name) {
		t.Errorf("docs/db-changelog.md 缺 896 的登记行 ⇒ SQL 变更没有进唯一 sha256 登记表")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ③ embeddata 副本逐字一致（up + down）
// ─────────────────────────────────────────────────────────────────────────

func Test896EmbeddedCopiesAreByteIdentical(t *testing.T) {
	for _, n := range []string{mig896File, mig896DownFile} {
		canonical := mig896Read(t, filepath.Join("sql", "migrations", "startup", n))
		embed := mig896Read(t, filepath.Join("installer", "cmd", "llm-gw-installer", "embeddata", "startup", n))
		if canonical != embed {
			t.Errorf("%s：embeddata 副本与 canonical 不逐字一致 ⇒ installer 会装出另一个版本", n)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ④ 锁与自证断言
// ─────────────────────────────────────────────────────────────────────────

func Test896HasLockTimeoutAndSelfAssertion(t *testing.T) {
	body := mig896Body(t, filepath.Join("sql", "migrations", "startup", mig896File))

	if !strings.Contains(body, "SET LOCAL lock_timeout") {
		t.Error("★ 896 没有 lock_timeout ⇒ 部署期可能挂在锁队列里放大为热路径停顿（878/882 款）")
	}
	if !strings.Contains(body, "SET LOCAL statement_timeout") {
		t.Error("★ 896 没有 statement_timeout ⇒ 异常形态下会无限期占着锁")
	}
	// ★ 自证：ADD COLUMN IF NOT EXISTS 不会报错 ⇒ 必须自己查「三列齐不齐」。
	if !strings.Contains(body, "RAISE EXCEPTION") {
		t.Error("★ 896 没有自证断言（RAISE EXCEPTION）⇒ 「ADD COLUMN IF NOT EXISTS」在缺列时会静默通过")
	}
	if !strings.Contains(body, "present <> 3") {
		t.Error("★ 896 的自证断言没有检查「三列是否齐备」⇒ 断言是空的")
	}
	// ★ 只碰 public：252 上另有 42 张 maintain.* 副本。
	if !strings.Contains(body, "public.offline_activation_requests") {
		t.Error("★ 896 没有显式限定 public.offline_activation_requests ⇒ 可能打到 maintain.* 的副本")
	}
	if strings.Contains(body, "maintain.") {
		t.Error("★ 896 的正文里出现了 maintain.* ⇒ 它明确要动不该动的副本")
	}
	// ★ 表不存在时跳过（licensing schema 未初始化的库）。
	if !strings.Contains(body, "to_regclass") {
		t.Error("★ 896 没有用 to_regclass 判表在不在 ⇒ 表不存在时会直接报错中断启动链")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ⑤ 幂等（全部 IF NOT EXISTS）
// ─────────────────────────────────────────────────────────────────────────

func Test896IsIdempotent(t *testing.T) {
	// ★ 逐条判「ADD COLUMN 必须带 IF NOT EXISTS」。
	raw := mig896StripComments(mig896Read(t, filepath.Join("sql", "migrations", "startup", mig896File)))
	for _, stmt := range strings.Split(raw, ";") {
		s := mig896Normalize(stmt)
		if !strings.Contains(strings.ToUpper(s), "ADD COLUMN") {
			continue
		}
		if !strings.Contains(strings.ToUpper(s), "IF NOT EXISTS") {
			t.Errorf("★ 896 里有不带 IF NOT EXISTS 的 ADD COLUMN ⇒ 重跑会报错：%s", s)
		}
	}
	for _, stmt := range strings.Split(raw, ";") {
		s := mig896Normalize(stmt)
		if !strings.Contains(strings.ToUpper(s), "CREATE INDEX") {
			continue
		}
		if !strings.Contains(strings.ToUpper(s), "IF NOT EXISTS") {
			t.Errorf("★ 896 里有不带 IF NOT EXISTS 的 CREATE INDEX ⇒ 重跑会报错：%s", s)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ⑥ down 只删账本行（列与索引的所有者是 375/376，down 不得越权拆掉）
// ─────────────────────────────────────────────────────────────────────────

func Test896DownOnlyDeletesLedgerRow(t *testing.T) {
	downPath := filepath.Join("sql", "migrations", "startup", mig896DownFile)
	if _, err := os.Stat(filepath.Join("..", "..", "..", downPath)); err != nil {
		t.Fatalf("896 的 down 文件不存在：%v（每条迁移都必须带同名 .down.sql）", err)
	}
	down := mig896StripComments(mig896Read(t, downPath))

	if !strings.Contains(down, "DELETE FROM public.schema_migrations") ||
		!strings.Contains(down, "'896'") {
		t.Errorf("★ 896 的 down 没有删 schema_migrations 的 '896' 账本行 ⇒ 账本会骗人")
	}
	for _, banned := range []string{"DROP COLUMN", "DROP INDEX", "DROP TABLE"} {
		if strings.Contains(strings.ToUpper(down), banned) {
			t.Errorf("★ 896 的 down 里出现了 %s ⇒ down 越权拆掉 375/376 的产物（列承载审批数据）", banned)
		}
	}
	if !strings.Contains(down, "COMMIT") {
		t.Error("★ 896 的 down 没有 COMMIT")
	}
}

// ─────────────────────────────────────────────────────────────────────────
// ⑦ 真库腿（**纯只读**：不建对象、不写行 ⇒ 可指向任何库，含生产）
//
// ★ 指向 252 时，它就是那个缺陷的**探测器**：三列缺任意一 ⇒ 红。
// ─────────────────────────────────────────────────────────────────────────

func Test896RealDBHasTheThreeColumns(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿（本腿纯只读，可指向任何库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连库失败: %v", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `SELECT to_regclass('public.offline_activation_requests') IS NOT NULL`).Scan(&exists)
	if err != nil {
		t.Fatalf("探测表是否存在失败: %v", err)
	}
	if !exists {
		t.Skip("库里没有 public.offline_activation_requests（licensing schema 未初始化）⇒ 跳过真库腿")
	}

	rows, err := pool.Query(ctx, `
		SELECT column_name
		  FROM information_schema.columns
		 WHERE table_schema = 'public'
		   AND table_name = 'offline_activation_requests'
		   AND column_name IN ('status', 'reject_reason', 'activation_code')`)
	if err != nil {
		t.Fatalf("读列失败: %v", err)
	}
	defer rows.Close()

	present := map[string]bool{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("扫列名失败: %v", err)
		}
		present[c] = true
	}
	for _, c := range []string{"status", "reject_reason", "activation_code"} {
		if !present[c] {
			t.Errorf("★ 真库里 public.offline_activation_requests 缺列 %s ⇒ 896 尚未应用（⇒ licensing 离线激活管理 SQL 会 42703）", c)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────
// 变异记录（2026-10-23 实测）
//
//	M1 把 896 里 status 的类型改掉（TEXT → INT）  ⇒ Test896DDLEquivalentTo375And376 红
//	M2 从 runner.go 的 StartupFiles 摘掉 896      ⇒ Test896RegisteredInEveryDeliverySurface 红
//	M3 给 embeddata 副本加一个空格                 ⇒ Test896EmbeddedCopiesAreByteIdentical 红
//	M4 让 down 去 DROP COLUMN                     ⇒ Test896DownOnlyDeletesLedgerRow 红
//	M5 删掉 896 的自证断言（RAISE EXCEPTION）     ⇒ Test896HasLockTimeoutAndSelfAssertion 红
//	M6 去掉 ADD COLUMN 的 IF NOT EXISTS            ⇒ Test896IsIdempotent 红
//
//	还原一律**逐字**（sha256 校验），顺序/位置变化也算改动。
// ─────────────────────────────────────────────────────────────────────────
