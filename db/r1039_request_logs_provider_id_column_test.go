package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// r1039_request_logs_provider_id_column_test.go —— boot ensure 链**自己索引的
// 那一列**必须由它自己补上（2026-10-21 deploy-local 中止实测）。
//
// ## 缺陷（真库实测，不是读代码猜的）
//
// db.go 的 ensureRequestLogSchema 在 _request_logs 上建两个带 provider_id 的索引：
//
//	CREATE INDEX idx_request_logs_provider_quality
//	    ON _request_logs (provider_id, quality_score, ts DESC)
//	CREATE INDEX idx_request_logs_provider_tool_calls
//	    ON _request_logs (provider_id, ts DESC)
//
// 这两条是 2026-06-19（9cd51d8a7「per-provider tool_call quality fix mode」）
// 引入的，当时目标表是 v1 的 request_logs；v2 改名（860：request_logs →
// _request_logs）把它们**一起搬到了新表名**上。
//
// 但 `_request_logs` 的 provider_id 列**从未被 ensure 链补过** ⇒
// 任何不带该列的库上，整条 boot 迁移在第一个索引处报 42703：
//
//	ERROR: column "provider_id" does not exist (SQLSTATE 42703)
//	[deploy-local] error: database migration failed
//
// ⇒ 迁移中止 ⇒ **网关起不来**（2026-10-21 本机 deploy-local 实测中止）。
// 已是 v1→v2 改名产物的库（211/252 等）本来就有这一列 ⇒ 那边看不见这个洞。
//
// ## 判据纪律
//
//   - **真库判据是承重的**：造一张**没有** provider_id 的 _request_logs，
//     跑 ensure 链必须不报错，且列与两个索引都得建出来。
//   - **源码顺序判据是辅助的**：ADD COLUMN 必须在两个 CREATE INDEX 之前。
//     顺序错了真库判据也会红，但它给出的失败信息更直接。
//   - **反向对照**：已经带 provider_id 的库跑 ensure 必须仍然是 no-op 且不报错。
//   - 「检查没跑成」与「检查通过」必须长得不一样 ⇒ 真库判据缺环境时**显式
//     说明本次没跑**（不是安静地跳过）。
//
// 真库判据**自带一次性库**（R78 轮改造）：
//
//	TEST_DATABASE_URL='postgres://…/<任意库>?sslmode=disable' go test ./db/ -run TestR1039
//
// 门会从该 DSN **派生一个一次性空库**（internal/testdb.Create），
// 所有 DROP/CREATE 都发生在那个一次性库上 ⇒ DSN 指向的那个库**分毫不动**。
// ⚠ 改造前（本文件直连 DSN 库）这三个门会把产品表
// `_request_logs` / `_request_logs_hot` / `session_turns_hot` /
// `model_probe_state` / `provider_models` / `credential_model_bindings`
// DROP 掉并用玩具形态重建 ⇒ 跑一次 db 包就洗掉一次形态库。
// 见 db/scratch_db_test.go 文件头。

// r1039EnsureBody 抽出 ensureRequestLogSchema 的函数体（源码级判据用）。
func r1039EnsureBody(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	const fnConst = "func (d *DB) ensureRequestLogSchema("
	start := strings.Index(text, fnConst)
	if start < 0 {
		t.Fatal("db.go missing ensureRequestLogSchema")
	}
	rest := text[start+1:]
	next := strings.Index(rest, "\nfunc (d *DB) ")
	if next < 0 {
		t.Fatal("db.go malformed: no function after ensureRequestLogSchema")
	}
	return text[start : start+1+next]
}

// TestR1039RequestLogsProviderIDColumnOrderInEnsureBody 钉住**顺序**：
// provider_id 的 ADD COLUMN 必须在两个 provider 索引之前。
//
// 为什么单独一条：真库判据能抓住「列没补」，但抓不住「补了却在索引之后」——
// 后者在 PG 里同样报 42703，只是错误信息换了个位置。这条把位置直接写死。
func TestR1039RequestLogsProviderIDColumnOrderInEnsureBody(t *testing.T) {
	body := r1039EnsureBody(t)

	// ⚠ needle 不带分号：那列是**多列 ALTER 的中间项**（逗号续行），写成
	//   `... BIGINT;` 会因为分号位置不同而永远匹配不到 ⇒ 判据恒红。
	addCol := strings.Index(body, "ADD COLUMN IF NOT EXISTS provider_id BIGINT")
	if addCol < 0 {
		t.Fatal("ensureRequestLogSchema 必须补 provider_id 列（它自己在下面给 _request_logs 建带 provider_id 的索引）")
	}
	for _, idx := range []string{
		"idx_request_logs_provider_quality",
		"idx_request_logs_provider_tool_calls",
	} {
		// ⚠ 必须锚在 **CREATE INDEX** 上，不能只找索引名：
		// 目录短路清单（catalogShortCircuitSQL）在函数体前面就列了这两个名字，
		// 拿名字定位会把「清单里的名字」当成「建索引的位置」⇒ 判据恒红。
		at := strings.Index(body, "CREATE INDEX IF NOT EXISTS "+idx)
		if at < 0 {
			t.Fatalf("ensureRequestLogSchema 不再创建索引 %s —— 若是有意删除，请同步删除本条判据与本文件", idx)
		}
		if addCol > at {
			t.Errorf("provider_id 列的 ADD COLUMN 必须在索引 %s 之前（否则 PG 先建索引 ⇒ 42703）", idx)
		}
	}

	// 目录短路清单必须与 ensure 体同步（db.go 自己的注释要求「两处一起改」）：
	// 漏了它 ⇒ 一台「列缺、索引也在」的库会被判成「齐备」⇒ 零 DDL ⇒ 列永远补不上。
	shortCircuit := body[strings.Index(body, "catalogShortCircuitSQL"):]
	if !strings.Contains(shortCircuit, "'provider_id'") {
		t.Error("catalog 短路清单必须包含 provider_id（否则缺列的库会被判成齐备 ⇒ ensure 被短路 ⇒ 列永远补不上）")
	}
}

// TestR1039RequestLogsProviderIDColumnRealDB 是**承重**的真库判据。
//
// 三个夹具形态，覆盖「部署形态」的三种组合：
//
//	A. _request_logs 有、_request_logs_hot 也有（完整族）⇒ ensure 必须成功，
//	   provider_id 列与两个索引都得建出来。
//	B. _request_logs 有、_request_logs_hot **没有**（族退役后的形态，本机 2026-10-21
//	   的实际形态）⇒ ensure 必须成功（不得 42P01）。
//	C. _request_logs 都没有（整个族没装）⇒ ensure 必须成功（跳过该族自愈）。
//
// 修复前：A 报 42703（provider_id 缺列）、B 报 42P01（_request_logs_hot 不存在）、
// C 报 42P01 ⇒ 三种形态都起不来。
func TestR1039RequestLogsProviderIDColumnRealDB(t *testing.T) {
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
	columnExists := func(table, column string) bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema='public' AND table_name=$1 AND column_name=$2`,
			table, column).Scan(&n); err != nil {
			t.Fatalf("probe %s.%s: %v", table, column, err)
		}
		return n > 0
	}
	indexExists := func(name string) bool {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_indexes WHERE schemaname='public' AND indexname=$1`,
			name).Scan(&n); err != nil {
			t.Fatalf("probe index %s: %v", name, err)
		}
		return n > 0
	}
	reset := func(withParent, withHot bool) {
		exec(`DROP TABLE IF EXISTS public._request_logs_hot, public._request_logs CASCADE;`)
		if withParent {
			// 夹具：**刻意不带** provider_id 的 _request_logs（最小/新装基线的形态）。
			exec(`CREATE TABLE public._request_logs (
				id BIGSERIAL PRIMARY KEY, tenant_id TEXT, ts TIMESTAMPTZ NOT NULL DEFAULT now());`)
		}
		if withHot {
			// hot 表由 ensure 链 ALTER（不是 CREATE）⇒ 夹具必须先给一张，
			// 否则报 42P01（那是夹具的锅，不是 ensure 的）。
			exec(`CREATE TABLE public._request_logs_hot (
				id BIGSERIAL PRIMARY KEY, gw_session_id TEXT, tenant_id TEXT, ts TIMESTAMPTZ NOT NULL DEFAULT now());`)
		}
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"DROP TABLE IF EXISTS public._request_logs_hot, public._request_logs CASCADE;")
	})

	t.Run("A_完整族_补列并建索引", func(t *testing.T) {
		reset(true, true)
		if columnExists("_request_logs", "provider_id") {
			t.Fatal("夹具失效：_request_logs 不该带 provider_id")
		}
		if err := d.ensureRequestLogSchema(ctx); err != nil {
			t.Fatalf("ensureRequestLogSchema 在没有 provider_id 的库上必须成功，got: %v", err)
		}
		if !columnExists("_request_logs", "provider_id") {
			t.Error("ensure 之后仍没有 provider_id 列 ⇒ ensure 体没补这一列")
		}
		for _, idx := range []string{"idx_request_logs_provider_quality", "idx_request_logs_provider_tool_calls"} {
			if !indexExists(idx) {
				t.Errorf("ensure 之后仍没有索引 %s", idx)
			}
		}
		if !columnExists("_request_logs_hot", "is_final_success") {
			t.Error("ensure 之后 _request_logs_hot 仍缺 is_final_success 列")
		}
		if !indexExists("uq_request_logs_hot_final_success_session") {
			t.Error("ensure 之后仍没有索引 uq_request_logs_hot_final_success_session")
		}
		// 幂等：第二次跑必须是 no-op 且不报错。
		if err := d.ensureRequestLogSchema(ctx); err != nil {
			t.Fatalf("ensure 第二次必须幂等成功，got: %v", err)
		}
	})

	t.Run("B_族已退役_没有hot表也不许中止", func(t *testing.T) {
		reset(true, false)
		if err := d.ensureRequestLogSchema(ctx); err != nil {
			t.Fatalf("族退役（无 _request_logs_hot）时 ensure 必须成功，got: %v", err)
		}
		if !columnExists("_request_logs", "provider_id") {
			t.Error("ensure 之后仍没有 provider_id 列")
		}
	})

	t.Run("C_整个族没装_必须跳过而不是42P01", func(t *testing.T) {
		reset(false, false)
		if err := d.ensureRequestLogSchema(ctx); err != nil {
			t.Fatalf("族表不存在时 ensure 必须跳过（不得 42P01），got: %v", err)
		}
	})
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
// ## 缺陷（结构性，不是本机环境问题）
//
// db.ensureBillingUnitAndAudioSeconds 的短路条件是
// `cmbOK && hotOK`，其中 hotOK = columnsAllPresent("_request_logs_hot",
// ["audio_seconds"])。而 columnsAllPresent 在**表不在**时返回 **true**
// （missing=0 ⇒ 没缺列）⇒ 于是：
//
//   · 族在、cmb.billing_unit 缺 ⇒ 走 DDL 分支（正常补列）；
//   · 族**不在**、cmb.billing_unit 缺 ⇒ **也**走 DDL 分支
//     ⇒ ALTER TABLE _request_logs_hot / _request_logs
//     ⇒ 42P01 ⇒ 整条 boot 迁移中止。
//
// ⚠ 在真实新装路径上，startup 迁移 848 早就把 cmb.billing_unit 补上了
// （848 跑在族退休之前：848 < 860 < 890）⇒ 短路条件成立 ⇒ 不进 DDL 分支
// ⇒ **新装路径不触发**。触发条件是「cmb.billing_unit 缺 + 族已退休」，例如
// 只跑了部分 startup 迁移的库、或 v1 fallback 体灌入的基线副本。
// ⇒ 本条是**防御性**判据：把那条 landmine 钉死，免得哪天族退休提前或 848 被
// 跳过时又炸一次 boot。
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
