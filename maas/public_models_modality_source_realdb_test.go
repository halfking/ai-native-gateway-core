package maas

// `ListPublicModels` 的真库判据 —— 目标第一半「标注多模态能力」的**租户可见面**。
//
// # 为什么需要这一批（2026-10-06）
//
// `catalog/display_test.go` 钉住了 `EffectiveModality` 这个**纯函数**的出处闸门，
// 但那证明不了「SQL 真的把 modality_source 查出来了」。
//
// 而这一处的失效形态与 admin 列表**完全不同**，所以不能只靠纯函数判据：
//
//	m := &ListPublicModels 里的 rows.Scan(...)
//	if err := rows.Scan(...); err != nil { return nil, err }
//
// 租户目录是**整体返回 error**（不是逐行 warnRowSkip 跳过）。⇒ 只要 SELECT 多一列
// 而 Scan 少一个目标（或反过来），`ListPublicModels` 直接报错，**整个租户模型列表
// 消失**；而且症状长得像「maas 挂了」，与「多模态标注」毫无关系。
// admin 那侧同样的疏漏只会让列表变空 —— 两者都需要真库判据，测的东西不一样。
//
// # 承重的三件事
//
//	① Scan 的**元数**与 SELECT 的列数一致（错位 ⇒ 整表报错或整表空）。
//	② 出处真的进了读路径：semantic / manual 的行，租户看到的必须等于库里存的。
//	③ **对照组**：inferred 的行仍然按名字推断。少了它，把闸门写成「一律返回 stored」
//	  也能让 ② 全绿 —— 那会把所有 inferred 的 text 模型一次性打成 text，
//	  是比原缺陷严重得多的回归。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具对象已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// publicModelsFixture 搭出 ListPublicModels 能跑的最小生产形态。
//
// # 依赖清单集中在这里一处
//
// 抄依赖本身就是这个文件的复发面（bg/baseline_price_sync_realdb_test.go 的
// 夹具注释记着同一件事：第二份夹具只抄了部分表就去 apply 迁移，撞 42P01）。
// 表名与 ListPublicModels 的 FROM / JOIN 列表一一对应：
//
//	models_canonical     主表（并由真 825 补上三个出处列）
//	model_families       LEFT JOIN，取 display_name / vendor / status
//	model_credit_rates   LEFT JOIN，取 credits_per_1m_*
//	maas_settings        GetSettings 的单行来源；缺表 ⇒ 整个函数报错
func publicModelsFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// ★ 安全闸清单必须**穷举**本夹具建出来的每一样东西：漏一张的话，下一轮
	// CREATE TABLE 会撞已存在而失败，或者（若改用 IF NOT EXISTS）静默沿用
	// **旧形状**，测试照样绿。
	tables := []string{"maas_settings", "model_credit_rates", "model_families", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理用 `t.Cleanup` 而**不是 `defer`**：defer 绑的是本函数返回，而本函数
	// 在夹具建好的那一刻就返回了 ⇒ 表格当场被删，后面全报 42P01（踩过一次）。
	// ★ 注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到「表已存在」
	// 直接 SKIP，人看到的是「通过」，实际一次都没跑。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.maas_settings;
			DROP TABLE IF EXISTS public.model_credit_rates;
			DROP TABLE IF EXISTS public.model_families;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	})

	// 三张表都走仓的逐对象 SSOT（不手抄：手抄是第二份真相，会静默腐烂）。
	// models_canonical 的 SSOT 里**没有** 825 加的三个出处列（真库 44 列 vs
	// SSOT 37 行）⇒ 出处列由下面的**真 825** 带进来。
	if _, err := pool.Exec(ctx,
		"CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
			schemaobj.Table(t,
				"../sql/objects/tables/models_canonical.sql",
				"../sql/objects/sequences/models_canonical_id.sql",
				"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
			)+"\n"+
			schemaobj.Table(t, "../sql/objects/tables/model_families.sql")+"\n"+
			schemaobj.Table(t, "../sql/objects/tables/model_credit_rates.sql")+"\n"+
			schemaobj.Table(t, "../sql/objects/tables/maas_settings.sql")); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// 应用**真** 825：三个出处列、pkey、证据表与判词视图都由它建，不手抄。
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}

	// ★ 量具自证：建完立刻确认**真**看得见这些对象**与那三列**。
	// 少了这一步，「夹具没建出出处列」会一路伪装成「断言红」——而且症状是
	// `column "modality_source" does not exist`，人排查的方向会跑到 SQL 上，
	// 而真因在夹具。
	for _, col := range []string{"modality_source", "modality_verified_at", "modality_evidence"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name='models_canonical' AND column_name=$1`,
			col).Scan(&n); err != nil {
			t.Fatalf("self-check column %s: %v", col, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.models_canonical.%s is absent after building "+
				"the fixture — every assertion below would fail for the wrong reason", col)
		}
	}
	for _, obj := range []string{"models_canonical", "model_families", "model_credit_rates", "maas_settings"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname='public' AND c.relname = $1`, obj).Scan(&n); err != nil {
			t.Fatalf("self-check %s: %v", obj, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.%s is absent after building the fixture", obj)
		}
	}
}

// seedModalityModels 铺一组**按出处可区分**的样本。
//
// ★ 这组样本的形状是承重的：三个模型的名字都长得像多模态（都走
// inferModalityFromName 的 minimax-m3 分支），存的却都是 text，差别只在
// modality_source。若三条样本的名字不同，闸门失效时会有一半仍然通过 ——
// 「没红」会被误读成「闸门在起作用」。
func seedModalityModels(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	// GetSettings 的单行来源；GetSettings 对缺表/缺行都是**整体报错**，
	// 所以这里必须有一行，且 NOT NULL 的列都给值。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.maas_settings (id, cents_per_credit, base_credits_per_1m, currency_display)
		VALUES (1, 1.0, 1000, 'CNY');`); err != nil {
		t.Fatalf("seed maas_settings: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical
			(canonical_name, modality, modality_source, status)
		VALUES
			('minimax-m3-sem', 'text',     'semantic', 'active'),
			('minimax-m3-man', 'text',     'manual',   'active'),
			('minimax-m3-inf', 'text',     'inferred', 'active'),
			('gemini-3-vid',   'video',    'inferred', 'active'),
			('disabled-one',   'multimodal','semantic','disabled');`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
}

// TestListPublicModels_modalityProvenanceReachesTenants 是承重的那条。
func TestListPublicModels_modalityProvenanceReachesTenants(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// 先注册本行、后注册夹具的 DROP ⇒ LIFO 让 DROP 先跑、池后关。
	t.Cleanup(pool.Close)

	publicModelsFixture(t, ctx, pool)
	seedModalityModels(t, ctx, pool)

	rows, err := NewService(pool).ListPublicModels(ctx)
	// ① 元数错位在这一行就会炸（整表 error），所以它本身就是最强的一条断言。
	if err != nil {
		t.Fatalf("ListPublicModels: %v", err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.CanonicalName] = r.Modality
	}

	want := map[string]string{
		// 核实判负的降级：租户必须看到 text。改前这里是 multimodal。
		"minimax-m3-sem": "text",
		// 人工覆盖的 text：同样不能被按名推断翻回去。
		"minimax-m3-man": "text",
		// ★ 对照组：没被标注的行**仍然**按名字推断。
		// 少了它，「闸门」写成「一律返回 stored」也能让上面两条绿。
		"minimax-m3-inf": "multimodal",
		// stored='video' 不该被按名推断吃掉（gemini- 前缀会推成 multimodal）。
		"gemini-3-vid": "video",
		// status='disabled' 的一行不该出现在租户目录里。
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("租户目录缺样本 %q（返回 %d 行：%v）", name, len(rows), got)
			continue
		}
		if g != w {
			t.Errorf("%s: 租户看到的 modality=%q，库里存的是 %q（source 决定）", name, g, w)
		}
	}
	if _, leaked := got["disabled-one"]; leaked {
		t.Errorf("status='disabled' 的模型出现在租户目录里：%v", got)
	}
	if len(rows) != 4 {
		t.Errorf("返回 %d 行，期望 4（5 个样本去掉 1 个 disabled）：%v", len(rows), got)
	}
}
