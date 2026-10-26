package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// r1039_boot_chain_fresh_install_test.go —— **boot ensure 链的链级真库判据**
// （2026-10-21 r1039 续轮）。
//
// ## 为什么需要链级判据
//
// 2026-10-21 本机 deploy-local 连撞两次 42P01（SQLSTATE），两次都在
// 「request_logs 族已退役」的库形态上：
//
//	第一次：relation "_request_logs" does not exist
//	第二次：relation "_request_logs_hot" does not exist
//
// 两次都发生在**同一条 boot 迁移链**上，而且是**不同的 ensure 步骤**
// ⇒ 说明真正的不变量不是「某个 ensure 写对了」，而是：
//
//	**整条 boot ensure 链必须能在「request_logs 族已退役」的库形态下跑完。**
//
// 只立单点判据 ⇒ 下一个新 ensure（或下一个漏网的老 ensure）会再炸一次，
// 而 CI 里没有任何东西会红。
//
// ## 库形态从哪来（**不自造**）
//
// 本判据自己建一个一次性库，形态 = **安装器自己的路径**：
//
//	① sql/schema/00-prereqs.sql → 01-schema.sql → 02-seed.sql
//	② installer/internal/dbinit/runner.go 的 StartupFiles 清单里的全部
//	   startup 迁移（installer/cmd/llm-gw-installer/embeddata/startup/）
//
// ①②合起来就是 installer/internal/dbinit/runner.go 的 InitSchema 顺序。
// startup 清单从**安装器的 Go 源码字面量**里解析（与
// scripts/audit/run-integration-gate.sh 的 sed 锚点同一个来源）——解析不出
// 迁移时**判红**（那意味着安装器清单变形了，判据会静默退回陈旧基线，
// 集成门脚本自己也是这么处理的）。
//
// ⚠ 为什么**不能**只用 ①：schema 基线**不含** startup 迁移建的表
// （实测 `session_aggregate_outbox` 来自 startup 630）⇒ 链会在那里报
// 42P01，而那是**夹具缺口的假阳性**，不是产品缺陷。2026-10-21 实测正是
// 这么误判了一次，先记在这里免得下一个人再踩。
//
// ## 判据纪律
//
//   - 承重的是「链真的跑完了」：直接调 db.applyMigrationsOnce（boot 时执行的
//     那个函数本体），不是另抄一份 ensure 列表。
//   - **防空跑**：跑之前先断言族表**确实不在**——否则这条判据在「族还在」的
//     库上会恒绿，变成一个什么都没测的假绿。
//   - **不许复活族表**：跑完之后族表必须仍然不在（ensure 链不许把退役的族
//     重新建出来）。
//   - **幂等**：跑第二遍不许报错（boot 每次都跑这条链）。
//   - 「检查没跑成」与「检查通过」必须长得不一样 ⇒ 缺环境变量 / 起步失败时
//     显式说明本次没跑（不是安静地跳过）。
func TestR1039BootEnsureChainSurvivesRetiredRequestLogsFamily(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 本次**没有**跑真库链级验证（只跑了单点判据）")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	t.Cleanup(cancel)

	// ── 1. 建一次性库 ───────────────────────────────────────────────────────
	adminCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	adminCfg.ConnConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg.ConnConfig)
	if err != nil {
		t.Skipf("connect admin db: %v", err)
	}
	// ⚠ 必须用 t.Cleanup 而不是 defer：t.Cleanup 是 **LIFO** ⇒ 先注册
	// 「关连接」后注册「drop 库」⇒ drop 先跑、连接后关。
	// 用 defer 的话 defer 在函数返回时先跑 ⇒ drop 时连接已关 ⇒ 库泄漏。
	t.Cleanup(func() { _ = admin.Close(context.Background()) })

	dbName := fmt.Sprintf("itgate_bootchain_%d", time.Now().UnixNano()%1_000_000)
	if _, err := admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{dbName}.Sanitize()); err != nil {
		t.Fatalf("create disposable db %s: %v", dbName, err)
	}
	t.Cleanup(func() {
		bg, bgCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer bgCancel()
		// DROP DATABASE 不能在事务里，也不能有活动连接 ⇒ 先踢掉残留连接。
		_, _ = admin.Exec(bg, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()`, dbName)
		if _, err := admin.Exec(bg, `DROP DATABASE IF EXISTS `+pgx.Identifier{dbName}.Sanitize()); err != nil {
			t.Logf("cleanup: drop %s: %v", dbName, err)
		}
	})

	targetCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	targetCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, targetCfg)
	if err != nil {
		t.Fatalf("connect %s: %v", dbName, err)
	}
	t.Cleanup(pool.Close)
	d := &DB{pool: pool}

	// ── 2. 基线（安装器顺序：prereqs → baseline → seed） ───────────────────
	for _, name := range []string{"00-prereqs.sql", "01-schema.sql", "02-seed.sql"} {
		path := filepath.Join("..", "sql", "schema", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		start := time.Now()
		// 无参数 ⇒ pgx 走 simple protocol ⇒ 多语句可一次执行（隐式事务，
		// 与 psql --single-transaction 同语义）。
		if _, err := pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
		t.Logf("applied %s (%d bytes, %s)", name, len(raw), time.Since(start).Round(time.Millisecond))
	}

	// ── 3. startup 迁移链（安装器注册的顺序） ─────────────────────────────
	files := startupFileNames(runnerSource(t))
	if len(files) < 100 {
		t.Fatalf("从安装器 runner.go 的 StartupFiles 里只解析出 %d 个迁移（明显偏少）—— "+
			"解析多半失配；继续跑等于拿半个起始库当链级判据的库形态。", len(files))
	}
	startupDir := filepath.Join("..", "installer", "cmd", "llm-gw-installer", "embeddata", "startup")
	applied, failed, skipped := 0, 0, 0
	var failures []string
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(startupDir, name))
		if err != nil {
			failed++
			failures = append(failures, name+" (missing)")
			continue
		}
		text := string(raw)
		// ⚠ psql 元命令：\gexec（把查询结果当 SQL 执行）**不是 SQL**，
		//   pgx 的 simple protocol 会报 syntax error。仓库对这批文件的
		//   处置写在文件头：「本文件含 \gexec（psql 元命令），**不能**用
		//   pgx/lib/pq 直接 Exec —— 必须走 psql，与 installer 的 applySQL
		//   同通道。」
		//   ⇒ 本判据**跳过**它们并留痕（它们只建 FTS/trigram 索引，
		//   不影响「族退役形态」这个前提）。
		//   \set ON_ERROR_STOP on 则是无害的会话设置 ⇒ 剥掉即可。
		if strings.Contains(text, `\gexec`) {
			skipped++
			failures = append(failures, name+" (skipped: psql \\gexec channel)")
			continue
		}
		text = psqlSetLine.ReplaceAllString(text, "")
		if _, err := pool.Exec(ctx, text); err != nil {
			// startup 迁移里有若干「修历史产物」的 repair_* 文件在**空库**上
			// 本来就跑不通（仓库自己在 sql/schema/startup_known_gaps.tsv 里
			// 登记了这份名单）⇒ 单个文件失败不判红，但**必须留痕**。
			failed++
			msg := err.Error()
			if len(msg) > 140 {
				msg = msg[:140]
			}
			failures = append(failures, name+" :: "+msg)
			continue
		}
		applied++
	}
	t.Logf("startup 迁移：applied=%d failed=%d skipped(psql-only)=%d", applied, failed, skipped)
	if len(failures) > 0 {
		shown := failures
		if len(shown) > 12 {
			shown = shown[:12]
		}
		t.Logf("未通过的 startup 迁移（前 %d 条，共 %d 条）：\n  %s", len(shown), len(failures), strings.Join(shown, "\n  "))
	}
	if applied < 100 {
		t.Fatalf("只应用了 %d 个 startup 迁移 —— 起始库不成形，链级判据测不到真实部署形态", applied)
	}

	// ── 4. 防空跑：族表必须**确实不在**，否则这条判据是假绿 ───────────────
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
	for _, name := range []string{"_request_logs", "_request_logs_hot", "request_logs", "request_logs_hot"} {
		if relationExists(name) {
			t.Fatalf("夹具失效：%s 在新装形态里仍然存在 —— 本条判据会变成恒绿的假绿"+
				"（退役形态没测到）", name)
		}
	}
	for _, name := range []string{"providers", "credentials", "session_turns", "credential_model_bindings"} {
		if !relationExists(name) {
			t.Fatalf("夹具失效：核心表 %s 缺失 —— 起始库不成形", name)
		}
	}

	// ── 5. 跑整条 boot ensure 链（这就是 boot 时执行的那个函数本体） ─────
	start := time.Now()
	if err := d.applyMigrationsOnce(ctx); err != nil {
		t.Fatalf("boot ensure 链在「request_logs 族已退役」的新装形态上失败：%v\n"+
			"  ⇒ 网关起不来（2026-10-21 本机 deploy-local 实测中止）。", err)
	}
	t.Logf("boot ensure 链跑通，耗时 %s", time.Since(start).Round(time.Millisecond))

	// ── 6. 幂等：boot 每次都跑这条链，第二遍必须也不许报错 ───────────────
	if err := d.applyMigrationsOnce(ctx); err != nil {
		t.Fatalf("boot ensure 链第二遍（幂等复跑）失败：%v", err)
	}

	// ── 7. 不许把退役的族复活 ────────────────────────────────────────────
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
// 是**前提漂移** —— 哪天基线或某个 startup 迁移又把族表建了出来（或者 890
// 的删除闸被改掉），链级判据就从「测退役形态」退化成「测族还在的形态」⇒
// 恒绿，而且没人知道。
//
// ⇒ 本条**不需要数据库**（每次 `go test ./db/` 都跑 ⇒ 漂移当场可见）：
//   - 基线（sql/schema/01-schema.sql）**没有**族表的建表语句；
//   - 安装器注册的 startup 迁移**也没有**族表的建表语句；
//   - 基线**有**核心表（providers / credentials / session_turns /
//     credential_model_bindings）—— 否则「新装形态」不成形；
//   - 安装器**注册了** 890（删族迁移）—— 否则「新装形态」不是退役形态。
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
	names := startupFileNames(runnerSource(t))
	if len(names) < 100 {
		t.Fatalf("从安装器 runner.go 的 StartupFiles 里只解析出 %d 个迁移（明显偏少）—— "+
			"解析多半失配；继续跑等于拿半个起始库当链级判据的库形态。", len(names))
	}
	// 890（删族迁移）必须在册 —— 没有它，新装形态就不是「族退役形态」。
	has890 := false
	for _, n := range names {
		if strings.HasPrefix(n, "890_") {
			has890 = true
			break
		}
	}
	if !has890 {
		t.Error("安装器没注册 890（删 request_logs 族迁移）—— 「族退役形态」不再是新装形态，链级判据会退化成恒绿假绿")
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

// psqlSetLine 匹配 psql 的 `\set` 会话设置行（startup 迁移里只有
// `\set ON_ERROR_STOP on` 一种用法）——它是 psql 元命令，不是 SQL，
// pgx 的 simple protocol 会把它当 SQL 解析 ⇒ syntax error。
var psqlSetLine = regexp.MustCompile(`(?m)^[ \t]*\\set\b[^\n]*\n`)

// runnerSource 读安装器的 dbinit runner 源码（StartupFiles 清单的来源）。
func runnerSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "installer", "internal", "dbinit", "runner.go"))
	if err != nil {
		t.Fatalf("read installer runner.go: %v", err)
	}
	return string(raw)
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
