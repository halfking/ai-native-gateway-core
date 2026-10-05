package provider

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// realModelsCanonical 是**从仓自己的逐对象 SSOT 拼出来的**真实表，不是手抄的
// 最小替身。四个来源：
//
//	sql/objects/tables/models_canonical.sql
//	sql/objects/sequences/models_canonical_id.sql                （id 的 DEFAULT）
//	sql/objects/constraints/models_canonical_..._canonical_name_key.sql
//	序列本身（内联 CREATE SEQUENCE，没有值得漂移的形状）
//
// ★ 为什么不能手抄一个「能让迁移跑起来」的最小替身：
//
//	这一版判据当初手写的是
//	    CREATE TABLE public.models_canonical (id bigserial PRIMARY KEY, …)
//	于是它**比真实表更宽松**（真表的 id 上没有主键、没有唯一约束、没有索引）。
//	结果是 825 的外键
//	    canonical_id bigint REFERENCES public.models_canonical(id)
//	根本建不出来这件事，在判据里被完美地藏住了——夹具自己补上了真表缺的
//	那个键。判据全绿，迁移在**任何真实环境上都装不上**（真 schema 实测：
//	"there is no unique constraint matching given keys for referenced table"）。
//
//	⇒ 手抄夹具时，抄的应该是「生产里实际长什么样」，不是「我希望它长什么样」。
//	能从仓里推导就别手抄：推导出���夹具会随真表一起变，而手抄的会静静烂掉。
//	推导逻辑见 internal/schemaobj；bg/supplier_view_cardinality_test.go 共用它，
//	避免同一个概念出现两份写法。
//
// 同一条纪律的第二半：**不要替被测迁移手写它该建的东西**。原先这里手写了
// 一份 825 的 model_modality_verification 当替身，于是 825 的 DDL 从来没被
// 执行过。现在改成直接应用真的 825 与真的 827。
func realModelsCanonical(t *testing.T) string {
	t.Helper()
	return "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)
}

// gateAlignmentCases 覆盖缺省闸门与严格闸门**每一个会分歧**的组合。
//
//	never-probed       零条证据：缺省不排除、严格排除
//	confirmed          有一条确认：两档都不排除
//	all-negative       全负：两档都排除
//	negative+unknown   ★ 缺省**不**排除（unknown 不是反证），严格排除
//	negative+confirmed 有一条确认：两档都不排除
//
// 第四行是唯一能把「漏掉 read_unknown = 0」这个漂移抓出来的用例。
var gateAlignmentCases = []struct {
	name string
	rows []string // 每个元素是 "credential_id:read_level"
}{
	{"never-probed", nil},
	{"confirmed", []string{"1:confirmed"}},
	{"all-negative", []string{"1:negative", "2:negative"}},
	{"negative+unknown", []string{"1:negative", "2:unknown"}},
	{"negative+confirmed", []string{"1:negative", "2:confirmed"}},
}

// TestModalityGateMatchesProgressViewSemantics 是「闸门与进度视图逐条对齐」的判据。
//
// 背景：路由侧有两档多模态闸门（modalityVerdictGateSQL），由
// LLM_GATEWAY_MODALITY_ROUTING_STRICT 控制；迁移 827 建了
// v_model_modality_verification_progress 用来回答「现在开严格档会挡掉多少个
// 模型」。两者的判据**必须逐条一致**，否则灰度的人看着视图说「只剩 2 个挡着
// 了」，开上去却挡了 40 个。
//
// 这条判据存在的理由是一次真实漂移：视图第一版把缺省档写成
//
//	read_negative > 0 AND read_confirmed = 0
//
// 而闸门是
//
//	EXISTS(negative) AND NOT EXISTS(任何非负的行)
//
// 差的是第三项 `read_unknown = 0`。后果是「一负 + 一 unknown」被判成「缺省档
// 会排除」——而闸门恰恰**不**排除它，因为 unknown 意味着「还不能判定」，而
// 不能判定不构成「这个模型看不见」的证据。真库上跑同一批数据立刻打脸。
//
// 读两边然后各自点头不构成证据。这条测试让**两个谓词在同一批数据上跑出同一个
// 值**，是对齐这件事唯一站得住的证明。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过。CI/运维现场有库就会真的跑。
func TestModalityGateMatchesProgressViewSemantics(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — the gate/view alignment check needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// **安全闸**：这两张表在真部署里是 825 建的生产表，而本测试要 DROP 它们。
	// 没装 825 的空库上它们不存在，才是本测试可以动手的前提。
	// 宁可跳过，也绝不在有生产 schema 的库上跑。
	var existing int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public'
		   AND table_name IN ('models_canonical','model_modality_verification')`).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("public.models_canonical / model_modality_verification already exist (%d) — "+
			"this test drops them, so it only runs on a database without migration 825 applied",
			existing)
	}

	viewSQL := readMigration(t, "827_modality_verification_progress_view.sql")
	modalitySQL := readMigration(t, "825_modality_graded_verification.sql")

	setup := func() {
		if _, err := pool.Exec(ctx, realModelsCanonical(t)); err != nil {
			t.Fatalf("create models_canonical from repo SSOT: %v", err)
		}
		// **夹具是否已过期的告警**（不是断言真表该缺约束，而是断言「这份
		// 夹具还代表真表」）。真表若哪天补上了主键，这里会红并说清该改
		// 什么。宁可响亮：夹具悄悄变宽松过一次，代价是迁移在真环境上装不上
		// 而判据全绿。
		var uniq int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_constraint
			 WHERE conrelid = 'public.models_canonical'::regclass
			   AND contype IN ('p','u')
			   AND conkey = ARRAY[(SELECT attnum FROM pg_attribute
			                        WHERE attrelid = 'public.models_canonical'::regclass
			                          AND attname = 'id')]::smallint[]`).Scan(&uniq); err != nil {
			t.Fatalf("probe models_canonical keys: %v", err)
		}
		if uniq > 0 {
			t.Fatalf("models_canonical.id now has a unique constraint (%d) — the repo SSOT and "+
				"this fixture are out of date. Re-derive realModelsCanonical from "+
				"sql/objects/ before changing anything else here", uniq)
		}
		if _, err := pool.Exec(ctx, modalitySQL); err != nil {
			t.Fatalf("apply 825: %v", err)
		}
		if _, err := pool.Exec(ctx, viewSQL); err != nil {
			t.Fatalf("apply 827 view: %v", err)
		}
	}
	teardown := func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verification_rollup;
			DROP VIEW IF EXISTS public.v_model_modality_verification_progress;
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}
	// ★ 顺序要紧：**先注册清理，再建表**。
	//
	// 写成 `setup(); defer teardown()` 时，setup() 里的任何 t.Fatalf 都会
	// longjmp 出去，defer 永远不注册 ⇒ 桩表留在库里 ⇒ 下一轮的安全闸看到
	// 「表已存在」直接 SKIP ⇒ 人看到的是「通过」，实际是**一次都没跑**。
	// 这个坑本会话已经踩过两次（一次在 bg 的偏差判据、一次在这里），
	// 两次都是「建表成功、defer 没注册」留下的残桩。
	//
	// 先 teardown() 一次还顺带清了上一次崩溃留下的残桩——否则改一次夹具
	// 再跑，就被自己上一次的红挡住，且报的是「表已存在」这种驴唇不对马嘴
	// 的错。
	teardown()
	defer teardown()
	setup()

	for _, tc := range gateAlignmentCases {
		t.Run(tc.name, func(t *testing.T) {
			teardown()
			setup()

			if _, err := pool.Exec(ctx,
				`INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
				 VALUES ($1, 'vision', 'semantic')`, tc.name); err != nil {
				t.Fatalf("seed model: %v", err)
			}
			for _, r := range tc.rows {
				parts := strings.SplitN(r, ":", 2)
				// canonical_id **必须**在这里显式写出来。
				//
				// ★ 漏掉它时，证据行的 canonical_id 是 NULL，视图的
				//   LEFT JOIN ... ON a.canonical_id = p.canonical_id
				// 永远匹配不上，于是每个用例都读到
				//   evidence_rows=0 / verdict=unknown / excl_def=false / excl_str=true
				// ——一个**常量**。而闸门侧在「缺省不排除」的用例上也恒为 false，
				// 两边恒等 ⇒ 测试全绿，且**它什么都验不到**。
				//
				// 也就是说：这条判据曾经是恒真的，而我差点据此宣布
				// 「视图漏掉 read_unknown 这条漂移测不出来」。
				// 真凶是量具自己，不是被测对象 —— 与真库手测的结果不一致时，
				// 先怀疑量具。
				if _, err := pool.Exec(ctx, `
					INSERT INTO public.model_modality_verification
						(canonical_id, canonical_name, credential_id, raw_model_name, modality, carry_level, read_level)
					SELECT id, canonical_name, $1::int, canonical_name, 'vision', 'accepted', $2
					  FROM public.models_canonical WHERE canonical_name = $3`,
					parts[0], parts[1], tc.name); err != nil {
					t.Fatalf("seed evidence %s: %v", r, err)
				}
			}

			// 闸门侧：把 modalityVerdictGateSQL 的片段接到一条最小 SELECT 的
			// WHERE 后面，取「该行是否通过闸门」。片段本身是 `AND NOT (...)`
			// 且只引用 mc.id 与 $3，所以这样包是等价的 —— 表别名必须叫 mc，
			// 且必须绑三个参数（$3 被片段自己用掉）。
			// 闸门「排除」= 行不通过。
			passes := func(strict bool) bool {
				q := `SELECT mc.id FROM public.models_canonical mc
				       WHERE mc.canonical_name = $1 AND mc.modality = $2 AND true` +
					modalityVerdictGateSQL(strict)
				var id int64
				return pool.QueryRow(ctx, q, tc.name, "vision", "vision").Scan(&id) == nil
			}
			gateDefault := !passes(false)
			gateStrict := !passes(true)

			// **量具自证**：视图必须真的看到了这批证据。
			// 少了这一条，种子写错（canonical_id 为 NULL、模态写错、表建到别的
			// schema）会让视图对每个用例都返回同一组常量，而闸门侧在部分用例上
			// 恰好也是同一个值 ⇒ 测试恒真且毫无察觉。
			var viewRows int
			var viewDefaultI, viewStrictI int
			if err := pool.QueryRow(ctx, `
				SELECT evidence_rows,
				       CASE WHEN excluded_by_default_gate THEN 1 ELSE 0 END,
				       CASE WHEN excluded_by_strict_gate  THEN 1 ELSE 0 END
				  FROM public.v_model_modality_verification_progress
				 WHERE canonical_name = $1 AND modality = 'vision'`, tc.name).
				Scan(&viewRows, &viewDefaultI, &viewStrictI); err != nil {
				t.Fatalf("read view: %v", err)
			}
			if viewRows != len(tc.rows) {
				t.Fatalf("the view sees %d evidence rows but the case seeded %d — "+
					"the fixture did not reach the view, so this comparison is vacuous",
					viewRows, len(tc.rows))
			}
			viewDefault := viewDefaultI == 1
			viewStrict := viewStrictI == 1

			if gateDefault != viewDefault {
				t.Errorf("default gate DRIFT: the routing gate excludes=%v but the progress view says %v",
					gateDefault, viewDefault)
			}
			if gateStrict != viewStrict {
				t.Errorf("strict gate DRIFT: the routing gate excludes=%v but the progress view says %v",
					gateStrict, viewStrict)
			}
		})
	}
}

func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../sql/migrations/startup/" + name)
	if err != nil {
		t.Fatalf("read migration %s: %v", name, err)
	}
	return string(b)
}
