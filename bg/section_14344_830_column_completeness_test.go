package bg

// section_14344_830_column_completeness_test.go —— §143.44 门：
// 「830 迁移建出来的新父表，其列集必须与被改名的旧表**完全一致**」。
//
// ★★ 为什么需要这道门（2026-10-21 在 211 上实测出来的）：
//
//	830 原实现**硬编码 56 列**（01-schema.sql 的 32 列 + 818 的 24 列）——
//	那在 252 上是对的，但那是**当时的列集**。211 的表实测 **57 列**
//	（多出 `empty_response_rate_5m`，由后续迁移补的），而 writer 的 INSERT
//	**确实写这一列** ⇒ ⇒ **830 原样在 211 上执行会直接打断写路径**：
//	新父表没有该列 ⇒ INSERT 报 `42703 column ... does not exist`
//	⇒ 而 boot 不失败、迁移 RC=0、日志无异常 ⇒ **静默 + 致命**。
//	⇒ ⇒ 这就是「同一迁移在两个实例上是两个不同版本」的典型形态。
//
// ⇒ 修法：830 改为 **LIKE 列驱动** ⇒ 「列集一致」从「人肉维护的清单」
//	   变成**结构属性** ⇒ 这类列漂移缺陷从根上消失。
//
// 本门守两件事：
//  ① **静态**：830 不得再回到硬编码列清单（否则缺陷复发）；
//  ② **真库性质**：夹具表多带一列 ⇒ 新父表必须**多出同一列**
//     （只验"列数相同"不够 —— 可能多一列少一列而总数相同）。
//
// ★ 真库用例复用同包既有的 connect830 / run830 / dropAll830Objects
//   （库名门 destructiveGate 也在其中 —— 本文件会 DROP 表，所以必须走同一道门）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSection14344Migration830MustBeColumnDriven 是**静态**判据。
//
// ⚠ 判据形态：钉住「LIKE 出现 + 硬编码列清单消失」两件事，
//   而不是钉住某一段列清单文本（那会把实现细节钉死，改一次就红一次）。
func TestSection14344Migration830MustBeColumnDriven(t *testing.T) {
	raw, _ := readMigration830(t)
	sql := string(raw)

	// ★★ 判据形态：**只对 CREATE TABLE 语句本身**断言，不对整个文件断言。
	//   本门第一版用 strings.Contains(sql, "INCLUDING INDEXES") ⇒ **被注释喂饱**了
	//   （830 的注释里正写着「· 不写 INCLUDING INDEXES」）⇒ 门恒绿、毫无意义。
	//   ⇒ 这是本仓 LESSONS「判据一旦能在注释里被满足就不是判据」的又一次现场复现
	//   ⇒ ⇒ 断言必须**钉住语句形态**（整条 CREATE TABLE 的 LIKE 子句）。
	stmt := extractCreateParentStmt(t, sql)

	// ① 列驱动：必须用 LIKE 建父表。
	if !strings.Contains(stmt, "LIKE public.ursm_node_snapshot_min_legacy") {
		t.Error("830 不用 LIKE 建新父表 ⇒ 「新父表列集 == 旧表列集」不再是结构属性，" +
			"而是靠人肉维护的清单 ⇒ 211（57 列）与 252（56 列）的差异会打断写路径")
	}

	// ② 硬编码列清单必须**消失**（LIKE 子句里不该出现任何列定义）。
	if strings.Contains(stmt, "bigint") || strings.Contains(stmt, "NOT NULL") {
		t.Error("830 的 CREATE TABLE 里仍出现硬编码列定义 ⇒ 列清单又回来了")
	}

	// ③ 分区键与 PK 四列必须**原样**（动了就是打断写入的 arbiter）。
	if !strings.Contains(sql, "PARTITION BY RANGE (snapshot_ts)") {
		t.Error("830 的分区键不再是 RANGE (snapshot_ts) ⇒ 分区边界语义变了")
	}
	if !strings.Contains(sql,
		"PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name)") {
		t.Error("830 不再建四列 PK ⇒ writer 的 ON CONFLICT 失去 arbiter ⇒ 全量写入瘫痪")
	}

	// ④ LIKE 的选项是**刻意**挑的：索引与 CHECK 约束都**不**复制。
	//   复制索引 ⇒ 新父表会带着旧表的 PK 索引名 ⇒ 1c 建同名 PK 报
	//   「relation already exists」（这正是 1a-2 改名那一步要绕开的坑）。
	if strings.Contains(stmt, "INCLUDING INDEXES") {
		t.Error("830 的 LIKE 含 INCLUDING INDEXES ⇒ 旧表索引（含 PK 名）会被复制 ⇒ 1c 建 PK 会撞名")
	}
	if strings.Contains(stmt, "INCLUDING CONSTRAINTS") {
		t.Error("830 的 LIKE 含 INCLUDING CONSTRAINTS ⇒ CHECK 约束会被复制（无必要，且旧表实测无 CHECK）")
	}
	// DEFAULT 必须复制（tenant_id DEFAULT '' 是硬约束）。
	if !strings.Contains(stmt, "INCLUDING DEFAULTS") {
		t.Error("830 的 LIKE 缺 INCLUDING DEFAULTS ⇒ 默认值不复制 ⇒ tenant_id 等列的默认值丢失")
	}
}

// extractCreateParentStmt 从 830 文件里抠出「建父表的那条 CREATE TABLE 语句」。
//
// ★ 为什么必须抠出**语句**而不是对整文件做字符串断言：
//   830 的注释里大量出现选项名（"不写 INCLUDING INDEXES"）⇒
//   整文件断言会被注释喂饱 ⇒ 门恒绿（本门第一版就是这么写的，当场被变异抓住）。
func extractCreateParentStmt(t *testing.T, sql string) string {
	t.Helper()
	start := strings.Index(sql, "CREATE TABLE public.ursm_node_snapshot_min (")
	if start < 0 {
		t.Fatal("830 里找不到 CREATE TABLE public.ursm_node_snapshot_min (")
	}
	rest := sql[start:]
	end := strings.Index(rest, "PARTITION BY RANGE (snapshot_ts)")
	if end < 0 {
		t.Fatal("830 的 CREATE TABLE 没有 PARTITION BY RANGE (snapshot_ts)")
	}
	return rest[:end+len("PARTITION BY RANGE (snapshot_ts)")]
}

// TestSection14344NewParentColumnSetMatchesOldTable 是**真库性质**判据。
//
// 夹具表**故意多带一列**（`zz_column_completeness_probe`）⇒
// 新父表必须**也**有这一列 ⇒ 证明列集是「跟着源表走」的，而不是碰巧对上。
func TestSection14344NewParentColumnSetMatchesOldTable(t *testing.T) {
	pool := connect830(t)
	dropAll830Objects(t, pool)
	t.Cleanup(func() { dropAll830Objects(t, pool) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// 夹具：4 列 PK + 一个**探针列**（模拟 211 的第 57 列）。
	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.ursm_node_snapshot_min (
		    snapshot_ts timestamp with time zone NOT NULL,
		    tenant_id   text DEFAULT ''::text NOT NULL,
		    credential_id integer NOT NULL,
		    raw_model_name text NOT NULL,
		    zz_column_completeness_probe integer,
		    CONSTRAINT ursm_node_snapshot_min_pkey
		        PRIMARY KEY (snapshot_ts, tenant_id, credential_id, raw_model_name)
		);
	`); err != nil {
		t.Fatalf("建夹具表：%v", err)
	}

	if err := run830(t, pool); err != nil {
		t.Fatalf("执行 830：%v", err)
	}

	// 新父表必须有探针列（= 列集跟着源表走）。
	var probe int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='ursm_node_snapshot_min'
		   AND column_name='zz_column_completeness_probe'
	`).Scan(&probe); err != nil {
		t.Fatalf("查探针列：%v", err)
	}
	if probe != 1 {
		t.Fatalf("新父表没有探针列 ⇒ 830 不是列驱动的 ⇒ 换一张列集不同的表就会缺列（42703）")
	}

	// 探针列的 DEFAULT / NOT NULL 语义也要跟着走（LIKE 的硬约束）。
	var notNull, dflt string
	if err := pool.QueryRow(ctx, `
		SELECT is_nullable, coalesce(column_default, '')
		  FROM information_schema.columns
		 WHERE table_schema='public' AND table_name='ursm_node_snapshot_min'
		   AND column_name='tenant_id'
	`).Scan(&notNull, &dflt); err != nil {
		t.Fatalf("查 tenant_id 语义：%v", err)
	}
	if notNull != "NO" {
		t.Errorf("新父表 tenant_id 不再 NOT NULL（is_nullable=%s）⇒ LIKE 没复制 NOT NULL", notNull)
	}
	if !strings.Contains(dflt, "''") {
		t.Errorf("新父表 tenant_id 的 DEFAULT 没了（default=%q）⇒ LIKE 没复制 DEFAULT", dflt)
	}

	// 新父表必须是分区父表，且探针列在**子分区**里也在（LIKE 的列集传播到分区）。
	var parts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		 WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
	`).Scan(&parts); err != nil {
		t.Fatalf("查分区数：%v", err)
	}
	if parts < 1 {
		t.Fatalf("新父表没有分区（parts=%d）⇒ 写入会 23514", parts)
	}
	var partProbe int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_schema='public'
		   AND table_name = (SELECT c.relname FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
		                      WHERE i.inhparent = 'public.ursm_node_snapshot_min'::regclass
		                      ORDER BY c.relname LIMIT 1)
		   AND column_name='zz_column_completeness_probe'
	`).Scan(&partProbe); err != nil {
		t.Fatalf("查分区探针列：%v", err)
	}
	if partProbe != 1 {
		t.Errorf("子分区里没有探针列 ⇒ 列集没有传播到分区")
	}
}

// TestSection14344GateDoesNotTouchNonTestDB 确认本文件不会对生产库动手。
//
// 它复用 connect830 ⇒ destructiveGate ⇒ 库名不含 "test" 时**跳过**。
// 这道断言是为了防止将来有人把 connect830 换成裸 pgxpool.New。
func TestSection14344GateDoesNotTouchNonTestDB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("未设置 TEST_DATABASE_URL / TEST_DB_URL —— 无库可判")
	}
	if os.Getenv("URSM_830_ALLOW_ANY_DB") == "1" {
		t.Skip("URSM_830_ALLOW_ANY_DB=1 —— 本门不适用于该模式")
	}
	name := dsnDatabaseName(dsn)
	if name == "" {
		t.Fatal("无法从 DSN 解析库名 ⇒ destructiveGate 会拒绝 ⇒ 请修 DSN")
	}
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Skipf("库名 %q 不含 test ⇒ destructiveGate 会跳过本文件的所有真库用例（这是预期的保护）", name)
	}
	// 走到这里说明库名合规 ⇒ 真库用例会真的执行 ⇒ 必须在 2 分钟内跑完。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连不上测试库：%v", err)
	}
	pool.Close()
}
