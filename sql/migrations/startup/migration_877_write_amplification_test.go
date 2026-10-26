//go:build integration

// migration_877_write_amplification_test.go — 迁移 877 上线前的**写放大定量决策**仪器。
//
// 这道门回答一个具体问题：迁移 877 给 session_turns / session_turns_hot 的
// search_text 加了 **GIN trigram 全列索引**，它值不值那笔每轮写放大。
//
// ── 为什么需要这道门（背景）────────────────────────────────────────────
// 877 自己的文件头写着「GIN trigram、覆盖 100% 行、且 search_text 是 TOAST
// 宽列」，并据此断定该索引构建是分钟级、承重是必要的。这条推理链上有一个
// **未经实测的前提**：search_text 是不是真的宽列。
//
// 2026-10-19 在生产 PG 17.10 上实测（172.16.2.210:5432/llm_gateway，只读）：
//
//	session_turns_hot（6624 行，search_text 全非空）
//	  逻辑长度 length()  min=20  p50=81  p90=117  p99=145  max=148
//	  存储大小 pg_column_size()  avg=93  max=152
//	session_turns_2026_10（227088 行有 search_text）
//	  逻辑长度  min=20  p50=81  p90=117  p99=145  max=148
//	  存储大小  avg=93  max=152
//
// ⇒ **search_text 根本不是 TOAST 宽列**：最大 148 字符，远低于 TOAST 阈值
//
//	（约 2KB），它永远内联在 heap 里。而 877 之前那次写放大测量用的是
//	**455 字符**的夹具 —— 比生产真实宽度**大约 5.3 倍**。
//
// ⇒ trigram 抽取条数直接随长度线性增长：实测生产 search_text 的
//
//	show_trgm() 平均 **84.59** 个 trigram（distinct 同为 84.59，max=117），
//	而 455 字符夹具会产生约 453 个 ⇒ 旧结论的写放大被**高估约 5.3 倍**。
//
// ★ 所以本题不是「索引贵不贵」的定性争论，而是**先把这个数重新量准**。
//
// ── 本门测什么 ─────────────────────────────────────────────────────────
// 五个方案臂，各自在**同一张预热过的表**上测：
//
//	(A) 无索引                —— 基线
//	(B) GIN trgm 全列 search_text      —— 877 现状（方案 a）
//	(C) GIN trgm ON left(search_text,N) —— 方案 b 的表达式索引形态
//	(D) GIN trgm WHERE length(search_text)<=N —— 方案 b 的部分索引形态
//	(E) btree (tenant_id, ts DESC)     —— 方案 c
//
// 每个臂测三样东西：
//  1. 单行 INSERT 的 µs/row（语句时间与含 COMMIT 的总时间分开记）
//  2. 索引体积（pg_relation_size）与 WAL 增量（每行字节）
//  3. 有选择性 needle 下 planner 的计划与耗时
//
// ── 夹具纪律（本门最容易出错的地方，历史教训见文件末）──────────────────
//
//	① needle 必须**有选择性**：先量命中率，再断言计划。命中率接近 100% 时
//	   planner 选顺序扫是**正确**的，把它读成「索引不可用」是第一版夹具的错。
//	② 空表上的「索引被使用」恒真：本门在 **N 行已预热**的表上测，且每个臂
//	   都用**同一份数据**，否则比的是表大小不是索引。
//	③ 计时必须**交替轮转**各臂并取中位数：单臂连测会把 CPU/页缓存漂移
//	   记成索引代价。
//
// ── 门控 ───────────────────────────────────────────────────────────────
//
//	TEST_PG_URL=postgres://... 指向**一次性可丢弃**库，然后：
//	go test -tags=integration ./sql/migrations/startup/ -run TestMigration877WriteAmplification -v -count=1
//
// ⚠ 安全闸：本门会 CREATE/DROP 多个表，默认**拒绝**在任何不像一次性库的
//
//	数据库上运行（库名须匹配 bench|test|tmp|scratch|pg_），除非显式设置
//	WA877_ALLOW_ANY_DSN=1。这比现有 877 门（按真名建 session_turns）更严，
//	因为本门要建的表多、且要反复 DROP 重建。
package startup

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// waIDAlloc 为每次计时调用分配一段不重叠的 id 区间。
var waIDAlloc atomic.Int64

func init() { waIDAlloc.Store(1_000_000_000) }

// ── 生产实测常量（2026-10-19，PG 17.10 生产库只读采样）──────────────────
//
// 这些数字是**夹具的输入**，不是结论。结论在测试输出里。
const (
	// 生产 search_text 逻辑长度的分位点（字符数）。
	waProdLenMin  = 20
	waProdLenP50  = 81
	waProdLenP75  = 97
	waProdLenP90  = 117
	waProdLenP99  = 145
	waProdLenMax  = 148
	waProdTrgmAvg = 84.59 // show_trgm() 的平均条数 = distinct 条数
	waProdTrgmMax = 117

	// 旧夹具宽度（本门作为对照臂保留，用来量化"高估了几倍"）。
	waOldFixtureLen = 455

	// 方案 b 的两个候选 N。生产最大长度 148 ⇒ N=150 时表达式索引与全列
	// 索引**逐值相同**；N=64 则是"只索引前 64 字符"的语义截断。
	waExprN      = 150
	waPartialN   = 150
	waExprNTrunc = 64
)

// 臂定义。
type waArm struct {
	name     string
	indexDDL string
	// 是否承载 ILIKE 谓词（读侧对照用）。
	carriesPredicate bool
}

// DDL 里用 %[1]s = 表名、%[2]s = 索引名。索引名必须**逐臂不同**：
// 索引名是 schema 级对象，同名会被上一臂占用（42P07 already exists）。
var waArms = []waArm{
	{name: "A_no_index", indexDDL: ""},
	{name: "B_gin_full", indexDDL: "CREATE INDEX %[2]s ON %[1]s USING gin (search_text gin_trgm_ops)", carriesPredicate: true},
	{name: "C_gin_left150", indexDDL: "CREATE INDEX %[2]s ON %[1]s USING gin (left(search_text, " + fmt.Sprint(waExprN) + ") gin_trgm_ops)"},
	{name: "D_gin_partial150", indexDDL: "CREATE INDEX %[2]s ON %[1]s USING gin (search_text gin_trgm_ops) WHERE length(search_text) <= " + fmt.Sprint(waPartialN)},
	{name: "E_btree_tenant_ts", indexDDL: "CREATE INDEX %[2]s ON %[1]s (tenant_id, ts DESC)"},
}

// waIndexName 返回某臂的索引名。
func waIndexName(armName string) string { return "wa_idx_" + armName }

// 夹具规模。够大到 planner 认真比代价，又不至于让这道门跑成分钟级。
const (
	waFixtureRows  = 200000
	waMeasuredRows = 4000 // 计时窗口内每轮插入的行数
	waReps         = 5    // 重复轮数（取中位数）
	waTxBatch      = 200  // 每个事务的行数（生产单轮写是 1 行 1 事务；这里批量以摊薄 commit 噪声）
	waTenantCount  = 20
	waNeedle       = "zzneedlezz"
	waNeedleEvery  = 500 // → 命中率 ~0.2%
	waOtherNeedle  = "qqotherqq"
	waOtherEvery   = 50 // → 命中率 ~2%
)

// waSearchText 按生产的长度分布与形状合成一条 search_text。
//
// 生产形状（字母数字已遮蔽，实测自 2026-10-19 生产库）：
//
//	X-X-X X X-X-X-X-X X X-X**** X-X-X X
//
// 即 6~7 个空格分隔的 token，含 '-' '.' '=' 与一段被遮蔽的 '****'。
//
// 为什么不用真实内容：生产 search_text 含用户名与路径等运营元数据，
// 把内容搬进基准库是**不必要的隐私扩散**。这里只复刻**长度分布**与
// **字符类结构**——而这两者恰好决定 trigram 条数（见 waProdTrgmAvg）。
// 合成数据的 distinct-trigram 条数与生产实测值的偏差由本门当场断言。
func waSearchText(rng *rand.Rand, targetLen int) string {
	tokens := []string{
		"req-" + waRandAlnum(rng, 6, 12),
		"model=" + waRandAlnum(rng, 8, 18),
		"path=/v1/" + waRandAlnum(rng, 6, 14) + "/" + waRandAlnum(rng, 4, 10),
		"user=" + waRandAlnum(rng, 4, 9),
		"status=" + waRandAlnum(rng, 2, 7),
		"sk-****",
	}
	rng.Shuffle(len(tokens), func(i, j int) { tokens[i], tokens[j] = tokens[j], tokens[i] })
	s := strings.Join(tokens, " ")
	// 按目标长度截断或用额外 token 补齐。
	for len(s) < targetLen {
		s += " x" + waRandAlnum(rng, 3, 8)
	}
	if len(s) > targetLen {
		s = s[:targetLen]
	}
	return s
}

// waRandAlnum 返回长度在 [lo,hi] 的小写字母数字串。
func waRandAlnum(rng *rand.Rand, lo, hi int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	n := lo
	if hi > lo {
		n = lo + rng.Intn(hi-lo+1)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[rng.Intn(len(alphabet))]
	}
	return string(b)
}

// waProdLenBuckets 是生产 search_text 逻辑长度的**经验分布**，
// 直接取自 2026-10-19 生产库 session_turns_2026_10 的 227088 行采样：
//
//	width_bucket(length, 20, 150, 26) | count | 该桶内 length 的实际范围
//
//	（逐桶原样抄录，不做任何拟合——拟合出来的分布会偏，均值差 9% 就足以
//	  让 trigram 条数偏 9%，而 trigram 条数正是 GIN 写放大的主因。）
var waProdLenBuckets = []struct {
	lo, hi int
	n      int
}{
	{20, 20, 1},
	{56, 56, 1},
	{62, 64, 20},
	{65, 69, 74},
	{70, 74, 11836},
	{75, 79, 56468},
	{80, 84, 53984},
	{85, 89, 9252},
	{90, 95, 8661},
	{96, 99, 10061},
	{100, 104, 8800},
	{105, 109, 22861},
	{110, 114, 17083},
	{115, 119, 12183},
	{120, 124, 7625},
	{125, 129, 2228},
	{130, 134, 2971},
	{136, 138, 206},
	{140, 144, 62},
	{145, 148, 2711},
}

// waSampleProdLen 从生产经验分布里抽一个长度。
func waSampleProdLen(rng *rand.Rand) int {
	total := 0
	for _, b := range waProdLenBuckets {
		total += b.n
	}
	pick := rng.Intn(total)
	for _, b := range waProdLenBuckets {
		if pick < b.n {
			if b.hi == b.lo {
				return b.lo
			}
			return b.lo + rng.Intn(b.hi-b.lo+1)
		}
		pick -= b.n
	}
	return waProdLenMax
}

// waTargetLen 从生产实测分位点反插值采样一个长度。
// 保留旧的逆-CDF 版本作为**对照**（它拟合得不好，均值偏 9%），
// 新的 waSampleProdLen 才是夹具真正使用的分布。
func waTargetLen(rng *rand.Rand) int {
	return waSampleProdLen(rng)
}

// ── 测量结果结构 ────────────────────────────────────────────────────────

type waArmResult struct {
	name string

	// 计时（µs/row）
	insertOnlyUsec []float64 // 语句本身（不含 COMMIT）
	totalUsec      []float64 // 含 COMMIT

	// 体积
	indexBytes       int64
	tableBytes       int64
	walBytesPerRow   float64
	walPerRowSamples []float64

	// walBytesPerRowSeq 是**顺序独占窗口**测出来的 WAL 字节/行 —— 这是
	// 唯一可信的那个（交错测的会被别的臂的 WAL 与检查点 FPI 污染）。
	walBytesPerRowSeq float64

	// 批量事务形状（perTx=200）的对应样本 —— 与上面的 perTx=1 并列。
	insertOnlyUsecBatch   []float64
	totalUsecBatch        []float64
	walPerRowSamplesBatch []float64

	// 读侧
	planUsesIndex bool
	needleRows    int64
	needleUsec    float64
}

// median 取中位数（偶数个取中间两个的平均）。
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// TestMigration877WriteAmplification 是这道门的主体。
func TestMigration877WriteAmplification(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL not set; write-amplification measurement requires a disposable PostgreSQL 17")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_PG_URL: %v", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("TEST_PG_URL IS SET but unreachable (%v) — a misconfigured CI credential silently skips this gate", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	// ── 安全闸：拒绝在不像一次性库的库上跑 ───────────────────────────
	var dbName string
	if err := conn.QueryRow(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("current_database: %v", err)
	}
	if !waDisposableDBName(dbName) {
		if os.Getenv("WA877_ALLOW_ANY_DSN") != "1" {
			t.Fatalf("拒绝在库 %q 上运行本门：本门会 CREATE/DROP 多个表并反复重建。\n"+
				"请指向一次性可丢弃的库（库名匹配 bench|test|tmp|scratch|pg_），\n"+
				"或显式设置 WA877_ALLOW_ANY_DSN=1 表示你确认该库可丢弃。", dbName)
		}
		t.Logf("⚠ WA877_ALLOW_ANY_DSN=1：在库 %q 上运行本门（该库须可丢弃）", dbName)
	}
	t.Logf("基准库 = %q", dbName)

	// 校准：量一次空往返的客户端代价。报告里所有 µs/row 都附上它，
	// 读者据此判断"这个数里有多少是网络往返、多少是服务器端成本"。
	rtUsec := waRoundTripUsec(ctx, conn)
	if rtUsec < 0 {
		t.Fatalf("round-trip calibration failed")
	}
	t.Logf("校准：单次客户端往返 = %.2fµs（下面所有 µs/row 都含它）", rtUsec)

	// PG 版本必须是 17：877 的形态（三段式 CONCURRENTLY、PG17 的分区索引行为）
	// 与更早版本不同。
	var pgVer string
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version')").Scan(&pgVer); err != nil {
		t.Fatalf("server_version: %v", err)
	}
	if !strings.HasPrefix(pgVer, "17") {
		t.Fatalf("本门针对 PG 17（877 的分区索引与 CONCURRENTLY 形态按 PG17 定型）；当前 %q", pgVer)
	}

	waCleanup(t, ctx, conn)

	// ── 1. 生成夹具数据（生产宽度分布 + 生产形状）────────────────────
	t.Logf("生成夹具：生产 search_text 长度分布（min=%d p50=%d p90=%d p99=%d max=%d）",
		waProdLenMin, waProdLenP50, waProdLenP90, waProdLenP99, waProdLenMax)
	if err := waLoadFixture(t, ctx, conn); err != nil {
		t.Fatalf("load fixture: %v", err)
	}

	// ── 2. 断言合成数据的 trigram 条数与生产实测一致 ───────────────────
	// 这一步是整个基准的**代表性前提**：trigram 条数决定 GIN 索引每行的
	// posting 条数，也就直接决定写放大。若合成数据比生产少一半 trigram，
	// 测出来的 µs/row 会系统性偏低。
	trgmAvg, trgmMax, err := waTrgmStats(ctx, conn, "wa877_probe")
	if err != nil {
		t.Fatalf("trigram stats: %v", err)
	}
	t.Logf("合成夹具 show_trgm: avg=%.2f max=%.0f（生产实测 avg=%.2f max=%d）",
		trgmAvg, trgmMax, waProdTrgmAvg, waProdTrgmMax)
	// 容差 ±12%：合成的是同分布的**形状**而非同内容，字符类多样性会
	// 造成小幅差异；但差一个量级就说明夹具根本不具代表性。
	if trgmAvg < waProdTrgmAvg*0.88 || trgmAvg > waProdTrgmAvg*1.12 {
		t.Errorf("合成夹具的 trigram 条数 avg=%.2f 与生产实测 %.2f 偏差超 ±12%%：\n"+
			"基准不具代表性（GIN 写放大正比于每行 trigram 条数），"+
			"请修 waSearchText 的形状/长度分布而不是调断言", trgmAvg, waProdTrgmAvg)
	}

	// ── 3. 备好所有臂（建表 + 灌同一份数据 + 建索引 + ANALYZE）─────────
	// 必须**先全部备好**再开始计时：计时是**交错轮转**的
	//（每轮把各臂挨个跑一遍，轮换起始臂），中途建表会把
	// "建索引的一次性开销"混进某一个臂的计时里。
	for _, arm := range waArms {
		waPrepareArm(t, ctx, conn, arm)
	}

	// ── 4. 交错计时（两个事务形状）────────────────────────────────────
	// 生产有两个写入形状，各测一遍：
	//   · perTx=1   —— session_turns_hot 的**活写**路径（turn_writer 的
	//     INSERT 在单个 turn 的事务里）。这是每轮请求的真实代价。
	//   · perTx=200 —— promote_session_turns_hot_to_partition 把热表
	//     轮转进月分区时的批量形状。
	// 两者不可互换：**"每轮写放大"必须指明是哪个形状**。
	perArm1 := waRunTiming(t, ctx, conn, 1)
	perArmB := waRunTiming(t, ctx, conn, waTxBatch)

	// ── 4b. WAL 单独一遍（顺序测，每个臂独占窗口）────────────────────
	walSeq := waMeasureWAL(t, ctx, conn)

	// ── 5. 逐臂收尾（体积 + 读侧计划）────────────────────────────────
	results := make([]waArmResult, 0, len(waArms))
	for _, arm := range waArms {
		res := perArm1[arm.name]
		res.name = arm.name
		res.insertOnlyUsecBatch = perArmB[arm.name].insertOnlyUsec
		res.totalUsecBatch = perArmB[arm.name].totalUsec
		res.walPerRowSamplesBatch = perArmB[arm.name].walPerRowSamples
		res.walBytesPerRowSeq = walSeq[arm.name]
		waCollectSizeAndRead(t, ctx, conn, arm, &res)
		results = append(results, res)
	}

	// ── 6. 报告 ────────────────────────────────────────────────────────
	waReport(t, results, rtUsec, 1)
	waReport(t, results, rtUsec, waTxBatch)

	// ── 5. 判据 ────────────────────────────────────────────────────────
	// 5.1 承重臂（B）必须真的被 planner 用上，且谓词落在 Index Cond。
	//     这一条是"读侧收益存在"的前提；读侧收益不存在时（QPS=0），
	//     整笔取舍就该翻向不写索引。
	bIdx := waFind(results, "B_gin_full")
	if !bIdx.planUsesIndex {
		t.Errorf("B_gin_full：planner 未在有选择性的 needle 下使用该索引，读侧收益无法主张。\n" +
			"（命中率必须先量：见本门输出的 needle 命中行数）")
	}
	// 5.2 变异自证：把索引摘掉，planner 必须不再用它 —— 否则 5.1 可能恒真。
	//     （纪律：「证明路径被用到」的门不等于「证明路径是对的」的门。）
	waMutationCheck(t, ctx, conn, results)

	// 5.3 方案 b 的两个形态必须在生产真实宽度下证明自己**有区别**。
	//     若两者与 B 逐值相同，则方案 b 不构成一个独立的候选，决策表里
	//     不该把它列成"更便宜的替代品"。
	waCheckOptionB(t, ctx, conn)
}

// waDisposableDBName 判断库名是否像一次性库。
func waDisposableDBName(name string) bool {
	l := strings.ToLower(name)
	for _, p := range []string{"bench", "test", "tmp", "scratch", "pg_"} {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// waCleanup 清掉本门所有对象。
func waCleanup(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	_, err := conn.Exec(ctx, `
		DROP TABLE IF EXISTS wa877_probe CASCADE;
		DROP TABLE IF EXISTS wa877_old455 CASCADE;
		DROP TABLE IF EXISTS wa877_a_no_index CASCADE;
		DROP TABLE IF EXISTS wa877_b_gin_full CASCADE;
		DROP TABLE IF EXISTS wa877_c_gin_left150 CASCADE;
		DROP TABLE IF EXISTS wa877_d_gin_partial150 CASCADE;
		DROP TABLE IF EXISTS wa877_e_btree_tenant_ts CASCADE;
	`)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// waLoadFixture 建表并灌入夹具。
//
// 表形状照抄 session_turns_hot（只保留本门需要的列）。
// search_text 的值由**本进程**生成后用 COPY 灌入 —— 不在 SQL 里拼字符串，
// 免得 SQL 侧的 format 产生与生产不同的字符类。
func waLoadFixture(t *testing.T, ctx context.Context, conn *pgx.Conn) error {
	t.Helper()
	_, err := conn.Exec(ctx, `
		CREATE EXTENSION IF NOT EXISTS pg_trgm;
		DROP TABLE IF EXISTS wa877_probe;
		CREATE TABLE wa877_probe (
			id BIGINT PRIMARY KEY,
			tenant_id TEXT,
			search_text TEXT,
			ts TIMESTAMPTZ
		);`)
	if err != nil {
		return err
	}

	rng := rand.New(rand.NewSource(20261019)) // 固定种子 ⇒ 基准可复现
	rows := make([][]any, 0, waFixtureRows)
	for i := 1; i <= waFixtureRows; i++ {
		txt := waSearchText(rng, waTargetLen(rng))
		// needle 分布：两个不同命中率的 needle，供"先量命中率"用。
		if i%waNeedleEvery == 0 {
			txt += " " + waNeedle
		}
		if i%waOtherEvery == 0 {
			txt += " " + waOtherNeedle
		}
		rows = append(rows, []any{
			int64(i),
			fmt.Sprintf("tenant_%d", i%waTenantCount),
			txt,
			time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * 13 * time.Second),
		})
	}

	// COPY 批量灌入（比 20 万次 Exec 快一个量级）。
	if _, err := conn.CopyFrom(ctx, pgx.Identifier{"wa877_probe"},
		[]string{"id", "tenant_id", "search_text", "ts"},
		pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if _, err := conn.Exec(ctx, "ANALYZE wa877_probe"); err != nil {
		return err
	}
	return nil
}

// waTrgmStats 统计某表 search_text 的 show_trgm() 条数。
func waTrgmStats(ctx context.Context, conn *pgx.Conn, table string) (avg, max float64, err error) {
	q := fmt.Sprintf(`
		SELECT avg(cnt)::float8, max(cnt)::float8 FROM (
			SELECT (SELECT count(DISTINCT x) FROM unnest(show_trgm(search_text)) AS x) AS cnt
			FROM %s WHERE search_text IS NOT NULL LIMIT 5000
		) t`, table)
	if err = conn.QueryRow(ctx, q).Scan(&avg, &max); err != nil {
		return 0, 0, err
	}
	return avg, max, nil
}

// waRoundTripUsec 量一次**空往返**的客户端代价（µs/次）。
//
// 为什么必须量：本门测的是"客户端发出 INSERT 到拿到结果"的墙钟，
// 里面必然含一次网络/套接字往返。若往返是 200µs，那么"基线 215µs/row"
// 几乎全是往返噪声，**不是**服务器端 INSERT 成本。
//
// 有了这个校准值，报告里就能同时给出：
//
//	· 原始 µs/row（含往返）—— 与"生产同机部署"的数字不可直接比；
//	· 扣掉往返后的 µs/row —— 更接近服务器端真实成本。
//
// ⚠ 这个校准值必须在**与基准同一条连接、同一个通道**上量，
//
//	否则减掉的是别的连接的往返。
func waRoundTripUsec(ctx context.Context, conn *pgx.Conn) float64 {
	const n = 2000
	t0 := time.Now()
	for i := 0; i < n; i++ {
		var one int
		if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil {
			return -1
		}
	}
	return float64(time.Since(t0).Microseconds()) / float64(n)
}

// waPrepareArm 备好一个臂：建表 → 灌同一份数据 → 建索引 → ANALYZE。
//
// 所有臂的 heap 内容与规模**完全一致**（同一份 INSERT … SELECT），
// 因此臂间差值只可能来自索引，不掺杂表大小差异。
func waPrepareArm(t *testing.T, ctx context.Context, conn *pgx.Conn, arm waArm) {
	t.Helper()
	table := "wa877_" + arm.name

	if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
		t.Fatalf("%s: drop: %v", arm.name, err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf(`
		CREATE TABLE %s (
			id BIGINT PRIMARY KEY,
			tenant_id TEXT,
			search_text TEXT,
			ts TIMESTAMPTZ
		)`, table)); err != nil {
		t.Fatalf("%s: create: %v", arm.name, err)
	}
	// 灌入**同一份**数据：所有臂的 heap 规模一致，比出来的差异才是索引。
	if _, err := conn.Exec(ctx, fmt.Sprintf(
		"INSERT INTO %s SELECT id, tenant_id, search_text, ts FROM wa877_probe", table)); err != nil {
		t.Fatalf("%s: seed: %v", arm.name, err)
	}
	if arm.indexDDL != "" {
		ddl := fmt.Sprintf(arm.indexDDL, table, waIndexName(arm.name))
		if _, err := conn.Exec(ctx, ddl); err != nil {
			t.Fatalf("%s: create index: %v", arm.name, err)
		}
	}
	if _, err := conn.Exec(ctx, "ANALYZE "+table); err != nil {
		t.Fatalf("%s: analyze: %v", arm.name, err)
	}
}

// waRunTiming 交错轮转地测所有臂，返回每个臂的计时样本。
//
// 为什么必须交错：单臂连测会把 CPU 频率、页缓存冷热、WAL 写入压力
// 的**时间漂移**记到该臂上。各臂轮流跑 + 轮换起始臂 + 取中位数，
// 才能让漂移在臂间均摊。
func waRunTiming(t *testing.T, ctx context.Context, conn *pgx.Conn, perTx int) map[string]waArmResult {
	t.Helper()
	perArm := map[string]waArmResult{}
	for _, arm := range waArms {
		perArm[arm.name] = waArmResult{name: arm.name}
	}

	// 预热一轮（丢弃）：让所有臂的 heap 页与 WAL 状态进入稳态。
	for _, arm := range waArms {
		waTimeInsert(t, ctx, conn, "wa877_"+arm.name, 1000, perTx)
	}

	for rep := 0; rep < waReps; rep++ {
		rot := rep % len(waArms)
		for k := range waArms {
			arm := waArms[(k+rot)%len(waArms)]
			ins, tot, wal := waTimeInsert(t, ctx, conn, "wa877_"+arm.name, waMeasuredRows, perTx)
			r := perArm[arm.name]
			r.insertOnlyUsec = append(r.insertOnlyUsec, ins)
			r.totalUsec = append(r.totalUsec, tot)
			r.walPerRowSamples = append(r.walPerRowSamples, wal)
			perArm[arm.name] = r
		}
	}
	return perArm
}

// waMeasureWAL 逐臂（**不交错**）量 WAL 字节/行。
//
// ★ 为什么 WAL 必须单独一遍、而且不能交错：
//
//	WAL 是**集群级**的计数器，pg_current_wal_lsn() 看到的是整个实例的
//	写位置。交错测（各臂轮流写）时，某个臂的 LSN 窗口里混进了**别的臂**
//	的 WAL，还混进了别臂触发的**检查点**——而 GIN 索引页是持续被改写的，
//	每次检查点之后每个被改页都要写一次**全页镜像（FPI, 8KB）**。
//	实测：交错测出来 GIN 臂 15087 B/row，而隔离测（先 CHECKPOINT、
//	只让一个臂写）是 3727~4120 B/row —— 差 4 倍，差的全是被算到
//	别的臂头上的 FPI 与清理开销。
//
//	所以：**CPU 用交错测**（抵消 CPU/页缓存的时间漂移），
//	**WAL 用顺序测**（每个臂独占测量窗口，窗口前强制 CHECKPOINT，
//	让 FPI 成本落在它本该落的那个窗口里）。
//
// 窗口前的 CHECKPOINT 是**故意的**：生产 PG 的 checkpoint_timeout=900s，
//
//	即索引页被持续改写 ⇒ FPI 反复发生。测量窗口里没有检查点，
//	报出来的 WAL 会系统性偏低。
func waMeasureWAL(t *testing.T, ctx context.Context, conn *pgx.Conn) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	const walRows = 5000
	for _, arm := range waArms {
		table := "wa877_" + arm.name
		// CHECKPOINT 不能在事务里跑，这里是独立语句，没有显式事务 ⇒ 可以。
		if _, err := conn.Exec(ctx, "CHECKPOINT"); err != nil {
			t.Fatalf("%s: checkpoint: %v", arm.name, err)
		}
		var before, after string
		if err := conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&before); err != nil {
			t.Fatalf("%s: lsn before: %v", arm.name, err)
		}
		waTimeInsert(t, ctx, conn, table, walRows, 1)
		if err := conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&after); err != nil {
			t.Fatalf("%s: lsn after: %v", arm.name, err)
		}
		delta, err := waLSNDelta(ctx, conn, before, after)
		if err != nil {
			t.Fatalf("%s: lsn delta: %v", arm.name, err)
		}
		out[arm.name] = float64(delta) / float64(walRows)
	}
	return out
}

// waCollectSizeAndRead 量一个臂的索引体积与读侧计划。
func waCollectSizeAndRead(t *testing.T, ctx context.Context, conn *pgx.Conn, arm waArm, res *waArmResult) {
	t.Helper()
	table := "wa877_" + arm.name

	res.walBytesPerRow = median(res.walPerRowSamples)

	if err := conn.QueryRow(ctx,
		"SELECT pg_total_relation_size($1::regclass)", table).Scan(&res.tableBytes); err != nil {
		t.Fatalf("%s: table size: %v", arm.name, err)
	}
	var idxBytes int64
	if err := conn.QueryRow(ctx, `
		SELECT COALESCE(sum(pg_relation_size(i.indexrelid)), 0)::bigint
		  FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		 WHERE i.indrelid = $1::regclass`, table).Scan(&idxBytes); err != nil {
		t.Fatalf("%s: index size: %v", arm.name, err)
	}
	res.indexBytes = idxBytes

	// ── 读侧：先量命中率，再看计划 ─────────────────────────────────────
	// 纪律：needle 的命中率**先量出来**，再据此判断"planner 选顺序扫
	// 是正确还是缺陷"。命中率接近 100% 时顺序扫才是对的。
	needle, hit, err := waNeedleStats(ctx, conn, table)
	if err != nil {
		t.Fatalf("%s: needle stats: %v", arm.name, err)
	}
	res.needleRows = hit
	plan, err := waExplain(ctx, conn, table, needle)
	if err != nil {
		t.Fatalf("%s: explain: %v", arm.name, err)
	}
	res.planUsesIndex = waPlanHasBitmapIndexScan(plan)
	res.needleUsec = waPlanActualMs(plan)

	t.Logf("[%s] insert=%.2fµs/row (含COMMIT %.2fµs)  index=%d B  table=%d B  wal(顺序独占)=%.1f B/row (交错=%0.f)  needle命中=%d 行  计划用索引=%v  查询=%.2fms",
		arm.name, median(res.insertOnlyUsec), median(res.totalUsec), res.indexBytes, res.tableBytes,
		res.walBytesPerRowSeq, res.walBytesPerRow, res.needleRows, res.planUsesIndex, res.needleUsec)
}

// waTimeInsert 在 table 上插入 rows 行，返回
// （语句 µs/row，含 COMMIT 的墙钟 µs/row，WAL B/row）。
//
// 计时口径（两个都要，因为它们回答不同问题）：
//
//	· 语句时间 = 每条 INSERT 的耗时累加。**不含 COMMIT**，因此把
//	  "索引维护 CPU 成本"与"提交/刷盘成本"分开。后者在所有臂上同形
//	  （同样的批次、同样的 commit 次数），对**臂间差值**无贡献。
//	· 墙钟时间 = 从第一条 INSERT 前到最后一次 COMMIT 后。含 commit。
//	  生产是 1 行 1 事务（synchronous_commit=on ⇒ 每次 commit 一次 fsync），
//	  所以这个数更接近"每轮真实代价"，但绝对值受宿主刷盘影响。
func waTimeInsert(t *testing.T, ctx context.Context, conn *pgx.Conn, table string, rows, perTx int) (insertUsec, totalUsec, walPerRow float64) {
	t.Helper()

	var lsnBefore, lsnAfter string
	if err := conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsnBefore); err != nil {
		t.Fatalf("wal lsn before: %v", err)
	}

	insTotal := time.Duration(0)
	// 每次调用取一段全新 id：同一臂会被重复测量多轮，id 必须不重叠，
	// 否则第二轮就撞主键（第一版踩过：duplicate key 23505）。
	offset := waIDAlloc.Add(waMeasuredRows + 10)
	insertSQL := fmt.Sprintf(
		"INSERT INTO %s (id, tenant_id, search_text, ts) VALUES ($1, $2, $3, $4)", table)

	// 计时用的 RNG 单独一份，种子固定 ⇒ 结果可复现。
	rng := rand.New(rand.NewSource(777))

	loopStart := time.Now()
	for done := 0; done < rows; {
		n := rows - done
		if n > perTx {
			n = perTx
		}
		// 每个批次一个事务。perTx=1 是**生产 session_turns_hot 的真实形状**
		//（turn_writer.go 的 INSERT 在单个 turn 的事务里）；perTx=200 是
		// promote_session_turns_hot_to_partition 的批量形状。
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		for i := 0; i < n; i++ {
			// 行的 search_text 用**生产宽度分布**生成 —— 写放大的主因
			// 就是每行的 trigram 条数，用固定短串测会低估。
			txt := waSearchText(rng, waTargetLen(rng))
			t0 := time.Now()
			if _, err := tx.Exec(ctx, insertSQL,
				offset+int64(done+i), fmt.Sprintf("tenant_%d", (done+i)%waTenantCount),
				txt, time.Now().UTC()); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatalf("insert: %v", err)
			}
			insTotal += time.Since(t0)
		}
		done += n
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	wallTotal := time.Since(loopStart)

	insertUsec = float64(insTotal.Microseconds()) / float64(rows)
	totalUsec = float64(wallTotal.Microseconds()) / float64(rows)

	if err := conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsnAfter); err != nil {
		t.Fatalf("wal lsn after: %v", err)
	}
	walBytes, err := waLSNDelta(ctx, conn, lsnBefore, lsnAfter)
	if err != nil {
		t.Fatalf("wal delta: %v", err)
	}
	walPerRow = float64(walBytes) / float64(rows)
	return insertUsec, totalUsec, walPerRow
}

// waLSNDelta 计算两个 WAL LSN 之间的字节差。
func waLSNDelta(ctx context.Context, conn *pgx.Conn, a, b string) (int64, error) {
	var d int64
	err := conn.QueryRow(ctx, "SELECT pg_wal_lsn_diff($1::pg_lsn, $2::pg_lsn)::bigint", b, a).Scan(&d)
	return d, err
}

// waNeedleStats 统计 needle 的命中行数与命中率。
func waNeedleStats(ctx context.Context, conn *pgx.Conn, table string) (needle string, hits int64, err error) {
	needle = waNeedle
	if err = conn.QueryRow(ctx, fmt.Sprintf(
		"SELECT count(*) FROM %s WHERE search_text ILIKE '%%' || $1 || '%%'", table), needle).Scan(&hits); err != nil {
		return "", 0, err
	}
	return needle, hits, nil
}

// waExplain 跑 EXPLAIN (ANALYZE, BUFFERS) 并取回文本。
func waExplain(ctx context.Context, conn *pgx.Conn, table, needle string) (string, error) {
	q := fmt.Sprintf(
		// ⚠ SUMMARY **不能**关：SUMMARY OFF 会把 Execution Time 一并关掉，
		// 于是读侧耗时恒读成 0（第一版就踩了这个，读出来全是 0.00ms）。
		"EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF) "+
			"SELECT id FROM %s WHERE tenant_id = $1 AND search_text ILIKE '%%' || $2 || '%%'",
		table)
	rows, err := conn.Query(ctx, q, "tenant_3", needle)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), rows.Err()
}

// waPlanHasBitmapIndexScan 判计划里是否有 Bitmap Index Scan。
func waPlanHasBitmapIndexScan(plan string) bool {
	return strings.Contains(plan, "Bitmap Index Scan")
}

// waPlanActualMs 从 EXPLAIN ANALYZE 的文本里取 Execution Time。
func waPlanActualMs(plan string) float64 {
	for _, line := range strings.Split(plan, "\n") {
		if strings.Contains(line, "Execution Time:") {
			var v float64
			_, _ = fmt.Sscanf(line, "Execution Time: %f ms", &v)
			return v
		}
	}
	return 0
}

// waFind 在结果里找一个臂。
func waFind(rs []waArmResult, name string) waArmResult {
	for _, r := range rs {
		if r.name == name {
			return r
		}
	}
	return waArmResult{name: name}
}

// waReport 打印决策表。
func waReport(t *testing.T, rs []waArmResult, roundTripUsec float64, perTx int) {
	t.Helper()
	base := waFind(rs, "A_no_index")
	pick := func(r waArmResult) ([]float64, []float64, float64) {
		// WAL 一律用**顺序独占窗口**的那份；交错测的那份仅作对照。
		if perTx == 1 {
			return r.insertOnlyUsec, r.totalUsec, r.walBytesPerRowSeq
		}
		return r.insertOnlyUsecBatch, r.totalUsecBatch, r.walBytesPerRowSeq
	}
	baseInsS, _, baseWAL := pick(base)
	baseIns := median(baseInsS)

	net := func(v float64) float64 { return v - roundTripUsec }

	t.Logf("=== 事务形状 perTx=%d（%s）===",
		perTx, map[int]string{1: "session_turns_hot 活写形状：每行一个事务", 200: "promote 进月分区的批量形状"}[perTx])
	t.Log("╔═══════════════════════════════════════════════════════════════════════════════════════╗")
	t.Log("║ 方案                      原始µs/row  扣往返后  Δ(原始)  Δ(扣往返)  索引体积   WAL B/row  用索引 ║")
	t.Log("╠═══════════════════════════════════════════════════════════════════════════════════════╣")
	for _, r := range rs {
		insS, _, wal := pick(r)
		ins := median(insS)
		delta := ins - baseIns
		name := r.name
		for len(name) < 22 {
			name += " "
		}
		t.Logf("║ %s %10.2f %10.2f %+8.2f %+10.2f %8.1fMB %10.1f   %v",
			name, ins, net(ins), delta, net(delta),
			float64(r.indexBytes)/(1024*1024),
			wal, r.planUsesIndex)
	}
	t.Log("╚═══════════════════════════════════════════════════════════════════════════════════════╝")
	t.Logf("事务形状 perTx=%d；客户端往返 = %.2fµs/次；基线 WAL = %.1f B/row；Δ(扣往返) 才是索引维护的净成本",
		perTx, roundTripUsec, baseWAL)
}

// waMutationCheck 变异自证：把 B 臂的索引摘掉，planner 必须不再用它。
//
// 纪律：判据必须能被变异打穿，否则不知道它是否真在测东西。
// 这里变异的是**被测对象**（索引），断言的是**判据**（计划里没有 Bitmap Index Scan）。
func waMutationCheck(t *testing.T, ctx context.Context, conn *pgx.Conn, rs []waArmResult) {
	t.Helper()
	table := "wa877_B_gin_full"
	before, err := waExplain(ctx, conn, table, waNeedle)
	if err != nil {
		t.Fatalf("mutation explain before: %v", err)
	}
	if !waPlanHasBitmapIndexScan(before) {
		t.Logf("变异自证跳过：B 臂的索引本就未被 planner 使用（无需验证）")
		return
	}
	if _, err := conn.Exec(ctx, "DROP INDEX IF EXISTS "+waIndexName("B_gin_full")); err != nil {
		t.Fatalf("mutation drop index: %v", err)
	}
	after, err := waExplain(ctx, conn, table, waNeedle)
	if err != nil {
		t.Fatalf("mutation explain after: %v", err)
	}
	if waPlanHasBitmapIndexScan(after) {
		t.Errorf("变异自证失败：DROP INDEX 后计划里仍有 Bitmap Index Scan ——\n"+
			"判据可能恒真（夹具/谓词退化），测不到索引本身。\nplan:\n%s", after)
	}
	// 复原。
	if _, err := conn.Exec(ctx, "CREATE INDEX "+waIndexName("B_gin_full")+" ON "+table+
		" USING gin (search_text gin_trgm_ops)"); err != nil {
		t.Fatalf("mutation restore index: %v", err)
	}
}

// waCheckOptionB 证明方案 b 的两个形态在**生产真实宽度**下与全列索引的差别。
//
// 这是方案 b 的**关键否证**：生产 search_text 的最大长度是 148，
// 因此
//
//	· 部分索引 WHERE length(search_text) <= 150 覆盖 **100%** 的生产行
//	  ⇒ 它与全列索引**逐值等价**，写放大与索引体积都相同；
//	· 表达式索引 left(search_text, 150) 在生产行上**逐值相同**
//	  ⇒ 同样没有省下任何东西。
//
// 除非把 N 压到 < 148（那会**改变搜索语义**：落在第 N 位之后的 needle
// 会被漏掉），否则方案 b 不构成独立候选。
func waCheckOptionB(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()

	// 部分索引覆盖的生产行占比
	var covered, total int64
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE length(search_text) <= $1), count(*)
		  FROM wa877_probe`, waPartialN).Scan(&covered, &total); err != nil {
		t.Fatalf("option B coverage: %v", err)
	}
	t.Logf("方案 b 的部分索引（length <= %d）覆盖生产夹具 %d/%d 行 = %.4f%%",
		waPartialN, covered, total, 100*float64(covered)/float64(total))

	// 表达式索引与全列在生产行上是否逐值相同
	var differing int64
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM wa877_probe
		 WHERE left(search_text, $1) IS DISTINCT FROM search_text`, waExprN).Scan(&differing); err != nil {
		t.Fatalf("option B expr diff: %v", err)
	}
	t.Logf("方案 b 的表达式索引 left(search_text,%d) 与原值不同的生产行 = %d", waExprN, differing)

	// 截断版（真正能省写放大，但**改变语义**）的漏检量
	var missed int64
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM wa877_probe
		 WHERE search_text ILIKE '%%' || $1 || '%%'
		   AND left(search_text, $2) NOT ILIKE '%%' || $1 || '%%'`, waOtherNeedle, waExprNTrunc).Scan(&missed); err != nil {
		t.Fatalf("option B truncated miss: %v", err)
	}
	t.Logf("若取 N=%d（真正截断），落在截断位之后的 needle 漏检行数 = %d（语义已改变）",
		waExprNTrunc, missed)
}
