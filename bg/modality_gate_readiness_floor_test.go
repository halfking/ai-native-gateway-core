package bg

// 「等 models_blocked_by_strict 降到 0 再开 LLM_GATEWAY_MODALITY_ROUTING_STRICT」
// 这条判据**构造上达不到**。这个文件把它变成可行动的数字。
//
// # 为什么它达不到（2026-10-05 真环境实测，不是推理）
//
// 两个事实相乘：
//
//  1. 827 视图的 `models_blocked_by_strict` 分母是 **models_canonical 全表**
//     （`per_pair` 枚举每个 canonical 模型的三个非文本模态）。真环境实测：
//     960 个模型 × 3 = 2880 对，`pairs_confirmed = 0`，
//     所以 `models_blocked_by_strict = 960`。
//
//  2. 核实 worker **只走绑定**（`dueTargets` 的 FROM 是
//     `credential_model_bindings`）。真环境用**它自己的谓词**实测：
//
//	canonical 模型总数               = 960
//	有可用绑定、能探到的模型         = 584
//	可探目标（可用绑定数）           = 1080
//	⇒ 永远拿不到读证据的模型         = 376（39%）
//
// ⇒ 那 376 个模型永远产生不了 `read_level='confirmed'` ⇒
// `excluded_by_strict_gate` 对它们恒为 true ⇒ **那个 0 不会来**。
//
// ★ 这不是 827 的缺陷：分母取全表是**刻意保守**的 —— 今天没绑定的模型明天可能
// 绑上，那时它理应被算进去。保守是对的，缺的是把地板说清楚。
//
// # 判据钉的承重
//
// 两条，缺一条这条检查就是「一个只会说话的东西」：
//
//	A. **有地板时必须报**，且报的三个数（总数 / 够得着的 / 地板）必须逐个对；
//	   detail 必须说出「这个 0 不可达」和「真正能降下去的是哪个数」。
//	B. **没有地板时必须闭嘴** —— 被挡的模型全都有可探绑定时，「等 0」是可达的，
//	   这条检查没有话说。防的是它退化成「只要有模型没核实就报」的健康面噪声。
//
// C. 顺带钉住 reachable 集合用的是 `modalityVerifyAddressableSource`：
//	   它必须与 `dueTargets` 是**同一份**谓词。抄第二份的话这条检查报出来的
//	   地板数是另一个集合的数，而它的全部价值就是「这个数永远降不下去」。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// readinessFixture 搭出「827 视图 + 绑定侧」并把被挡模型分成「有绑定」与「无绑定」。
//
// withBinding 数是**唯一**的旋钮：它控制地板存不存在，因此两条承重共用一个夹具。
func readinessFixture(t *testing.T, ctx context.Context, withBinding int) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	tables := []string{"routing_health_checks", "model_modality_verification", "provider_models",
		"credentials", "providers", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}
	t.Cleanup(func() {
		// ★ 清理**必须穷举**这两个迁移建出来的每一样东西，而且错误**不许吞**。
		//
		// 第一版漏了 825 建的 `v_model_modality_verdict` ⇒ `DROP TABLE
		// models_canonical` 因依赖仍在而失败 ⇒ 而清理写成 `_, _ =` 把错误丢了
		// ⇒ 残桩静默留库 ⇒ 下一轮安全闸看到「表已存在」直接 SKIP。
		// 症状与「测试跑过了」只差一行日志，而那行日志恰恰是被吞掉的那行。
		// （与 bg/modality_health_check_test.go 的夹具需要同一份清单：它也应用
		// 825+827。）
		//
		// 用 CASCADE 兜住「清单又漏了一个依赖对象」：安全闸已保证进来时这些表
		// 都不存在，所以 CASCADE 不会碰到任何不属于本夹具的东西。
		_, err := pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_model_modality_verification_rollup CASCADE;
			DROP VIEW IF EXISTS public.v_model_modality_verification_progress CASCADE;
			DROP VIEW IF EXISTS public.v_model_modality_verdict CASCADE;
			DROP TABLE IF EXISTS public.routing_health_checks CASCADE;
			DROP TABLE IF EXISTS public.model_modality_verification CASCADE;
			DROP TABLE IF EXISTS public.credential_model_bindings CASCADE;
			DROP TABLE IF EXISTS public.provider_models CASCADE;
			DROP TABLE IF EXISTS public.credentials CASCADE;
			DROP TABLE IF EXISTS public.providers CASCADE;
			DROP TABLE IF EXISTS public.models_canonical CASCADE;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq CASCADE;`)
		if err != nil {
			t.Errorf("fixture teardown failed: %v — the next run will SKIP on \"table already "+
				"exists\" and report no conclusion at all", err)
		}
	})

	if _, err := pool.Exec(ctx, `CREATE TABLE public.routing_health_checks (
		check_id text, severity text, entity_type text, entity_id bigint,
		entity_name text, detail text, fix_sql text, status text,
		created_at timestamptz, updated_at timestamptz,
		UNIQUE (check_id, entity_type, entity_id))`); err != nil {
		t.Fatalf("create routing_health_checks: %v", err)
	}
	if _, err := pool.Exec(ctx, supplierViewFixture(t)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// 脚手架补列：共享常量（= dueTargets 的投影）要 secret_ciphertext，准入谓词要
	// status / lifecycle_status / manual_disabled，providers 要 enabled。
	// 一次只能 ALTER 一张表。
	if _, err := pool.Exec(ctx, `
		ALTER TABLE public.provider_models ADD COLUMN IF NOT EXISTS canonical_cleared_at timestamptz;
		ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS secret_ciphertext bytea;
		ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS status text;
		ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS lifecycle_status text;
		ALTER TABLE public.credentials ADD COLUMN IF NOT EXISTS manual_disabled boolean;
		ALTER TABLE public.providers    ADD COLUMN IF NOT EXISTS enabled boolean;
		ALTER TABLE public.providers    ADD COLUMN IF NOT EXISTS manual_disabled boolean;
		ALTER TABLE public.providers    ADD COLUMN IF NOT EXISTS base_url text;
		ALTER TABLE public.providers    ADD COLUMN IF NOT EXISTS protocol text;
		ALTER TABLE public.providers    ADD COLUMN IF NOT EXISTS catalog_code text;
		ALTER TABLE public.provider_models ADD COLUMN IF NOT EXISTS outbound_model_name text;
		ALTER TABLE public.provider_models ADD COLUMN IF NOT EXISTS modality text;
		ALTER TABLE public.models_canonical  ADD COLUMN IF NOT EXISTS status text;
		-- available 必须带 DEFAULT TRUE：真表是 NOT NULL DEFAULT true，而谓词写的是
		-- cmb.available = TRUE —— NULL 不等于 TRUE ⇒
		-- 后补一列却不给默认值，会让**所有**绑定都进不了 reachable 集合。
		ALTER TABLE public.credential_model_bindings ADD COLUMN IF NOT EXISTS available boolean DEFAULT TRUE;`); err != nil {
		// 补列清单是**从共享常量的投影列反推**出来的，不是逐个报错试出来的：
		// 每漏一列就 42703 一次，而 42703 的消息只说「哪一列」，不说「谁要它」。
		t.Fatalf("scaffold columns: %v", err)
	}
	// 应用**真** 825 与真 827，不手抄 DDL：825 建 models_canonical 上那几列
	// （modality_source 等）**以及证据表本身** —— 827 的 per_pair/agg 从它们读。
	// 顺序不能反：先 827 会 42703（`column mc.modality_source does not exist`，
	// 踩过一次）。与 bg/modality_health_check_test.go 同一取舍：手抄 DDL 会把
	// 「迁移里到底写了什么」从判据里摘出去。
	for _, name := range []string{
		"825_modality_graded_verification.sql",
		"827_modality_verification_progress_view.sql",
	} {
		mig, err := os.ReadFile("../sql/migrations/startup/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(mig)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	// withBinding 是**唯一**的旋钮：按 canonical_name 排序取前 N 个模型配绑定。
	// 6 个模型（m-with0..2 / m-without0..2）全部零读证据 ⇒ 六个全被严格门挡住。
	//   N=2 ⇒ 2 个够得着、4 个永远够不着 ⇒ 地板 4（承重 A）
	//   N=6 ⇒ 六个全够得着               ⇒ 地板 0（承重 B：必须闭嘴）
	// 取前 N 而不是「只从 m-with* 里取」，是为了让 N=6 有意义（那一组只有 3 个）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-with0','text','inferred'), ('m-with1','text','inferred'), ('m-with2','text','inferred'),
		       ('m-without0','text','inferred'), ('m-without1','text','inferred'), ('m-without2','text','inferred');
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		-- ★ 必须显式写 status / lifecycle_status：共享谓词要求
		-- lifecycle_status='active' 且 status IN ('active','cooling','degraded')，
		-- 而这两列是**后补的**脚手架列，默认为 NULL ⇒ 谓词不成立 ⇒ reachable=0。
		-- 第一版忘了写，症状是检查照常报出一行，只是三个数全错（6/0/6）——
		-- 判据没红是因为它**只钉了形状之外的东西还没跑到**。
		INSERT INTO public.credentials (provider_id, status, lifecycle_status)
		SELECT id, 'active', 'active' FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, 'raw-' || mc.canonical_name, mc.id, mc.canonical_name
		  FROM public.providers pr, public.models_canonical mc
		 WHERE mc.canonical_name IN ('m-with0','m-with1','m-with2',
		                             'm-without0','m-without1','m-without2')
		  ORDER BY mc.canonical_name
		 LIMIT `+fmt.Sprintf("%d", withBinding)+`;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id, 1.00, 2.00, 'USD'
		  FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ★ 量具自证：把**每一环**的计数打出来。缺了它，「reachable=0」只告诉你
	// 结果，不告诉你是「绑定没种进去」「canonical_id 没连上」还是「准入谓词
	// 某一条不成立」—— 而这三种的修法完全不同（第一版就是这样卡了三轮）。
	var models, pms, binds, reach int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM public.models_canonical),
		       (SELECT count(*) FROM public.provider_models),
		       (SELECT count(*) FROM public.credential_model_bindings),
		       (SELECT count(DISTINCT pp.canonical_id) FROM (`+modalityVerifyAddressableSource+`) pp
		         WHERE pp.canonical_id > 0)`).Scan(&models, &pms, &binds, &reach); err != nil {
		t.Fatalf("self-check: %v", err)
	}
	if reach != withBinding {
		t.Fatalf("fixture self-check: models=%d provider_models=%d bindings=%d reachable=%d, "+
			"want reachable=%d — the bindings did not survive the prober's admission predicate, "+
			"so every assertion below is measuring a different set than the one intended",
			models, pms, binds, reach, withBinding)
	}
	return pool
}

// A（有地板）与 B（没地板）必须分两个测试：共用一个测试的话，把 WHERE 写成
// 恒假（"永远闭嘴"）只能让 A 红，把 reachable 写成全集只能让 B 红 —— 但如果
// 两个断言在同一个函数里，先失败的那个会 `t.Errorf` 之后继续跑，**两个方向都能
// 在同一次运行里被看到**。所以其实可以共用 —— 这里仍分开，是为了让失败信息
// 直接指向是哪一条承重，而不是让读者去数第几个 Errorf。
func TestModalityGateReadinessFloorIsReportedWhenUnreachableModelsExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 6 个被挡模型，其中 2 个有可探绑定 ⇒ 地板 = 4
	pool := readinessFixture(t, ctx, 2)

	def := healthCheckDef(t, "modality_gate_readiness_floor")

	// 量具自证：被挡集合真的是 6、且 rollup 的分母真的是全表 —— 这两条是
	// 本文件全部推理的起点，错了下面全是空转。
	var blocked int
	if err := pool.QueryRow(ctx, `SELECT models_blocked_by_strict
		FROM public.v_model_modality_verification_rollup`).Scan(&blocked); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if blocked != 6 {
		t.Fatalf("rollup says %d blocked, want 6 — the fixture or the view drifted, and the "+
			"assertions below would be measuring something else", blocked)
	}

	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	var entityID int64
	var name, detail string
	if err := pool.QueryRow(ctx, `SELECT entity_id, entity_name, detail
		FROM public.routing_health_checks
		WHERE check_id='modality_gate_readiness_floor'`).Scan(&entityID, &name, &detail); err != nil {
		t.Fatalf("read the reported row: %v", err)
	}
	// ⚠ 期望串改过一次（2026-10-05），**不要改回去**：初版文案把整个 floor 一律
	// 说成 "can never be verified"，而实测发现 floor 混了两种完全不同的东西 ——
	// 「压根没有 provider_models 行」（结构性死）与「有绑定但当前状态被门挡住」
	// （人为禁用，可恢复）。初版那句文案过强，会让运维去等一个不会来的 0。
	// 本夹具的 4 个地板模型**全部**是结构性的（withBinding=2 ⇒ 另 4 个没有
	// provider_models 行），所以 conditional 那一段必须报 0 —— 这本身也是断言：
	// 两个数相等时不能被凑成一句话糊过去。
	for _, want := range []string{
		"block 6 model(s)",
		"4 can never be verified", // 结构性那一半（withBinding=2 ⇒ 另 4 个无 provider_models 行）
		"a further 0 are unreachable only until credential/provider state changes",
	} {
		if !strings.Contains(name, want) {
			t.Errorf("entity_name=%q does not contain %q — the operator needs the three counts "+
				"(blocked / structural / conditional) to act on this", name, want)
		}
	}
	// ⚠ "driven down is 2" 这条期望是**故意删掉**的：它把「可降下去」与
	// 「够得着」当成同一个数，而两者在 conditional > 0 时不再相等。
	// 下面的新判据 TestModalityGateReadinessFloorSeparatesStructuralFromStateGated
	// 覆盖那个情形。
	for _, want := range []string{
		"UNSATISFIABLE",
		"4 have NO binding",
		"4 of those are not registered with any provider at all",
		"2 probeable binding(s)",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail does not contain %q — without it the row reads as \"we are not ready "+
				"yet\" instead of \"this particular number can never be reached\". detail=%q", want, detail)
		}
	}
	// 汇总行的 entity_id 必须**稳定**：读数变了哈希会变，health 表就会堆历史行。
	if entityID == 0 {
		t.Errorf("entity_id=0 — the no-scan-branch signature")
	}
	// 再跑一轮，确认**不会**因为句子里的计数变了而多出一行。
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("second runChecks: %v", err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='modality_gate_readiness_floor'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d rows after two identical runs, want 1 — the entity must be keyed on "+
			"something stable, not on the sentence that contains the counts", rows)
	}
}

func TestModalityGateReadinessFloorStaysQuietWhenEveryBlockedModelIsReachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 全部 6 个被挡模型都有可探绑定 ⇒ 地板 = 0 ⇒ 「等 0」是可达的，没话说。
	pool := readinessFixture(t, ctx, 6)

	def := healthCheckDef(t, "modality_gate_readiness_floor")
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='modality_gate_readiness_floor'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("the check reported %d row(s) with a floor of 0 — when every blocked model is "+
			"reachable, \"wait for models_blocked_by_strict to reach 0\" is satisfiable and this "+
			"check has nothing to say. Reporting anyway turns it into standing noise.", n)
	}
}

// readinessFixtureGated 在 readinessFixture 之上再加一类模型，它正是 2026-10-05
// 二次实测挖出来的那一类：**有 provider_models 行、有绑定，但进不了 reachable**。
//
// 为什么必须单独造：readinessFixture 的 withBinding 旋钮只能造
// 「有 provider_models 行 ⇒ 一定进 reachable」和「连 provider_models 行都没有」
// 两种，两种都落在 floor 的**同一个**桶里。而真环境里 384 个地板模型中约 150 个
// 是第三种（绑定在、被 manual_disabled / lifecycle / available 挡住）——
// **没有这个夹具，检查里那条区分就是一个没人跑过的分支**。
//
// 用单独一个 provider + 单独一个凭据，而不是给 p1 的凭据加 manual_disabled：
// 后者会把 withBinding 那一批**一起**门掉，两个桶就又混回去了。
func readinessFixtureGated(t *testing.T, ctx context.Context, withBinding int) *pgxpool.Pool {
	t.Helper()
	pool := readinessFixture(t, ctx, withBinding)
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-gated0','text','inferred'), ('m-orphan','text','inferred');
		INSERT INTO public.providers (code, display_name) VALUES ('p2','P2');
		-- 显式写 status/lifecycle：谓词要求它们在 (active,cooling,degraded) / ='active'，
		-- 而这两列是后补的脚手架列，默认 NULL ⇒ 谓词不成立 ⇒ 那就成了"结构性不可达"，
		-- 正好毁掉这个夹具想造的那一类。manual_disabled=TRUE 才是这里唯一的门。
		INSERT INTO public.credentials (provider_id, status, lifecycle_status, manual_disabled)
		SELECT id, 'active', 'active', TRUE FROM public.providers WHERE code='p2';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, 'raw-' || mc.canonical_name, mc.id, mc.canonical_name
		  FROM public.providers pr, public.models_canonical mc
		 WHERE mc.canonical_name IN ('m-gated0','m-orphan') AND pr.code = 'p2';
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		-- ★ 必须把 c 限定到 p2 的凭据。第一版写的是 FROM credentials c,
		-- provider_models pm WHERE pm.canonical_raw_name='m-gated0' ——
		-- 那是**跨连接**，于是给 m-gated0 绑上了 p1（未被禁用）与 p2（被禁用）
		-- 两条绑定，而 reachable 是集合去重 ⇒ m-gated0 **够得着** ⇒
		-- 夹具造出的不是「被门挡」而是「能探到」，整条判据测的是别的东西。
		--
		-- ★★ 且**只**绑 m-gated0：m-orphan 故意不绑。
		-- 它是 2026-10-05 一次变异实测逼出来的样本 —— 把结构性判据从
		-- 「没有 provider_models 行」换成「没有 credential_model_bindings 行」
		-- 时，前三个测试**一个都没红**：因为在没有绑定的模型上，这两个判据
		-- 外延完全相同。m-orphan（登记过 provider_models、但没有任何绑定）正是
		-- 唯一能把二者分开的事件，缺了它，那条定义就没人钉得住。
		--
		-- （上面刻意不写反引号：这段在 Go raw string 里，一个反引号就提前结束
		--  字面量，报出来的是一串莫名其妙的「missing ',' in argument list」。）
		SELECT c.id, pm.id, 1.00, 2.00, 'USD'
		  FROM public.credentials c, public.provider_models pm
		 WHERE pm.canonical_raw_name = 'm-gated0'
		   AND c.provider_id = pm.provider_id;`); err != nil {
		t.Fatalf("seed the state-gated model: %v", err)
	}
	// 量具自证（与 readinessFixture 同一纪律）：两个样本必须各自三样对得上 ——
	// m-gated0 是「有 provider_models + 有绑定、但进不了 reachable」；
	// m-orphan  是「有 provider_models + 没有任何绑定、进不了 reachable」。
	// 少任何一样，下面那条判据就在测另一个集合。
	var gatedPM, gatedBind, gatedReach, orphanPM, orphanBind, orphanReach int
	if err := pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM public.provider_models pm
		          JOIN public.models_canonical mc ON mc.id = pm.canonical_id
		         WHERE mc.canonical_name = 'm-gated0'),
		       (SELECT count(*) FROM public.credential_model_bindings cmb
		          JOIN public.provider_models pm ON pm.id = cmb.provider_model_id
		          JOIN public.models_canonical mc ON mc.id = pm.canonical_id
		         WHERE mc.canonical_name = 'm-gated0'),
		       (SELECT count(DISTINCT pp.canonical_id) FROM (`+modalityVerifyAddressableSource+`) pp
		          JOIN public.models_canonical mc ON mc.id = pp.canonical_id
		         WHERE mc.canonical_name = 'm-gated0'),
		       (SELECT count(*) FROM public.provider_models pm
		          JOIN public.models_canonical mc ON mc.id = pm.canonical_id
		         WHERE mc.canonical_name = 'm-orphan'),
		       (SELECT count(*) FROM public.credential_model_bindings cmb
		          JOIN public.provider_models pm ON pm.id = cmb.provider_model_id
		          JOIN public.models_canonical mc ON mc.id = pm.canonical_id
		         WHERE mc.canonical_name = 'm-orphan'),
		       (SELECT count(DISTINCT pp.canonical_id) FROM (`+modalityVerifyAddressableSource+`) pp
		          JOIN public.models_canonical mc ON mc.id = pp.canonical_id
		         WHERE mc.canonical_name = 'm-orphan')`).
		Scan(&gatedPM, &gatedBind, &gatedReach, &orphanPM, &orphanBind, &orphanReach); err != nil {
		t.Fatalf("gated fixture self-check: %v", err)
	}
	if gatedPM != 1 || gatedBind != 1 || gatedReach != 0 {
		t.Fatalf("gated fixture self-check: m-gated0 provider_models=%d bindings=%d reachable=%d, "+
			"want 1/1/0 — that model must be blocked by STATE, not by having no binding, otherwise "+
			"the assertion below measures the structural case a second time", gatedPM, gatedBind, gatedReach)
	}
	if orphanPM != 1 || orphanBind != 0 || orphanReach != 0 {
		t.Fatalf("gated fixture self-check: m-orphan provider_models=%d bindings=%d reachable=%d, want "+
			"1/0/0 — this is the ONLY sample that separates \"no provider_models row\" from "+
			"\"no binding row\". Without it the two definitions are extensionally equal here and "+
			"assertion below can tell them apart.", orphanPM, orphanBind, orphanReach)
	}
	return pool
}

// 承重 D（2026-10-05 新增）：floor 必须**分两截**报。
//
// 缺了它，检查里 structurally_dead 那个 CTE 就是一段没人跑过的 SQL —— 而它的
// 作用恰恰是纠正一句**过强的断言**（把「当前够不着」说成「永远够不着」）。
// 一条用来纠正措辞的分支，如果不测，它和当初那句错话一样不可信。
func TestModalityGateReadinessFloorSeparatesStructuralFromStateGated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// 8 个被挡模型 = 2 可探 + 4 无 provider_models 行 + 1 孤儿（有 pm 无绑定）
	//                             + 1 被状态门挡（有绑定但 manual_disabled）
	// ⇒ 结构性 5、可恢复 1
	pool := readinessFixtureGated(t, ctx, 2)

	def := healthCheckDef(t, "modality_gate_readiness_floor")
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	var name, detail string
	var blockedN int
	if err := pool.QueryRow(ctx, `SELECT entity_name, detail
		FROM public.routing_health_checks
		WHERE check_id='modality_gate_readiness_floor'`).Scan(&name, &detail); err != nil {
		t.Fatalf("read the reported row: %v", err)
	}
	for _, want := range []string{
		"block 8 model(s)",
		"5 can never be verified",                // 无绑定：4 个无 pm 行 + 1 个孤儿（有 pm 无绑定）
		"a further 1 are unreachable only until", // 可恢复：有绑定但被 manual_disabled
	} {
		if !strings.Contains(name, want) {
			t.Errorf("entity_name=%q does not contain %q", name, want)
		}
	}
	// detail 必须把两半的**处置**说成两件事，否则"1 can be recovered"只是一个数字，
	// 运维不知道该去改禁用开关还是该去接供应商。
	// ★ 孤儿模型（m-orphan：有 provider_models 行、无绑定）必须落在**无绑定**那一桶。
	// 这是 2026-10-05 实测改正的判据：第一版按 provider_models 分桶，它被算成
	// 「可恢复」，而那个模型根本没有开关可改 —— 报成可恢复会诱导运维去解禁用。
	for _, want := range []string{
		"4 of those are not registered with any provider at all",
		"onboard a supplier",
		"1 are registered but unbound",
		"create the binding",
		"DO have bindings",
		"deliberate operator decision",
	} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail does not contain %q — the two halves need different remedies "+
				"(onboard a supplier vs re-enable a credential). detail=%q", want, detail)
		}
	}
	// ★ 阴性对照：若 detail 里**只剩**一个地板数，措辞就退化回初版那句过强的
	// 「can never be verified」，而这正是本判据存在的理由。
	// 判据形态刻意写成「两个数都必须出现且不相等」：光断言字符串存在的话，
	// 把两个数写成同一个（t.dead_n 与 t.floor_n 都取 4）照样能过。
	deadN, condN := 0, 0
	if _, err := fmt.Sscanf(name, "(readiness) strict gate would block %d model(s); %d can never be verified, and a further %d are unreachable", &blockedN, &deadN, &condN); err != nil {
		t.Fatalf("cannot parse the three counts out of %q: %v", name, err)
	}
	if blockedN != 8 || deadN != 5 || condN != 1 {
		t.Errorf("counts parsed out of entity_name are blocked=%d dead=%d conditional=%d, want 8/5/1 — "+
			"if this drifts without the assertion noticing, the two halves have merged back into the "+
			"single over-strong number this check exists to correct", blockedN, deadN, condN)
	}
	if deadN == condN {
		t.Errorf("dead=%d equals conditional=%d — the two halves must be distinguishable, otherwise "+
			"the message is one number wearing two labels", deadN, condN)
	}
}

// stripSQLLineComments 去掉 `--` 起到行尾的内容，返回**新的**字符串。
//
// 为什么需要（2026-10-05 被真事件打出来的）：承重 C 原本直接对查询文本做
// substring 查 `credential_model_bindings`。而 2026-10-05 给检查加
// `structurally_dead` 那个 CTE 时，我在它的**注释**里写了一句
// 「判据用 provider_models 而不是 credential_model_bindings」——
// 于是判据红了，而那条 SQL **不是**第二条谓词，它是一句散文。
//
// 这就是「分不清散文与载荷的判据不是判据」：一个只查字面量的判据，
// 既会被注释误伤，也**分不清**「抄了第二份谓词」和「解释为什么不抄」。
// 剥掉行注释之后，查的才是载荷。
//
// ⚠ 用的是 `credential_selfcheck_pick_test.go` 里**已有的**那个
// stripSQLLineComments（它会避开引号内的 `--`），不是就地再写一个：
// 我第一版在本格子里新写了一个朴素版，go vet 直接报 redeclared。
// 仓里已经有对的实现时，重复定义不是「更独立」，是**更差** —— 朴素版会把
// `'a--b'` 这样的字面量从中间切掉。

// TestModalityGateReadinessFloorUsesTheProbersOwnPredicate 钉 C：reachable 集合
// 必须与 dueTargets 用**同一份**谓词。
//
// 形态说明（这一条本身就是从一次假红学来的）：常量在**编译期**就被插进 Query
// 字符串，所以 `def.Query` 里必然看得到 `credential_model_bindings` 字面量 ——
// 查「有没有这个字面量」会永远红，那是**错的判据**，我第一版就是这么写的。
//
// 正确的形状：把共享常量从查询里**抠掉**，剩下的**载荷**（剥掉 `--` 注释之后）
// 不得再出现那张表。
// ⇒ 「唯独允许出现它的地方是那份共享常量」被真正钉住：手抄第二份谓词立刻红，
// 正常引用常量不会，而在注释里解释设计不会。
func TestModalityGateReadinessFloorUsesTheProbersOwnPredicate(t *testing.T) {
	// ★ 阳性对照：先证明剥除器本身有牙，否则下面那条「没查到」可能是
	// 「剥得太狠，把整条查询都删空了」。用一段与生产无关的固定输入验。
	probe := "SELECT 1 -- 这里提到 credential_model_bindings\nFROM t"
	if got := stripSQLLineComments(probe); strings.Contains(got, "credential_model_bindings") {
		t.Fatalf("stripSQLLineComments did not remove a line comment: %q", got)
	} else if !strings.Contains(got, "SELECT 1") || !strings.Contains(got, "FROM t") {
		t.Fatalf("stripSQLLineComments removed payload too: %q", got)
	}

	def := healthCheckDef(t, "modality_gate_readiness_floor")
	rest := strings.Replace(def.Query, modalityVerifyAddressableSource, "", 1)
	if rest == def.Query {
		t.Fatal("the check does NOT embed the shared addressable source at all, so the floor it " +
			"reports comes from a set of its own")
	}
	// ⚠ 2026-10-05 第三次改这条判据的**范围**。
	// 「整条查询里不得出现 credential_model_bindings」是**过宽**的：分桶用的
	// no_binding / unregistered 两个 CTE 合法地要查那张表（它们问的是另一个
	// 问题：有没有绑定），而把 reachable 单独抠出来才问对了问题。
	// 第一版因为太宽，被我自己写在 no_binding 注释里的一句话判红。
	//
	// 正确的形状：**reachable AS ( ... ) 那一段**必须内嵌共享常量；
	// 抠掉整段常量后，可达集合的来源就必须不存在。
	start := strings.Index(def.Query, "reachable AS (")
	if start < 0 {
		t.Fatal("the check no longer has a reachable CTE — this assertion pins that CTE by name, " +
			"so renaming it is a deliberate edit to this file, not something to discover here")
	}
	restOfQuery := def.Query[start:]
	end := strings.Index(restOfQuery, "\n), ")
	if end < 0 {
		t.Fatal("could not find the end of the reachable CTE (expected a line starting with ), )")
	}
	reachableCTE := restOfQuery[:end]
	if !strings.Contains(reachableCTE, modalityVerifyAddressableSource) {
		t.Error("the reachable CTE does NOT embed the shared addressable source — the floor this " +
			"check reports is then computed over a hand-maintained second predicate, and the two " +
			"will drift; when they do, the floor belongs to a different set than the prober walks")
	}
	// 量具自证：抠掉之后**确实**少了一大段（否则上面可能靠空串蒙混过关）。
	payload := stripSQLLineComments(rest)
	if len(payload) == 0 {
		t.Error("after removing the shared source and stripping comments the payload is empty — " +
			"the assertions above would then pass for any query at all, including one that never ran")
	}
	// 量具自证：抠掉之后**确实**少了一大段（否则上面那条可能靠空串蒙混过关）。
	if len(rest) >= len(def.Query) {
		t.Errorf("removing the shared source did not shrink the query (%d -> %d bytes)",
			len(def.Query), len(rest))
	}
	// 剥完不能把查询清空 —— payload 仍须保有实质内容，否则上面是恒真通过。
	if strings.TrimSpace(payload) == "" {
		t.Error("after stripping comments the remaining payload is empty — the check above would " +
			"then pass for any query at all, including one that never ran")
	}
}
