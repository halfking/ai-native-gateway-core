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
