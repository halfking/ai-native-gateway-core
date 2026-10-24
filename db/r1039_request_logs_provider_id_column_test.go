package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// r1039_request_logs_provider_id_column_test.go —— 2026-10-21 立的门，
// **2026-10-24「v1 → session_turns 替换轮」把它的承重判据换了主人**。
//
// # 原缺陷（真库实测，不是读代码猜的）
//
// db.go 的 ensureRequestLogSchema 在 _request_logs 上建两个带 provider_id 的索引：
//
//	CREATE INDEX idx_request_logs_provider_quality
//	    ON _request_logs (provider_id, quality_score, ts DESC)
//	CREATE INDEX idx_request_logs_provider_tool_calls
//	    ON _request_logs (provider_id, ts DESC)
//
// 但 `_request_logs` 的 provider_id 列**从未被 ensure 链补过** ⇒ 任何不带该列的
// 库上，整条 boot 迁移在第一个索引处报 42703 ⇒ 迁移中止 ⇒ 网关起不来。
//
// # 原判据的形状（这里要记住的是**形状**，不是 v1 本身）
//
//	「一个 DDL 引用了某列 ⇒ 那个 DDL 所在的那一层必须保证这一列存在」
//
// 这条教训是**普适**的：它说的是「索引 / 视图引用的列，必须由它自己所在的
// 那一层负责补齐」，而不是「v1 要补 provider_id」。
//
// # 2026-10-24：v1 族退役 ⇒ 这条链删了 ⇒ 承重判据换了主人
//
//	旧主人：boot ensure 链（ALTER … ADD COLUMN）给 v1 族补列
//	新主人：**启动迁移链 + canonical 视图的依赖跟踪**
//
//	· 视图引用 session_turn_details.provider_id ⇒ PG 拒绝 DROP 该列
//	  （cannot drop column because other objects depend on it）
//	  ⇒ 视图存在 ⇒ 它引用的列必然在。
//	· 列不在 ⇒ CREATE VIEW 阶段就 42703 ⇒ boot **响亮失败**。
//
// ⇒ 本文件现在钉的是「**换主人之后那条链是完整的**」：
//
//	① 迁移链必须真的建出那些列（session_family_contract_test.go 是总门；
//	   本文件把 r1039 当年那两列 provider_id / credits_charged 单独点名）。
//	② 生产代码里不得再有 v1 DDL。
//
// # 保留的两条真库判据（不受本轮影响，仍然承重）
//
//	· TestR1039RecentSuccessRateEnsureRealDB —— recent_success_rate 的函数体
//	  早已只读 session_turns_hot；判的是「族退役形态下 CREATE 必须成功」。
//	· TestR1039BillingUnitEnsureSurvivesRetiredFamily —— 848 的 cmb 侧
//	  必须真的补上列（不许为了躲 42P01 而整段跳过）。

// TestR1039ProviderIDAndCreditsLiveInTheSessionFamily 钉住 r1039 当年那两列
// 的**新宿主**：它们必须由启动迁移链建出来。
//
// ⚠ 为什么点名这两列：它们是 r1039 教训的原始载体。列在 v1 上时，
//
//	「谁保证它存在」是 ensure 链；列搬到会话族后，保证者必须是迁移链。
//
// ⚠ 两列的**宿主不同**（实测投影文本）：
//
//		provider_id     → d.provider_id     = public.session_turn_details
//		credits_charged → t.credits_charged = public.session_turns
//
//	  搞混宿主会让这道门断言一个「其实存在」的列 ⇒ 恒绿 ⇒ 失去意义。
func TestR1039ProviderIDAndCreditsLiveInTheSessionFamily(t *testing.T) {
	root := repoRootForTest(t)

	createdDetails := migrationCreatedColumns(t, root, "session_turn_details")
	if !createdDetails["provider_id"] {
		t.Errorf("session_turn_details.provider_id 必须由基线/启动迁移建出来 —— " +
			"它是 r1039 当年教训的载体（索引引用了列，而列由别的层负责），" +
			"搬到会话族后保证者必须是迁移链")
	}

	createdTurns := migrationCreatedColumns(t, root, "session_turns")
	if !createdTurns["credits_charged"] {
		t.Errorf("session_turns.credits_charged 必须由基线/启动迁移建出来 —— " +
			"它是计费口径的直接来源（canonical 视图投影 t.credits_charged）")
	}

	// 反向：这两列必须在 canonical 投影里被真的用上，
	// 否则「建了但没人读」是另一种漂移。
	detailCols := projectedDetailsColumns(t)
	if !detailCols["provider_id"] {
		t.Errorf("canonical 投影必须引用 session_turn_details.provider_id —— " +
			"它被读面直接消费（provider 维度）")
	}
	turnCols := projectedTurnColumns(t)
	if !turnCols["credits_charged"] {
		t.Errorf("canonical 投影必须引用 session_turns.credits_charged —— " +
			"它被计费口径直接消费（maas.RequestLogCreditsSQL）")
	}
}

// TestR1039NoV1DDLInStartupPath 钉住「v1 那条 ensure 链确实没了」。
//
// ⚠ 为什么这条要单独写而不是只靠 admin 那道总门：总门扫的是**全仓生产代码**
//
//	（字符串字面量口径），而这里钉的是**具体这个文件**——
//	db.go 是 boot 迁移链的所在地，它曾经是 v1 DDL 的**唯一**集中点。
//	⇒ 两条门一道扫全仓、一道钉原地，双保险。
func TestR1039NoV1DDLInStartupPath(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	text := string(src)

	for _, gone := range []string{
		"func (d *DB) ensureRequestLogSchema(",
		"ALTER TABLE _request_logs",
		"ALTER TABLE _request_logs_hot",
		"CREATE INDEX IF NOT EXISTS idx_request_logs_",
		"workTypeRequestLogsDDL",
		"workTypeRequestLogsCurrent",
		"maasRequestLogsDDL",
		"maasRequestLogsCurrent",
	} {
		if strings.Contains(text, gone) {
			t.Errorf("db.go 里仍有 %q —— v1 族已随 890 退役；这些是 boot 路径上对 v1 的"+
				"最后几处操作（每次 boot 对最热表取 ACCESS EXCLUSIVE，且在族不在时 42P01）", gone)
		}
	}

	// ⚠ 判据的**方向**说明：这里断的是「**定义**与 **DDL**」，不是「这个词不许出现」。
	//   db.go 里保留着「这里原来有一条 v1 ensure 链」的注释 —— 那是**承重证据**
	//   （下一个读代码的人要知道这一跳去哪了）。把注释也清掉，等于把这段历史
	//   从代码里删掉 ⇒ 半年后有人会把同样的链再加回来。
	//   ⇒ 「不许出现」只对**可执行的东西**成立。

	// 反向判据：ensureRequestLogsCurrentMonthView（canonical 视图自愈）**必须还在**
	// —— 它是 v1 自愈链删掉之后，接管「读面保证」的那一层。
	if !strings.Contains(text, "ensureRequestLogsCurrentMonthView(migCtx)") {
		t.Error("ensureRequestLogsCurrentMonthView 必须仍然挂在启动序列上 —— " +
			"它是 v1 族退役后 canonical 读面的唯一自愈机制")
	}
}

// TestR1039RecentSuccessRateEnsureRealDB 是 **recent_success_rate 函数体**的
// 真库层判据（2026-10-21 r1039 续轮）。
//
// ## 为什么源码级判据不够
//
// recent_success_rate 是 **LANGUAGE sql** ⇒ PostgreSQL 在 CREATE 时就**校验体里
// 的表引用** ⇒ 体里写着一个不存在的表 ⇒ 42P01 ⇒ 整条 boot 迁移中止。
// 源码级判据（读函数返回的字符串）证明不了「PG 到底认不认这个表」——
// 只有真库 CREATE 才算数。
//
// ## 覆盖的两种部署形态
//
//   - 族退役（无 _request_logs_hot）：函数体必须只留 session_turns_hot 一条腿，
//     且 CREATE 必须成功（本机 2026-10-21 新装实测：修复前此处 42P01 中止）。
//   - 旧族仍在（_request_logs_hot 存在）：函数体必须**双腿都在**（旧语义不许丢）。
//
// 判据同层：驱动的是 boot 链上真正调用的 db.ensureRoutingRecentSuccessRate，
// 断言的是 PostgreSQL 里 pg_proc.prosrc 的**实际函数体**（不是另抄一份）。
func TestR1039RecentSuccessRateEnsureRealDB(t *testing.T) {
	pool := scratchPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	d := &DB{pool: pool}

	exec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, q)
		}
	}
	// functionBody 读回 PG 里**实际**的函数体（prosrc），不是测试里另抄的。
	functionBody := func() string {
		t.Helper()
		var src string
		if err := pool.QueryRow(ctx, `
			SELECT prosrc FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname='public' AND p.proname='recent_success_rate'`,
		).Scan(&src); err != nil {
			t.Fatalf("read recent_success_rate body: %v", err)
		}
		return src
	}

	// 夹具：ensureRoutingRecentSuccessRate 的 backfill UPDATE 需要这三张表，
	// 函数体需要 session_turns_hot（CREATE 时校验表引用 ⇒ 表必须在场）。
	exec(`DROP TABLE IF EXISTS public._request_logs_hot, public._request_logs CASCADE;`)
	exec(`CREATE TABLE IF NOT EXISTS public.session_turns_hot (
		id BIGSERIAL PRIMARY KEY, credential_id TEXT, model TEXT, canonical_model TEXT,
		success BOOLEAN, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
		task_type TEXT, origin_stage TEXT, request_id TEXT);`)
	exec(`CREATE TABLE IF NOT EXISTS public.credential_model_bindings (
		credential_id BIGINT, provider_model_id BIGINT, available BOOLEAN DEFAULT TRUE,
		unavailable_reason TEXT, unavailable_at TIMESTAMPTZ, admin_protected BOOLEAN DEFAULT FALSE);`)
	exec(`CREATE TABLE IF NOT EXISTS public.provider_models (
		id BIGSERIAL PRIMARY KEY, raw_model_name TEXT);`)
	exec(`CREATE TABLE IF NOT EXISTS public.model_probe_state (
		credential_id BIGINT, raw_model_name TEXT, state TEXT);`)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP FUNCTION IF EXISTS public.recent_success_rate(bigint, text, int, int);`)
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS public._request_logs_hot, public._request_logs CASCADE;`)
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS public.session_turns_hot, public.model_probe_state, public.provider_models, public.credential_model_bindings CASCADE;`)
	})

	t.Run("族退役_只留会话腿且CREATE必须成功", func(t *testing.T) {
		exec(`DROP TABLE IF EXISTS public._request_logs_hot;`)
		if err := d.ensureRoutingRecentSuccessRate(ctx); err != nil {
			t.Fatalf("族退役（无 _request_logs_hot）时 ensure 必须成功（42P01 会中止整条 boot 迁移），got: %v", err)
		}
		body := functionBody()
		if strings.Contains(body, "_request_logs_hot") {
			t.Error("族退役形态的函数体仍引用 _request_logs_hot：CREATE 时校验表引用即 42P01")
		}
		if !strings.Contains(body, "session_turns_hot") {
			t.Error("族退役形态的函数体必须保留 session_turns_hot（停写后新流量唯一落点）")
		}
		if strings.Contains(body, "UNION ALL") {
			t.Error("族退役形态只剩一条腿，UNION ALL 会变成悬空语法错")
		}
	})

	t.Run("旧族仍在_仍只留会话腿_族已退役读面不依赖它", func(t *testing.T) {
		// ⚠ 夹具的 credential_id 必须是 **bigint**：v1 request_logs 的真实列型
		// （installer/.../01-schema.sql: credential_id bigint）就是 bigint；
		// 旧腿写的是 `credential_id = p_credential_id`（p_credential_id 是 BIGINT），
		// 拿 TEXT 当夹具 ⇒ PG 报 42883 operator does not exist: text = bigint
		// ——那是**夹具的锅**，不是 ensure 的。
		// （会话腿那侧是 TEXT —— 707 S1a —— 所以它才有 `~ '^[0-9]+$'` 数字守卫。）
		exec(`CREATE TABLE IF NOT EXISTS public._request_logs_hot (
			id BIGSERIAL PRIMARY KEY, credential_id BIGINT, outbound_model TEXT, client_model TEXT,
			success BOOLEAN, ts TIMESTAMPTZ NOT NULL DEFAULT now(),
			task_type TEXT, origin_stage TEXT, request_id TEXT);`)
		if err := d.ensureRoutingRecentSuccessRate(ctx); err != nil {
			t.Fatalf("旧族仍在时 ensure 必须成功，got: %v", err)
		}
		body := functionBody()
		// 族已退役（860 改名 → 890 删除）⇒ 读面**不许**再依赖它：
		// 体里写 _request_logs_hot 的话，在族退役库上 CREATE 时校验表引用
		// ⇒ 42P01 ⇒ 整条 boot 迁移中止（2026-10-21 本机实测）。
		// ⇒ 哪怕物理表还在（211/252 的遗留形态），体里也**只**许有会话腿。
		if strings.Contains(body, "_request_logs_hot") {
			t.Error("recent_success_rate 体里仍引用 _request_logs_hot：族已退役，读面不得依赖它（退役库上 CREATE 即 42P01）")
		}
		if !strings.Contains(body, "session_turns_hot") {
			t.Error("会话腿 session_turns_hot 必须在场（停写后新流量唯一落点）")
		}
		if strings.Contains(body, "UNION ALL") {
			t.Error("只剩一条腿时 UNION ALL 会变成悬空语法错")
		}
	})
}

// TestR1039BillingUnitEnsureSurvivesRetiredFamily 是 **848 ensure** 的真库判据。
//
// ★ 2026-10-24：848 的 v1 腿（audio_seconds）已整条删除 ⇒ 本判据现在钉的是
//
//	「cmb 侧该补的列**必须真的补上**」——不许为了躲 42P01 而整段跳过。
func TestR1039BillingUnitEnsureSurvivesRetiredFamily(t *testing.T) {
	pool := scratchPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	d := &DB{pool: pool}

	exec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("exec failed: %v\nSQL: %s", err, q)
		}
	}

	// 夹具：族**不在** + cmb 缺 billing_unit。
	exec(`DROP TABLE IF EXISTS public._request_logs_hot, public._request_logs CASCADE;`)
	// 848 ensure 末尾会往 schema_migrations 记账 ⇒ 该表必须在场。
	exec(`CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version TEXT PRIMARY KEY, description TEXT);`)
	exec(`CREATE TABLE IF NOT EXISTS public.credential_model_bindings (
		credential_id BIGINT, provider_model_id BIGINT, available BOOLEAN DEFAULT TRUE,
		unavailable_reason TEXT, unavailable_at TIMESTAMPTZ, admin_protected BOOLEAN DEFAULT FALSE);`)
	exec(`ALTER TABLE public.credential_model_bindings DROP COLUMN IF EXISTS billing_unit;`)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS public.credential_model_bindings CASCADE;`)
	})

	if err := d.ensureBillingUnitAndAudioSeconds(ctx); err != nil {
		t.Fatalf("族已退役 + cmb 缺 billing_unit 时 ensure 必须成功（42P01 会中止整条 boot 迁移），got: %v", err)
	}
	// cmb 侧该补的列必须真的补上了（不许为了躲 42P01 而整段跳过）。
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema='public' AND table_name='credential_model_bindings'
		  AND column_name='billing_unit'`).Scan(&n); err != nil {
		t.Fatalf("probe cmb.billing_unit: %v", err)
	}
	if n == 0 {
		t.Error("ensure 之后仍没有 credential_model_bindings.billing_unit ⇒ cmb 侧没补上")
	}
	// 幂等：第二遍必须也不许报错。
	if err := d.ensureBillingUnitAndAudioSeconds(ctx); err != nil {
		t.Fatalf("ensure 第二遍必须幂等成功，got: %v", err)
	}
}
