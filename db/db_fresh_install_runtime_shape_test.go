package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// db_fresh_install_runtime_shape_test.go
//
// 「全新安装 + 启动一次」之后的**运行时形态**不变量。
//
// # 这道门补的是哪个盲点
//
// 2026-10-21（R61 轮）复核 R60 的「全新安装缺 23 个列」结论时实测发现：
//
//	R60 的测量口径是「**装完、ensure 链没跑**」的一次性库 ⇒ 量到 23 条
//	「生产有、新装没有」的列。但产品**启动时**的 ensure 链（db.Open）
//	会自己 `ADD COLUMN IF NOT EXISTS` 补掉其中 **20 条**：
//
//	  models_canonical.standard_iq / .standard_iq_source / .standard_iq_updated_at
//	  credentials.is_free_tier / .free_quota_limit / .free_quota_window_type
//	  provider_catalog.has_free_tier / .free_tier_notes / .official_free_docs_url
//	  _request_logs.outbound_body
//	  session_turns.host / .tags
//
//	⇒ 同口径复算（装完 **且启动过**）：新装 9245 列 / 生产 14122 列
//	⇒ 差 26 条，**产品读写真正依赖的 = 0 条** ⇒ 「新装开不了库」已不存在。
//
// # 为什么这道门必须存在
//
// 上面那 20 列的「补齐」**只由 ensure 链负责**（它们不在 installer's 迁移链里：
// 350 在 domain 通道、015/075 之类的在 root 通道、session_turns.host/tags
// 与 _request_logs.outbound_body 由 db.go 的 ensure 加）。
// ⇒ ★ **谁把那几段 ensure 的 ALTER 删了，缺口就会静默回来**，
// 而且**两道现有真库门都看不见**：
//   - installer 迁移链的连续性门：管的是「登记的迁移还在不在」，与列无关；
//   - `sql/schema/fresh_install_column_gaps.tsv` 那道门：量的是「生产 vs 新装」的差集，
//     而这些列**装完就没了、启动才有** ⇒ 它的 oracle 必须是 post-ensure 形态
//     （那道门自己有量具自检：库里没有 models_canonical.standard_iq ⇒ 跳过）。
//
// ⇒ 所以这里把「ensure 自愈清单」钉成**显式不变量**：列不在 ⇒ 红。
//
// # 口径说明
//
// 这道门是**有副作用的**：它在 TEST_DATABASE_URL 指向的库上跑 db.Open，
// 也就是**真的启动一次产品**（ensure 链 + 迁移守卫）。
// ⇒ 只能指向**一次性库**（scripts/audit/probe-onetime-db-shape.sh 建的那种），
//
//	绝不要指向生产。
// canonicalMustProjectColumns 是 canonical 视图**必须投影**的列集合。
//
// ★ 为什么是「集合」而不是「列数」：
//   R69 轮实测过分类信息的载体契约——分类信息（request_class / due_at）的载体
//   换到了 `session_turn_details`，但 canonical 视图这一层**仍必须投影它们**
//   （否则读路径拿不到分类）。
//   R77 轮实测 canonical 视图 **118 列**，且 `customer_id` / `request_class` /
//   `due_at` **3/3 在位** ⇒ ⇒ 只差 customer_id 没被门钉住。
//
// ★ **刻意不钉绝对列数**（118 / 104 / 106 / 108 都钉过、都会腐烂）：
//   R74 轮实测 `session_turns` 列数 104→106→108 三次变化 ⇒ 绝对数 pin 必然腐烂。
//   要守的是「**这几列在不在**」，不是「一共几列」。
//
// ★★ 为什么还要一道静态 pin（TestCanonicalMustProjectColumnsIsPinned）：
//   R80 轮实测到一个真实陷阱——把 customer_id 从这个集合里删掉、把阈值改回 2，
//   **门照样绿**（它只断言「集合里的列都在」；集合自己变小，门当然满足）。
//   ⇒ ⇒ 形态门可以被**静默削弱**而不被任何人发现。
//   ⇒ ⇒ 所以集合的**内容**另有一道静态门钉住：删一个元素 ⇒ 静态门红
//   ⇒ ⇒ **削弱变成响亮的失败，而不是无声的退化**。
var canonicalMustProjectColumns = []string{
	"request_class",
	"due_at",
	"customer_id",
}

// TestCanonicalMustProjectColumnsIsPinned —— 契约集合的**内容**必须钉死。
//
// ★ 这道门防的是「静默削弱」：形态门断言的是「集合里的列都在」⇒ 有人把某一列
//   从集合里删掉（并把阈值改回集合大小），形态门**照样绿**。
//   ⇒ 削弱不会自己暴露 ⇒ 必须另有一道门把集合内容本身钉住。
//
// ★ 这道门**不需要 DSN**（纯静态）⇒ 任何环境下都会真跑，不会被 SKIP 掉。
func TestCanonicalMustProjectColumnsIsPinned(t *testing.T) {
	want := []string{"request_class", "due_at", "customer_id"}
	if len(canonicalMustProjectColumns) != len(want) {
		t.Fatalf("canonical 必投影列集合的大小变了：现在 %d 列（%v），期望 %d 列（%v）⇒ "+
			"若这是**有意**的契约变更，请同步改本门并在提交信息里写明理由；"+
			"若不是，说明有人为了让形态门变绿而静默削弱了契约",
			len(canonicalMustProjectColumns), canonicalMustProjectColumns, len(want), want)
	}
	for i := range want {
		if canonicalMustProjectColumns[i] != want[i] {
			t.Errorf("canonical 必投影列集合第 %d 项 = %q，期望 %q ⇒ 同上（静默削弱）",
				i, canonicalMustProjectColumns[i], want[i])
		}
	}
}

func TestFreshInstallRuntimeShape_EnsureChain(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过真库腿" +
			"（⚠ 本门会在目标库上跑 db.Open ⇒ 只能指向一次性库）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ⓪ ★ 形态锚点自检（oracle 有效性）—— **必须排在 db.Open 之前**。
	//
	// 这道门的 oracle 是「**全新安装 + 启动一次**」之后的真实形态。
	// ⇒ 若目标库压根不是全新安装形态（例如只跑了一半迁移链的手工子集库），
	//   db.Open 自己就会失败 ⇒ 那时的红是「**量具无效**」而不是「产品缺陷」
	//   ⇒ 所以先把量具有效性断言掉，不合格就**显式 SKIP 并说清缺哪一个**。
	//
	// ★ 为什么这道自检是必需的（R74 轮实测）：
	//   R69 轮拿一个「手工子集库」（缺 credentials / session_turns_hot /
	//   schema_migrations / request_logs_with_current_month）跑这道门 ⇒
	//   **它照样 PASS** ⇒ 当时据此得出了「库形态不可信」的结论，
	//   但门本身**一声不吭** ⇒ 这就是一道**漏检门**（自己缺关键对象时仍绿）。
	//   ⇒ 现在它会先自检：不是新装形态 ⇒ 诚实跳过，而不是假装通过。
	//
	// ★ 锚点选的是「ensure 链自己要改的那几张表」——它们由基线 01-schema.sql 建，
	//   缺了就说明**基线没跑过** ⇒ ensure 清单的判定没有意义。
	anchors := []string{"models_canonical", "credentials", "provider_catalog", "session_turns"}
	{
		probe, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("连目标库失败（做形态锚点自检）: %v", err)
		}
		var missing []string
		for _, a := range anchors {
			var n int
			if err := probe.QueryRow(ctx, `
				SELECT count(*) FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = 'public' AND c.relname = $1 AND c.relkind IN ('r', 'p')`,
				a).Scan(&n); err != nil {
				probe.Close()
				t.Fatalf("查形态锚点 %s: %v", a, err)
			}
			if n == 0 {
				missing = append(missing, a)
			}
		}
		probe.Close()
		if len(missing) > 0 {
			t.Skipf("目标库不是「全新安装」形态（缺形态锚点：%s）⇒ 本门的 oracle"+
				"（装完且启动过的真实形态）不成立 ⇒ 跳过，避免把量具失效报成产品缺陷"+
				"（R74 轮实测：这道门在手工子集库上曾「照绿不误」）",
				strings.Join(missing, ", "))
		}
	}

	// ① ★★★「**装完**」（还没启动）时的安装链形态不变量。
	//
	// ★ 为什么必须排在 db.Open **之前**（R74 轮变异实测出来的）：
	//   `session_turns_hot` 是**安装链**建的（installer 的
	//   `session_turns_hot_bootstrap.sql` + 706b + 707），
	//   **ensure 链不建它**（全仓 Go 代码里没有任何 CREATE session_turns_hot；
	//   db/db.go 只有读它的语句）⇒ 没有任何「ensure 之后」的检查覆盖它。
	//   ⇒ 而且 hot 表一旦缺失，**db.Open 自己就会死**：canonical 视图要按
	//   「会话族两面」重建，而两面之一就是 session_turns_hot
	//   （实测报错：`rebuild request_logs_with_current_month as session-only
	//   two-arm body: ERROR: relation "public.session_turns_hot" does not exist`）
	//   ⇒ ⇒ 若把断言排在 db.Open 之后，它在「hot 表缺失」这个场景里**根本走不到**
	//   （先被 db.Open 的报错打断）⇒ 只能报出一句看不懂的启动失败。
	//   ⇒ 排在这里 = 量的是**装完那一刻**的形态，且报的是**人话**。
	//
	// ★ 两条不变量：
	//   (a) `session_turns_hot` 必须存在；
	//       ★★ 但「缺它」有两种可能，必须**同源判别**（见下面实现处的注释）：
	//       「安装链跑过却没建出来」（真缺陷 ⇒ 红）
	//       vs「这个库压根没跑过安装链」（靶子不对 ⇒ 诚实 SKIP）。
	//   (b) hot 面与父表 `session_turns` 必须**逐列同形**
	//       （column_name / data_type / is_nullable / ordinal_position 四元组）
	//       —— 这是 707/869 维持的 lock-step 扩列不变量；
	//       hot 面一旦落后于父表，写路径就会把新列写丢。
	//
	// ★ 口径（2026-10-21 R74 轮实测，新装库 itgate_r74_hot）：
	//   session_turns / session_turns_hot 各 108 列，四元组逐列比较差集为空。
	{
		probe, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatalf("连目标库失败（查装完形态）: %v", err)
		}
		defer probe.Close()

		var hotExists bool
		if err := probe.QueryRow(ctx,
			`SELECT to_regclass('public.session_turns_hot') IS NOT NULL`).Scan(&hotExists); err != nil {
			t.Fatalf("查 session_turns_hot 是否存在: %v", err)
		}
		if !hotExists {
			// ★★★★ **同源判别**：这是本轮（2026-10-21 R80 续四）修掉的**假红**。
			//
			// ★ 机制：`session_turns_hot` **不由基线建**（`sql/schema/01-schema.sql`
			//   末尾自己写明：「Migration 526 historically owns session_turns_hot …
			//   Fresh installer bootstrap supplies their current final-state form
			//   through session_turns_hot_bootstrap.sql」）⇒ ⇒ **基线形态的库里
			//   它本来就不存在** ⇒ ⇒ 「它不在」**不能**直接判成「安装链没生效」。
			//
			// ★ 判别式 = **同一个 bootstrap 文件建的另一个产物**：
			//   `public.promote_session_turns_hot_to_partition`（函数）。
			//   ⇒ 选**函数**而不选视图，是因为视图依赖这张表 ⇒ 缺表时视图已被
			//     CASCADE 带走 ⇒ 拿它当判别式会在「真缺陷」那一支永远判错。
			//   ⇒ 该函数**基线里 0 命中**（实测）⇒ 它是**启动序列专属标记**。
			//
			// ★ 两条分支：
			//   函数**在** ⇒ 安装链**跑过** ⇒ 表不该缺 ⇒ ★ **真缺陷** ⇒ 红；
			//   函数**不在** ⇒ 这个库**根本没跑过安装链** ⇒ ★ **靶子不对**
			//     ⇒ 本门的前提（「全新安装形态」）不成立 ⇒ **诚实 SKIP**，
			//     而不是报一条会把人引向「产品缺陷」的假红。
			//
			// ★ 为什么必须修：R74 轮实测的 R69「手工子集库」正是这一类
			//   （缺 credentials / session_turns_hot / schema_migrations /
			//   request_logs_with_current_month，但**有** session_turns）
			//   ⇒ 旧写法在那上面会报「安装链的 session_turns_hot_bootstrap 没生效」
			//   ⇒ ⇒ **把量具失效报成了产品缺陷**。
			var promoteFnExists bool
			if err := probe.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
					WHERE n.nspname = 'public'
					  AND p.proname = 'promote_session_turns_hot_to_partition'
				)`).Scan(&promoteFnExists); err != nil {
				t.Fatalf("查同源 promote 函数是否存在: %v", err)
			}
			if promoteFnExists {
				t.Errorf("public.session_turns_hot 在「装完」时就不存在，**但同一个 bootstrap " +
					"文件建的 promote 函数在** ⇒ 安装链**跑过**却没建出这张表 ⇒ " +
					"真缺陷（安装链的 session_turns_hot_bootstrap 没生效）")
			} else {
				t.Skipf("目标库不是「全新安装」形态：public.session_turns_hot 缺，**且同一个 " +
					"bootstrap 文件建的 promote 函数也缺** ⇒ 这个库**没跑过安装链** " +
					"（基线形态里本来就没有这张表 ⇒ sql/schema/01-schema.sql 末尾有说明）⇒ " +
					"本门量的是「全新安装形态」，前提不成立 ⇒ 跳过，避免把**靶子不对**报成产品缺陷。" +
					"★ 要跑这道门，请用 scripts/audit/probe-onetime-db-shape.sh 建的「装完且启动过」形态库。")
			}
		} else {
			var sameShape bool
			if err := probe.QueryRow(ctx, `
				WITH parent_columns AS (
					SELECT column_name, data_type, is_nullable, ordinal_position
					FROM information_schema.columns
					WHERE table_schema = 'public' AND table_name = 'session_turns'
				), hot_columns AS (
					SELECT column_name, data_type, is_nullable, ordinal_position
					FROM information_schema.columns
					WHERE table_schema = 'public' AND table_name = 'session_turns_hot'
				)
				SELECT NOT EXISTS (
					(SELECT * FROM parent_columns EXCEPT SELECT * FROM hot_columns)
					UNION ALL
					(SELECT * FROM hot_columns EXCEPT SELECT * FROM parent_columns)
				)`).Scan(&sameShape); err != nil {
				t.Fatalf("比对 session_turns 与 session_turns_hot 的列形状: %v", err)
			}
			if !sameShape {
				t.Errorf("session_turns_hot 与父表 session_turns 的列形状不一致 ⇒ " +
					"hot 面落后于父表 ⇒ 707/869 的 lock-step 扩列断了 ⇒ 写路径会把新列写丢")
			}
		}
	}

	// ② 先跑一次 db.Open = 产品启动一次（ensure 链 + 迁移守卫）。
	d, err := Open(ctx, dsn)
	if err != nil {
		t.Fatalf("db.Open（= 产品启动一次）失败: %v", err)
	}
	if d == nil {
		t.Fatal("db.Open 返回 nil DB")
	}
	defer d.Close()

	// ③ ensure 自愈清单：每一列都必须在（少一列 ⇒ 有个 ensure 的 ALTER 被删了）。
	want := []struct{ table, column string }{
		{"models_canonical", "standard_iq"},
		{"models_canonical", "standard_iq_source"},
		{"models_canonical", "standard_iq_updated_at"},
		{"credentials", "is_free_tier"},
		{"credentials", "free_quota_limit"},
		{"credentials", "free_quota_window_type"},
		{"provider_catalog", "has_free_tier"},
		{"provider_catalog", "free_tier_notes"},
		{"provider_catalog", "official_free_docs_url"},
		// ★ `_request_logs.outbound_body` 已从本清单移除（2026-10-21 R62 轮）：
		//   并行会话的迁移 `890_drop_request_logs_v1_family.sql` **退役了 v1
		//   request_logs 族**（表与函数一起 DROP）⇒ 这一列**不再存在**
		//   ⇒ 要求它「装完+启动后在位」是**要求一个已退役对象复活**。
		{"session_turns", "host"},
		{"session_turns", "tags"},
	}
	for _, w := range want {
		var n int
		if err := d.pool.QueryRow(ctx, `
			SELECT count(*) FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`,
			w.table, w.column).Scan(&n); err != nil {
			t.Fatalf("查 %s.%s: %v", w.table, w.column, err)
		}
		if n == 0 {
			t.Errorf("ensure 自愈清单里的 %s.%s 在「装完 + 启动一次」之后仍然缺 ⇒ "+
				"对应的 ensure 段被删了或改了名 ⇒ 全新安装会回到「缺列」状态（R61 实测过这件事）",
				w.table, w.column)
		}
	}

	// ④ ★ canonical 视图必须投影 canonicalMustProjectColumns 里的每一列。
	//
	// ★ 为什么排在 db.Open **之后**（与 ① 相反）：db.Open 会把 canonical 视图
	//   按「会话族两面」形态**重建**（实测：ensure 链里有
	//   `rebuild request_logs_with_current_month as session-only two-arm body`）
	//   ⇒ 视图这一层的形态是**启动时**决定的 ⇒ 必须在启动之后量。
	//
	// ★★ 集合内容见 canonicalMustProjectColumns 的注释（契约来源：R69/R77 轮实测）。
	//   ★ 判据形态 = **集合投影**（「这几列在不在」），**不是**绝对列数 pin。
	var projected int
	if err := d.pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'request_logs_with_current_month'
		  AND column_name::text = ANY($1::text[])`,
		canonicalMustProjectColumns).Scan(&projected); err != nil {
		t.Fatalf("查 canonical 视图的必投影列: %v", err)
	}
	if projected != len(canonicalMustProjectColumns) {
		t.Errorf("canonical 视图 request_logs_with_current_month 只投影了 %d/%d 个必投影列"+
			"（%s）⇒ 分类信息与客户维度的读路径断了"+
			"（R69 轮实测过前两列的载体契约；R77 轮实测三列 3/3 在位）",
			projected, len(canonicalMustProjectColumns),
			strings.Join(canonicalMustProjectColumns, " / "))
	}
}
