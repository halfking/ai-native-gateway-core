package streaming

// models_scope_realdb_test.go —— 精选口径过滤的**真库**判据。
//
// ★ 为什么需要真库：精选过滤（modelsFeaturedPredicate）是一条 SQL 表达式，
//   而它最危险的地方**恰好是纯文本判据看不见的**：
//
//   1. PostgreSQL 的 set-returning function 在 target list 里**先于 LIMIT 展开**
//      ⇒ `SELECT LOWER(unnest(featured_models)) ... LIMIT 1` 只返回数组的
//      **第一个元素**。这一条用字符串判据钉不住（两种写法都「包含 unnest
//      和 LIMIT」），必须让真库把两种样本跑一遍。
//   2. 精选名可以写成**供应商侧的名字**（raw_model_name /
//      standardized_name）⇒ 过滤必须认那三种名字形态。
//   3. 精选口径筛不出任何模型时的回退路径，必须真的走到第二条查询。
//
//   这三条里，第 1 条是「看起来对、实际错」的典型：精选过滤只认一个模型，
//   而每一行日志、每一条单测都还是绿的。
//
// # 跑法
// TEST_DATABASE_URL=postgres://... go test ./domains/streaming/ -run Scope_RealDB
//
// 未设置该环境变量时本文件整体 SKIP（与仓内其它真库判据同款）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// modelsScopeFixture 建出精选过滤跑得动的最小生产形态。
//
// ★ 表清单与 modelsRoutableSQL 的 FROM / JOIN 列表一一对应（漏一张 ⇒ 42P01
// 或谓词失效）：models_canonical / provider_models /
// credential_model_bindings / credentials / providers / node_probe_state /
// routing_policy。
//
// ★ DDL 走仓的逐对象 SSOT（internal/schemaobj.Table），**不手抄**：手抄就是
// 第二份真相，会静默腐烂。
func modelsScopeFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	tables := []string{
		"models_canonical", "provider_models", "credential_model_bindings",
		"credentials", "providers", "node_probe_state", "routing_policy",
	}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test owns them and drops them", existing)
	}

	// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP —— 人看到的是「通过」，实际一次都没跑。
	//
	// ★ 逐条 DROP、且**不吞错误**：一条多语句 Exec 在 PostgreSQL 里是隐式
	//   事务 —— 任何一条失败 ⇒ 整段回滚 ⇒ 表全留着 ⇒ 下一轮安全闸直接
	//   SKIP（本文件第一版就踩了这个：清理静默失败，残桩留到下一轮）。
	t.Cleanup(func() {
		dropCtx := context.WithoutCancel(ctx)
		for _, stmt := range []string{
			"DROP VIEW IF EXISTS public.model_offers",
			"DROP TABLE IF EXISTS public.node_probe_state",
			"DROP TABLE IF EXISTS public.routing_policy",
			"DROP TABLE IF EXISTS public.credential_model_bindings",
			"DROP TABLE IF EXISTS public.credentials",
			"DROP TABLE IF EXISTS public.providers",
			"DROP TABLE IF EXISTS public.provider_models",
			"DROP TABLE IF EXISTS public.models_canonical",
			"DROP SEQUENCE IF EXISTS public.models_canonical_id_seq",
			"DROP SEQUENCE IF EXISTS public.provider_models_id_seq",
			"DROP SEQUENCE IF EXISTS public.credential_model_bindings_id_seq",
			"DROP SEQUENCE IF EXISTS public.credentials_id_seq",
			"DROP SEQUENCE IF EXISTS public.providers_id_seq",
		} {
			if _, err := pool.Exec(dropCtx, stmt); err != nil {
				t.Errorf("cleanup %q: %v", stmt, err)
			}
		}
	})

	ddl := "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.provider_models_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.credential_model_bindings_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.credentials_id_seq;\n" +
		"CREATE SEQUENCE IF NOT EXISTS public.providers_id_seq;\n" +
		schemaobj.Table(t,
			"../../sql/objects/tables/models_canonical.sql",
			"../../sql/objects/sequences/models_canonical_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../../sql/objects/tables/provider_models.sql",
			"../../sql/objects/sequences/provider_models_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../../sql/objects/tables/credential_model_bindings.sql",
			"../../sql/objects/sequences/credential_model_bindings_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../../sql/objects/tables/credentials.sql",
			"../../sql/objects/sequences/credentials_id.sql",
		) + "\n" +
		schemaobj.Table(t,
			"../../sql/objects/tables/providers.sql",
			"../../sql/objects/sequences/providers_id.sql",
		) + "\n" +
		schemaobj.Table(t, "../../sql/objects/tables/node_probe_state.sql") + "\n" +
		schemaobj.Table(t, "../../sql/objects/tables/routing_policy.sql")

	if _, err := pool.Exec(ctx, ddl); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// ★ 量具自证：建完立刻确认真库看得见这些对象。少了这一步，「夹具没建出
	// 某张表」会一路伪装成「断言红」，而排查方向会跑到 SQL 上。
	for _, obj := range tables {
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

// modelsScopeSeed 铺一组**每条断言各自可区分**的样本。
// featured-a   精选名单里写的是 canonical 名
// featured-b   精选名单里写的是**供应商侧**的名字（raw_model_name）
// plain-c      不在精选名单里
// dead-d       在精选名单里，但唯一凭据已死（auth_failed）⇒ 不可路由
//
// ★ 「每条样本只验一件事」是承重的：如果四个模型都用同一种名字形态，
// 那么「只认 canonical 名」的实现也会全绿 ⇒ 门没有牙。
func modelsScopeSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.providers (id, code, display_name, protocol, base_url)
		VALUES (1, 'p-live', 'Live Provider', 'openai-completions', 'https://example.invalid/v1'),
		       (2, 'p-off',  'Off Provider',  'openai-completions', 'https://example.invalid/v1');
		INSERT INTO public.credentials (id, provider_id, label, fp_slot_limit)
		VALUES (1, 1, 'live-cred', 8),
		       (2, 1, 'dead-cred', 8);
		-- dead-cred：凭据级死（availability_state != ready）
		UPDATE public.credentials SET availability_state = 'auth_failed' WHERE id = 2;
		INSERT INTO public.models_canonical (id, canonical_name, family)
		VALUES (1, 'featured-a', 'test'),
		       (2, 'featured-b', 'test'),
		       (3, 'plain-c',    'test'),
		       (4, 'dead-d',     'test');
		INSERT INTO public.provider_models (id, provider_id, canonical_id, raw_model_name, standardized_name, canonical_raw_name)
		VALUES (1, 1, 1, 'featured-a',        'featured-a',        'featured-a'),
		       (2, 1, 2, 'vendor-special-b',  'featured-b',        'featured-b'),
		       (3, 1, 3, 'plain-c',           'plain-c',           'plain-c'),
		       (4, 1, 4, 'dead-d',            'dead-d',            'dead-d');
		INSERT INTO public.credential_model_bindings (id, credential_id, provider_model_id)
		VALUES (1, 1, 1),
		       (2, 1, 2),
		       (3, 1, 3),
		       (4, 2, 4);
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// modelsScopeList runs the discovery endpoint and returns the model ids.
func modelsScopeList(t *testing.T, h *ModelsHandler, url string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status = %d, body=%s", url, rec.Code, rec.Body.String())
	}
	var payload struct {
		Data []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("%s: decode: %v", url, err)
	}
	out := make([]string, 0, len(payload.Data))
	for _, e := range payload.Data {
		out = append(out, e.ID)
	}
	return out
}

func modelsScopeHas(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// TestModelsScope_RealDB_FeaturedFilter pins the whole scope contract
// against a REAL PostgreSQL.
func TestModelsScope_RealDB_FeaturedFilter(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// ★ 池的关闭注册在**夹具清理之前**：t.Cleanup 是 LIFO ⇒ 夹具的 DROP
	//   先跑、池后关。若用 defer pool.Close()，defer 先于 t.Cleanup 执行 ⇒
	//   清理撞上「closed pool」⇒ 表全留着 ⇒ 下一轮安全闸直接 SKIP。
	t.Cleanup(pool.Close)

	modelsScopeFixture(t, ctx, pool)
	modelsScopeSeed(t, ctx, pool)

	// ★ 精选名单**同时**包含 canonical 名与供应商侧名字。
	//   顺序很重要：'featured-a' 在前，'vendor-special-b' 在后。
	//   若 CTE 写法踩了「target list 的 SRF 先于 LIMIT 展开」那个坑，
	//   清单会被截成**第一个元素** ⇒ featured-b 消失 ⇒ 本用例变红。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.routing_policy (id, tenant_id, featured_models)
		VALUES (1, 'default', ARRAY['featured-a', 'vendor-special-b', 'dead-d']);`); err != nil {
		t.Fatalf("seed routing_policy: %v", err)
	}

	h := NewModelsHandler()
	h.SetDB(pool)

	t.Run("default is featured", func(t *testing.T) {
		got := modelsScopeList(t, h, "/v1/models")
		if !modelsScopeHas(got, "featured-a") {
			t.Errorf("featured-a (matched by canonical name) must be listed: %v", got)
		}
		if !modelsScopeHas(got, "featured-b") {
			t.Errorf("featured-b (matched by the PROVIDER-side name) must be listed — "+
				"a curated model the router treats as featured must not be invisible: %v", got)
		}
		if modelsScopeHas(got, "plain-c") {
			t.Errorf("plain-c is not curated and must not be listed: %v", got)
		}
		if modelsScopeHas(got, "dead-d") {
			t.Errorf("dead-d has only a dead credential and must not be listed: %v", got)
		}
		if len(got) != 2 {
			t.Errorf("expected exactly the 2 routable curated models, got %d: %v", len(got), got)
		}
	})

	t.Run("scope=all returns the full list", func(t *testing.T) {
		got := modelsScopeList(t, h, "/v1/models?scope=all")
		if !modelsScopeHas(got, "plain-c") {
			t.Errorf("scope=all must include the non-curated routable model: %v", got)
		}
		if modelsScopeHas(got, "dead-d") {
			t.Errorf("scope=all must still exclude the unroutable model: %v", got)
		}
		if len(got) != 3 {
			t.Errorf("expected 3 routable models, got %d: %v", len(got), got)
		}
	})

	t.Run("all=true is the same opt-in", func(t *testing.T) {
		got := modelsScopeList(t, h, "/v1/models?all=true")
		if !modelsScopeHas(got, "plain-c") {
			t.Errorf("?all=true must behave like scope=all: %v", got)
		}
	})

	t.Run("scope=featured is the default", func(t *testing.T) {
		got := modelsScopeList(t, h, "/v1/models?scope=featured")
		if modelsScopeHas(got, "plain-c") {
			t.Errorf("scope=featured must not include plain-c: %v", got)
		}
	})

	t.Run("empty curated set falls back to the full list", func(t *testing.T) {
		// ★ 精选名单里只剩一个**不可路由**的模型 ⇒ 精选口径筛不出东西
		//   ⇒ 必须退回全量，而不是交一张空清单。
		if _, err := pool.Exec(ctx, `
			UPDATE public.routing_policy SET featured_models = ARRAY['dead-d'] WHERE tenant_id = 'default';`); err != nil {
			t.Fatalf("update routing_policy: %v", err)
		}
		defer func() {
			if _, err := pool.Exec(context.WithoutCancel(ctx), `
				UPDATE public.routing_policy
				SET featured_models = ARRAY['featured-a', 'vendor-special-b', 'dead-d']
				WHERE tenant_id = 'default';`); err != nil {
				t.Errorf("restore routing_policy: %v", err)
			}
		}()

		got := modelsScopeList(t, h, "/v1/models")
		if !modelsScopeHas(got, "plain-c") {
			t.Errorf("an empty curated result must fall back to the full list: %v", got)
		}
		if len(got) != 3 {
			t.Errorf("expected the full routable list (3), got %d: %v", len(got), got)
		}
	})
}

// TestModelsScope_RealDB_CuratedListIsNotTruncated is the dedicated arm for
// the LIMIT/unnest trap.
//
// ★ It asks the database directly (not through the handler) how many names
// the curated CTE yields. If the CTE were written in the truncating
// one-liner form, the answer would be 1 instead of 3 — and every other
// assertion in this file would still look fine, because the first element
// happens to be a valid curated model.
func TestModelsScope_RealDB_CuratedListIsNotTruncated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	modelsScopeFixture(t, ctx, pool)
	modelsScopeSeed(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.routing_policy (id, tenant_id, featured_models)
		VALUES (1, 'default', ARRAY['featured-a', 'vendor-special-b', 'dead-d']);`); err != nil {
		t.Fatalf("seed routing_policy: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			WITH policy AS (
				SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) AS names
				FROM routing_policy
				WHERE tenant_id = 'default'
				ORDER BY id
				LIMIT 1
			),
			featured AS (
				SELECT LOWER(unnest(names)) AS name
				FROM policy
			)
			SELECT name FROM featured
		) t`).Scan(&n); err != nil {
		t.Fatalf("count curated names: %v", err)
	}
	if n != 3 {
		t.Fatalf("the curated CTE yielded %d names, want 3 — a set-returning function in "+
			"the target list is expanded BEFORE LIMIT, so the one-liner form would yield 1", n)
	}
}
