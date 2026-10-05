package bg

// 迁移 825 / 826 / 827 / 832 / 833 在**真生产 schema** 上的应用判据，以及一条
// 把「健康面依赖哪些迁移」钉成不变量的判据。
//
// # 一、真 schema 上应用（此前从未被回答过的问题）
//
// 本工作流此前所有真库判据都跑在**手工搭的最小夹具**上（`supplierViewFixture`、
// `supplierPriceFixture` 等）。那类夹具验的是「迁移的字节有没有产生它声称的效果」，
// 但它**验不到迁移落到真表上会发生什么** —— 真表与夹具表的差别正是容易出事的地方：
//
//   · 真 `models_canonical` 有 **25 列、4 个 CHECK、4 个索引**（含一个既有的
//     `models_canonical_modality_check`），夹具表是空的；
//   · 真 `models_canonical` **没有主键**（只有 canonical_name 的 UNIQUE）—— 825
//     补主键这件事在夹具上永远是「本来就有」；
//   · 真 `credential_model_bindings` 的列与默认值和夹具不同，833 的四个
//     `ADD CONSTRAINT` 只有落在真表上才算验过。
//
// 2026-10-05 实测结论：五个迁移在真 schema 上**全部干净应用**，种子数据完好，
// 逆序 down 干净回退。
//
// # 二、为什么不能在这条判据里跑整轮 RunChecks（写下这条是为了不让下一个人重踩）
//
// 第一版把「10 条检查一次跑完」也放进来了，在本文件搭的这个库上直接失败：
//
//	RunChecks failed: query canonical_id_null: ERROR: column
//	pm.canonical_cleared_at does not exist (SQLSTATE 42703)
//
// 那列来自**迁移 693**，而 `sql/schema/01-schema.sql` 里**没有**它（实测 grep = 0）。
//
// ⇒ **`01-schema.sql` 单独不是任何已部署环境会有的形态**，它只是基线。真实环境
// 一定是「基线 + 全部已登记的 startup 迁移」。仓里已经知道这件事：
// `scripts/audit/run-integration-gate.sh` 明确区分 `installer`（baseline + 全链）
// 与 `baseline`（只有基线）两种夹具形态，并注明后者是中间态。
//
// 而「整轮 10 条在生产形态库上跑一遍」这件事**不该**加进集成门：
// `sql/schema/integration_fixture_shapes.tsv` 自己写着 **「a shape label is NOT a
// green light」**，且实测 `bg` 在 installer 形态上**已 14 FAIL**。往一个已红的包里
// 加判据只会埋掉信号。所以它是**一次性实测**（2026-10-05 结论记在 changelog 里），
// 不是本文件的承重项。
//
// 依赖真库：需要能 CREATE DATABASE 的连接（没有 TEST_DATABASE_URL 时跳过）。
// 整条约 4 秒，所以判据自带建库与灌 schema，而不是要求外部预置一个库。

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// realschemaMigrations 是本工作流拥有的五个迁移，按 up 顺序。
//
// ⚠ 只含**本工作流自己的**：828 / 829 是别的会话的，混进来会让这条判据因它们的
// 问题而红，而那不是这条判据要回答的问题。
var realschemaMigrations = []string{
	"825_modality_graded_verification",
	"826_model_baseline_price",
	"827_modality_verification_progress_view",
	"832_model_baseline_observation_health",
	"833_supplier_price_nonneg_check",
}

// withDatabase 返回把 dsn 的库名换成 dbName 的副本。
func withDatabase(t *testing.T, dsn, dbName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + dbName
	return u.String()
}

func TestRealSchemaAppliesNewMigrationsAndRevertsCleanly(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 280*time.Second)
	defer cancel()

	// 一次性 scratch 库。名字带纳秒后缀，避免与并发跑同一判据的会话撞名。
	scratch := fmt.Sprintf("mavis_mig_e2e_%d", time.Now().UnixNano()%1_000_000)

	// 维护连接连到 postgres —— 对还不存在的库建连接会失败。
	admin, err := pgxpool.New(ctx, withDatabase(t, dsn, "postgres"))
	if err != nil {
		t.Fatalf("connect to maintenance db: %v", err)
	}
	defer admin.Close()

	// ★ 清理注册在**建库之前**。DROP DATABASE 在还有连接时会失败，所以这个 defer
	// 必须排在 pool.Close 的 defer **之后**执行 —— defers 是 LIFO，先注册 drop、
	// 后注册 close，才能拿到「先关连接再删库」的顺序。
	defer func() {
		// 兜底：即使建库后某步失败、连接没关干净，也不会有会话挂着。
		_, _ = admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = $1 AND pid <> pg_backend_pid()`, scratch)
		if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+scratch); err != nil {
			t.Errorf("drop the scratch database %s: %v", scratch, err)
		}
	}()

	if _, err := admin.Exec(ctx, `CREATE DATABASE `+scratch); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	pool, err := pgxpool.New(ctx, withDatabase(t, dsn, scratch))
	if err != nil {
		t.Fatalf("connect to the scratch database: %v", err)
	}
	defer pool.Close()

	// ---- 灌真 schema ----
	//
	// 顺序不能换：01-schema 用到 columnar 访问方法，那个 AM 由 00-prereqs 注册。
	for _, f := range []string{"00-prereqs.sql", "01-schema.sql", "02-seed.sql"} {
		b, err := os.ReadFile(filepath.Join("../sql/schema", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("load %s: %v", f, err)
		}
	}

	// **量具自证**：schema 真的灌进去了。空库也能让后面所有 CREATE 成功，
	// 那会让「迁移应用成功」变成一句毫无信息量的话。
	var tbl, seedModels, mcColsBefore int
	if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM information_schema.tables WHERE table_schema='public'),
			(SELECT count(*) FROM models_canonical),
			(SELECT count(*) FROM information_schema.columns
			 WHERE table_schema='public' AND table_name='models_canonical')`).
		Scan(&tbl, &seedModels, &mcColsBefore); err != nil {
		t.Fatalf("probe the loaded schema: %v", err)
	}
	if tbl < 100 {
		t.Fatalf("the loaded schema has only %d tables — 01-schema.sql did not really load, and "+
			"every assertion below would be measuring an empty database", tbl)
	}
	if seedModels == 0 {
		t.Fatal("models_canonical is empty after loading 02-seed.sql — the seed data every " +
			"assertion below depends on is missing")
	}

	// ---- 应用五个迁移 ----
	for _, m := range realschemaMigrations {
		b, err := os.ReadFile("../sql/migrations/startup/" + m + ".sql")
		if err != nil {
			t.Fatalf("read %s: %v", m, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s on the real schema: %v", m, err)
		}
	}

	// 种子数据必须完好：迁移是加列/加视图/加约束，不是重建表。
	var seedAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM models_canonical`).Scan(&seedAfter); err != nil {
		t.Fatalf("count models_canonical after: %v", err)
	}
	if seedAfter != seedModels {
		t.Errorf("models_canonical had %d rows before the migrations and %d after — a migration "+
			"rebuilt or truncated the table. Prices and verification state are business data, not "+
			"something a migration is allowed to drop", seedModels, seedAfter)
	}

	// 825 加的三列。
	for _, col := range []string{"modality_source", "modality_verified_at", "modality_evidence"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns
			WHERE table_schema='public' AND table_name='models_canonical' AND column_name=$1)`, col).
			Scan(&exists); err != nil {
			t.Fatalf("probe column %s: %v", col, err)
		}
		if !exists {
			t.Errorf("825 did not add models_canonical.%s on the real schema", col)
		}
	}

	// 825 补的主键：真表原本**没有**，所以这条在夹具上永远是「本来就有」。
	var hasPK bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_constraint
		WHERE conrelid='public.models_canonical'::regclass AND contype='p')`).Scan(&hasPK); err != nil {
		t.Fatalf("probe the primary key: %v", err)
	}
	if !hasPK {
		t.Error("after 825, public.models_canonical still has no primary key. On the real schema " +
			"this is the one thing 825 had to self-heal (the table shipped with none) — if it did " +
			"not take effect here, the LATERAL joins and UPSERTs that assume a unique id are unsafe")
	}

	// 826 的九个基准价列。
	var baselineCols int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='models_canonical'
		  AND column_name LIKE 'baseline_%'`).Scan(&baselineCols); err != nil {
		t.Fatalf("count baseline columns: %v", err)
	}
	if baselineCols != 9 {
		t.Errorf("models_canonical has %d baseline_* columns after 826, want 9", baselineCols)
	}

	// 833 的四个非负约束，落在**真**表上。
	var nonneg int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='public.credential_model_bindings'::regclass
		  AND conname LIKE 'cmb_price_nonneg%'`).Scan(&nonneg); err != nil {
		t.Fatalf("count 833 constraints: %v", err)
	}
	if nonneg != 4 {
		t.Errorf("the real credential_model_bindings carries %d cmb_price_nonneg* constraints after "+
			"833, want 4", nonneg)
	}

	// 827 的两个视图 + 832 的表。
	for _, rel := range []string{
		"v_model_modality_verification_progress", "v_model_modality_verification_rollup",
		"model_baseline_price_observation_health",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+rel).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", rel, err)
		}
		if !exists {
			t.Errorf("public.%s does not exist after applying the migrations", rel)
		}
	}

	// ---- 逆序 down：结构干净回退、数据仍在 ----
	for i := len(realschemaMigrations) - 1; i >= 0; i-- {
		b, err := os.ReadFile("../sql/migrations/startup/" + realschemaMigrations[i] + ".down.sql")
		if err != nil {
			t.Fatalf("read %s down: %v", realschemaMigrations[i], err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Errorf("apply %s down: %v", realschemaMigrations[i], err)
		}
	}
	for _, rel := range []string{
		"v_model_modality_verification_progress", "v_model_modality_verification_rollup",
		"model_baseline_price_observation_health",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+rel).Scan(&exists); err != nil {
			t.Fatalf("probe %s after down: %v", rel, err)
		}
		if exists {
			t.Errorf("public.%s survived the down migrations", rel)
		}
	}
	var mcColsAfter int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='models_canonical'`).Scan(&mcColsAfter); err != nil {
		t.Fatalf("count columns after down: %v", err)
	}
	if mcColsAfter != mcColsBefore {
		t.Errorf("models_canonical has %d columns after the down migrations, want the %d it had "+
			"before — the rollback did not restore the structure", mcColsAfter, mcColsBefore)
	}
}

// TestHealthSurfaceOnlyDependsOnShippedMigrations 钉一条不变量：
// **健康面读到的、基线里没有的对象，必须由一条已登记的 startup 迁移提供。**
//
// # 为什么需要它
//
// 2026-10-05 的实测：在一个只灌了 `01-schema.sql` 的库上跑生产入口 `RunChecks`，
// 第一条检查就炸：
//
//	column pm.canonical_cleared_at does not exist (SQLSTATE 42703)
//
// 那列由迁移 693 提供，而 `01-schema.sql` 里没有它。**这不是缺陷** —— 基线本来
// 就不是可部署形态（`run-integration-gate.sh` 明写 baseline 与 installer 两种形态，
// 后者才是真实安装器产物）。但它是一类**很容易再犯**的错：任何新写的检查若读了
// 一个「谁都没登记、谁都没发布」的东西，症状是运行时报 42703/42P01，而报错信息
// 指向的是列名，不会告诉你是「这个对象根本没被交付」。
//
// 这条判据把那个信息提前到**文本层**：被列出来的对象，要么在基线里，要么在一条
// 已登记且磁盘上存在的迁移文件里。两者都不是 ⇒ 有人依赖了一个不存在的交付物。
//
// # 这不是「检查全部 10 条查询的每个列」
//
// 刻意只列**已知的跨基线依赖**（下面那张表）。解析 SQL 抽列在视图/CTE/别名上极脆，
// 而脆的判据会训练人忽略它。这张表是人工维护的：新增一条跨基线依赖就加一行；
//
//	若哪天某个对象被折叠进 01-schema.sql，把这一行**移走**即可（这是维护动作，
//	不是缺陷）—— 判据会在它仍在表里时报「基线里已经有了，这行该撤了」。
var healthSurfacePostBaselineDeps = []struct {
	object     string // 对象在 SQL 里出现的形态
	provider   string // 提供它的 startup 迁移文件（已登记）
	whyItIsOut string // 一句话说明为什么它不在基线里
}{
	{
		"canonical_cleared_at",
		"693_provider_models_canonical_cleared_at.sql",
		"运营显式解绑的标记；canonical_id_null 检查与 autoFixCanonicalID 都靠它，缺了会复活被解绑的绑定",
	},
	{
		// 第二条故意挑一个**关系**（视图）而不是列：只靠一条列依赖撑着的话，
		// 「对象是列」这件事就成了隐含前提，而这正是最小夹具能造出来、真 schema
		// 造不出来的那类差别。probe_missing 检查读它。
		//
		// ⚠ 它的创建语句是 `CREATE OR REPLACE VIEW v_node_probe_state_compat`
		// ——**无 public. 前缀**。2026-10-05 我按 `CREATE ... VIEW public\.` 去
		// grep，得出「716 不创建它」的错结论。列名/对象名的查找要容忍限定前缀
		// 的有无。
		"v_node_probe_state_compat",
		"716_unify_probe_health_views.sql",
		"probe_state 的兼容投影；probe_missing 检查与它 NOT EXISTS 互斥，缺了那条检查会把每一个新绑定永久报成假阳性",
	},
}

func TestHealthSurfaceOnlyDependsOnShippedMigrations(t *testing.T) {
	baseline, err := os.ReadFile("../sql/schema/01-schema.sql")
	if err != nil {
		t.Fatalf("read 01-schema.sql: %v", err)
	}
	tsv, err := os.ReadFile("../sql/schema/installed_startup_migrations.tsv")
	if err != nil {
		t.Fatalf("read installed_startup_migrations.tsv: %v", err)
	}
	registered := map[string]bool{}
	for _, ln := range strings.Split(string(tsv), "\n") {
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		if parts := strings.Split(ln, "\t"); len(parts) == 2 {
			registered[parts[1]] = true
		}
	}
	if len(registered) == 0 {
		t.Fatal("the manifest parsed to zero entries — this test is about registered migrations, " +
			"so an empty parse would make every assertion below vacuously true")
	}

	for _, dep := range healthSurfacePostBaselineDeps {
		// (1) 提供者必须已登记。
		if !registered[dep.provider] {
			t.Errorf("%s is provided by %s, which is NOT in installed_startup_migrations.tsv. "+
				"The installer's ordered startup list will never run it, so every environment "+
				"built by the real installer lacks the object %s depends on",
				dep.object, dep.provider, dep.object)
			continue
		}
		// (2) 提供者必须真的在磁盘上 —— 而「磁盘上」有**两个**地方。
		//
		// ⚠ 第一版只查了 `sql/migrations/startup/`，于是我得出一个**错的**结论：
		// 「tsv 登记但磁盘上不存在」。实测 seq 9 / seq 62 两条当时报「缺失」，
		// 而真实情况是它们在 `installer/cmd/llm-gw-installer/embeddata/startup/`
		// 下有副本（3.5KB / 30KB），`main.go` 直接 `//go:embed` 它们。
		//
		// 根因：tsv 是 `Runner.StartupFiles` 的**派生产物**（由
		// `installer/internal/dbinit/startup_manifest_test.go` 生成），而
		// `StartupFiles` 是安装器自己的有序清单 —— 它的源目录是 embeddata，
		// **不是** deploy 线的 `sql/migrations/startup/`。tsv 头部自己写着
		// deploy 目录有 458 个迁移号、只有约 200 个进安装器清单。
		// ⇒ 只在一个目录里找，就会把 installer-only 资产误判成「没交付」。
		//
		// 两处都查，且**不要求同时存在** —— 同一资产在两处都有一份副本是常态。
		b, err := os.ReadFile("../sql/migrations/startup/" + dep.provider)
		source := "sql/migrations/startup"
		if err != nil {
			b, err = os.ReadFile("../installer/cmd/llm-gw-installer/embeddata/startup/" + dep.provider)
			source = "installer/.../embeddata/startup"
		}
		if err != nil {
			t.Errorf("%s is registered as provided by %s, but that file is in neither "+
				"sql/migrations/startup/ nor installer/cmd/llm-gw-installer/embeddata/startup/. "+
				"The installer embeds from embeddata, so every environment it builds lacks the "+
				"object %s depends on: %v", dep.object, dep.provider, dep.object, err)
			continue
		}
		// (3) 提供者里必须真的有那个对象名。
		if !strings.Contains(string(b), dep.object) {
			t.Errorf("%s does not appear in %s (%s), so that migration does not create it",
				dep.object, dep.provider, source)
		}
		// (4) 维护提示：若它已经在基线里，这一行该撤掉了。
		if strings.Contains(string(baseline), dep.object) {
			t.Logf("NOTE: %s is now in 01-schema.sql — drop it from "+
				"healthSurfacePostBaselineDeps (reason: %s)", dep.object, dep.whyItIsOut)
		}
	}
}
