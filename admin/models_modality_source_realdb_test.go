package admin

// `listModels` / `getModel` 的真库判据 —— 目标第一半「标注多模态能力」的
// **管理可见面**，以及读路径出处闸门在这两个 handler 上的落地。
//
// # 为什么需要这一批（2026-10-06）
//
// `catalog/display_test.go` 钉住了 `EffectiveModality` 纯函数，**证明不了**
// 「SQL 真的把 modality_source 查出来了」，更证明不了「响应体真的带上了出处」。
// 三个只有真库 handler 判据才量得到的东西：
//
//	① **列元数**。listModels 逐行 `warnRowSkip` 后 continue，getModel 则是
//	   **整表报错**。多一列而 Scan 少一个目标 ⇒ 列表变空 / 详情 500。
//	② **出处进了响应体**。这三个字段此前**全仓零读者**（写侧一直在盖章，
//	   读侧只吐 modality 本身）。
//	③ **对照组**：inferred 的行仍然按名字推断。少了它，把闸门写成「一律返回
//	   stored」也能让 ② 全绿 —— 那会把 960 行 inferred 里的 text 模型一次性
//	   打成 text，比原缺陷严重得多。
//
// ★ 本文件当场抓到过一个「已实现 ≠ 已接线」：加上 struct 字段与 SELECT 列之后，
//   真库复跑仍然「响应里没有」—— 因为 getModel 拼的是**显式 map**，
//   struct 上的 json tag 在这个 handler 里**不生效**。纯函数判据与源码形状判据
//   都看不见这件事（前者不碰 HTTP，后者只会数到 SELECT 与 struct）。
//   ⇒ 这就是「handler 级真库判据」买到的具体东西，写在这里以免下一个人
//   又只改 struct 就宣布完成。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具对象已存在时也跳过（它要建表）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// modelsHandlerFixture 搭出 listModels / getModel 能跑的最小生产形态。
//
// # 依赖清单集中在这里一处
//
// 抄依赖本身就是本文件的复发面（bg/baseline_price_sync_realdb_test.go 的夹具
// 注释记着同一件事）。表名与两个 handler 的 FROM / JOIN 列表一一对应：
//
//	models_canonical   主表（真 825 补上三个出处列）
//	model_families     listModels / 报价面板都 LEFT JOIN 它取 vendor / display_name
//	model_aliases      列表的 LATERAL 计数 + 详情别名面板 + 报价面板的子查询
//	model_offers       列表的 LATERAL 计数 + 详情报价面板
//	providers/credentials  报价面板的 JOIN（脚手架，只为让面板不降级）
//
// # 为什么对象 SSOT 拿不到那三个出处列
//
// sql/objects/tables/models_canonical.sql 是 **pg_dump 形态的快照**，落后于迁移：
// 真库 44 列 vs 该文件 37 行，缺 825 的 3 个出处列与 826 的 9 个 baseline_*。
// ⇒ 出处列由下面的**真 825** 带进来，不在夹具里手抄。
// （这不是本文件的债：provisioning 走迁移 + db.go 的自愈块，两条都覆盖了这些列。）
func modelsHandlerFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	tables := []string{"model_offers", "credentials", "providers", "model_aliases",
		"model_families", "models_canonical", "model_iq_runs", "node_iq_latest"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到「表已存在」
	// 直接 SKIP，人看到的是「通过」，实际一次都没跑。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.model_offers;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.model_aliases;
			DROP TABLE IF EXISTS public.model_families;
			DROP TABLE IF EXISTS public.node_iq_latest;
			DROP TABLE IF EXISTS public.model_iq_runs;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	})

	if _, err := pool.Exec(ctx,
		"CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
			schemaobj.Table(t,
				"../sql/objects/tables/models_canonical.sql",
				"../sql/objects/sequences/models_canonical_id.sql",
				"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
			)+"\n"+
			schemaobj.Table(t, "../sql/objects/tables/model_families.sql")+"\n"+
			// 脚手架：报价面板要 JOIN 它们，缺表会让面板降级（warn 而非断言红），
			// 于是本文件会**在一个降级的 handler 上**宣布通过。
			`CREATE TABLE public.providers (
    id bigserial PRIMARY KEY, display_name text, catalog_code text, base_url text,
    enabled boolean DEFAULT TRUE);
CREATE TABLE public.credentials (
    id bigserial PRIMARY KEY, provider_id bigint NOT NULL, label text, status text,
    health_status text, concurrency_limit integer);
CREATE TABLE public.model_aliases (
    id bigserial PRIMARY KEY, canonical_id bigint NOT NULL, raw_name text NOT NULL,
    quantization text, surface text, status text DEFAULT 'active', notes text,
    updated_at timestamptz DEFAULT now());
CREATE TABLE public.model_offers (
    id bigserial PRIMARY KEY, canonical_id bigint, credential_id bigint NOT NULL,
    raw_model_name text, standardized_name text, p95_latency_ms integer,
    success_rate numeric, available boolean DEFAULT TRUE,
    unit_price_in_per_1m numeric, unit_price_out_per_1m numeric,
    cache_read_price_per_1m numeric, cache_write_price_per_1m numeric);`); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// 应用**真** 825 与**真** 350：出处列 / pkey / 证据表来自 825，standard_iq
	// 三列来自 350（listModels 的 SELECT 要读 standard_iq）。两段都不手抄。
	//
	// ★ 这两段本身就是「对象 SSOT 落后」的证据：models_canonical.sql 是
	// pg_dump 形态的快照（真库 44 列 vs 该文件 37 行），既没有 825 的出处列、
	// 也没有 350 的 standard_iq。⇒ 任何走 `schemaobj.Table` 建表的夹具拿到的
	// 都不是生产形状；本文件第一次跑时就是这样红的：
	//   `column mc.standard_iq does not exist` ⇒ 列表返回 `[]`。
	// 而 listModels 对查询失败的处理是 `writeJSON([]any{})` **返回空列表**，
	// 症状是「模型列表空了」，与「夹具缺列」毫无关系。
	for _, mig := range []string{
		"../sql/migrations/startup/825_modality_graded_verification.sql",
		"../sql/migrations/domain/350_model_iq.sql",
	} {
		body, err := os.ReadFile(mig)
		if err != nil {
			t.Fatalf("read %s: %v", mig, err)
		}
		if _, err := pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", mig, err)
		}
	}

	// ★ 量具自证：缺这一步，「夹具没建出某列」会伪装成「断言红」，
	// 而且症状是 column does not exist ⇒ 排查方向会跑到 SQL 上。
	for _, col := range []string{"modality_source", "modality_verified_at", "modality_evidence", "standard_iq"} {
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
	for _, obj := range []string{"models_canonical", "model_families", "model_aliases", "model_offers"} {
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

// seedModalityRows 铺一组**按出处可区分**的样本。
//
// ★ 样本形状是承重的：三个模型的名字都长得像多模态（minimax-m3 前缀）、
// 存的却都是 text，差别只在 modality_source。若样本名字不同，闸门失效时
// 会有一半仍然通过 ——「没红」会被误读成「闸门在起作用」。
func seedModalityRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	rows := []struct{ name, modality, source string }{
		{"minimax-m3-sem", "text", "semantic"},
		{"minimax-m3-man", "text", "manual"},
		{"minimax-m3-inf", "text", "inferred"},
		{"gemini-3-vid", "video", "inferred"},
	}
	ids := map[string]int64{}
	for _, r := range rows {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO public.models_canonical
				(canonical_name, modality, modality_source, modality_verified_at, modality_evidence)
			VALUES ($1, $2, $3, now() - interval '2 hours', $4::jsonb)
			RETURNING id`,
			r.name, r.modality, r.source,
			`{"modality":"vision","verdict":"negative","bindings_probed":2}`).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", r.name, err)
		}
		ids[r.name] = id
	}
	return ids
}

// TestListModels_modalityProvenanceReachesTheList 承重：出处进了列表响应。
func TestListModels_modalityProvenanceReachesTheList(t *testing.T) {
	pool := modelsHandlerRealDB(t)
	ctx := context.Background()
	seedModalityRows(t, ctx, pool)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	NewHandler(pool, "", nil).listModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Total int `json:"total"`
		Items []struct {
			CanonicalName  string  `json:"canonical_name"`
			Modality       string  `json:"modality"`
			ModalitySource *string `json:"modality_source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode list body: %v (body=%s)", err, rec.Body.String())
	}
	if resp.Total != 4 || len(resp.Items) != 4 {
		t.Fatalf("列表 %d/%d 行，期望 4 —— 少行说明 SELECT/Scan 元数错位被 warnRowSkip 吞掉了",
			resp.Total, len(resp.Items))
	}

	got := map[string]struct {
		modality, source string
	}{}
	for _, it := range resp.Items {
		src := ""
		if it.ModalitySource != nil {
			src = *it.ModalitySource
		}
		got[it.CanonicalName] = struct{ modality, source string }{it.Modality, src}
	}
	want := map[string]struct{ modality, source string }{
		"minimax-m3-sem": {"text", "semantic"},
		"minimax-m3-man": {"text", "manual"},
		// ★ 对照组：没被标注的行**仍然**按名字推断。
		"minimax-m3-inf": {"multimodal", "inferred"},
		"gemini-3-vid":   {"video", "inferred"},
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("列表缺样本 %q（返回 %v）", name, got)
			continue
		}
		if g.modality != w.modality {
			t.Errorf("%s: 列表 modality=%q，期望 %q（库里存的是 %q，source=%q 决定读出来是什么）",
				name, g.modality, w.modality, w.modality, w.source)
		}
		if g.source != w.source {
			t.Errorf("%s: 列表缺出处 —— modality_source=%q，期望 %q。"+
				"这一列此前零读者，运维分不出「猜的」与「验过的」", name, g.source, w.source)
		}
	}
}

// TestGetModel_exposesProvenanceInTheResponseBody 钉住本文件开头记的那个坑：
// struct 上加了字段、SELECT 里也查了，响应体里仍然可能没有 —— 因为
// getModel 拼的是显式 map。
func TestGetModel_exposesProvenanceInTheResponseBody(t *testing.T) {
	pool := modelsHandlerRealDB(t)
	ctx := context.Background()
	ids := seedModalityRows(t, ctx, pool)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/models/"+strconv.FormatInt(ids["minimax-m3-sem"], 10), nil)
	NewHandler(pool, "", nil).getModel(rec, req, int(ids["minimax-m3-sem"]))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// 用 map 解码：**键不存在**与「值为 null」必须能分开，否则「没写进 map」
	// 会被当成「库里是 NULL」而通过。
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	for _, key := range []string{"modality_source", "modality_verified_at", "modality_evidence"} {
		if _, ok := body[key]; !ok {
			t.Errorf("响应体里**没有** %q 这个键。getModel 拼的是显式 map，"+
				"struct 上的 json tag 在这个 handler 里不生效", key)
		}
	}

	var src, modality string
	var verifiedAt string
	var evidence struct {
		Verdict string `json:"verdict"`
	}
	_ = json.Unmarshal(body["modality_source"], &src)
	_ = json.Unmarshal(body["modality"], &modality)
	_ = json.Unmarshal(body["modality_verified_at"], &verifiedAt)
	_ = json.Unmarshal(body["modality_evidence"], &evidence)
	if src != "semantic" {
		t.Errorf("modality_source=%q，期望 %q", src, "semantic")
	}
	// 详情读的是**存储值**（不经 EffectiveModality）⇒ 判负降级后的 text
	// 必须在详情里原样出现，否则「核实降级」只在两个 handler 之一可见。
	if modality != "text" {
		t.Errorf("详情 modality=%q，期望存储值 %q（详情不经按名推断）", modality, "text")
	}
	if verifiedAt == "" || verifiedAt == "null" {
		t.Errorf("modality_verified_at=%q，期望有值 —— 这一列是「定时核实跑没跑过」的唯一出口", verifiedAt)
	}
	if evidence.Verdict != "negative" {
		t.Errorf("modality_evidence.verdict=%q，期望 %q（证据 JSON 要能被读出来）",
			evidence.Verdict, "negative")
	}
}

// modelsHandlerRealDB 连真库 + 装夹具，顺序与 Cleanup 的 LIFO 都按
// bg/baseline_price_sync_realdb_test.go 的注释处理：先注册池的关闭、后注册
// 夹具的 DROP ⇒ DROP 先跑。
func modelsHandlerRealDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
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
	t.Cleanup(pool.Close)
	modelsHandlerFixture(t, ctx, pool)
	return pool
}
