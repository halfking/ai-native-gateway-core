package bg

// 运行中 catalog 的两条门（2026-10-14 加）。
//
// 为什么单独一个文件、且**故意不复用** destructiveGate：
// 同目录 bg/ursm_830_realdb_test.go 里的真库门会 DROP 表，历史上有人把
// TEST_DATABASE_URL 指向 245 就把生产表删了，所以那份文件加了 destructiveGate
// 做准入拦截。**本文件的三条门全是 SELECT，一条写语句都没有**，
// 而它们恰恰**必须在生产上能跑**才有意义（缺陷只存在于生产）。
// ⇒ 这里不复用 destructiveGate；理由写在这里，免得下一个会话以为是漏了。
//
// 门控沿用同目录既有真库门：TEST_DATABASE_URL 优先，其次 TEST_DB_URL，
// 都没有则干净 skip（skip 不构成「检查通过」的证据）。
//
// ★ 本文件补的是 bg/partition_830_contract_test.go 覆盖不到的那一层：
// 那条门对**迁移文件的 DDL 文本**做正则断言（它守的「刻意不建 DEFAULT 分区」
// 是对的），但**从不查运行中的 catalog**。文本与实况是两个对象，
// 这里把同一不变量落到实况上。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func ursmSnapshotDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 URSM 快照 catalog 门；" +
			"★ skip 不构成「检查通过」的证据")
	}
	return dsn
}

func openURSMSnapshotPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, ursmSnapshotDSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// knownURSMSnapshotOrphans 是**封闭登记表**。
//
// ★ 用登记表而不是「必须零个」：条目**减少**永远允许（登记表里多出来的可以删），
// **增加**必须显式改这张表并写清理由 —— 门守的是「不许悄悄多一个」，
// 不是「不许推进」。
//
// _legacy：迁移 830 把旧表 RENAME 成它（830…sql.skip:91），它承载**分区化之前的
// 全部历史**，并且是 830 的**唯一回滚路径**（830…down.sql:84 把它 RENAME 回原名）。
// 它不是父表的分区 ⇒ retention 的 SQL 只作用于父表，**永远够不到它**，
// 所以它在登记表里是「已知且有意保留」，不是「忘了删」。
//
// ⚠ 用 var 不是 const：Go 的映射类型不可常量，这是编译器的硬限制。
//
//	改成 var 后它可被同包测试改写 —— 那正是「显式改门并写理由」的字面形态，
//	所以不要试图把它做成不可变。
var knownURSMSnapshotOrphans = map[string]string{
	"ursm_node_snapshot_min_legacy": "迁移 830 的回滚副本（pre-partition 历史 + down 的唯一退路），有意保留",
}

// TestURSMSnapshotCatalogHasNoDefaultPartition 把
// partition_830_contract_test.go:Test830StillDeclaresNoDefaultPartition 的不变量
// 落到运行中的 catalog 上：分区父表**不得有 DEFAULT 分区**。
//
// 理由（830 契约门原文）：本表无 DEFAULT 分区，所以「今天的分区不存在」
// = **全量写入失败** —— 宁可失败，也不要静默落进兜底桶。
// 一旦出现 DEFAULT 分区，写入路径就不再 fail-loud，这条设计意图就失效了。
//
// ★ 2026-10-14 生产实测：9 个分区、全部 RANGE 边界、DEFAULT 分区 **0 个** ⇒ 通过。
func TestURSMSnapshotCatalogHasNoDefaultPartition(t *testing.T) {
	pool := openURSMSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var parentExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE n.nspname = 'public' AND c.relname = 'ursm_node_snapshot_min'
		                  AND c.relkind = 'p')`).Scan(&parentExists); err != nil {
		t.Fatalf("probe parent: %v", err)
	}
	if !parentExists {
		t.Skip("ursm_node_snapshot_min 不存在或尚未分区化（830 未应用）—— 本条不适用")
	}

	rows, err := pool.Query(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class c ON c.oid = i.inhrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND (SELECT p.relname FROM pg_class p WHERE p.oid = i.inhparent) = 'ursm_node_snapshot_min'
		  AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query default partitions: %v", err)
	}
	defer rows.Close()

	var found []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		found = append(found, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(found) > 0 {
		t.Fatalf("ursm_node_snapshot_min 出现了 DEFAULT 分区 %v —— "+
			"这会让「今天的分区不存在 ⇒ 全量写入失败」这条刻意设计的不变量失效，"+
			"写入会静默落进兜底桶。若确需保留，请评估后果后显式豁免本门。", found)
	}
}

// TestURSMSnapshotCatalogHasNoUnregisteredOrphan 抓**不在父表分区集合里、
// 但名字还挂着 ursm_node_snapshot_min 前缀**的关系。
//
// ★ 这条门存在的理由是一个被真实犯过的错（runbook §10.110.31/§10.110.32）：
// 有人（或是我）只查 pg_class.relpartbound，看到空值就断言「它是 DEFAULT 分区」。
// 而**非分区关系的 relpartbound 本来就是 NULL**（全库 407 张普通表都是），
// 空值只说明「不是分区」。判「是不是子分区」必须查 **pg_inherits**，
// 不是查 relpartbound。
// ⇒ 实测 `ursm_node_snapshot_min_legacy` 的 parent_count = 0：它是**独立堆表**，
// 不是任何表的分区。
//
// 后果不是学术问题：retention 的 SQL 只作用于父表，够不到孤儿；
// 于是 _legacy 的 852 MB 与 retention 天数**完全无关**，永不回收。
// 而「无人读 + 永不回收」正是这种孤儿最危险的组合 —— 它安静地一直占着。
func TestURSMSnapshotCatalogHasNoUnregisteredOrphan(t *testing.T) {
	pool := openURSMSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rows, err := pool.Query(ctx, `
		SELECT c.relname,
		       pg_size_pretty(pg_total_relation_size(c.oid)),
		       c.reltuples::bigint
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relname LIKE 'ursm\_node\_snapshot\_min%'
		  AND c.relkind IN ('r', 'p', 'm')
		  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
		  AND c.relname <> 'ursm_node_snapshot_min'
		ORDER BY c.relname`)
	if err != nil {
		t.Fatalf("query orphans: %v", err)
	}
	defer rows.Close()

	type orphan struct {
		name  string
		size  string
		rows  int64
		known bool
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.name, &o.size, &o.rows); err != nil {
			t.Fatalf("scan: %v", err)
		}
		_, o.known = knownURSMSnapshotOrphans[o.name]
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	var unregistered []string
	for _, o := range orphans {
		if o.known {
			// 已登记：打印出来，让人看见它还占着多少，而不是让门假装没这回事。
			t.Logf("已登记孤儿 %s（%s，%d 行）：%s", o.name, o.size, o.rows,
				knownURSMSnapshotOrphans[o.name])
			continue
		}
		unregistered = append(unregistered, o.name)
	}
	if len(unregistered) > 0 {
		t.Fatalf("发现未登记的孤儿关系 %v —— 它们不是父表的分区，"+
			"retention 的 SQL 作用不到，等于体积无限期占用。"+
			"确认它该被 DROP，或确认它有保留理由后加进 knownURSMSnapshotOrphans。",
			unregistered)
	}
}

// TestURSMSnapshotParentChildSetMatchesCatalog 钉住一件容易被忽略的事：
// 「父表存在」与「父表挂了正确的分区」是两件事。
//
// 830 真库门抓到过一个**静默且致命**的缺陷（见 ursm_830_realdb_test.go 头注）：
// ensure 二次短路按**分区名**判断，up→down→up 往返后同名分区仍挂在
// _post825 下 ⇒ 新父表**零分区** ⇒ 此后每次写入都报
// `no partition of relation ... found for row`，而迁移 RC=0、日志无异常。
// 那条门跑的是迁移往返；本条查的是**当前实况**是否仍挂着分区。
func TestURSMSnapshotParentChildSetMatchesCatalog(t *testing.T) {
	pool := openURSMSnapshotPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var parentExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE n.nspname = 'public' AND c.relname = 'ursm_node_snapshot_min'
		                  AND c.relkind = 'p')`).Scan(&parentExists); err != nil {
		t.Fatalf("probe parent: %v", err)
	}
	if !parentExists {
		t.Skip("ursm_node_snapshot_min 尚未分区化 —— 本条不适用")
	}

	var childCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_inherits i
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_namespace n ON n.oid = p.relnamespace
		WHERE n.nspname = 'public' AND p.relname = 'ursm_node_snapshot_min'`).Scan(&childCount); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if childCount == 0 {
		t.Fatal("ursm_node_snapshot_min 是分区父表但一个分区都没有 —— " +
			"此后每次写入都会报 no partition of relation ... found for row，" +
			"而这不会产生任何迁移失败或日志异常")
	}
	t.Logf("父表挂着 %d 个分区", childCount)
}
