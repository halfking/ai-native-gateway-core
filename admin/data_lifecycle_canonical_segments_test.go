package admin

// 判据：data-lifecycle/stats 的总量/分段/按租户必须走**单次规范源扫描**，
// 且口径与「v1 减去与 session_turns 重叠的那部分」严格一致。
//
// 背景（154，2726-87bcc80d）：端点稳定 **500@30.05s**，且在数据库完全空闲时
// 3/3 必现（不是争抢）。真库实测：
//
//	SELECT COUNT(*) FROM _request_logs                  →  280 ms（338,976 行）
//	SELECT COUNT(*) FROM request_logs_with_current_month →  >60 s，跑不完
//
// 原实现为拿「总量 / 四段 / 按租户」发了 3 条查询，合计约 9 次视图全扫
// （分段那条内部还有 total CTE + 4 个 UNION ALL）。
//
// ⚠ 去重条件不能省：它剔掉 338,976 行里的 135,546 行（40%），
//   去掉会让所有段位计数虚高 40%。本文件对「去重后的总量」做独立重算，
//   防止有人为了快把 NOT EXISTS 删掉——那样判据会立刻红。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDataLifecycleDedupSegmentsMatchesIndependentRecompute —— 真库层，
// 同层调 dataLifecycleCanonicalSegmentsSQL（生产函数，不是手抄的 SQL）。
func TestDataLifecycleCanonicalSegmentsMatchViewSemantics(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	const allScope = "__ALL__"
	var total, hot, warm, cold, expired int64
	var tenants int
	// 只跑一次：这条查询本身要几秒，判据里重复执行会把「判据耗时」
	// 和「被测查询耗时」混在一起，最后分不清是谁慢。
	start := time.Now()
	rows, err := pool.Query(ctx, dataLifecycleCanonicalSegmentsSQL(""))
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	defer rows.Close()
	got := false
	for rows.Next() {
		var scope string
		var tot, h, w, c, e int64
		if err := rows.Scan(&scope, &tot, &h, &w, &c, &e,
			new(int64), new(string), new(int64)); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if scope == allScope {
			total, hot, warm, cold, expired = tot, h, w, c, e
			got = true
			continue
		}
		tenants++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if !got {
		t.Fatal("没有 __ALL__ 行：handler 依赖它填 TotalRows，缺了会让四段占比全变 0")
	}

	// ★ 正向下限。只断言「没报错」的判据，在「干脆没算」面前永远是绿的。
	if total <= 0 {
		t.Fatalf("去重后总量为 %d：端点会报出 total_rows=0", total)
	}
	if sum := hot + warm + cold + expired; sum != total {
		t.Errorf("四段之和 %d ≠ 总量 %d：四个时间边界必须无重叠无遗漏", sum, total)
	}

	// ★ 独立重算视图的等价内容，**不调生产函数**，也不读那个视图（它 >60s 跑不完）：
	//   会话族（hot ∪ 父表分区）∪（v1 里没被会话族覆盖的行）
	//   —— 依据是 pg_get_viewdef 实读的视图定义，见 dataLifecycleCanonicalSourceSQL。
	// ★ session_turns_hot 是**移动靶**：两次查询之间就有行被 promote 进父表分区
	//   （实测跑一次判据就漂 1 行）。所以同一个下界要量两次，用区间夹住，
	//   而不是拍一个「容差 10 行」——那种容差会让真缺一条腿也照样绿。
	countLegs := func() (hot int64, par int64) {
		t.Helper()
		var h, p int64
		if err := pool.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM session_turns_hot WHERE ts > $1::timestamptz - INTERVAL '90 days'),
			        (SELECT count(*) FROM session_turns      WHERE ts > $1::timestamptz - INTERVAL '90 days')`,
			time.Now()).Scan(&h, &p); err != nil {
			t.Fatalf("重算会话族行数失败：%v", err)
		}
		return h, p
	}
	hotBefore, sessParBefore := countLegs()

	var sessHot, sessPar, v1Par, v1Dup int64
	if err := pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM _request_logs WHERE ts > $1::timestamptz - INTERVAL '90 days')`,
		time.Now()).Scan(&v1Par); err != nil {
		t.Fatalf("重算 v1 行数失败：%v", err)
	}
	sessHot, sessPar = hotBefore, sessParBefore
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM _request_logs p
		 WHERE p.ts > $1::timestamptz - INTERVAL '90 days'
		   AND (EXISTS (SELECT 1 FROM session_turns_hot th WHERE th.request_id = p.request_id)
		     OR EXISTS (SELECT 1 FROM session_turns t  WHERE t.request_id = p.request_id))
	`, time.Now()).Scan(&v1Dup); err != nil {
		t.Fatalf("重算 v1 重复行失败：%v", err)
	}
	// 90 天内 expired 段必然为 0，所以这里的下界就是「90 天内的全部行」。
	hotAfter, sessParAfter := countLegs()
	// ⚠ 必须**先求和再取包络**。promote 是 hot 减、父表增的同步变化，
	//   把两个独立的 min 相加会得到一个**任何真实瞬间都不存在**的更小值
	//   （实测因此差 1~2 行，判据时红时绿）。会话族总数在 promote 前后守恒，
	//   所以先各自求和，min/max 才是真正的上下包络。
	sessBefore, sessAfter := hotBefore+sessParBefore, hotAfter+sessParAfter
	lo, hi := v1Par-v1Dup, v1Par
	if sessBefore < sessAfter {
		lo += sessBefore
		hi += sessAfter
	} else {
		lo += sessAfter
		hi += sessBefore
	}
	// 两端都留 ±2% 余量：这是生产活库，测量期间既有 promote（守恒）
	// 也有新请求写入（单调增），还会因为 90 天窗口滑动而掉行。
	lo -= lo / 50
	// 上界要额外留**活库插入余量**：这是生产库，测量期间一直有新请求进来，
	// before/after 两次夹逼只能覆盖「已经发生的漂移」，盖不住「之后写的」。
	// 不留余量时判据会时红时绿（本轮实测过一次：复原后先红、再跑就绿）。
	// 2%~5% 对应本端点守护的失败模式（少一条腿 = 少 4 倍）来说绰绰有余，
	// 既不会放过真缺陷，也不会被正常写入量触发。
	hi += hi / 20
	wantMin := lo
	if total < lo || total > hi {
		t.Errorf("总量 %d 小于视图等价源在 90 天内的行数下界 %d"+
			"（会话 hot=%d + 父=%d + v1 未重复=%d）：某条腿被漏掉了。"+
			"★ 第一版就是在这里出的错——当时只算了 v1 未重复那一条腿，"+
			"少了近 4 倍，而独立重算和它自洽、判据全绿",
			total, wantMin, sessHot, sessPar, v1Par-v1Dup)
	}
	if total > sessHot+sessPar+v1Par {
		t.Errorf("总量 %d 超过各族行数之和 %d：不可能，说明有重复计数",
			total, sessHot+sessPar+v1Par)
	}

	t.Logf("规范源查询 %v；总量=%d（会话 hot=%d 父=%d，v1 未重复=%d）；hot=%d warm=%d cold=%d expired=%d，%d 个租户",
		time.Since(start).Round(time.Millisecond), total, sessHot, sessPar, v1Par-v1Dup,
		hot, warm, cold, expired, tenants)
}

// TestDataLifecycleDedupSQLHasNoCanonicalView —— 源码层：折叠后的那条查询
// 不得再出现 request_logs_with_current_month。
func TestDataLifecycleCanonicalSQLDropsOnlyTheColumnBloat(t *testing.T) {
	sql := dataLifecycleCanonicalSegmentsSQL("")
	if strings.Contains(sql, "request_logs_with_current_month") {
		t.Error("折叠后的查询里仍有 request_logs_with_current_month：" +
			"该视图真库 >60s 跑不完（母表单腿 280ms），端点会重新变回 500@30s")
	}
	if !strings.Contains(sql, "_request_logs p") {
		t.Error("折叠后的查询没有读 _request_logs 母表")
	}
	// 两条反连接都要在：少一条就会把 hot 或分区里的重复 request_id 算回去。
	if n := strings.Count(sql, "NOT EXISTS"); n != 2 {
		t.Errorf("反连接只有 %d 条，应为 2 条（session_turns_hot + session_turns）", n)
	}
	// 必须单次扫描出全局+按租户，不是两次。
	if !strings.Contains(sql, "GROUPING SETS ((tenant_id), ())") {
		t.Error("没按 GROUPING SETS 出全局+按租户：会退回多次扫描")
	}
}
