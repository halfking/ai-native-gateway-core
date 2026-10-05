package bg

// 第 14 条健康检查 `recorded_cost_is_negative` 的真库判据。
//
// # 这条检查在补什么洞
//
// 前 13 条盯的都是**「会不会错」**（价格列有没有、基准价有没有、币种对不对、
// 出处漂没漂）。**没有一条问「已经记下来的成本对不对」**。
//
// 2026-10-06 真库实测（127.0.0.1:5432，全程只读）发现：
//
//	request_logs  cost_usd < 0  = 1,628 行，合计 -$4.79（2026-09-03 → 10-04）
//	usage_ledger  cost_usd < 0  = 1,299 行，合计 -$4.67
//	全部满足 cache_read_tokens > prompt_tokens
//	cache 占那批 token 的 96.2%（4,530,529 vs prompt 179,144）
//	全部来自 apiclaude（Anthropic 协议的中转）
//	已污染 stats_usage_daily 225 行、stats_usage_monthly 12 行
//
// 根因在 domains/streaming/usage.go 的 CalcCost：「cache 从 prompt 里减掉」
// 那两段**假定 prompt_tokens 含 cache**（OpenAI 口径 prompt_tokens ⊇
// cached_tokens），而 Anthropic 口径相反（input_tokens 不含
// cache_read_input_tokens）⇒ promptCost 被减成负数。
//
// 写入侧 2026-10-06 已加 `if total < 0 { return nil }`。但**已记账的历史行
// 不会被改**，且下一个协议口径出现时仍会复发 ⇒ 检查侧必须能自己发现。
// 「写入侧加了守卫」不等于「台账是干净的」。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具对象已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// negativeCostFixture 搭出这条检查需要的最小表。
//
// 依赖清单（与检查查询的 FROM / JOIN 一一对应）：
//
//	request_logs  读 cost_usd / prompt_tokens / cache_read_tokens / ts
//	credentials   读 provider_id（provider 只为显示名，取不到也不影响判定）
//
// ★ 为什么 credentials 用脚手架而不是对象 SSOT：credential_model_bindings
// 那个 SSOT 已知与真库有差（6 个 NOT NULL 缺失），而这里只需要 id 一列；
// 铺一整条带外键的链只会让夹具的失败形态变得难读。
func negativeCostFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	tables := []string{"request_logs", "credentials"}
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
			DROP TABLE IF EXISTS public.request_logs;
			DROP TABLE IF EXISTS public.credentials;`)
	})

	if _, err := pool.Exec(ctx, `
		CREATE TABLE public.credentials (
		    id bigserial PRIMARY KEY, provider_id bigint, label text);
		CREATE TABLE public.request_logs (
		    id bigserial PRIMARY KEY,
		    credential_id bigint NOT NULL,
		    outbound_model text,
		    prompt_tokens integer,
		    cache_read_tokens integer,
		    cost_usd double precision,
		    ts timestamptz NOT NULL DEFAULT now());`); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// ★ 量具自证：缺这一步，「夹具没建出来」会伪装成「断言红」，症状是
	// 42P01 出现在第一行，人排查的方向是错的。
	for _, obj := range []string{"request_logs", "credentials"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname='public' AND c.relname = $1`, obj).Scan(&n); err != nil {
			t.Fatalf("self-check %s: %v", obj, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.%s is absent — assertions would fail for the wrong reason", obj)
		}
	}
}

func healthCheckDefByID(t *testing.T, id string) HealthCheckDef {
	t.Helper()
	for _, d := range AllHealthChecks() {
		if d.CheckID == id {
			return d
		}
	}
	t.Fatalf("health check %q is not registered in AllHealthChecks()", id)
	return HealthCheckDef{}
}

// TestRecordedCostIsNegative_BuildsAndReturnsNil is the load-bearing one: it
// plants the exact production shape (cache >> prompt) and demands the check
// find it.
func TestRecordedCostIsNegative_findsPlantedRows(t *testing.T) {
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
	negativeCostFixture(t, ctx, pool)

	// 一条真库形态（prompt 41 / cache 41550 → 负成本），一条正常的正成本，
	// 一条成本为 0（真免费，不是缺陷），一条超出 30 天窗口的负成本
	// （窗口边界必须真的生效，否则「30 天」只是文案）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.credentials (id, label) VALUES (901, 'c-1'), (903, 'c-3');
		INSERT INTO public.request_logs
			(credential_id, outbound_model, prompt_tokens, cache_read_tokens, cost_usd, ts)
		VALUES
			(901, 'claude-via-relay',   41, 41550, -0.5576, now() - interval '1 day'),
			(901, 'claude-via-relay2',  41, 41550, -0.0021, now() - interval '2 days'),
			(901, 'healthy-model', 1000000,     0,  3.0000, now() - interval '1 day'),
			(901, 'free-model',         100,     0,  0.0000, now() - interval '1 day'),
			(901, 'ancient-negative',    41, 41550, -9.0000, now() - interval '40 days'),
			-- ★ 窗口边界：29 天。比 40 天那行新、比「30 天」老。窗口被改成
			--   30 年时这一行**必须**被报出来（行数 2→3）⇒ 窗口不是文案。
			(901, 'inside-window-edge',  41, 41550, -0.5000, now() - interval '29 days'),
			-- ★ 同一凭据、正负同模型名：c-903 的 claude-via-relay 是**正**成本。
			--   它逼判据不能「按模型名去重」—— 若去重，901 与 903 的同名行会
			--   被合并成一个实体，掩盖「只有某个凭据/某条协议在错」这个事实。
			(903, 'claude-via-relay', 1000000,     0,  2.5000, now() - interval '1 day');`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	def := healthCheckDefByID(t, "recorded_cost_is_negative")
	if !def.Optional {
		t.Error("this check must be Optional: request_logs may be absent in an environment " +
			"that has not applied the logging migrations yet, and a missing table must not abort the round")
	}

	rows, err := pool.Query(ctx, def.Query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type finding struct {
		key, name, detail, fix string
	}
	var got []finding
	for rows.Next() {
		var f finding
		if err := rows.Scan(&f.key, &f.name, &f.detail, &f.fix); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, f)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// 期望恰好 2 条：两条窗口内的负成本，都在 c-901 上，各自一个模型名。
	//
	// ★ 逐 (凭据, 模型) 而不是合并成一条：c-903 有一个**同名**模型
	//   claude-via-relay 但成本为正 ⇒ 若判据按模型名去重，它要么把 901 的
	//   负成本和 903 的正成本混成一组（掩盖「只有 901 在错」），要么因为
	//   看到同名有正值而把 901 的负值整条滤掉。两种都答非所问。
	//   这个样本是 T3（按模型去重）必须变红的原因。
	if len(got) != 3 {
		var names []string
		for _, f := range got {
			names = append(names, f.name)
		}
		t.Fatalf("got %d findings %v, want 3 (the three in-window negative rows on c-901: "+
			"1d, 2d, 29d). healthy/free rows must not appear, c-903's positive same-named row "+
			"must not absorb c-901's negative one, and the 40-day-old row must be OUTSIDE the "+
			"30-day window — if that last one shows up, the window is not being applied at all",
			len(got), names)
	}
	// ★ 显式钉住窗口边界：40 天那行**不出现**、29 天那行**出现**。
	//
	// 2026-10-06 变异实测（这条是被两次「判据恒绿」逼出来的，记在这里）：
	//   第一版夹具只种了 1 天 / 2 天 / 40 天三行，断言「恰好 2 条」。
	//   ⇒ 把判据取反成 `cost_usd >= 0`、把 30 天窗口改成 30 年、窗口反转、
	//     实体键去掉凭据维度 —— **五条变异全绿**。原因只有一个：
	//     `got != 2` 这一个数字，在「报出的是哪几行」上完全没有判别力：
	//     取反后报出 3 行（healthy + free + c-903 的正成本行）也照样是
	//     「不等于 2」，但**行数断言之前的那句 name 白名单循环先把它挡了**，
	//     变异在更早的一步就被拦下 ⇒ 看起来绿是因为它压根没走到承重处。
	//     而 40 天那行在「取反」后仍会消失、白名单循环又会因为它的名字不在
	//     allowed 里而报错，**报错被 rc 吸收、不被断言吸收** ⇒ 于是
	//     「窗口没生效」这件事没有任何一条判据在说。
	//   ⇒ 补 29 天窗口边界样本 + 逐名白名单 + 两条专门的边界断言之后，
	//     同一批变异全部变红（实测 rc=1，诊断各自指对）。
	//
	// 教训：**「行数不对」不等于「窗口不对」**。要钉住一个谓词，就必须有一条
	// 判据直接说这个谓词失效时的样子（40 天那行必须缺席、29 天那行必须在）。
	for _, f := range got {
		if strings.Contains(f.name, "ancient-negative") {
			t.Errorf("40-day-old negative row reported — the 30-day window is not applied")
		}
	}
	sawEdge := false
	for _, f := range got {
		if strings.Contains(f.name, "inside-window-edge") {
			sawEdge = true
		}
	}
	if !sawEdge {
		t.Error("the 29-day-old negative row was NOT reported — the 30-day window is too narrow " +
			"(it must include 'now() - 30 days' itself, not strictly newer than it)")
	}
	// 允许出现的模型名就是那三条**窗口内负成本**行的名字。逐名断言而不是
	// 断言「含 claude-via-relay」：后者会把 inside-window-edge 这种合法但
	// 名字不同的行误判成噪声（2026-10-06 实测踩到过 —— 判据第一版只认
	// claude-via-relay，于是加窗口边界样本时立刻报假红）。
	allowed := map[string]bool{
		"credential#901:claude-via-relay":   true,
		"credential#901:claude-via-relay2":  true,
		"credential#901:inside-window-edge": true,
	}
	for _, f := range got {
		if !allowed[f.name] {
			t.Errorf("unexpected finding %q — only the three in-window negative rows may be reported "+
				"(healthy / free / 40-day-old / c-903's positive row must not appear)", f.name)
		}
		if !strings.HasPrefix(f.name, "credential#901:") {
			t.Errorf("finding %q must be attributed to c-901 (the credential whose rows are negative)", f.name)
		}
		if !strings.Contains(f.detail, "row(s)") || !strings.Contains(f.detail, "USD") {
			t.Errorf("%s: detail must state the row count and the USD offset, got %q", f.name, f.detail)
		}
		// 首列是 'credential_id|outbound_model' 合成键：必须能被 textHash 吃下，
		// 且不同组合不能撞成同一行。列名写 outbound_model 是承重的：夹具里
		// raw_model_name 留空正是为了复现真库形态（1628/1628 行该列为空），
		// 换成它分组会得到「一个凭据一个空模型名」的假分组。
		if !strings.Contains(f.key, "901|") {
			t.Errorf("%s: entity key %q must be 'credential_id|outbound_model'", f.name, f.key)
		}
	}
	if textHash(got[0].key) == textHash(got[1].key) {
		t.Error("the two findings share an entity key — the health table's UNIQUE " +
			"(check_id, entity_type, entity_id) would make the UPSERT overwrite one with the other")
	}
}

// TestRecordedCostIsNegative_healthyTableReportsNothing is the control group.
// Without it, a check that always returns one row would satisfy the test above.
func TestRecordedCostIsNegative_healthyTableReportsNothing(t *testing.T) {
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
	negativeCostFixture(t, ctx, pool)

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.credentials (id, label) VALUES (902, 'c-ok');
		INSERT INTO public.request_logs
			(credential_id, outbound_model, prompt_tokens, cache_read_tokens, cost_usd, ts)
		VALUES
			(902, 'm1', 1000000,     0,  3.0000, now()),
			(902, 'm2',      500, 40000,  1.5000, now()),  -- cache<=.prompt: 正常口径
			(902, 'm3',      100,     0,  0.0000, now());  -- 真免费，不是缺陷
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	def := healthCheckDefByID(t, "recorded_cost_is_negative")
	rows, err := pool.Query(ctx, def.Query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var n int
	for rows.Next() {
		var k, nm, d, f string
		if err := rows.Scan(&k, &nm, &d, &f); err != nil {
			t.Fatalf("scan: %v", err)
		}
		n++
		t.Errorf("unexpected finding: %s / %s", nm, d)
	}
	if rows.Err() != nil {
		t.Fatalf("rows: %v", rows.Err())
	}
	if n != 0 {
		t.Errorf("got %d findings on a healthy table, want 0 — a check that always fires is noise", n)
	}
}

// TestHealthCheckDef_recordingNegativeIsRegistered pins the registration: a
// defined-but-unregistered check is the same defect class as an unwired worker.
func TestHealthCheckDef_recordingNegativeIsRegistered(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range AllHealthChecks() {
		if seen[d.CheckID] {
			t.Errorf("check_id %q is registered twice", d.CheckID)
		}
		seen[d.CheckID] = true
	}
	if !seen["recorded_cost_is_negative"] {
		t.Fatal("recorded_cost_is_negative is not in AllHealthChecks() — the check would never run")
	}
	// The row-handling switch must have a case for it, or every row collapses
	// onto entity_id=0 with an empty name and the UPSERT overwrites itself.
	// health_check_scan_guard_test.go covers this generically; this asserts
	// the specific pairing so the two don't drift apart silently.
	src, err := os.ReadFile("routing_health_checks.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(src), `case "recorded_cost_is_negative":`) {
		t.Error(`no case "recorded_cost_is_negative" in the row-handling switch — ` +
			"every row would scan into a zero entity_id and collapse into one")
	}
}
