package bg

// dueTargets 的真库判据 —— 目标第一半「自动对**未曾标注核实过的**模型定时
// 核实」的**选人**那一半。
//
// # 为什么必须钉
//
// 上一轮钉的是写回（rollupVerdict，把判词写进 models_canonical）。**选谁的
// 那条 SELECT 此前零判据**（`grep -rn dueTargets` 只有定义处与一个 fallback，
// 没有任何测试引用它）。
//
// 而 dueTargets 里有一处**承重**的排序，注释原话：
//
//	排序刻意把「一条证据都没有」的行排在最前：存量模型的
//	modality_verified_at 恒 NULL，若按 checked_at 排，新老证据行会互相
//	挤占额度，未核实队列可能永远排不上 —— 那正是本任务要修的缺口。
//
// 这不是「排序偏好」，它是**这个任务存在的前提**。如果未核实队列被已核实
// （但已 stale）的行挤掉，那么「自动核实未曾核实过的模型」在生产上就是空的
// —— 而它**不会报错、不会空集、每轮都正常返回行**，只是永远在核同一批老模型。
// 与本包其余几条判据同一族「看起来做了」。
//
// # 承重的是什么
//
// 双侧：① 未核实过的行**必须被选中**；② 它们**排在 stale 行之前**（排序一
// 旦被改回纯 checked_at，`NULLS LAST` 会让它们沉到最后，被 LIMIT 截掉）。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// dueTargetsFixture 是 dueTargets 读的那几张表。
//
// ★ providers / credentials / provider_models 是**脚手架**，但它们必须带上
// dueTargets 真实 SELECT 里读到的每一个列（enabled、manual_disabled、status、
// lifecycle_status、catalog_code、outbound_model_name、modality…）。少一列就是
// `column … does not exist`，而这不是「夹具太简」——它会让「SQL 能跑通」这条
// 前提悄悄不成立。models_canonical 不手抄，走仓的逐对象 SSOT（同
// provider/modality_gate_alignment_test.go、bg/supplier_view_cardinality_test.go）。
func dueTargetsFixture(t *testing.T) string {
	t.Helper()
	return "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		) + `
CREATE TABLE public.providers (
    id             bigserial PRIMARY KEY,
    code           text NOT NULL,
    base_url       text NOT NULL,
    protocol       text,
    catalog_code   text,
    enabled        boolean NOT NULL DEFAULT TRUE,
    manual_disabled boolean NOT NULL DEFAULT FALSE
);
CREATE TABLE public.credentials (
    id                bigserial PRIMARY KEY,
    provider_id       bigint NOT NULL REFERENCES public.providers(id),
    secret_ciphertext bytea,
    status            text NOT NULL DEFAULT 'active',
    lifecycle_status  text NOT NULL DEFAULT 'active',
    manual_disabled   boolean NOT NULL DEFAULT FALSE
);
CREATE TABLE public.provider_models (
    id                 bigserial PRIMARY KEY,
    provider_id        bigint NOT NULL,
    raw_model_name     text NOT NULL,
    canonical_id       bigint,
    canonical_raw_name text,
    outbound_model_name text,
    modality           text
);
CREATE TABLE public.credential_model_bindings (
    id                bigserial PRIMARY KEY,
    credential_id     bigint NOT NULL,
    provider_model_id bigint NOT NULL,
    available         boolean NOT NULL DEFAULT TRUE
);
`
}

func TestDueTargetsPicksNeverProbedFirst(t *testing.T) {
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
	defer pool.Close()

	tables := []string{"credential_model_bindings", "provider_models", "credentials",
		"providers", "model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, dueTargetsFixture(t)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	mig, err := os.ReadFile("../sql/migrations/startup/825_modality_graded_verification.sql")
	if err != nil {
		t.Fatalf("read 825: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 825: %v", err)
	}

	// 三个模型：
	//   never-1 / never-2 —— 零证据（「未曾标注核实过的」那批）
	//   stale-1           —— 有一条 **40 天前** 的证据（超过 staleAfter=30 天，
	//                       所以它**也**该被选中 —— 但必须排在未核实之后）
	// 刻意只种 1 个 stale、2 个 never：让 batchLimit 把窗口收窄到
	// batchLimit*scanFactor，若排序被改回「纯 checked_at 且 NULLS LAST」，
	// 未核实的两行会沉到末尾并被 LIMIT 截掉。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.providers (code, base_url, protocol) VALUES ('p1','https://x.invalid','openai-completions');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('never-1','text','inferred'), ('never-2','text','inferred'), ('stale-1','vision','semantic');
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, mc.canonical_name, mc.id, mc.canonical_name
		  FROM public.providers pr CROSS JOIN public.models_canonical mc;
		INSERT INTO public.credential_model_bindings (credential_id, provider_model_id)
		SELECT c.id, pm.id FROM public.credentials c, public.provider_models pm;
		INSERT INTO public.model_modality_verification
			(canonical_id, canonical_name, credential_id, raw_model_name, modality,
			 carry_level, read_level, read_pos_streak, checked_at)
		SELECT mc.id, mc.canonical_name, 1, mc.canonical_name, 'vision',
		       'accepted', 'confirmed', 2, now() - interval '40 days'
		  FROM public.models_canonical mc WHERE mc.canonical_name = 'stale-1';`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 量化断言：LIMIT = batchLimit × 4 = 1×4 = 4，而种了 3 个绑定。
	// 窗口必须**刚好**放得下「2 个未核实 + 1 个 stale」= 3；若排序错到让未核实
	// 沉底，4 的窗口仍会挤掉它们（因为 stale 的 checked_at 非 NULL 会排在前面）。
	v := &ModalityVerification{
		db:         pool,
		staleAfter: 30 * 24 * time.Hour,
		batchLimit: 1,
	}
	targets, err := v.dueTargets(ctx)
	if err != nil {
		t.Fatalf("dueTargets: %v", err)
	}

	// **量具自证**：种子必须真的落到位。
	var seeds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.credential_model_bindings`).Scan(&seeds); err != nil {
		t.Fatalf("count seed bindings: %v", err)
	}
	if seeds != 3 {
		t.Fatalf("the fixture produced %d bindings, want 3 — the seed did not land and every "+
			"assertion below is vacuous", seeds)
	}

	// ---- 承重之三：batchLimit 未设置时，窗口**不许塌成 0** ----
	//
	// 2026-10-05 实测到的洞：这个查询的窗口是 `m.batchLimit * scanFactor` 直接
	// 喂给 `LIMIT $2`。`batchLimit==0` ⇒ `LIMIT 0` ⇒ **扫不出任何行**，而循环侧
	// 当时已经在跑，于是整轮「cycle done」而实际一条都没探 —— 日志里完全看不出来。
	//
	// 这一段曾经是绿的：上一轮那条判据用 `scan` 接缝**绕过了真正的 SQL**，
	// 量的是我给的形状而不是真在跑的那条路径。⇒ 判据必须打在 `dueTargets` 上。
	unset := &ModalityVerification{
		db:         pool,
		staleAfter: 30 * 24 * time.Hour,
		batchLimit: 0, // 未设置
	}
	if got := unset.scanLimit(); got <= 0 {
		t.Errorf("scanLimit() with an unset batchLimit = %d, want a positive window — it is "+
			"bound to SQL LIMIT, where 0 means \"scan zero rows\"", got)
	}
	unsetTargets, err := unset.dueTargets(ctx)
	if err != nil {
		t.Fatalf("dueTargets with an unset batchLimit: %v", err)
	}
	if len(unsetTargets) == 0 {
		t.Errorf("dueTargets returned 0 targets with an unset batchLimit, although the fixture "+
			"holds %d bindings (2 never-verified + 1 stale). With batchLimit<=0 the scan window "+
			"collapsed to LIMIT 0 and the whole verification worker silently did nothing", seeds)
	}
	if len(unsetTargets) < 3 {
		t.Errorf("dueTargets returned %d targets with an unset batchLimit, want at least 3 (the "+
			"same population the configured case selects) — the window fell back to something "+
			"smaller than the documented default", len(unsetTargets))
	}

	got := map[string]bool{}
	order := make([]string, 0, len(targets))
	for _, tg := range targets {
		got[tg.CanonicalName] = true
		order = append(order, tg.CanonicalName)
	}
	t.Logf("dueTargets order: %v", order)

	// 承重之一：两个「未曾标注核实过的」都必须被选中。
	for _, want := range []string{"never-1", "never-2"} {
		if !got[want] {
			t.Errorf("dueTargets did not select %q — it has no evidence row at all, so this is "+
				"exactly the 「未曾标注核实过的模型」 the task exists to verify. Selected: %v",
				want, order)
		}
	}
	// 承重之二：stale 行**也**该被选中（它超过 30 天没复核了）。
	if !got["stale-1"] {
		t.Errorf("dueTargets did not select the 40-day-old evidence row 'stale-1' — the stale "+
			"re-verification window is not firing. Selected: %v", order)
	}
	// 承重之三（★ 最承重的那条）：未核实过的必须**排在** stale 之前。
	// 排序被改回「纯 checked_at + NULLS LAST」时，未核实的两行会沉到末尾，
	// 被 LIMIT 截掉 —— 而那条 SELECT 仍然每轮正常返回行，不报错不空集。
	idxNever, idxStale := -1, -1
	for i, n := range order {
		if (n == "never-1" || n == "never-2") && idxNever < 0 {
			idxNever = i
		}
		if n == "stale-1" {
			idxStale = i
		}
	}
	if idxNever >= 0 && idxStale >= 0 && idxNever > idxStale {
		t.Errorf("a never-probed model sorted at %d, after the stale row at %d — the anti-starvation "+
			"ordering is gone, so once stale rows outnumber the scan window the never-verified "+
			"queue can never be reached again. Order: %v", idxNever, idxStale, order)
	}
	// 承重之四：模态路由 —— stored='text' 的行**走 vision 分支**。
	// 那正是 text→多模态 升级唯一的发现入口（文件头第 2 条：本任务是现状
	// 唯一能发现升级的机制，因为 verifyTargetModality 在 text 时直接 return）。
	for _, tg := range targets {
		if tg.CanonicalName == "never-1" {
			if tg.Modality != "vision" {
				t.Errorf("a model stored as text was routed to probe_modality=%q, want vision — "+
					"that routing is the only entry point that can discover a text→multimodal "+
					"upgrade", tg.Modality)
			}
			if tg.StoredModality != "text" {
				t.Errorf("never-1 stored_modality=%q, want text", tg.StoredModality)
			}
		}
	}
}
