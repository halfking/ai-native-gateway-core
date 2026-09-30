//go:build integration

package admin

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

// 会话存储解耦 v3 审计（2026-10-01）：fallback 端点从「一条 115 列 LEFT JOIN」
// 拆成「phase 1 取轮次键 + phase 2 批量取正文」。
//
//	旧单查询（gw_7a19bfa5，20 轮）  40,483 ms   8,513,122 buffers
//	phase 1                        41 ms       1,029 buffers
//	phase 2（有命中）               501 ms
//	phase 2（全 miss）              7 ms
//
// 单元守卫只能证明 SQL **长得像**该有的样子，证明不了它跑起来是否仍与旧查询
// 等价。这里把拆开前的原始查询逐字保留为 legacyFallbackQuery，对同一批真实
// 会话跑旧/新两条路径，比较**端点真正吐出去的** turns（TurnNo + 两个正文
// 解码值），而不是中间行 —— 中间行相等但合并错了，端点照样坏。
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestSessionSummaryV2Fallback
func TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the split preserved the old query's rows")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	probePool := newFallbackProbePool(t, ctx, pool)
	if len(probePool) == 0 {
		t.Skip("no usable probe sessions in this database — skipping proves nothing either way")
	}

	// upToTurn 同时钉住有界与无界两条分支。
	//
	// 轮数必须小。旧查询约 0.8 秒/轮（逐轮 ColumnarScan），跑 200 轮就是
	// 160 秒；第一版把 53,851 轮的 sys:probe 会话丢进无界分支，整门跑满
	// 15 分钟超时。**旧形态的慢正是本节要修的东西，拿它当夹具必须先设上限。**
	for _, probe := range probePool {
		for _, limit := range probe.limits {
			label := "limit=nil " + probe.sessionID
			if limit != nil {
				label = "limit=" + strconv.Itoa(*limit) + " " + probe.sessionID
			}
			if len(label) > 40 {
				label = label[:40]
			}
			t.Run(label, func(t *testing.T) {
				legacy := queryLegacyFallbackTurns(t, ctx, pool, probe.sessionID, limit)
				split := querySplitFallbackTurns(t, ctx, pool, probe.sessionID, limit)
				if len(legacy) == 0 {
					t.Fatalf("probe returned no rows — an empty comparison proves nothing")
				}
				if len(legacy) != len(split) {
					t.Fatalf("turn count diverged: legacy=%d split=%d", len(legacy), len(split))
				}
				for i := range legacy {
					if !reflect.DeepEqual(legacy[i], split[i]) {
						t.Fatalf("turn %d diverged:\n legacy=%s\n split =%s", i, formatTurn(legacy[i]), formatTurn(split[i]))
					}
				}
				t.Logf("%d turns compared, %d of them carry a stored body", len(legacy), probe.bodies)
			})
		}
	}
}

// TestSessionSummaryV2FallbackBodiesStaysOnIndexPath pins the *plan*, not the
// SQL text. The shapes measured on 2026-10-01:
//
//	IN (VALUES ...)       → Index Scan using request_logs_bodies_2026_09_pkey    7 ms
//	IN (SELECT unnest)    → Index Scan using request_logs_bodies_2026_09_pkey    7 ms
//	unnest + LEFT JOIN    → ColumnarScan 全扫 2,216,660 行                   10,365 ms
//	（旧的整体 LEFT JOIN） → 逐轮 ColumnarScan                              40,483 ms
//
// The SQL-text guard cannot tell the first two from the third: all three name
// the same view and the same columns. What separates them is whether the planner
// walks the primary key or scans columnar chunks — so that is what is asserted.
func TestSessionSummaryV2FallbackBodiesStaysOnIndexPath(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that phase 2 stays on the index path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 取真实轮次键，避免合成值让规划器走出与生产不同的分支。
	keys := pickFallbackKeys(t, ctx, pool, 20)
	if len(keys) == 0 {
		t.Skip("no session_turns rows available")
	}
	requestIDs := make([]string, len(keys))
	timestamps := make([]time.Time, len(keys))
	for i, k := range keys {
		requestIDs[i] = k.requestID
		timestamps[i] = k.ts
	}

	rows, err := pool.Query(ctx, "EXPLAIN "+fallbackBodiesSQL, requestIDs, timestamps)
	if err != nil {
		t.Fatalf("explain phase 2: %v", err)
	}
	defer rows.Close()
	var planLines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		planLines = append(planLines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	t.Logf("phase 2 plan:\n%s", strings.Join(planLines, "\n"))

	for _, line := range planLines {
		if !strings.Contains(line, "ColumnarScan on request_logs_bodies") {
			continue
		}
		if strings.Contains(line, "never executed") {
			continue
		}
		t.Fatalf("phase 2 fell back to a columnar chunk scan of the bodies partitions:\n%s\n"+
			"that is the 10,365ms shape — the semi-join must stay drivable by the "+
			"(request_id, ts) primary key", line)
	}
}

func fallbackLimitPtr(i int) *int { return &i }

// fallbackProbe is one session to compare the legacy and split paths on,
// discovered from the live database rather than hard-coded — hard-coded ids rot
// as soon as the bodies retention window or the session TTL moves.
type fallbackProbe struct {
	sessionID string
	tenantID  string
	turns     int
	bodies    int // how many of those turns actually have a stored body
	limits    []*int
}

// newFallbackProbePool picks a small, cheap, but shape-diverse set. Every step
// is one set-based query — the second version of this helper probed 60 candidate
// sessions one at a time, and at ~3s per probe the *discovery* cost 180s, which
// is how a test whose subject is "the split is equivalent" ended up timing out.
//
//   - several short sessions that DO have bodies — the only shape that exercises
//     the body-hit path, and each costs one legacy iteration;
//   - one multi-turn session with no bodies at all — the consecutive-miss path;
//   - one sys:-prefixed session — its projected gw_session_id is CASE'd to NULL,
//     the easiest thing in this query to get wrong.
func newFallbackProbePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []fallbackProbe {
	t.Helper()
	var out []fallbackProbe

	// 1) 有正文的会话：从 bodies 侧反查。全库这种命中只有约 1,175 行，
	//    带 LIMIT 实测 1.9s；反过来先列 session_turns 再逐个去数正文要 60 × 3s。
	bodyHits := queryStrings(t, ctx, pool, `
		SELECT t.session_id || '|' || t.tenant_id
		  FROM request_logs_bodies_with_current_month b
		  JOIN session_turns t ON t.request_id = b.request_id AND t.ts = b.ts
		 LIMIT 8`)
	for _, raw := range bodyHits {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		p := fallbackProbe{sessionID: id, tenantID: tenant, bodies: 1}
		p.turns = countTurns(t, ctx, pool, id, tenant)
		p.limits = []*int{nil, fallbackLimitPtr(1)}
		out = append(out, p)
	}

	// 2) 多轮、正文全缺：连续 miss。只从 session_turns 找，不碰 bodies。
	for _, raw := range queryStrings(t, ctx, pool, `
		SELECT session_id || '|' || tenant_id
		  FROM session_turns
		 WHERE session_id NOT LIKE 'sys:%'
		 GROUP BY session_id, tenant_id
		HAVING count(*) BETWEEN 5 AND 60
		 ORDER BY session_id
		 LIMIT 8`) {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		turns, bodies := countTurnsAndBodies(t, ctx, pool, id, tenant)
		if bodies != 0 {
			continue // 这一组不覆盖 miss 路径，换下一个
		}
		out = append(out, fallbackProbe{
			sessionID: id, tenantID: tenant, turns: turns, bodies: 0,
			limits: []*int{nil, fallbackLimitPtr(7)},
		})
		break
	}

	// 3) sys: 前缀：投影 gw_session_id 为 NULL 的那一类。
	if p, ok := pickSysPrefixed(t, ctx, pool); ok {
		p.turns = countTurns(t, ctx, pool, p.sessionID, p.tenantID)
		p.limits = []*int{fallbackLimitPtr(7)}
		out = append(out, p)
	}

	for i := range out {
		t.Logf("probe %s turns=%d bodies=%d limits=%d", out[i].sessionID, out[i].turns, out[i].bodies, len(out[i].limits))
	}
	return out
}

func queryStrings(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) []string {
	t.Helper()
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("discover sessions: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("discover sessions scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("discover sessions rows: %v", err)
	}
	return out
}

func countTurns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID, tenantID string) int {
	t.Helper()
	turns, _ := countTurnsAndBodies(t, ctx, pool, sessionID, tenantID)
	return turns
}

func countTurnsAndBodies(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID, tenantID string) (int, int) {
	t.Helper()
	var turns, bodies int
	err := pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(b.request_id)::int
		  FROM session_turns t
		  LEFT JOIN request_logs_bodies_with_current_month b
		    ON b.request_id = t.request_id AND b.ts = t.ts
		 WHERE t.session_id = $1 AND t.tenant_id = $2`, sessionID, tenantID).Scan(&turns, &bodies)
	if err != nil {
		t.Fatalf("count turns for %s: %v", sessionID, err)
	}
	return turns, bodies
}

func pickSysPrefixed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (fallbackProbe, bool) {
	t.Helper()
	var p fallbackProbe
	err := pool.QueryRow(ctx, `
		SELECT session_id, tenant_id FROM session_turns
		 WHERE session_id LIKE 'sys:%'
		 ORDER BY session_id LIMIT 1`).Scan(&p.sessionID, &p.tenantID)
	if err != nil {
		return p, false
	}
	p.turns, p.bodies = countTurnsAndBodies(t, ctx, pool, p.sessionID, p.tenantID)
	return p, true
}

const fallbackTenant = "default"

// legacyFallbackQuery is the pre-split query, kept verbatim so the equivalence
// test compares against the real previous behaviour rather than a paraphrase.
func legacyFallbackQuery(sessionID, tenantID string, upToTurn *int) (string, []any) {
	query := `
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM ` + db.SessionFamilyTurnsForSessionSQL() + ` rl
		LEFT JOIN request_logs_bodies_with_current_month rb
			ON rb.request_id = rl.request_id
			AND rb.ts = rl.ts
		WHERE 1 = 1`
	args := []any{sessionID}
	if tenantID != "" {
		query += " AND rl.tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY rl.ts ASC"
	if upToTurn != nil {
		query += " LIMIT $" + strconv.Itoa(len(args)+1)
		args = append(args, *upToTurn)
	}
	return query, args
}

// queryLegacyFallbackTurns produces what the endpoint returned before the split.
func queryLegacyFallbackTurns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID string, upToTurn *int) []turnForSummary {
	t.Helper()
	query, args := legacyFallbackQuery(sessionID, fallbackTenant, upToTurn)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("legacy query: %v", err)
	}
	defer rows.Close()
	var out []turnForSummary
	for rows.Next() {
		var requestID string
		var ts time.Time
		var reqRaw, respRaw []byte
		if err := rows.Scan(&requestID, &ts, &reqRaw, &respRaw); err != nil {
			t.Fatalf("legacy scan: %v", err)
		}
		out = append(out, turnForSummary{
			TurnNo:        len(out) + 1,
			RequestDelta:  decodeStoredJSON("request_body", requestID, reqRaw),
			ResponseDelta: decodeStoredJSON("response_body", requestID, respRaw),
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("legacy rows: %v", err)
	}
	return out
}

// querySplitFallbackTurns produces what the endpoint returns now, by calling
// the production method rather than re-implementing the two phases here.
func querySplitFallbackTurns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID string, upToTurn *int) []turnForSummary {
	t.Helper()
	api := NewSessionSummaryV2API(pool)
	turns, err := api.queryRequestLogsFallback(ctx, sessionID, fallbackTenant, upToTurn)
	if err != nil {
		t.Fatalf("split query: %v", err)
	}
	return turns
}

func formatTurn(tn turnForSummary) string {
	return "TurnNo=" + strconv.Itoa(tn.TurnNo) +
		" req=" + describeDelta(tn.RequestDelta) +
		" resp=" + describeDelta(tn.ResponseDelta)
}

func describeDelta(v any) string {
	if v == nil {
		return "<nil>"
	}
	s := reflect.ValueOf(v)
	switch s.Kind() {
	case reflect.Map, reflect.Slice:
		return "<" + s.Kind().String() + " len=" + strconv.Itoa(s.Len()) + ">"
	default:
		return reflect.ValueOf(v).String()
	}
}

// fallbackProbeKey is the raw (request_id, ts) pair as stored — the caller needs
// the time.Time itself to bind, not the normalized fallbackTurnKey.
type fallbackProbeKey struct {
	requestID string
	ts        time.Time
}

func pickFallbackKeys(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) []fallbackProbeKey {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT t.request_id, t.ts
		  FROM session_turns t
		 WHERE t.session_id NOT LIKE 'sys:%'
		 ORDER BY t.ts DESC
		 LIMIT $1`, n)
	if err != nil {
		t.Fatalf("pick keys: %v", err)
	}
	defer rows.Close()
	var keys []fallbackProbeKey
	for rows.Next() {
		var requestID string
		var ts time.Time
		if err := rows.Scan(&requestID, &ts); err != nil {
			t.Fatalf("pick keys scan: %v", err)
		}
		keys = append(keys, fallbackProbeKey{requestID: requestID, ts: ts})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("pick keys rows: %v", err)
	}
	return keys
}
