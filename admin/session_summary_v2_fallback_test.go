package admin

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/db"
)

func TestSessionSummaryV2_QueryTurnsTargetsUnifiedView(t *testing.T) {
	sql := renderQueryTurnsForSummarySQL()
	if !strings.Contains(sql, "public.session_bodies_unified b") {
		t.Fatalf("queryTurnsForSummary must LEFT JOIN public.session_bodies_unified, got:\n%s", sql)
	}
	if strings.Contains(sql, "FROM public.session_bodies ") ||
		strings.Contains(sql, "JOIN public.session_bodies ") {
		t.Fatalf("legacy public.session_bodies reference must not appear, got:\n%s", sql)
	}
}

func renderQueryTurnsForSummarySQL() string {
	return `
		SELECT
			t.turn_no,
			b.request_delta,
			b.response_delta
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
			ON t.tenant_id = b.tenant_id
			AND t.session_id = b.session_id
			AND t.turn_no = b.turn_no
			AND t.request_id = b.request_id
		WHERE t.session_id = $1 AND t.tenant_id = $2
		ORDER BY t.turn_no ASC`
}

// 审计缺陷 7（2026-10-01）：这条守卫在 2026-09-30 被**反转**。
//
// 原守卫要求 fallback 的 turns 腿读 `db.SessionFamilyTurnsForSessionSQL()`，
// 并显式禁止 `FROM request_logs_with_current_month rl`。它不是随手写的 ——
// 注释里给出了理由（外层再加 `gw_session_id` 会打掉谓词下推，丢掉 13× 收益）。
//
// 那个理由在**性能**上是对的，在**正确性**上漏了一件事：
// `generateSummary` 只在主路径返回 0 轮时才走 fallback。让 fallback 去读
// session 族原生源，就是让它去读主路径刚判定为空的同一批表 —— 它必然返回
// 0 轮，`no turns found` → HTTP 500。它服务的那批会话，按定义就是原生源
// **没有**的会话。
//
// 代价已实测并接受（同一会话 gw_63798b79，169 轮）：
//
//	v1 视图 + 排除谓词   21.6 ms / 10,013 buffers
//	原生源               0.55 ms /    280 buffers   （≈40×）
//
// 这 40× 落在一条今天服务 0 个会话的路径上（实测：有业务轮次却缺失于原生源的
// 会话为 0），而加上 v1 源是它能返回任何东西的唯一办法。所以保留 v1，把
// 性能数字写在这里而不是删掉这条守卫。
//
// 判据仍打在**产物**上：钉 FROM 的字面来源、排除谓词、以及 `gw_session_id`
// 谓词必须落在具体列上（它是 CASE 投影，裸 `$1` 推不出类型 → 42P18）。
func TestSessionSummaryV2RequestLogsFallbackReadsV1WithExclusion(t *testing.T) {
	limit := 3
	sql, args := buildRequestLogsFallbackQuery("session-1", "tenant-a", &limit)
	for _, want := range []string{
		"FROM request_logs_with_current_month rl",
		"WHERE rl.gw_session_id = $1",
		// 注意：产物里是**展开后**的 CASE，不是 `db.MirrorDriftClassSQL`
		// 这个标识符。断言标识符会永远红；断言"来自 SSOT"要用
		// strings.Contains(sql, db.MirrorDriftClassSQL)，那是
		// TestSessionSummaryV2FallbackTurnsLegReadsV1 的职责。
		"= 'genuine_loss'", // 方向：保留业务轮次
		"rl.tenant_id = $2",
		"ORDER BY rl.ts ASC",
		"LIMIT $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("fallback SQL missing %q:\n%s", want, sql)
		}
	}
	for _, forbidden := range []string{
		"SessionFamilyTurnsForSessionSQL", // 缺陷 7 的形态：读与主腿同源的原生源
		"<> 'genuine_loss'",               // 方向写反：留下内部调用、丢掉业务轮次
		"FROM request_logs_hot rl",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("fallback SQL must not contain %q:\n%s", forbidden, sql)
		}
	}
	if len(args) != 3 || args[0] != "session-1" || args[1] != "tenant-a" || args[2] != limit {
		t.Fatalf("unexpected fallback args: %#v", args)
	}
}

// 会话存储解耦 v3 审计（2026-10-01）：正文腿从 phase 1 里摘了出去。
//
// 这条守卫盯的是**性能契约**，不是正确性。合并成一条查询时它是正确的，只是
// 要 40,483ms —— LEFT JOIN 让规划器对 request_logs_bodies_2026_09（迁移 765
// 之后的 Citus 列存分区）逐轮做 ColumnarScan，8,513,122 buffers 换来 20 行。
// 拆成 phase 1（只取 request_id + ts）+ phase 2（批量半连接）后是 41ms。
//
// 判据打在**产物**上：phase 1 的 SQL 里不得再出现 bodies 视图或两个正文列。
// 只断言"有 request_id 和 ts"是守不住的 —— 那个 SELECT 在合并形态下同样存在。
func TestSessionSummaryV2FallbackPhase1CarriesNoBodyColumns(t *testing.T) {
	limit := 3
	sql, _ := buildRequestLogsFallbackQuery("session-1", "tenant-a", &limit)
	// 注意判据里没有 " JOIN "：原生源自身就带一个 LEFT JOIN public.session_turn_details
	// d（特征层），那是 734 的既定形状，不是被摘掉的正文腿。正文腿的身份是
	// bodies 视图、正文列和 rb 别名三者。
	for _, forbidden := range []string{
		"request_logs_bodies",
		"request_body",
		"response_body",
		"rb.",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("phase 1 must not reference the bodies view or any body column; "+
				"found %q — that is the 40-second nested loop:\n%s", forbidden, sql)
		}
	}
}

// phase 2 的形态本身就是性能契约，理由是实测的三档计划：
//
//	IN (VALUES ...)     → Index Scan using request_logs_bodies_2026_09_pkey    7 ms
//	IN (SELECT unnest)  → Index Scan using request_logs_bodies_2026_09_pkey    7 ms
//	unnest + LEFT JOIN  → ColumnarScan 全扫 2,216,660 行                   10,365 ms
//
// LEFT JOIN 贴着函数扫描时规划器没法重排，判定全表列存扫描最便宜。半连接把
// 20 行的哈希交给它，它就会走主键探针。
func TestSessionSummaryV2FallbackBodiesSQLUsesIndexedSemiJoin(t *testing.T) {
	for _, want := range []string{
		"FROM request_logs_bodies_with_current_month rb",
		"WHERE (rb.request_id, rb.ts) IN (",
		"unnest($1::text[], $2::timestamptz[])",
	} {
		if !strings.Contains(sessionBodiesByRequestIDAndTSSQL, want) {
			t.Fatalf("phase 2 SQL missing %q:\n%s", want, sessionBodiesByRequestIDAndTSSQL)
		}
	}
	if strings.Contains(sessionBodiesByRequestIDAndTSSQL, "LEFT JOIN") {
		t.Fatalf("phase 2 must be a semi-join, not a LEFT JOIN — the LEFT JOIN "+
			"form forces a full columnar scan (10,365ms vs 7ms):\n%s", sessionBodiesByRequestIDAndTSSQL)
	}
	// 两个数组参数（而非每轮两个占位符）：upToTurn 为 nil 时轮数无上界，
	// VALUES 形态会在 32767 轮撞上 65535 参数上限。
	if strings.Contains(sessionBodiesByRequestIDAndTSSQL, "$3") {
		t.Fatalf("phase 2 must bind exactly two array parameters, got:\n%s", sessionBodiesByRequestIDAndTSSQL)
	}
}

// fallbackTurnKey 不得携带 *time.Location。Go 的 time.Time `==` 会比较 loc
// 指针：phase 1 扫出来的 ts 带着连接的 Location，phase 2 再扫回来可能带着
// 另一个，同一时刻的两个值就 `!=`，于是每一次正文命中都变成 miss —— 而且
// 不报错，只是总结正文悄悄全空。
func TestFallbackTurnKeyIsLocationIndependent(t *testing.T) {
	typ := reflect.TypeOf(fallbackTurnKey{})
	want := map[string]reflect.Kind{
		"requestID":  reflect.String,
		"tsUnixMicr": reflect.Int64,
	}
	if typ.NumField() != len(want) {
		t.Fatalf("fallbackTurnKey must have exactly %d fields, got %d (%v)", len(want), typ.NumField(), typ)
	}
	for name, kind := range want {
		f, ok := typ.FieldByName(name)
		if !ok {
			t.Fatalf("fallbackTurnKey is missing field %q", name)
		}
		if f.Type.Kind() != kind {
			t.Fatalf("fallbackTurnKey.%s must be %v, got %v — a time.Time here "+
				"reintroduces the location-sensitive comparison", name, kind, f.Type)
		}
	}

	// 同一时刻的三种时区表示。必须用 .In() 换算，不能用 time.Date 分别构造 ——
	// 后者造出的是三个不同时刻，测的就不是时区无关性了。
	base := time.Date(2026, 9, 10, 17, 4, 13, 904215000, time.UTC)
	instants := []time.Time{
		base,
		base.In(time.FixedZone("CST", 8*3600)),
		base.In(time.FixedZone("PDT", -7*3600)),
	}
	first := newFallbackTurnKey("req-1", instants[0])
	for _, ts := range instants[1:] {
		if got := newFallbackTurnKey("req-1", ts); got != first {
			t.Fatalf("same instant in a different location produced a different key: %+v vs %+v", got, first)
		}
	}
	// 归一化不能把不同请求或不同时刻压成同一个键。
	if newFallbackTurnKey("req-2", instants[0]) == first {
		t.Fatal("keys must differ by request_id")
	}
	if newFallbackTurnKey("req-1", instants[0].Add(time.Microsecond)) == first {
		t.Fatal("keys must differ by ts at microsecond resolution")
	}
}

// 合并语义就是旧 LEFT JOIN 的语义：顺序由 phase 1 定，缺正文留 nil，
// 同一 (request_id, ts) 出现两次时两轮都拿到同一份正文。
func TestMergeFallbackTurnsPreservesOrderAndLeftSemantics(t *testing.T) {
	ts := time.Date(2026, 9, 10, 17, 4, 13, 0, time.UTC)
	k1 := newFallbackTurnKey("req-1", ts)
	k2 := newFallbackTurnKey("req-2", ts.Add(time.Second))
	k3 := newFallbackTurnKey("req-3", ts.Add(2*time.Second))

	keys := []fallbackTurnKey{k1, k2, k3}
	// 2026-10-01：正文按 request_id 单键配对（见 queryRequestLogsFallback 的
	// 命中率对比），所以 map 的键类型跟着变；turn 身份仍然是 (request_id, ts)。
	bodies := map[string]sessionBody{
		k1.requestID: {requestBody: strPtr(`{"a":1}`), responseBody: strPtr(`{"b":2}`)},
		k3.requestID: {requestBody: strPtr(`{"a":3}`), responseBody: strPtr(`{"b":4}`)},
	}

	turns := mergeFallbackTurns(keys, bodies)
	if len(turns) != 3 {
		t.Fatalf("want 3 turns, got %d", len(turns))
	}
	for i, turn := range turns {
		if turn.TurnNo != i+1 {
			t.Fatalf("turn %d has TurnNo %d; numbering must follow phase 1 order", i, turn.TurnNo)
		}
	}
	// 顺序由 phase 1 决定，不能被 bodies 的 map 遍历顺序带偏。
	if turns[0].RequestDelta.(map[string]any)["a"] != float64(1) ||
		turns[2].RequestDelta.(map[string]any)["a"] != float64(3) {
		t.Fatalf("bodies were not paired in phase-1 order: %+v", turns)
	}
	// k2 没有正文 —— 必须留 nil，而不是被相邻轮次的正文顶替。
	if turns[1].RequestDelta != nil || turns[1].ResponseDelta != nil {
		t.Fatalf("turn with no stored body must keep nil deltas, got %+v", turns[1])
	}
	if got := mergeFallbackTurns(nil, nil); got != nil {
		t.Fatalf("no turns must yield nil, got %+v", got)
	}

	// 同一 (request_id, ts) 在会话里出现两次：旧 LEFT JOIN 会产出两行，
	// 两轮都带同一份正文。合并也必须如此。
	dup := mergeFallbackTurns([]fallbackTurnKey{k1, k1}, bodies)
	if len(dup) != 2 || dup[0].RequestDelta == nil || dup[1].RequestDelta == nil {
		t.Fatalf("duplicate key must still produce two turns, each with the body: %+v", dup)
	}
}

func TestSessionSummaryV2RequestLogsFallbackPermitsSuperAdminScope(t *testing.T) {
	sql, args := buildRequestLogsFallbackQuery("session-1", "", nil)
	// 判据是「有没有加谓词」，不是「SQL 里有没有 tenant_id 这个词」——
	// 原生源的投影本身就带 t.tenant_id AS tenant_id，按词判会永远假阳性。
	for _, predicate := range []string{"AND rl.tenant_id =", "WHERE rl.tenant_id =", "LIMIT $"} {
		if strings.Contains(sql, predicate) {
			t.Fatalf("unscoped fallback query must not add %q:\n%s", predicate, sql)
		}
	}
	// 无租户作用域时，下推的 session 谓词是唯一的收敛条件，必须仍在。
	// 2026-10-01 缺陷 7：turns 腿改回 v1 视图后，谓词随之从原生源的
	// `t.session_id` 变成视图的 `rl.gw_session_id`（CASE 投影，裸 $1 会
	// 报 42P18，所以必须保留这个具体列比较）。
	if !strings.Contains(sql, "WHERE rl.gw_session_id = $1") {
		t.Fatalf("unscoped fallback must still scope by session (rl.gw_session_id):\n%s", sql)
	}
	if len(args) != 1 || args[0] != "session-1" {
		t.Fatalf("unexpected fallback args: %#v", args)
	}
}

// TestSessionSummaryV2FallbackTurnsLegReadsV1 pins the source of the fallback's
// turns leg, without needing a database.
//
// 缺陷 7（02c93d04e 引入）：turns 腿被换成 db.SessionFamilyTurnsForSessionSQL()，
// 展开是 `session_turns_hot UNION ALL session_turns` —— 与主路径
// session_turns_with_current_month 同源。fallback 只在主路径读不到轮次时才被
// 调用，于是它必然返回 0 行 → `no turns found` → HTTP 500。实测 1,700/1,700。
//
// 为什么要有这道**无库**守卫：真库门（TestSessionSummaryV2FallbackServesSessions-
// NativeSourceLacks）只在 TEST_PG_URL 存在时跑，而本仓库的 CI 默认不设它。
// 换句话说，缺陷 7 溜过去的那次，恰好是所有静态门全绿的那次。
func TestSessionSummaryV2FallbackTurnsLegReadsV1(t *testing.T) {
	sql, _ := buildRequestLogsFallbackQuery("session-1", "tenant-1", nil)

	if !strings.Contains(sql, "FROM request_logs_with_current_month rl") {
		t.Errorf("the fallback turns leg must read the v1 view; the fallback exists to serve "+
			"sessions the native source does not have, so reading the native source makes it "+
			"return 0 rows for exactly the population it exists to serve (audit defect 7):\n%s", sql)
	}
	if strings.Contains(sql, "SessionFamilyTurnsForSessionSQL") {
		t.Errorf("the fallback turns leg must NOT use db.SessionFamilyTurnsForSessionSQL(): it "+
			"expands to session_turns_hot UNION ALL session_turns, the same store the primary "+
			"path reads (audit defect 7):\n%s", sql)
	}
	// 会话谓词必须落在 gw_session_id 这个具体列上。它是 CASE 投影的表达式，
	// 裸 `$1` 推不出参数类型（真库实测 42P18），删掉它则整个查询失去收敛条件。
	if !strings.Contains(sql, "WHERE rl.gw_session_id = $1") {
		t.Errorf("the fallback must stay scoped by session on the concrete column "+
			"rl.gw_session_id:\n%s", sql)
	}
	// 内部调用排除：读回 v1 视图会把网关自己生成的标题/摘要 LLM 调用带进
	// 对话文本（internal_loopback 36,693 行）与 in_progress 占位
	//（non_terminal 1,541 行）。谓词与 dual-read-drift 同源，不另起一份。
	// 方向判据：必须**保留** 'genuine_loss'，排除 internal_loopback /
	// non_terminal。写反不报错也不空 —— 近 3 天窗口下 `<> 'genuine_loss'`
	// 会留下 1,829 行（loopback 1,721 + non_terminal 108，正好是被丢掉的那
	// 批），把 14,546 行业务轮次全扔掉。所以这里断言等号，不只断言"有谓词"。
	if !strings.Contains(sql, "= 'genuine_loss'") {
		t.Errorf("the fallback turns leg must KEEP genuine_loss rows (the ones the mirror hook "+
			"would have written) and drop internal_loopback / non_terminal, via "+
			"db.MirrorDriftClassSQL:\n%s", sql)
	}
	if strings.Contains(sql, "<> 'genuine_loss'") {
		t.Errorf("`<> 'genuine_loss'` keeps the loopback/non_terminal rows and discards the "+
			"business turns — the label reads like 'bad rows' but means 'rows the hook would "+
			"have mirrored', i.e. exactly the ones this endpoint serves:\n%s", sql)
	}
	// SSOT：断言打在**展开后的 SQL 产物**上，不打源码文本。
	//
	// 第一版断言是 `strings.Contains(源码, "db.MirrorDriftClassSQL")`，
	// 变异验证时被一分钟拆穿：把谓词逐字复制到本地常量、再在函数里留一句
	// 无用的 `_ = db.MirrorDriftClassSQL`，断言照样绿 —— 整文件子串匹配会被
	// 任何一处**死**引用满足，这是「守卫写整文件子串匹配」的典型弱形态。
	//
	// 断言产物的好处：本地复制与 db 版即使逐字相同，判据也只在「真正被
	// 复制的那份」与 db 版**产生分歧**时才放过。也就是说它保证的是行为
	// 一致（这才是要紧的），不是「一定没有第二份定义」—— 后者编译器管不了。
	if !strings.Contains(sql, db.MirrorDriftClassSQL) {
		t.Errorf("the exclusion must be built from db.MirrorDriftClassSQL verbatim so a local "+
			"copy cannot drift from the dual-read-drift classification; the expanded query "+
			"does not contain it:\n%s", sql)
	}
}
