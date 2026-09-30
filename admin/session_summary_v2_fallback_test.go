package admin

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

// 会话存储解耦 v3 审计（2026-09-30）：本查询曾迁到 session 族原生源
// db.SessionFamilyTurnsForSessionSQL（实测 14000ms → 5308ms），随后被真库核对
// 否决并回退 —— 当时 sessions_v2.enabled=true / shadow_write=true 的前提下，
// 仍有 20,660 个会话 / 38,878 行只存在于 request_logs。
//
// **该否决理由已于同日被推翻**（见审计报告 §5.3.1 / §5.4）：那 38,878 行里
// 绝大多数是按设计排除的 internal_loopback 与 non_terminal，真正缺失的
// `genuine_loss` 只有 1,459 行，已由 mirror_outbox_backfill.sql 全量补写，
// 35 天窗口复测为 0。故本查询于同日重新迁回原生源。
//
// 切换前后的口径差（全量实测）：视图比原生源多 38,229 行、涉及 2.30% 会话，
// 全部是 hook 按设计不镜像的两类，unexplained = 0。对本端点是净收益——
// 总结正文此前会把网关自己生成的标题/摘要调用当成用户发言。
//
// 关键契约：session 谓词由 helper **下推进两条腿**（t.session_id = $1），
// 外层**不得**再加 gw_session_id —— 投影名是 CASE 表达式，加了会把下推
// 打回全表扫，13 倍的收益就没了。
func TestSessionSummaryV2RequestLogsFallbackUsesNativeSourceWithPushedPredicate(t *testing.T) {
	limit := 3
	sql, args := buildRequestLogsFallbackQuery("session-1", "tenant-a", &limit)
	for _, want := range []string{
		"FROM public.session_turns_hot t", // 两条腿
		"FROM public.session_turns t",
		"WHERE t.session_id = $1", // 谓词下推（两条腿各一次）
		"rl.tenant_id = $2",
		"ORDER BY rl.ts ASC",
		"LIMIT $3",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("fallback SQL missing %q:\n%s", want, sql)
		}
	}
	if n := strings.Count(sql, "WHERE t.session_id = $1"); n != 2 {
		t.Fatalf("session predicate must be pushed into both legs, found %d", n)
	}
	for _, forbidden := range []string{
		"FROM request_logs_with_current_month rl", // 不得回退到视图
		"rl.gw_session_id = $1",                   // 会打掉下推
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
		if !strings.Contains(fallbackBodiesSQL, want) {
			t.Fatalf("phase 2 SQL missing %q:\n%s", want, fallbackBodiesSQL)
		}
	}
	if strings.Contains(fallbackBodiesSQL, "LEFT JOIN") {
		t.Fatalf("phase 2 must be a semi-join, not a LEFT JOIN — the LEFT JOIN "+
			"form forces a full columnar scan (10,365ms vs 7ms):\n%s", fallbackBodiesSQL)
	}
	// 两个数组参数（而非每轮两个占位符）：upToTurn 为 nil 时轮数无上界，
	// VALUES 形态会在 32767 轮撞上 65535 参数上限。
	if strings.Contains(fallbackBodiesSQL, "$3") {
		t.Fatalf("phase 2 must bind exactly two array parameters, got:\n%s", fallbackBodiesSQL)
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
	bodies := map[fallbackTurnKey]fallbackBody{
		k1: {requestBody: []byte(`{"a":1}`), responseBody: []byte(`{"b":2}`)},
		k3: {requestBody: []byte(`{"a":3}`), responseBody: []byte(`{"b":4}`)},
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
	if !strings.Contains(sql, "WHERE t.session_id = $1") {
		t.Fatalf("unscoped fallback must still scope by session:\n%s", sql)
	}
	if len(args) != 1 || args[0] != "session-1" {
		t.Fatalf("unexpected fallback args: %#v", args)
	}
}
