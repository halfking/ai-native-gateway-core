package db

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

// r1039_boot_chain_fresh_install_test.go —— **boot ensure 链的链级真库判据**
// （2026-10-21 r1039 续轮）。
//
// ## 为什么需要链级判据
//
// 前几轮修的都是**单点**：ensureRequestLogSchema 的 provider_id 缺列、
// 族表无条件 ALTER、recent_success_rate 的旧腿引用。每一处都单独立了判据。
//
// 但 2026-10-21 的实测说明：**逐点修不够**。修完 provider_id，链往下走撞上
// `_request_logs_hot` 不存在（42P01）；修完那个，又撞上 work_type 的索引探针
// 缺表守卫（还是 42P01）。两个缺陷在**同一个库形态**下先后触发 ⇒ 说明真正
// 的不变量不是「某个 ensure 写对了」，而是：
//
//	**整条 boot ensure 链必须能在「request_logs 族已退役」的库形态下跑完。**
//
// 只立单点判据 ⇒ 下一个新 ensure（或下一个漏网的老 ensure）会再炸一次，
// 而 CI 里没有任何东西会红。
//
// ## 库形态从哪来（不自造）
//
// 本判据**不自己建库**——它复用仓库既有的集成门机制：
// `scripts/audit/run-integration-gate.sh` 造一个一次性库，形态 = 安装器自己
// 的路径（embeddata 00-prereqs → 01-schema → 02-seed + runner.go StartupFiles
// 里的全部 startup 迁移），然后把该库的 DSN 注入 `TEST_DATABASE_URL`。
//
// ⚠ 为什么**不能**只用 `sql/schema/*.sql` 自造：schema 基线**不含** startup
// 迁移建的表（实测 `session_aggregate_outbox` 来自 startup 630）⇒ 链会在
// 那里报 42P01，而那是**夹具缺口的假阳性**，不是产品缺陷。
// 2026-10-21 实测正是这么误判了一次，先记在这里免得下一个人再踩。
//
// 跑法：
//
//	bash scripts/audit/run-integration-gate.sh ./db TestR1039BootEnsureChain
//
// ## 判据纪律
//
//   - 承重的是「链真的跑完了」：直接调 db.applyMigrationsOnce（boot 时执行的
//     那个函数本体），不是另抄一份 ensure 列表。
//   - **防空跑**：跑之前先断言族表**确实不在**、核心表**确实在**——否则这条
//     判据在「形态不对」的库上会恒绿或误红，变成一个什么都没测的假绿。
//   - 形态对不上 ⇒ **显式 skip 并说明本次没跑**（「检查没跑成」与「检查通过」
//     必须长得不一样），不是安静地跳过。
//   - **不许复活族表**：跑完之后族表必须仍然不在（ensure 链不许把退役的族
//     重新建出来）。
//   - **幂等**：跑第二遍不许报错（boot 每次都跑这条链）。
func TestR1039BootEnsureChainSurvivesRetiredRequestLogsFamily(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本次**没有**跑真库链级验证（只跑了单点判据）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)
	d := &DB{pool: pool}

	relationExists := func(name string) bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1`,
			name).Scan(&n); err != nil {
			t.Fatalf("probe %s: %v", name, err)
		}
		return n > 0
	}

	// ── 形态校验：族必须不在，核心表必须在 ────────────────────────────────
	// ⚠ 族在 ⇒ 这不是「新装形态」⇒ 本次**不跑**（跑它测的是 211/252 形态，
	//   由单点判据覆盖；在这里跑只会得到一个与本条无关的结论）。
	for _, name := range []string{"_request_logs", "_request_logs_hot"} {
		if relationExists(name) {
			t.Skipf("本次**没有**跑链级验证：%s 在这个库里存在 —— 这不是「request_logs 族已退役」的新装形态", name)
		}
	}
	// 核心表不在 ⇒ 库连新装形态都不是（多半是空的一次性库）⇒ 同样不跑。
	var missingCore []string
	for _, name := range []string{"providers", "credentials", "session_turns", "credential_model_bindings"} {
		if !relationExists(name) {
			missingCore = append(missingCore, name)
		}
	}
	if len(missingCore) > 0 {
		t.Skipf("本次**没有**跑链级验证：核心表缺失 %v —— 这个库不是新装形态（多半是空库）", missingCore)
	}

	// ── 跑整条 boot ensure 链（这就是 boot 时执行的那个函数本体） ─────────
	start := time.Now()
	if err := d.applyMigrationsOnce(ctx); err != nil {
		t.Fatalf("boot ensure 链在「request_logs 族已退役」的新装形态上失败：%v\n"+
			"  ⇒ 网关起不来（2026-10-21 本机 deploy-local 实测中止）。", err)
	}
	t.Logf("boot ensure 链跑通，耗时 %s", time.Since(start).Round(time.Millisecond))

	// ── 幂等：boot 每次都跑这条链，第二遍必须也不许报错 ─────────────────
	if err := d.applyMigrationsOnce(ctx); err != nil {
		t.Fatalf("boot ensure 链第二遍（幂等复跑）失败：%v", err)
	}

	// ── 不许把退役的族复活 ────────────────────────────────────────────────
	for _, name := range []string{"_request_logs", "_request_logs_hot"} {
		if relationExists(name) {
			t.Errorf("boot ensure 链把已退役的 %s 重新建了出来 —— 退役语义被破坏", name)
		}
	}
}

// TestR1039BootEnsureChainFixtureIsHonest 钉住上面那条链级判据的**前提**：
// 「安装器形态 = 族已退役形态」。
//
// 判据纪律第 3 条的镜像：一条「在新装形态上跑链」的判据，最危险的失效模式
// 是**前提漂移** —— 哪天 schema 基线或某个 startup 迁移又把族表建了出来，
// 链级判据就从「测退役形态」退化成「测族还在的形态」⇒ 恒绿，而且没人知道。
//
// ⇒ 本条**不需要数据库**（每次 `go test ./db/` 都跑 ⇒ 漂移当场可见）：
//   - schema 基线（sql/schema/01-schema.sql）**没有**族表的建表语句；
//   - 安装器注册的 startup 迁移（installer/internal/dbinit/runner.go 的
//     StartupFiles 清单）**也没有**族表的建表语句；
//   - 基线**有**核心表（providers / credentials / session_turns /
//     credential_model_bindings）—— 否则「新装形态」不成形。
//
// startup 迁移清单从 **Go 源码字面量**里解析（与
// scripts/audit/run-integration-gate.sh 的 sed 锚点同一个来源），
// 解析不出文件时**判红**（那意味着安装器清单变形了，门禁库会静默退回陈旧基线
// —— 集成门脚本自己也是这么处理的）。
func TestR1039BootEnsureChainFixtureIsHonest(t *testing.T) {
	baseline, err := os.ReadFile(filepath.Join("..", "sql", "schema", "01-schema.sql"))
	if err != nil {
		t.Fatalf("read baseline schema: %v", err)
	}
	baselineCreated := createdTables(string(baseline))
	if len(baselineCreated) == 0 {
		t.Fatal("扫描器没匹配到任何 CREATE TABLE —— 扫描器本身可能已失效")
	}
	for _, name := range []string{"_request_logs", "_request_logs_hot"} {
		if baselineCreated[name] {
			t.Errorf("schema 基线又建了 %s —— 「族退役形态」不再是新装形态，链级判据会退化成恒绿假绿", name)
		}
	}
	for _, name := range []string{"providers", "credentials", "session_turns", "credential_model_bindings"} {
		if !baselineCreated[name] {
			t.Errorf("schema 基线缺核心表 %s —— 新装形态不成形，链级判据测不到真实部署形态", name)
		}
	}

	// startup 迁移清单：从安装器的 Go 源码字面量解析。
	runnerPath := filepath.Join("..", "installer", "internal", "dbinit", "runner.go")
	runner, err := os.ReadFile(runnerPath)
	if err != nil {
		t.Fatalf("read %s: %v", runnerPath, err)
	}
	names := startupFileNames(string(runner))
	if len(names) < 100 {
		t.Fatalf("从 %s 的 StartupFiles 里只解析出 %d 个迁移（明显偏少）—— 解析多半失配；"+
			"继续跑等于拿半个起始库当链级判据的库形态。", runnerPath, len(names))
	}
	startupDir := filepath.Join("..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")
	familyHits := []string{}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(startupDir, name))
		if err != nil {
			t.Fatalf("read startup migration %s: %v", name, err)
		}
		created := createdTables(string(raw))
		for _, family := range []string{"_request_logs", "_request_logs_hot"} {
			if created[family] {
				familyHits = append(familyHits, name+" → "+family)
			}
		}
	}
	if len(familyHits) > 0 {
		t.Errorf("startup 迁移又建了退役族表 %v —— 「族退役形态」不再是安装器形态，链级判据会退化成恒绿假绿", familyHits)
	}
}

// createdTables 抽出 SQL 文本里 CREATE TABLE 建出来的表名（小写）。
//
// ⚠ 只认「建表」不认「改名/改列」：退役族的复活路径是 CREATE TABLE
// （860 改名是 ALTER TABLE RENAME，基线里不出现），所以只匹配 CREATE TABLE
// 足够覆盖「族表被重新建出来」这个失效模式。
func createdTables(sqlText string) map[string]bool {
	out := map[string]bool{}
	re := regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:public\.)?([a-z0-9_]+)`)
	for _, m := range re.FindAllStringSubmatch(sqlText, -1) {
		out[strings.ToLower(m[1])] = true
	}
	return out
}

// startupFileNames 从 installer/internal/dbinit/runner.go 的 StartupFiles
// 字面量里解析出 .sql 文件名。
func startupFileNames(runnerText string) []string {
	start := strings.Index(runnerText, "StartupFiles: []string{")
	if start < 0 {
		return nil
	}
	rest := runnerText[start:]
	end := strings.Index(rest, "\n\t}")
	if end < 0 {
		return nil
	}
	block := rest[:end]
	re := regexp.MustCompile(`"([0-9a-zA-Z_]+\.sql)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}
