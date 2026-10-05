package bg

// pricing_plan_stale 的真库判据（realDB，55432）。
//
// 为什么值得单独一条：仓里**本来就有**一套定价 SSOT
// （docs/02-resources/research/pricing/scripts/vendor-pricing-table.py，
// 79 条 CANONICAL_PRICING，USD 19 / CNY 60，规则写明「国内厂商必须 CNY」），
// 落地表是 public.pricing_plans。生产实测 284 行、created_at 全部 2026-06-12、
// 最旧 115 天、覆盖 39/960 个模型、116/284 行没有 model_canonical_id。
//
// 而 provider/client.go 的 CalcCost 在计划价缺失时**回落到 pricing_plans**
// ⇒ 成本计算正在用一份 115 天前的价，而 15 条检查里**没有一条**问过它新不新。
//
// 本判据钉三件不同的事，任何一件被写坏都红：
//
//	A 陈旧且非空 ⇒ 必须报 1 行，且 detail 里带**实测**天数、行数、孤儿行数、
//	  以及孤儿行挂在**几个凭据**上；
//	B 数据是新的 ⇒ 必须**不报**（否则每轮都嘶吼，等于没有）；
//	C 表是空的 ⇒ 必须**不报**，且理由不是「没事」而是「还没定价不是故障」
//	  —— 与 baseline_price_missing 的 '(no priced bindings yet)' 同一原则。
//
// ★ 夹具刻意让「孤儿行数 ≠ 孤儿凭据数」（4 行 / 2 个凭据）。这不是随手写的：
//   生产实测是 116 行 / 7 个凭据，而那 7 个凭据各挂着数百个已映射模型绑定
//   ⇒ **可行动的工作量是「一份 7 个凭据的清单」，不是「补 116 行」**。
//   若两个数相等，一个写死的数字能同时蒙过两个断言，凭据数那栏就看着有
//   判据、实际没有。变异 M17（把凭据数别名成孤儿数）实测红在凭据断言上，
//   证明这个不对称是承重的。
//
// 变异台账（2026-10-06 实跑；每条都自证 md5 变了 + go vet 通过 + -count=1，
// 红都归因到具体断言行，还原后逐字节比对）：
//
//	M9  30 天 → 0 天（陈旧条件恒真） ⇒ 红在 **B 段 :206**；A 段照常红不了
//	    （恒真嘶吼是 A 段本来看不见的缺陷，只能靠 B 段抓）
//	M11 孤儿行数硬编码 0            ⇒ 红在 **A 段 :185**；凭据断言 :193 未报
//	M16 孤儿凭据数硬编码 0          ⇒ 红在 **A 段 :193**；孤儿计数断言 :185 未报
//	    （M11 与 M16 互为反向证明：两个数各自独立承重，不是同一个数字报两遍）
//	M17 凭据数别名成孤儿数          ⇒ 红在 **A 段 :193**，报出 "across 4 credential(s)"
//	M18 30 天 → 365 天（阈值过宽）  ⇒ 红在 **A 段 :175**（0 行），B 段未报
//
// ★ C 段**没有**单点变异能打红，且这不是缺陷：空表上 `max()` 返回 NULL，
//   `NULL < x` 求值为 NULL 而非 true，WHERE 永不通过 ⇒ 「空表不报」是
//   **SQL 三值逻辑的结构性保证**。查询里那两个守卫（`total > 0` 与 COALESCE）
//   **各自单独就够、两个同时去掉也够**（变异 M10 / M12 / M13 全部为绿），
//   它们是可读性不是承重。写在这里是为了别让下一个人把功劳记到它们头上。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPricingPlanStaleReportsTheLoadAge(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass('public.pricing_plans') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe pricing_plans: %v", err)
	}
	if exists {
		t.Skip("public.pricing_plans already exists in this database — this test creates and drops it")
	}

	// ★ 清理注册在**建表之前**。清单穷举这次建出来的每一样东西。
	defer func() {
		if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS public.pricing_plans`); err != nil {
			t.Errorf("cleanup pricing_plans: %v", err)
		}
	}()

	if _, err := pool.Exec(ctx, `CREATE TABLE public.pricing_plans (
		id bigserial PRIMARY KEY,
		currency text NOT NULL DEFAULT 'USD',
		model_canonical_id bigint,
		credential_id bigint,
		source text NOT NULL DEFAULT 'manual',
		scraped_url text,
		created_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create pricing_plans: %v", err)
	}
	// 夹具表只建这条检查**读到的**那几列，比生产表（16 列，含 scope /
	// plan_type / plan_json / effective_from / confidence / provider_id …）窄。
	// 窄可以，少一列不行：下面 A 段能过就说明 model_canonical_id、
	// credential_id、created_at 全部到位 —— 若哪次把其中一列从夹具里删掉，
	// 查询会报 42703 undefined_column，而 Optional 只兜 42P01 ⇒ 直接红。

	def := healthCheckByID(t, "pricing_plan_stale")
	run := func(label string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, def.Query)
		if err != nil {
			t.Fatalf("%s: run %s: %v", label, def.CheckID, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a, b, c, d any
			if err := rows.Scan(&a, &b, &c, &d); err != nil {
				t.Fatalf("%s: scan: %v", label, err)
			}
			out = append(out, fmt.Sprintf("%v | %v", b, c))
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: rows: %v", label, err)
		}
		return out
	}
	seed := func(label, createdAt string, orphanRows, orphanCreds int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `DELETE FROM public.pricing_plans`); err != nil {
			t.Fatalf("%s: clear: %v", label, err)
		}
		// 3 行带 canonical 指针（凭据级为 NULL，即模型级价）。
		q := fmt.Sprintf(`INSERT INTO public.pricing_plans
			(model_canonical_id, currency, source, created_at)
			SELECT g, 'USD', 'scraped', %s FROM generate_series(1, $1) g`, createdAt)
		if _, err := pool.Exec(ctx, q, 3); err != nil {
			t.Fatalf("%s: seed pointed rows: %v", label, err)
		}
		if orphanRows > 0 {
			if orphanCreds <= 0 || orphanCreds > orphanRows {
				t.Fatalf("%s: bad fixture spec orphanRows=%d orphanCreds=%d — orphanCreds must be "+
					"1..orphanRows, otherwise the two counts are the same number and asserting one "+
					"proves nothing about the other", label, orphanRows, orphanCreds)
			}
			// ★ 孤儿行铺在 orphanCreds 个凭据上，且**行数与凭据数刻意不相等**。
			// 生产实测是 116 行 / 7 个凭据（比例 16.6:1）；这里用 4 行 / 2 个凭据
			// （2:1）就足够判别。之所以必须不等：若两者相等，一个写死的数字
			// （比如把 orphan_cred 也硬编码成 4）能同时蒙对两个断言，
			// 于是「凭据数」这一栏看着有判据、实际没有。
			// ⚠ SQL 里的取模必须写 `%%`：这是 fmt.Sprintf 的参数，取模符号
			//   漏转义的话 vet 直接报 "unknown verb %"（本轮真踩到过）。
			q2 := fmt.Sprintf(`INSERT INTO public.pricing_plans
				(model_canonical_id, credential_id, currency, source, created_at)
				SELECT NULL, 100 + ((g - 1) %% $2), 'CNY', 'scraped', %s
				  FROM generate_series(1, $1) g`, createdAt)
			if _, err := pool.Exec(ctx, q2, orphanRows, orphanCreds); err != nil {
				t.Fatalf("%s: seed orphan rows: %v", label, err)
			}
		}
		// 量具自证：种进去几行必须报出来，否则「下面断言 0 行」与「根本没种进去」
		// 是同一个读数。
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.pricing_plans`).Scan(&n); err != nil {
			t.Fatalf("%s: recount: %v", label, err)
		}
		if n != 3+orphanRows {
			t.Fatalf("%s: seeded %d row(s) but the table holds %d — everything below is vacuous",
				label, 3+orphanRows, n)
		}
		// 孤儿行那几行**确实**没指针、且**确实**落在预期的凭据数上。
		// 不验这一层的话，夹具若哪天把 credential_id 一起写成 NULL，
		// orphan_cred 会变成 0，而上面那些 Contains 断言照样可能过。
		var gotOrphan, gotCred int
		if err := pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE model_canonical_id IS NULL)::int,
			count(DISTINCT credential_id) FILTER (WHERE model_canonical_id IS NULL)::int
		  FROM public.pricing_plans`).Scan(&gotOrphan, &gotCred); err != nil {
			t.Fatalf("%s: recount orphan: %v", label, err)
		}
		if gotOrphan != orphanRows || gotCred != orphanCreds {
			t.Fatalf("%s: fixture is not the shape the assertion assumes — orphans=%d/%d creds=%d/%d. "+
				"Fix the fixture, not the assertion", label, gotOrphan, orphanRows, gotCred, orphanCreds)
		}
		t.Logf("%s: fixture holds %d row(s) (orphan=%d across %d cred), created_at=%s",
			label, n, gotOrphan, gotCred, createdAt)
	}

	// ---- A：陈旧且非空 ⇒ 必须报 ----
	seed("A stale", "now() - interval '60 days'", 4, 2)
	got := run("A")
	if len(got) != 1 {
		t.Fatalf("A: 60-day-old pricing_plans produced %d row(s), want 1: %v", len(got), got)
	}
	line := got[0]
	if !strings.Contains(line, "60") {
		t.Errorf("A: detail must name the measured age (60 days), got %q", line)
	}
	if !strings.Contains(line, "7 pricing plan row(s)") {
		t.Errorf("A: detail must name the row count (7 = 3 pointed + 4 orphan), got %q", line)
	}
	if !strings.Contains(line, "4 of them have model_canonical_id IS NULL") {
		t.Errorf("A: detail must name the orphan count (4) — those rows cannot be attributed to a "+
			"model, which is a different problem from staleness and has a different fix, got %q", line)
	}
	// ★ 「跨几个凭据」是**另一个数**（夹具刻意设成 2 ≠ 4）。生产实测 116 行 /
	// 7 个凭据，而那 7 个凭据各挂着数百个已映射模型绑定 ⇒ **可行动的工作量
	// 是「一份 7 个凭据的清单」，不是「补 116 行」**。少了这一栏，运维会去
	// 做 116 件其实不存在的活。
	if !strings.Contains(line, "across 2 credential(s)") {
		t.Errorf("A: detail must name how many credentials the orphans hang off (2, deliberately "+
			"!= the 4 orphan rows) — \"116 rows\" reads as \"fix 116 things\" when the work is "+
			"\"here are the 7 credentials\", got %q", line)
	}
	// created_at 是**入库**时间，不是核实时间。不写明这一点，读到告警的人会以为
	// 「115 天」是「115 天没向厂商核实过」，而它只证明「115 天没重新灌过」。
	if !strings.Contains(line, "LOAD time") {
		t.Errorf("A: detail must say created_at is the load time, not the verification time, got %q", line)
	}

	// ---- B：数据是新的 ⇒ 必须不报（陈旧的另一半）----
	seed("B fresh", "now() - interval '1 day'", 4, 2)
	if got := run("B"); len(got) != 0 {
		t.Errorf("B: pricing_plans loaded yesterday produced %d row(s), want 0. A check that fires on "+
			"fresh data is the same as no check at all — operators learn to ignore it: %v", len(got), got)
	}

	// ---- C：表是空的 ⇒ 必须不报 ----
	if _, err := pool.Exec(ctx, `DELETE FROM public.pricing_plans`); err != nil {
		t.Fatalf("C: clear: %v", err)
	}
	if got := run("C"); len(got) != 0 {
		t.Errorf("C: an EMPTY pricing_plans produced %d row(s), want 0. An empty table means "+
			"\"nothing has been priced yet\", which is not a fault — reporting it sends people to fix a "+
			"problem that does not exist: %v", len(got), got)
	}
}
