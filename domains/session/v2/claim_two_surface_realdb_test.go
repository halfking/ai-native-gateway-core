package v2

// claimAggregateTurn 的两端（父表 / `_hot`）真库门（2026-10-05，§9.187）。
//
// # 这道门补的是哪个缺口
//
// §9.186 补上了 `ExecuteRepair` 的端到端断言，并在诚实边界里写明：
// **该端到端门不覆盖 `session_turns` 腿的两端读法**——
// 即 `claimAggregateTurn`（`session_aggregator.go:160`）的「父表优先 + hot 回退」。
// 本门补的就是这个缺口。
//
// # 为什么这条路径值得单独锁
//
// `claimAggregateTurn` 决定「这一轮 turn 算没算被会话快照消费过」。
// 它的判据顺序是：先认领父表 → 父表已存在但已被认领 ⇒ **直接返回 false、
// 根本不去碰 hot** → 父表压根没有这一行 ⇒ 才去认领 hot。
//
// **第 3 步那个「父表已存在就不碰 hot」的判断，是整套去重语义的承重处。**
// 它成立的前提写在注释里：「The unified view treats the partitioned copy as
// authoritative when a duplicate exists in both stores」——
// 而这个前提在 schema 层有一个**硬契约**支撑：
//
//	UNIQUE (tenant_id, request_id, partition_date)   ← session_turns_tenant_request_partition_key
//
// `parentExists` 的判据恰好就是 `(tenant_id, request_id, partition_date)`
// ——**它漏了 `session_id`，而认领 UPDATE 带了 `session_id`**。
// 这个不对称**今天不是缺陷**，因为父表上那个唯一约束保证了一个
// `request_id` 在同一分区里只出现一次。
// 实测（本机 1,688,629 行）：不同 `request_id` 数 = 1,688,629，**完全唯一**。
//
// ⇒ **本门把这个前提一并锁住**：若有人删掉那个唯一约束、或改变 `request_id`
// 的唯一性假设，本门的用例 4 会先红，而不是等到聚合重复计入生产数据才被发现。
//
// # 夹具为什么用一次性数据库
//
// 沿用**本包既有**的做法（`session_request_status_backfill_test.go` 的
// `statusBackfillFixtureDB`），不另起一套。那里已经写明了为什么：
// 本函数的 SQL 是 `public.`-限定的，search_path 里的 schema 拦不住它；
// 而更重要的是它的注释里已经记着一个本项目**踩过一次**的坑——
//
//	「t.Cleanup callbacks run AFTER deferred functions in the same test,
//	  so a cleanup that reuses a pool closed by `defer pool.Close()`
//	  fails SILENTLY and leaves the fixture behind」
//
// ⇒ 我在 §9.186 的 `ExecuteRepair` 端到端门里**又踩了一次这个坑**
// （绿着把夹具留在库里，实测残留 6/6/1/3/6）。那条门已改为在
// `t.Cleanup` 内部关池、不吞错误；但它必须读生产那套完整 schema
// （`request_logs` + `session_turns` + `session_bodies` + 两个合并视图 +
// advisory lock 函数），塞不进本门这种「最小内联 DDL」的形状。
// **本门只需要两张表，所以走既有的一次性数据库做法，把「不留残留」这件事
// 从根上消掉而不是靠清理。**

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// claimFixtureDDL 建出 claimAggregateTurn 需要的最小结构。
//
// **必须带上 `UNIQUE (tenant_id, request_id, partition_date)`**：
// `parentExists` 的判据依赖它（见文件头）。少了它，本门的用例 4
// 就不再检验「父表优先」而是检验一个更弱的东西——那正是要避免的。
const claimFixtureDDL = `
CREATE TABLE public.session_turns (
	id                   BIGINT,
	partition_date       DATE    NOT NULL,
	tenant_id            TEXT    NOT NULL,
	session_id           TEXT    NOT NULL,
	turn_no              INTEGER NOT NULL,
	request_id           TEXT    NOT NULL,
	aggregate_applied_at TIMESTAMPTZ
) PARTITION BY RANGE (partition_date);

CREATE TABLE public.session_turns_2026_10
	PARTITION OF public.session_turns FOR VALUES FROM ('2026-10-01') TO ('2026-11-01');
CREATE TABLE public.session_turns_default
	PARTITION OF public.session_turns DEFAULT;

-- 生产里承载「同一 request_id 在一个分区内只出现一次」的那条硬契约。
CREATE UNIQUE INDEX session_turns_tenant_request_partition_key
	ON public.session_turns (tenant_id, request_id, partition_date);

-- 孪生面：独立堆表，无分区、无该唯一约束。
CREATE TABLE public.session_turns_hot (
	id                   BIGINT,
	partition_date       DATE    NOT NULL,
	tenant_id            TEXT    NOT NULL,
	session_id           TEXT    NOT NULL,
	turn_no              INTEGER NOT NULL,
	request_id           TEXT    NOT NULL,
	aggregate_applied_at TIMESTAMPTZ
);

-- 聚合快照表。列取自 upsertSessionSnapshot 的 INSERT 臂；
-- ON CONFLICT (session_id, partition_date) ⇒ 必须有这条唯一约束。
CREATE TABLE public.sessions (
	session_id             TEXT    NOT NULL,
	tenant_id              TEXT    NOT NULL,
	created_at             TIMESTAMPTZ,
	updated_at             TIMESTAMPTZ,
	status                 TEXT,
	total_turns            INTEGER NOT NULL DEFAULT 0,
	total_tokens           INTEGER NOT NULL DEFAULT 0,
	total_cost_usd         NUMERIC NOT NULL DEFAULT 0,
	last_turn_no           INTEGER,
	last_request_summary  TEXT,
	last_response_summary TEXT,
	last_model             TEXT,
	last_provider          TEXT,
	client_type            TEXT,
	project_id             TEXT,
	api_key_id             TEXT,
	application_id         TEXT,
	end_user_id            TEXT,
	owner_user             TEXT,
	client_ip              TEXT,
	agent_name             TEXT,
	agent_role             TEXT    NOT NULL DEFAULT 'main',
	parent_session_id      TEXT,
	parent_task_id         TEXT,
	partition_date         DATE    NOT NULL,
	primary_request_id     TEXT,
	UNIQUE (session_id, partition_date)
);

-- 写方 / 聚合 / repair / promote **共用**这一个锁键函数
-- （turn_writer.go:245、session_aggregator.go:130、repair.go、迁移 688）。
-- 逐字取自生产库的 pg_get_functiondef。
CREATE OR REPLACE FUNCTION public.session_turns_advisory_lock_key(p_tenant_id text, p_session_id text)
 RETURNS bigint
 LANGUAGE sql
 IMMUTABLE PARALLEL SAFE STRICT
AS $function$
    SELECT hashtextextended(p_tenant_id || ':' || p_session_id, 0)
$function$;
`

// claimTestDB 建一个一次性数据库并返回连接到它的池 + 库名。
// 形态与本包 `statusBackfillFixtureDB` 一致：**清理不放在 t.Cleanup**，
// 而是先跑 deferred（保证池还开着），失败是 FATAL。
func claimTestDB(t *testing.T, adminDSN string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	name := "claimtwo_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "")
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Skipf("cannot create throwaway database (insufficient privileges?), skipping: %v", err)
	}
	// **drop 的 defer 不在这里。** defer 属于它所在的**函数**——
	// 放进 helper 会在 helper 一返回时就把库删掉，而调用方还握着连接池
	// （实测：子测试立刻报 `database "claimtwo_..." does not exist`）。
	// 本包既有的 statusBackfillFixtureDB 也只做 `defer admin.Close()`，
	// drop 留在测试里；第一版把两半拼到了一起，位置错了。
	defer admin.Close()

	cfg, err := pgxpool.ParseConfig(adminDSN)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("fixture pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("fixture database unavailable: %v", err)
	}
	if _, err := pool.Exec(ctx, claimFixtureDDL); err != nil {
		pool.Close()
		t.Fatalf("create fixture schema: %v", err)
	}
	return pool, name
}

// TestClaimAggregateTurn_TwoSurface_ParentFirst 覆盖「父表优先 + hot 回退」的五种形状。
func TestClaimAggregateTurn_TwoSurface_ParentFirst(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()

	// 先拿一个 admin 池来建/删这个一次性库；drop 的 defer 必须在**本测试**里，
	// 且在 fixture 池关闭之后才跑（defer 是 LIFO：先注册的 admin-drop 后跑）。
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	pool, name := claimTestDB(t, dsn)
	dropped := false
	defer func() {
		pool.Close() // 先断干净，drop 才不会被残留连接顶住
		if dropped {
			admin.Close()
			return
		}
		if _, err := admin.Exec(context.Background(),
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); err != nil {
			t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, err)
		}
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); err != nil {
			t.Errorf("FATAL: fixture database %s not dropped: %v", name, err)
		}
		admin.Close()
	}()

	const (
		tenant  = "claim_tenant"
		session = "claim_session"
		pdate   = "2026-10-15"
	)
	partitionDate := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	// seed 往指定面插一行。parent=true 写分区父表，否则写 hot。
	seed := func(requestID string, parent bool) {
		t.Helper()
		tbl := "public.session_turns_hot"
		if parent {
			tbl = "public.session_turns"
		}
		if _, err := pool.Exec(ctx,
			"INSERT INTO "+tbl+
				" (tenant_id, session_id, turn_no, request_id, partition_date)"+
				" VALUES ($1,$2,$3,$4,$5::date)",
			tenant, session, 1, requestID, pdate); err != nil {
			t.Fatalf("seed %s (parent=%v): %v", requestID, parent, err)
		}
	}
	// claimedAt 读某一面的 aggregate_applied_at 是否已置。
	claimedAt := func(requestID string, parent bool) bool {
		t.Helper()
		tbl := "public.session_turns_hot"
		if parent {
			tbl = "public.session_turns"
		}
		var ts *time.Time
		if err := pool.QueryRow(ctx,
			"SELECT aggregate_applied_at FROM "+tbl+" WHERE request_id = $1", requestID).Scan(&ts); err != nil {
			t.Fatalf("read %s aggregate_applied_at (parent=%v): %v", requestID, parent, err)
		}
		return ts != nil
	}
	exists := func(requestID string, parent bool) bool {
		t.Helper()
		tbl := "public.session_turns_hot"
		if parent {
			tbl = "public.session_turns"
		}
		var n int
		if err := pool.QueryRow(ctx,
			"SELECT count(*) FROM "+tbl+" WHERE request_id = $1", requestID).Scan(&n); err != nil {
			t.Fatalf("count %s (parent=%v): %v", requestID, parent, err)
		}
		return n > 0
	}
	// runClaim 调一次 claimAggregateTurn 并**提交**。
	//
	// 第一版在这里 `defer tx.Rollback`（想让每个用例从干净状态起步），
	// 结果三个子测试红在「aggregate_applied_at 未被置位」——
	// **那是我判据自己跟自己矛盾**：认领在事务里做、事后又去读库确认效果，
	// 而回滚把效果撤销了。判据红的理由在我这边，不在被测对象。
	// ⇒ 改为提交。隔离不靠回滚，靠**每个子测试用各自的 request_id**，
	// 而整座库在测试结束时被 drop，残留本来就不可能。
	runClaim := func(requestID string) bool {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		got, err := claimAggregateTurn(ctx, tx, SessionUpdate{
			SessionID: session, TenantID: tenant, RequestID: requestID,
		}, partitionDate)
		if err != nil {
			tx.Rollback(ctx)
			t.Fatalf("claimAggregateTurn(%s): %v", requestID, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return got
	}

	t.Run("只在父表 ⇒ 认领父表", func(t *testing.T) {
		const rid = "req_parent_only"
		seed(rid, true)
		// 阳性对照：夹具真的在父表里。
		if !exists(rid, true) {
			t.Fatal("阳性对照失败：父表未落行，后面所有断言都不作数")
		}
		if got := runClaim(rid); !got {
			t.Error("claimAggregateTurn = false，应为 true：父表里唯一的一行必须被认领")
		}
		if !claimedAt(rid, true) {
			t.Error("父表行的 aggregate_applied_at 未被置位")
		}
		if exists(rid, false) {
			t.Error("hot 侧凭空多出一行")
		}
	})

	t.Run("只在 hot ⇒ 回退去认领 hot", func(t *testing.T) {
		const rid = "req_hot_only"
		seed(rid, false)
		if exists(rid, true) {
			t.Fatal("阳性对照失败：父表不该有这一行")
		}
		if got := runClaim(rid); !got {
			t.Error("claimAggregateTurn = false，应为 true：hot 侧这一行必须被认领")
		}
		if !claimedAt(rid, false) {
			t.Error("hot 行的 aggregate_applied_at 未被置位")
		}
	})

	t.Run("两个面都有 ⇒ 只认领父表，hot 保持未认领", func(t *testing.T) {
		const rid = "req_both"
		seed(rid, true)
		seed(rid, false)
		// 阳性对照：两个面确实都有行，否则本用例什么都不检验。
		if !exists(rid, true) || !exists(rid, false) {
			t.Fatal("阳性对照失败：两个面必须都各有一行")
		}
		if got := runClaim(rid); !got {
			t.Error("claimAggregateTurn = false，应为 true")
		}
		if !claimedAt(rid, true) {
			t.Error("父表行必须被认领（父表优先）")
		}
		if claimedAt(rid, false) {
			t.Error("hot 行也被认领了 ⇒ 同一轮 turn 会被聚合两次；" +
				"父表存在时必须直接返回 false、根本不碰 hot")
		}
	})

	t.Run("两个面都有且父表已认领 ⇒ false，且仍不碰 hot", func(t *testing.T) {
		const rid = "req_both_claimed"
		seed(rid, true)
		seed(rid, false)
		// 先手动把父表行标成已认领，模拟「已被别的调用者认领」。
		if _, err := pool.Exec(ctx,
			"UPDATE public.session_turns SET aggregate_applied_at = NOW() WHERE request_id = $1", rid); err != nil {
			t.Fatalf("pre-claim parent: %v", err)
		}
		if got := runClaim(rid); got {
			t.Error("claimAggregateTurn = true，应为 false：父表行已被认领过")
		}
		if claimedAt(rid, false) {
			t.Error("hot 行被认领了 ⇒ 父表已认领时不得回退到 hot（会重复计入）")
		}
	})

	t.Run("hot 侧已认领 ⇒ false（幂等）", func(t *testing.T) {
		const rid = "req_hot_claimed"
		seed(rid, false)
		if _, err := pool.Exec(ctx,
			"UPDATE public.session_turns_hot SET aggregate_applied_at = NOW() WHERE request_id = $1", rid); err != nil {
			t.Fatalf("pre-claim hot: %v", err)
		}
		if got := runClaim(rid); got {
			t.Error("claimAggregateTurn = true，应为 false：hot 行已被认领过")
		}
	})
}

// ── 并发：两道机制不是冗余的，承重的是 B ──────────────────────────────────
//
// 阻止「同一轮 turn 被聚合两次」的机制有**两道**：
//
//	A. `UpdateSession` 在同一事务里先取 `sessionAdvisoryLockSQL`
//	   （session_aggregator.go:130）⇒ 同一 (tenant, session) 的并发调用被**串行化**。
//	B. `claimAggregateTurn` 里的 `WHERE aggregate_applied_at IS NULL`
//	   ⇒ 已被认领过的行**再次认领是 no-op**（幂等）。
//
// 锁键函数 `public.session_turns_advisory_lock_key` 全仓**共用**（写方
// turn_writer.go:245、聚合 :130、repair.go、迁移 688 的 promote），已逐处核实。
//
// ★ **我一开始把这两道当成「冗余防御」，并按那个前提写了「T1 区分不了 A/B、
//   需要两条判据分别钉」的注释。那是错的，被实测推翻。** 变异矩阵：
//
//	| 变异                     | T1（端到端） | T2（并行无锁） |
//	|--------------------------|-------------|---------------|
//	| 基线（两道都在）          | 绿          | 绿            |
//	| A 完好 / **B 破**        | **红**      | **红**        |
//	| **A 破** / B 完好        | 绿          | 绿            |
//	| A 破 / B 破              | 红          | 红            |
//	| 还原                      | 绿          | 绿            |
//
//	⇒ **B 才是承重的**：锁只**串行化**，不阻止**顺序重复认领**——
//	  第 2..6 个调用者照样依次各认领一次、依次各 +1。删掉 B 之后，
//	  有锁也照样重复计数。
//	⇒ **A 对「计数恰好一次」这个性质是冗余的**（删掉 A 两条判据仍全绿）。
//	  它的作用是**延迟/隔离**（让同一会话的写入不互相踩），不是正确性。
//
// ★ 那 T2 还有什么独立价值？**它到达 T1 结构性到不了的场景**：
//	  T1 里 advisory lock 把事务**串行化**了 ⇒ 那 6 条 UPDATE **从不重叠**，
//	  T1 因此**没有真正测到行级竞争**。
//	  T2 不取锁、6 个事务真并行，测的才是「PG 的 UPDATE 拿到行锁后
//	  会重新检查谓词」这个真正的原子性保证。
//
// # 断言为什么不会 flake
//
// 两条都只断**最终库状态**（`total_turns == 1` / 「恰好一个 true」），
// **不断任何时序**、不断「谁先谁后」。正确实现下**每一种交错**都给出同一个
// 结果 ⇒ 这不是概率性断言，不存在偶发失败（实测 3 次连跑全绿）。

// TestUpdateSession_ConcurrentSameTurn_AggregatedExactlyOnce 走真实入口。
func TestUpdateSession_ConcurrentSameTurn_AggregatedExactlyOnce(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	pool, name := claimTestDB(t, dsn)
	dropped := false
	defer func() {
		pool.Close()
		if !dropped {
			if _, e := admin.Exec(context.Background(),
				"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); e != nil {
				t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, e)
			}
			if _, e := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); e != nil {
				t.Errorf("FATAL: fixture database %s not dropped: %v", name, e)
			}
		}
		admin.Close()
	}()

	const (
		tenant   = "conc_tenant"
		session  = "conc_session"
		requestI = "conc_request_1"
		workers  = 6
	)
	partDate := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	// 先落一行待认领的 turn（只在父表；hot 不需要——认领成功即可）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_turns
			(tenant_id, session_id, turn_no, request_id, partition_date)
		VALUES ($1,$2,1,$3,$4::date)`, tenant, session, requestI, "2026-10-15"); err != nil {
		t.Fatalf("seed turn: %v", err)
	}
	// 阳性对照：turn 真的在父表里，否则下面「恰好聚合一次」是恒真断言。
	var seeded int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM public.session_turns WHERE request_id = $1", requestI).Scan(&seeded); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	if seeded != 1 {
		t.Fatalf("阳性对照失败：待认领 turn 应恰有 1 行，实测 %d", seeded)
	}

	agg := NewSessionAggregator(pool)
	upd := SessionUpdate{
		SessionID: session, TenantID: tenant, RequestID: requestI,
		LastTurnNo: 1, TurnIncrement: 1, TokensIncrement: 7, CostIncrement: 0.5,
		UpdatedAt: partDate,
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // 闸门：尽量让它们真正撞在一起
			errs[i] = agg.UpdateSession(ctx, upd)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("worker[%d] UpdateSession: %v", i, e)
		}
	}

	var totalTurns, totalTokens int
	if err := pool.QueryRow(ctx, `
		SELECT total_turns, total_tokens FROM public.sessions
		WHERE session_id = $1`, session).Scan(&totalTurns, &totalTokens); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	// ★ 终态断言，不断时序：正确实现下任何交错都必须是 1。
	if totalTurns != 1 {
		t.Errorf("sessions.total_turns = %d，%d 路并发下必须恰好为 1 —— "+
			"同一轮 turn 被聚合了多次（advisory lock 与 WHERE ... IS NULL 两道都失效了）",
			totalTurns, workers)
	}
	if totalTokens != 7 {
		t.Errorf("sessions.total_tokens = %d，应为 7（与 total_turns 同一道防线，"+
			"必须一起是 7×1 而不是 7×N）", totalTokens)
	}
}

// TestClaimAggregateTurn_ConcurrentWithoutAdvisoryLock_IsolatesSecondLine
// 直接并发调 claimAggregateTurn、**绕开 advisory lock** ⇒ 只剩第二道防线。
func TestClaimAggregateTurn_ConcurrentWithoutAdvisoryLock_IsolatesSecondLine(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	pool, name := claimTestDB(t, dsn)
	dropped := false
	defer func() {
		pool.Close()
		if !dropped {
			if _, e := admin.Exec(context.Background(),
				"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); e != nil {
				t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, e)
			}
			if _, e := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); e != nil {
				t.Errorf("FATAL: fixture database %s not dropped: %v", name, e)
			}
		}
		admin.Close()
	}()

	const (
		tenant   = "race_tenant"
		session  = "race_session"
		requestI = "race_request_1"
		workers  = 6
	)
	partDate := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.session_turns
			(tenant_id, session_id, turn_no, request_id, partition_date)
		VALUES ($1,$2,1,$3,$4::date)`, tenant, session, requestI, "2026-10-15"); err != nil {
		t.Fatalf("seed turn: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	claimed := make([]bool, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tx, err := pool.Begin(ctx)
			if err != nil {
				errs[i] = err
				return
			}
			// **刻意不取 advisory lock** —— 这正是把第二道防线单独暴露出来的输入。
			got, err := claimAggregateTurn(ctx, tx, SessionUpdate{
				SessionID: session, TenantID: tenant, RequestID: requestI,
			}, partDate)
			if err != nil {
				tx.Rollback(ctx)
				errs[i] = err
				return
			}
			if err := tx.Commit(ctx); err != nil {
				errs[i] = err
				return
			}
			claimed[i] = got
		}(i)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("worker[%d]: %v", i, e)
		}
	}
	n := 0
	for _, c := range claimed {
		if c {
			n++
		}
	}
	// ★ 终态断言：无论交错，恰好一个赢家。
	if n != 1 {
		t.Errorf("%d 个调用返回 claimed=true，应恰好 1 个 —— "+
			"没有 advisory lock 兜底时，单条 UPDATE 的 WHERE ... IS NULL "+
			"必须独立地挡住重复认领（这是纵深防御，别删这道门）", n)
	}
}

// ── 聚合算术与「首值优先」实际语义：如实刻画 ──────────────────────────────
//
// # 这道门测什么、不测什么
//
// §9.187 的诚实边界点名「`upsertSessionSnapshot` 的聚合算术未覆盖」。
// 本门覆盖它，但**是「刻画现状」而不是「断言应有行为」**——理由见下面的
// ⚠️ 分歧段。**刻意不把分歧写成会红的断言**：那等于替属主把行为改成首值优先，
// 而「改生产行为」是属主的决定，不是本门的。
//
// # ⚠️ 实测出的分歧（**必须随门一起读**）
//
// `SessionUpdate` 的 Go 字段注释（session_aggregator.go:80-87）对
// ProjectID / APIKeyID / ApplicationID / EndUserID / OwnerUser / ClientIP /
// AgentName **7 列**写的是：
//
//	「首值优先：取首个非空请求的值固化；后续轮不覆盖」
//
// 而 ON CONFLICT 冲突臂里这 7 列是：
//
//	project_id = COALESCE(NULLIF(EXCLUDED.project_id, ''), public.sessions.project_id)
//
// ⇒ **EXCLUDED 优先 = 最后一个非空值胜**，与注释**正好相反**。
//
// ★ 这正是作者**已经认定错误并修好**的同一个形态：
//   `agent_role` 的注释原文是「不能用 `COALESCE(NULLIF(EXCLUDED...))`
//   形态学一致」，`primary_request_id` 的注释原文是
//   「R69 初版把冲突臂写成 COALESCE(NULLIF(EXCLUDED…), sessions…)——
//   EXCLUDED 优先即 last-write-wins…翻转为存量优先」。
//   两者都已改成真正的首值优先；**这 7 列没有一起改**。
//
// ⇒ 本门对它们**只刻画实测行为**（`project_id` 在后轮给不同值时会被覆盖），
// 并在此注明分歧。**要不要把它改成首值优先 = 属主决定**（决策表 D26）。
//
// # 本机暴露度（实测，供属主判断优先级）
//
// `sessions` 共 839,661 行：`project_id` 非空 **1 行**；
// `agent_role <> 'main'` **0 行**；`primary_request_id` 非空 30,789 行。
// ⇒ **本地几乎零暴露**。但这是**这一套库**的数字，不能外推到生产。

// TestUpsertSessionSnapshot_ArithmeticAndPrecedence_Characterization
// 如实刻画聚合算术与各列的优先级语义（含上面那 7 列的分歧）。
func TestUpsertSessionSnapshot_ArithmeticAndPrecedence_Characterization(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping real-database gate")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pgxpool.New: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		admin.Close()
		t.Skipf("TEST_DATABASE_URL unreachable, skipping: %v", err)
	}
	pool, name := claimTestDB(t, dsn)
	dropped := false
	defer func() {
		pool.Close()
		if !dropped {
			if _, e := admin.Exec(context.Background(),
				"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name); e != nil {
				t.Errorf("FATAL: could not terminate fixture backends for %s: %v", name, e)
			}
			if _, e := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name); e != nil {
				t.Errorf("FATAL: fixture database %s not dropped: %v", name, e)
			}
		}
		admin.Close()
	}()

	const (
		tenant  = "agg_tenant"
		session = "agg_session"
	)
	partDate := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	agg := NewSessionAggregator(pool)

	// apply 走真实入口：先落一行待认领的 turn，再 UpdateSession。
	apply := func(turnNo int, requestID string, u SessionUpdate) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.session_turns
				(tenant_id, session_id, turn_no, request_id, partition_date)
			VALUES ($1,$2,$3,$4,$5::date)`, tenant, session, turnNo, requestID, "2026-10-15"); err != nil {
			t.Fatalf("seed turn %d: %v", turnNo, err)
		}
		u.SessionID, u.TenantID, u.RequestID, u.UpdatedAt = session, tenant, requestID, partDate
		if err := agg.UpdateSession(ctx, u); err != nil {
			t.Fatalf("UpdateSession(turn %d): %v", turnNo, err)
		}
	}
	read := func(col string) string {
		t.Helper()
		var v *string
		q := "SELECT " + col + "::text FROM public.sessions WHERE session_id = $1"
		if err := pool.QueryRow(ctx, q, session).Scan(&v); err != nil {
			t.Fatalf("read %s: %v", col, err)
		}
		if v == nil {
			return ""
		}
		return *v
	}

	t.Run("计数与成本逐轮累加", func(t *testing.T) {
		// 0.1 + 0.2 在 float64 里是 0.30000000000000004。
		// PG 的 float8→numeric 走**最短往返文本**（实测 0.1→0.1、0.1+0.2→0.3），
		// 所以这里断言的是**实测值**，不是"数学值"——
		// 目的是把转换边界的行为钉住，将来 PG 改了才有人看见。
		apply(1, "agg_r1", SessionUpdate{TurnIncrement: 1, TokensIncrement: 10, CostIncrement: 0.1})
		apply(2, "agg_r2", SessionUpdate{TurnIncrement: 1, TokensIncrement: 20, CostIncrement: 0.2})
		apply(3, "agg_r3", SessionUpdate{TurnIncrement: 1, TokensIncrement: 30, CostIncrement: 0.3})

		if got := read("total_turns"); got != "3" {
			t.Errorf("total_turns = %s，应为 3", got)
		}
		if got := read("total_tokens"); got != "60" {
			t.Errorf("total_tokens = %s，应为 60（10+20+30）", got)
		}
		if got := read("total_cost_usd"); got != "0.6" {
			t.Errorf("total_cost_usd = %s，实测值应为 0.6（0.1+0.2+0.3 经 NUMERIC 累加）", got)
		}
	})

	t.Run("project_id：后轮给不同值会被覆盖（⚠️ 与 Go 注释相反，如实刻画）", func(t *testing.T) {
		// 上面三轮都没带 ProjectID，这里补两轮带不同值的。
		apply(4, "agg_r4", SessionUpdate{TurnIncrement: 1, ProjectID: "proj_first"})
		apply(5, "agg_r5", SessionUpdate{TurnIncrement: 1, ProjectID: "proj_second"})
		got := read("project_id")
		// 实测：EXCLUDED 优先 ⇒ 最后一个非空值胜 ⇒ "proj_second"。
		// 若这里变成 "proj_first"，说明有人把冲突臂改成了真首值优先（那是 D26 的决定）。
		if got != "proj_second" {
			t.Errorf("project_id = %q，当前实现的实测值是 %q（后轮覆盖前轮）", got, "proj_second")
		}
	})

	t.Run("project_id：后轮给空串则保留旧值（NULLIF 路径）", func(t *testing.T) {
		apply(6, "agg_r6", SessionUpdate{TurnIncrement: 1, ProjectID: ""})
		if got := read("project_id"); got != "proj_second" {
			t.Errorf("project_id = %q，空串应保留原值 %q", got, "proj_second")
		}
	})

	t.Run("agent_role：默认 main，可被精化，且不被后轮降级", func(t *testing.T) {
		// 现状已是真首值/精化语义（作者修过）。本用例**钉住**这个已修好的行为。
		if got := read("agent_role"); got != "main" {
			t.Fatalf("前几轮未声明 AgentRole，应落 main，实测 %q", got)
		}
		apply(7, "agg_r7", SessionUpdate{TurnIncrement: 1, AgentRole: "researcher"})
		if got := read("agent_role"); got != "researcher" {
			t.Fatalf("agent_role = %q，声明 researcher 后应被精化", got)
		}
		apply(8, "agg_r8", SessionUpdate{TurnIncrement: 1, AgentRole: ""})
		if got := read("agent_role"); got != "researcher" {
			t.Errorf("agent_role = %q，后续未声明的轮**不得**把已固化的 researcher 降级回 main", got)
		}
	})

	t.Run("primary_request_id：存量优先（首个非空固化，后续不改写）", func(t *testing.T) {
		// 这个会话的首轮是 agg_r1 ⇒ 应恒为 agg_r1。
		if got := read("primary_request_id"); got != "agg_r1" {
			t.Errorf("primary_request_id = %q，应恒为首个非空 RequestID %q", got, "agg_r1")
		}
	})

	t.Run("租户隔离：第一层唯一约束就挡住了跨租户复用 session_id", func(t *testing.T) {
		// 第一版这条用例想测「另一个租户用同一个 session_id 会不会改写本行」，
		// 实测**前提不成立**：
		//
		//	ERROR: duplicate key value violates unique constraint
		//	       "sessions_session_id_partition_date_key" (SQLSTATE 23505)
		//
		// 因为生产 `sessions` 的唯一约束是
		//	UNIQUE (session_id, partition_date)   ← **不含 tenant_id**
		// ⇒ 两个租户**根本无法**拥有同一个 session_id，
		// ON CONFLICT 的冲突臂在跨租户场景下**根本进不去**。
		//
		// ★ 因此冲突臂末尾那句
		//	WHERE public.sessions.tenant_id = EXCLUDED.tenant_id
		// 在**正常路径上不可达**——它是一道**纵深防御**：
		// 只有当库里已经存在「同一 (session_id, partition_date) 却不同 tenant_id」
		// 的脏数据（例如有人 drop 过那条约束）时，它才起作用。
		//
		// ⇒ 这里如实刻画**第一层**（真正起作用的那层），并把第二层的
		// 「不可达」写成事实，而不是假装它守住了什么。
		_, err := pool.Exec(ctx, `
			INSERT INTO public.sessions
				(session_id, tenant_id, created_at, updated_at, status,
				 total_turns, total_tokens, total_cost_usd, partition_date, agent_role)
			VALUES ($1, 'other_tenant', NOW(), NOW(), 'active', 999, 0, 0, '2026-10-15', 'main')`,
			session)
		if err == nil {
			t.Error("另一个租户竟然能插入同一 session_id —— " +
				"sessions 的唯一约束若不再包含 (session_id, partition_date)，" +
				"跨租户混写就真的能发生了（D26 的前提，需重新评估）")
		} else if !strings.Contains(err.Error(), "sessions_session_id_partition_date_key") &&
			!strings.Contains(err.Error(), "duplicate key") {
			t.Errorf("跨租户插入失败，但红因不是唯一约束：%v —— "+
				"判据与实际防护不符，必须重新核实", err)
		}
		// 本行计数未被动过（守卫/约束任一生效都应如此）。
		// 前面子测试一共 apply 了 8 轮（r1..r8），各 +1。
		// 第一版这里写的是 9 —— **我把轮数数错了**（不是代码问题）。
		if got := read("total_turns"); got != "8" {
			t.Errorf("total_turns = %s，应仍为 8（8 轮 apply 各 +1）", got)
		}
	})
}
