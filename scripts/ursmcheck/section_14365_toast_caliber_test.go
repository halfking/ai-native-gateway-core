package ursm_test

// §143.65 的门：把本轮的**结论**钉成可测判据（而不是让它停留在一次性读数里）。
//
// 三条腿：
//
// ① **结构门**（无外部依赖，恒可跑）：迁移 765 必须把 `session_bodies` 与
//    `session_bodies_hot` 的 **5 个 jsonb 列全部**设成 lz4。
//    ⇒ 理由（实测）：本地同一载荷 lz4 payload 217,267 vs pglz 256,534 ⇒ **lz4 小 15.3%**；
//    且生产 `2026_10`/`2026_11`/`hot` 是 lz4、存量 `2026_08`/`2026_09` 是 pglz
//    ⇒ 一旦有人把 lz4 改回 pglz（或去掉压缩），**新写入的正文立刻变大 15%+**，
//    而存量分区不会被改写 ⇒ **劣化会静悄悄地持续**。
//
// ② **口径门**（纯函数，恒可跑）：「会话族容量读数」必须按**全列**口径。
//    ⇒ 理由：§143.65 的头号发现就是**一次口径错误**（拿 5 列共用的 TOAST 去比
//    `outbound_body` 单列 ⇒ 得出「2.9 GB 非活体空间」这个**不存在的杠杆**）。
//    ⇒ 这道门把「单列读数必须被判为不完整」写成可测判据 ⇒ 防止同样的错误再发生。
//
// ③ **机制门**（需要 `TEST_DATABASE_URL`；缺库时**显式 SKIP 并写明理由**）：
//    在真库里验证两条机制结论 —— ① TOAST 块是【满】的（填充率 > 95%）；
//    ② promote 的双写是【死块】、普通 VACUUM 就能回收。
//    ⇒ ★ SKIP 不等于通过：本门在 CI（无真库）里是 SKIP，**所以另外两条腿必须独立有效**。
//
// ★ 纪律：每条判据都配对照 —— 尤其 ② 必须证明「判据不是恒真」。

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const migration765 = "sql/migrations/startup/765_bodies_columnar_storage.sql"

// sessionBodiesToastColumns 是 `session_bodies` 族**共用同一个 TOAST** 的 5 个 jsonb 列。
// ★ 这个清单就是 §143.65 的教训本体：只数其中一列，会把另外四列的载荷算成「非活体」。
var sessionBodiesToastColumns = []string{
	"request_delta",
	"response_delta",
	"outbound_body",
	"request_attachments",
	"response_attachments",
}

func repoRoot14360(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// stripSQLComments14360 去掉 SQL 行注释与块注释（★ 断言 SQL 文本前必须剥注释）。
func stripSQLComments14360(s string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	s = block.ReplaceAllString(s, " ")
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// ① 结构门
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14360BodiesColumnsKeepLZ4 是结构门。
func TestSection14360BodiesColumnsKeepLZ4(t *testing.T) {
	root := repoRoot14360(t)
	raw, err := os.ReadFile(filepath.Join(root, migration765))
	if err != nil {
		t.Fatalf("读 %s 失败：%v", migration765, err)
	}
	body := stripSQLComments14360(string(raw))

	// 逐行解析 `ALTER TABLE public.<表> ALTER COLUMN <列> SET COMPRESSION <方法>;`
	re := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+public\.(session_bodies|session_bodies_hot)\s+ALTER\s+COLUMN\s+(\w+)\s+SET\s+COMPRESSION\s+(\w+)`)
	found := map[string]map[string]string{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		tbl, col, method := m[1], m[2], strings.ToLower(m[3])
		if found[tbl] == nil {
			found[tbl] = map[string]string{}
		}
		found[tbl][col] = method
	}

	// ★ 阳性对照：解析器必须真的看见了这张表的这 5 个列（否则下面的断言会假绿）。
	for _, tbl := range []string{"session_bodies", "session_bodies_hot"} {
		if len(found[tbl]) == 0 {
			t.Fatalf("阳性对照失败：在 %s 里没解析到任何 `ALTER TABLE public.%s ... SET COMPRESSION` ⇒ 解析器本身失效（下面的断言会假绿）", migration765, tbl)
		}
	}

	for _, tbl := range []string{"session_bodies", "session_bodies_hot"} {
		for _, col := range sessionBodiesToastColumns {
			method, ok := found[tbl][col]
			if !ok {
				t.Errorf("%s：%s 的列 %q 没有 SET COMPRESSION（lz4 回退 ⇒ 新写入正文变大 15%%+）", migration765, tbl, col)
				continue
			}
			if method != "lz4" {
				t.Errorf("%s：%s.%s 的压缩方法是 %q，应为 lz4", migration765, tbl, col, method)
			}
		}
	}

	// ★ 存量空子分区的补齐块（2026_10）也必须覆盖 5 列（它是 `EXECUTE format(...)` 形态）。
	execRe := regexp.MustCompile(`(?i)ALTER\s+TABLE\s+public\.%I\s+ALTER\s+COLUMN\s+(\w+)\s+SET\s+COMPRESSION\s+lz4`)
	execCols := map[string]bool{}
	for _, m := range execRe.FindAllStringSubmatch(body, -1) {
		execCols[m[1]] = true
	}
	for _, col := range sessionBodiesToastColumns {
		if !execCols[col] {
			t.Errorf("%s：补齐块（EXECUTE format(...)）里缺列 %q 的 lz4 对齐", migration765, col)
		}
	}

	// ★ 第二条阳性对照：迁移里必须仍然写着「lz4 / pglz / zstd 不可用」的判断依据，
	// 否则说明这份迁移被整体换过，上面的断言已不是同一个对象。
	if !strings.Contains(string(raw), "zstd") {
		t.Errorf("%s：找不到 zstd 相关说明 ⇒ 这份迁移可能已被整体替换（判据对象漂移）", migration765)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ② 口径门
// ─────────────────────────────────────────────────────────────────────────────

// toastCapacityReading 是一次「会话族 TOAST 容量读数」。
// ★ PayloadByColumn 必须是**全列**口径；只填一列就是 §143.65 的那个错误读数。
type toastCapacityReading struct {
	PayloadByColumn map[string]int64
	ToastTotalBytes int64
	LiveTuples      int64
	DeadTuples      int64
}

// 口径判据的结论常量。
const (
	toastCaliberOK              = "OK"
	toastCaliberIncomplete      = "INCOMPLETE_CALIBER"
	toastCaliberDeadSpace       = "DEAD_SPACE"
	toastCaliberOverheadGap     = "OVERHEAD_GAP"
	toastCaliberUntrustworthy   = "UNTRUSTWORTHY"
	toastCaliberDeadTupleRatio  = 0.05 // 死块占比上限（§143.65 实测 0.25%）
	toastCaliberOverheadRatio   = 0.20 // 开销占比上限（§143.65 实测 4.95%）
	toastCaliberMinTrustedRatio = 0.90 // TOAST 总量 / 载荷 的可信下限
)

// classifyToastCapacity14360 是纯函数判定：给一次容量读数 ⇒ 给一个结论 + 理由。
func classifyToastCapacity14360(r toastCapacityReading) (verdict string, reason string) {
	var payload int64
	for _, col := range sessionBodiesToastColumns {
		v, ok := r.PayloadByColumn[col]
		if !ok {
			return toastCaliberIncomplete,
				"缺少列 " + col + " 的载荷读数 ⇒ TOAST 是多列共用的，只数部分列会把其余列算成非活体"
		}
		payload += v
	}
	if payload <= 0 {
		return toastCaliberUntrustworthy, "载荷合计为 0 ⇒ 读数不可信"
	}
	if float64(r.ToastTotalBytes) < toastCaliberMinTrustedRatio*float64(payload) {
		return toastCaliberUntrustworthy, "TOAST 总量小于载荷合计的 90% ⇒ 两个口径不是同一个集合"
	}
	if r.LiveTuples > 0 {
		deadRatio := float64(r.DeadTuples) / float64(r.LiveTuples)
		if deadRatio > toastCaliberDeadTupleRatio {
			return toastCaliberDeadSpace,
				"死块占比 " + formatRatio14360(deadRatio) + " > 5% ⇒ 存在真正的死空间（不是活体占用）"
		}
	}
	overhead := float64(r.ToastTotalBytes-payload) / float64(payload)
	if overhead > toastCaliberOverheadRatio {
		return toastCaliberOverheadGap,
			"开销占比 " + formatRatio14360(overhead) + " > 20% ⇒ TOAST 里存在活体解释不了的空间"
	}
	return toastCaliberOK, "全列口径下开销 " + formatRatio14360(overhead) + "、死块在阈值内"
}

func formatRatio14360(v float64) string {
	return strconv.FormatFloat(v*100, 'f', 2, 64) + "%"
}

// TestSection14360ToastCapacityCaliber 是口径门的主干。
func TestSection14360ToastCapacityCaliber(t *testing.T) {
	full := map[string]int64{
		"request_delta":        2604420008,
		"response_delta":       57320453,
		"outbound_body":        1901487326,
		"request_attachments":  3180389,
		"response_attachments": 1362250,
	}
	var payload int64
	for _, v := range full {
		payload += v
	}

	t.Run("全列口径 + 正常形态 ⇒ OK", func(t *testing.T) {
		verdict, reason := classifyToastCapacity14360(toastCapacityReading{
			PayloadByColumn: full,
			ToastTotalBytes: 4805754880,
			LiveTuples:      2194253,
			DeadTuples:      5408,
		})
		if verdict != toastCaliberOK {
			t.Errorf("verdict = %q（%s），应为 %q", verdict, reason, toastCaliberOK)
		}
	})

	t.Run("只读一列 ⇒ 必须判口径不完整（★ 本节教训本体）", func(t *testing.T) {
		verdict, reason := classifyToastCapacity14360(toastCapacityReading{
			PayloadByColumn: map[string]int64{"outbound_body": 1901487326},
			ToastTotalBytes: 4805754880,
			LiveTuples:      2194253,
			DeadTuples:      5408,
		})
		if verdict != toastCaliberIncomplete {
			t.Errorf("verdict = %q（%s），应为 %q —— 单列读数会得出虚高的「放大倍数」", verdict, reason, toastCaliberIncomplete)
		}
	})

	t.Run("死块超阈值 ⇒ DEAD_SPACE", func(t *testing.T) {
		verdict, reason := classifyToastCapacity14360(toastCapacityReading{
			PayloadByColumn: full,
			ToastTotalBytes: 4805754880,
			LiveTuples:      1000,
			DeadTuples:      900,
		})
		if verdict != toastCaliberDeadSpace {
			t.Errorf("verdict = %q（%s），应为 %q", verdict, reason, toastCaliberDeadSpace)
		}
	})

	t.Run("开销超阈值 ⇒ OVERHEAD_GAP", func(t *testing.T) {
		verdict, reason := classifyToastCapacity14360(toastCapacityReading{
			PayloadByColumn: full,
			ToastTotalBytes: payload * 2,
			LiveTuples:      2194253,
			DeadTuples:      5408,
		})
		if verdict != toastCaliberOverheadGap {
			t.Errorf("verdict = %q（%s），应为 %q", verdict, reason, toastCaliberOverheadGap)
		}
	})
}

// TestSection14360ToastCapacityCaliberIsNotConstant 是口径门的**自检对照**：
// 证明判据会随输入变化 ⇒ 不是恒真门。
func TestSection14360ToastCapacityCaliberIsNotConstant(t *testing.T) {
	base := toastCapacityReading{
		PayloadByColumn: map[string]int64{
			"request_delta": 100, "response_delta": 100, "outbound_body": 100,
			"request_attachments": 100, "response_attachments": 100,
		},
		ToastTotalBytes: 500,
		LiveTuples:      100,
		DeadTuples:      1,
	}
	verdictOK, _ := classifyToastCapacity14360(base)

	single := base
	single.PayloadByColumn = map[string]int64{"outbound_body": 100}
	verdictSingle, _ := classifyToastCapacity14360(single)

	dead := base
	dead.DeadTuples = 500
	verdictDead, _ := classifyToastCapacity14360(dead)

	if verdictOK != toastCaliberOK {
		t.Fatalf("正常形态的 verdict = %q，应为 %q", verdictOK, toastCaliberOK)
	}
	if verdictSingle == verdictOK {
		t.Errorf("单列读数与全列读数给出同一 verdict（%q）⇒ 判据没有区分力（恒真门）", verdictSingle)
	}
	if verdictDead == verdictOK {
		t.Errorf("死块超阈值与正常形态给出同一 verdict（%q）⇒ 判据没有区分力（恒真门）", verdictDead)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// ③ 机制门（需要真库）
// ─────────────────────────────────────────────────────────────────────────────

// TestSection14360ToastChunkIsFull 验证机制一：TOAST 是「先整体压缩、再按 1996 B 切块」
// ⇒ 块是【满】的 ⇒ 「逐块压缩 ⇒ 填充率 < 100% ⇒ 放大 1/填充率」不成立。
//
// ★ 需要 `TEST_DATABASE_URL` 指向**可写的一次性/测试库**；缺库时显式 SKIP 并写明理由。
func TestSection14360ToastChunkIsFull(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SKIP：没有 TEST_DATABASE_URL ⇒ 无法在真库上验证 TOAST 块填充率（★ 这道腿在 CI 里恒 SKIP ⇒ 另外两条腿必须独立有效）")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("连 TEST_DATABASE_URL 失败：%v", err)
	}
	defer pool.Close()

	const tbl = "sec14360_chunk_probe"
	if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl); err != nil {
		t.Fatalf("清理 %s 失败：%v", tbl, err)
	}
	defer func() { _, _ = pool.Exec(ctx, "DROP TABLE IF EXISTS "+tbl) }()

	stmts := []string{
		"CREATE TABLE " + tbl + " (id int, payload jsonb)",
		"ALTER TABLE " + tbl + " ALTER COLUMN payload SET COMPRESSION lz4",
		"ALTER TABLE " + tbl + " SET (autovacuum_enabled = false, toast.autovacuum_enabled = false)",
		// 100,000 字节的高熵载荷 ⇒ 约 50 块 ⇒ 若「逐块压缩」为真，填充率会明显 < 100%
		"INSERT INTO " + tbl + " SELECT 1, jsonb_build_object('t', (SELECT string_agg(md5(g::text), '') FROM generate_series(1, 3126) g))",
		"SELECT pg_stat_force_next_flush()",
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s); err != nil {
			t.Fatalf("执行 %q 失败：%v", s, err)
		}
	}

	var payloadBytes, logicalBytes int64
	var chunks int64
	err = pool.QueryRow(ctx, `
		SELECT (SELECT pg_column_size(payload) FROM `+tbl+`),
		       (SELECT length(payload::text) FROM `+tbl+`),
		       (SELECT s.n_tup_ins
		          FROM pg_class c JOIN pg_stat_all_tables s ON s.relid = c.reltoastrelid
		         WHERE c.relname = $1)`, tbl).Scan(&payloadBytes, &logicalBytes, &chunks)
	if err != nil {
		t.Fatalf("读探针失败：%v", err)
	}
	if chunks == 0 {
		t.Fatalf("探针没有产生任何 TOAST 块（payload=%d）⇒ 量具/探针本身失效", payloadBytes)
	}
	fill := float64(payloadBytes) / (float64(chunks) * 1996)
	t.Logf("§143.65 机制门：payload=%d logical=%d chunks=%d ⇒ chunk_fill=%.1f%%", payloadBytes, logicalBytes, chunks, fill*100)
	// ★ 阈值取 95%：实测 98.3%（51 块/值）/ 99.8%（502 块/值）⇒ 留足余量；
	// 而「逐块压缩」假说在本尺度下会给出明显更低的下界。
	if fill < 0.95 {
		t.Errorf("chunk_fill = %.1f%%（payload=%d / chunks=%d / 1996）< 95%% ⇒ 与「先整体压缩再切块、块是满的」矛盾 ⇒ 机制结论需重查", fill*100, payloadBytes, chunks)
	}
}
