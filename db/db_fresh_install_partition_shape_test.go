package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// db_fresh_install_partition_shape_test.go
//
// 「月分区形态」不变量：目标库里每个有月分区的族都必须**覆盖当月与次月**，
// 且运行期要调的 ensure 函数**一个都不许报错**。
//
// # 这道门补的是哪个盲点
//
// 2026-10-21（R61 轮）在 handoff §19.7 记下一个更重的缺陷同族：
//
//	全新安装库里 15 个 RANGE 族只有 2026_07/2026_08 ⇒ **没有当月分区**
//	⇒ 核心写路径直接 23514：
//	   ERROR: no partition of relation "session_turns" found for row
//
// R62 轮用迁移 `893_fresh_install_partition_prereq.sql` 修掉了它。
// 而**两道现有真库门都看不见这类缺陷**：
//
//   - installer 迁移链的连续性门：管的是「登记的迁移还在不在」，与分区无关；
//   - `sql/schema/fresh_install_column_gaps.tsv` 那道门：量的是**列**的差集。
//
// ⇒ 而且**分区与函数也是资产**：谁把 893 从安装链里摘掉、或把某个 ensure 函数
//
//	改坏，缺口会静默回来，两道门都不会响。
//
// # 为什么「分区在」还不够（★ 本门最核心的一条）
//
// R62 轮实测踩到的一个**反直觉**形态：
//
//	分区**在**、函数**也在**，但函数造出来的**分区名不对**
//	⇒ baseline 自带的 ensure 函数每次都判「不存在」→ 重建 → 边界重叠报错：
//	   ERROR: partition "request_logs_2026_10" would overlap partition "_request_logs_2026_10"
//	⇒ 于是该族**永远无法自愈**，而且**只有真的调用它才看得见**。
//
// ⇒ 所以这道门不满足于「分区在不在」：它要断言**运行期真的会用的那批函数
//
//	一个都不许报错**，并且**真的写一行**看它落到哪个分区。
//
// # ★★ oracle（量具自检）为什么**不能**用「893 的产物」
//
// 第一版本门用「`llm_gateway_ensure_monthly_partition` 存在吗」当 oracle，
// 不存在就 t.Skip。⇒ **实测证明这是恒绿门**：变异 M1 把 893 从安装链摘掉后，
// 标记函数随之消失 ⇒ oracle 判「这不是全新安装」⇒ **SKIP** ⇒ 门绿。
// ⇒ 「用被测对象自己的产物当 oracle」会让门在被测对象坏掉时自动闭嘴。
//
// ⇒ 现在改成：
//   - **oracle = 仓内约定**（TEST_DATABASE_URL / TEST_DB_URL 是否设置）——
//     它与被测对象**无关** ⇒ 893 被摘掉时门照样跑、照样红；
//   - 893 的产物**降级成断言**（第 ① 条）—— 摘掉 893 ⇒ 这里红。
//
// # ★ 本门**无副作用**（为什么敢在生产上跑）
//
// 第 ④ 条（调 ensure 函数）与第 ⑤ 条（写一行）都放进**会 ROLLBACK 的事务**：
// - ensure 函数建的分区随事务回滚 ⇒ 生产上不会留下任何对象；
// - 探针行随事务回滚 ⇒ 生产上不会留下一行数据。
//
// ⇒ 所以「误把 TEST_DATABASE_URL 指到生产」的代价 = 一次事务往返，**不是**生产写入。
//
//	⚠ 但**仍建议指向一次性库**：生产有 RLS / 租户守卫，探针 INSERT 可能被拒
//	⇒ 那会是**假红**，不是真缺陷。
func TestFreshInstallPartitionShape(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿" +
			"（⚠ 本门会写一行 session_turns（事务内回滚）⇒ 建议指向一次性库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open 失败: %v", err)
	}
	if d == nil {
		t.Fatal("db.Open 返回 nil DB")
	}
	defer d.Close()

	// ── ① 迁移 893 的两个助手函数必须存在 ─────────────────────────────────
	//
	// 它们是 893 独有的产物 ⇒ 「893 被摘掉 / 被改名」在这里直接红。
	// ★ 这一条**必须留在门里**：否则「893 不在链上」这件事没有任何判据。
	for _, fn := range []string{
		"llm_gateway_ensure_monthly_partition",
		"llm_gateway_monthly_family_prefix",
	} {
		var n int
		if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_proc p
			  JOIN pg_namespace n ON n.oid = p.pronamespace
			 WHERE n.nspname = 'public' AND p.proname = $1`, fn).Scan(&n); err != nil {
			t.Fatalf("查函数 %s: %v", fn, err)
		}
		if n == 0 {
			t.Errorf("★ 迁移 893 的助手函数 %s 不存在 ⇒ "+
				"893 很可能没被登记进安装链（installer/internal/dbinit/runner.go 的 StartupFiles）"+
				"⇒ 目标库不是「装完 893」的形态", fn)
		}
	}

	// ── ② 当月 / 次月分区必须已被覆盖（按**边界**判定，不看名字）────────────
	//
	// 判据：存在某个**月分区**子表，其范围覆盖目标月。
	//   - 用边界而不是名字 ⇒ 对「名字不对但边界对」的既有分区也算覆盖；
	//   - 排除 default 分区（DEFAULT 的 bound 不是 FROM..TO 形态）⇒
	//     「只有 default 兜底」**不算**覆盖（那正是 23514 的形态）。
	var curMonth, nextMonth time.Time
	if err := d.pool.QueryRow(ctx, `
		SELECT date_trunc('month', (now() AT TIME ZONE 'Asia/Shanghai')::date)::date,
		       (date_trunc('month', (now() AT TIME ZONE 'Asia/Shanghai')::date)::date
		          + INTERVAL '1 month')::date`).Scan(&curMonth, &nextMonth); err != nil {
		t.Fatalf("取当月/次月边界: %v", err)
	}

	rows, err := d.pool.Query(ctx, `
		SELECT p.relname
		  FROM pg_inherits i
		  JOIN pg_class p ON p.oid = i.inhparent
		  JOIN pg_class c ON c.oid = i.inhrelid
		  JOIN pg_namespace n ON n.oid = p.relnamespace
		 WHERE n.nspname = 'public'
		   AND p.relkind = 'p'
		   AND c.relname ~ '_\d{4}_\d{2}$'
		 GROUP BY p.relname
		 ORDER BY 1`)
	if err != nil {
		t.Fatalf("列月分区族: %v", err)
	}
	var families []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			t.Fatalf("扫描月分区族: %v", err)
		}
		families = append(families, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历月分区族: %v", err)
	}
	if len(families) == 0 {
		t.Fatalf("这个库里**一个**有月分区的族都没有 ⇒ 量具不对（这个库不是本门要验的形态）")
	}

	for _, fam := range families {
		for _, m := range []struct {
			label string
			at    time.Time
		}{{"当月", curMonth}, {"次月", nextMonth}} {
			var n int
			if err := d.pool.QueryRow(ctx, `
				SELECT count(*)
				  FROM pg_inherits i
				  JOIN pg_class c ON c.oid = i.inhrelid
				 WHERE i.inhparent = $1::regclass
				   AND c.relname ~ '_\d{4}_\d{2}$'
				   AND to_date(substring((regexp_match(pg_get_expr(c.relpartbound, c.oid),
				         'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[1] FROM 1 FOR 10),
				         'YYYY-MM-DD') <= $2::date
				   AND to_date(substring((regexp_match(pg_get_expr(c.relpartbound, c.oid),
				         'FROM \(''([^'']+)''\) TO \(''([^'']+)''\)'))[2] FROM 1 FOR 10),
				         'YYYY-MM-DD') > $2::date`,
				fam, m.at).Scan(&n); err != nil {
				t.Fatalf("查 %s 的 %s覆盖: %v", fam, m.label, err)
			}
			if n == 0 {
				t.Errorf("★ %s 族没有覆盖%s（%s）的**月分区** ⇒ 落在该月的写入会 23514"+
					"（`no partition of relation %q found for row`）"+
					"⇒ 新装库的核心写路径是坏的（迁移 893 被摘掉 / 被改坏？）",
					fam, m.label, m.at.Format("2006-01-02"), fam)
			}
		}
	}

	// ── ③ 运行期要用的 ensure 函数必须齐备 ───────────────────────────────
	//
	// 这 9 个就是 `bg/partition_manager.go` 的 ensureSpecs() 里对**仍然存活的
	// 月分区族**的调用目标（6 个 timestamptz 族 + 3 个 date 族）。
	//
	// ★★ **为什么是 9 个而不是 11 个**（2026-10-21 R62 轮合并后发现）：
	//   并行会话的迁移 `890_drop_request_logs_v1_family.sql` **退役了 v1
	//   request_logs 族** ⇒ 它把 `ensure_request_logs_partition` 与
	//   `ensure_request_logs_bodies_partition` **一起 DROP 了**
	//   （`DROP FUNCTION IF EXISTS public.ensure_request_logs_partition(timestamptz);`）
	//   ⇒ ★ **这两个函数不该再存在** ⇒ 本门**不得**要求它们在
	//   ⇒ ★★ 而迁移 893 在**链尾** ⇒ 它跑在退役迁移**之后**
	//   ⇒ ★★★ **若 893 还建这两个函数，就会把已退役的族「复活」**
	//     ⇒ 这就是「893 不建任何函数」这个决定在合并后**变成安全关键属性**的原因。
	//
	// ★ 它们**全部在 baseline `sql/schema/01-schema.sql` 里**
	//   （R61 轮记的「来源迁移未登记 ⇒ 新装库没函数」是**错的**，R62 轮实测纠正）。
	// ⇒ 它们的「缺」不会表现为「函数不在」，而表现为「函数在但**造错分区**」
	//   ⇒ 见下面第 ④ 条。
	wantFns := []string{
		"ensure_request_wal_partition",
		"ensure_routing_decision_log_partition",
		"ensure_credential_model_index_partition",
		"ensure_usage_ledger_partition",
		"ensure_credit_ledger_partition",
		"ensure_tool_usage_stats_partition",
		"ensure_dashboard_events_partition",
		"ensure_session_module_executions_partition",
		"ensure_sessions_v2_partitions",
	}
	for _, fn := range wantFns {
		var n int
		if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM pg_proc p
			  JOIN pg_namespace ns ON ns.oid = p.pronamespace
			 WHERE ns.nspname = 'public' AND p.proname = $1`, fn).Scan(&n); err != nil {
			t.Fatalf("查函数 %s: %v", fn, err)
		}
		if n == 0 {
			t.Errorf("★ 运行期要调的 %s 不存在 ⇒ 24h tick 调它会 42883「function does not exist」"+
				"⇒ 该族到月切换时无人补分区 ⇒ 23514", fn)
		}
	}

	// ── ④ ★ 逐个真的调用那 9 个 ensure 函数 ⇒ **一个都不许报错** ──────────
	//
	// ★★★ 这是本门最硬的一条断言，也是 R62 轮实际抓到缺陷的那条：
	//
	//   分区在、函数在，但函数造出来的**分区名**与该族既有约定不一致
	//   ⇒ 函数每次都判「不存在」→ 重建 → **边界重叠报错**
	//   ⇒ 该族永远无法自愈。
	//
	// 只看「分区在不在」是看不出来的 ⇒ 必须**真的调一次**。
	// （实测形态：ERROR: partition "request_logs_2026_10" would overlap
	//   partition "_request_logs_2026_10"）
	//
	// ★ 每个调用都在**独立事务**里做并 ROLLBACK ⇒ 建出来的分区随事务消失，
	//   ⇒ 目标库不留任何对象（误指生产也只是事务往返）。
	//   独立事务是为了**错误隔离**：一个函数报错不会让后面的调用全变成
	//   「current transaction is aborted」。
	for _, fn := range wantFns {
		var argSpec string
		if err := d.pool.QueryRow(ctx, `
			SELECT pg_get_function_identity_arguments(p.oid)
			  FROM pg_proc p
			  JOIN pg_namespace ns ON ns.oid = p.pronamespace
			 WHERE ns.nspname = 'public' AND p.proname = $1`, fn).Scan(&argSpec); err != nil {
			t.Fatalf("查函数签名 %s: %v", fn, err)
		}
		low := strings.ToLower(argSpec)
		var call string
		var arg any
		switch {
		case strings.Contains(low, "timestamp"):
			call = fmt.Sprintf("SELECT public.%s($1::timestamptz)", fn)
			arg = time.Now()
		case strings.Contains(low, "date"):
			call = fmt.Sprintf("SELECT public.%s($1::date)", fn)
			arg = time.Now().Format("2006-01-02")
		default:
			t.Fatalf("%s 的参数类型看不懂: %q", fn, argSpec)
		}

		tx, err := d.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("开启事务（%s）: %v", fn, err)
		}
		_, callErr := tx.Exec(ctx, call, arg)
		_ = tx.Rollback(ctx) // ★ 回滚 ⇒ 建出来的分区不留痕
		if callErr != nil {
			t.Errorf("★ 调用 %s 报错: %v\n"+
				"  ⇒ 运行期每 24h 调它都会得到同样的错（partition_manager 只 slog.Error 不中断）\n"+
				"  ⇒ 该族**无法自愈**：分区名与既有约定不符 ⇒ 函数判「不存在」→ 重建 → 边界重叠。\n"+
				"  ⇒ 典型形态：子分区前缀不能拿父表名去拼（`_request_logs` 族的子分区叫\n"+
				"     `request_logs_YYYY_MM`），必须按族内既有兄弟的众数命名推导。",
				fn, callErr)
		}
	}

	// ── ⑤ ★ 真的写一行 ⇒ 必须落在**当月的月分区**里（不是 default）──────────
	//
	// 「分区在」不等于「写得进去」⇒ 用一条真实写入收口。
	// 判定：落点分区名 = 该行自己 partition_date 的 YYYY_MM 后缀，且不是 default。
	// ★ 同样在**回滚事务**里做 ⇒ 目标库不留这一行。
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("开启事务（探针写入）: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	sid := fmt.Sprintf("r62-gate-%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_turns (session_id, turn_no, tenant_id, request_id)
		VALUES ($1, 1, 'r62-gate-tenant', $1)`, sid); err != nil {
		t.Fatalf("★ 写 session_turns 失败（这正是新装库的 23514 形态）: %v", err)
	}

	var landed string
	var partDate time.Time
	if err := tx.QueryRow(ctx, `
		SELECT tableoid::regclass::text, partition_date
		  FROM session_turns WHERE session_id = $1`, sid).Scan(&landed, &partDate); err != nil {
		t.Fatalf("读回探针行: %v", err)
	}
	wantSuffix := "_" + partDate.Format("2006_01")
	if strings.HasSuffix(landed, "_default") {
		t.Errorf("★ 探针行落到了 default 分区 %q ⇒ 当月分区缺失/边界不对"+
			"（默认分区会让写入**静默落错地方**，而不是报错 ⇒ 更难发现）", landed)
	}
	if !strings.HasSuffix(landed, wantSuffix) {
		t.Errorf("★ 探针行落到了 %q，但它自己的 partition_date 是 %s ⇒ 期望落在 *%s"+
			"（分区边界与月份对不上）", landed, partDate.Format("2006-01-02"), wantSuffix)
	}
}
