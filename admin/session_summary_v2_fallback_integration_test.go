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
				// 不变式 1：轮次集合。phase 1 拥有 ORDER BY / LIMIT，phase 2 只
				// 供正文，所以拆分不得丢轮、不得改序、不得改编号。
				if len(legacy) != len(split) {
					t.Fatalf("turn count diverged: legacy=%d split=%d", len(legacy), len(split))
				}
				for i := range legacy {
					if legacy[i].TurnNo != split[i].TurnNo {
						t.Fatalf("turn %d numbering diverged: legacy=%d split=%d",
							i, legacy[i].TurnNo, split[i].TurnNo)
					}
				}
				// 不变式 2：正文只能变多，不能变错也不能串位。
				//
				// 元组键 → 单键是一次**有意**的行为变更，所以逐轮 DeepEqual
				// 不再成立（那 89% 的轮次 legacy 是 nil、split 有内容）。真正
				// 需要钉住的是方向性：
				//   - legacy 配上了的轮次，split 必须配上**同一份**正文
				//     （request_id 在 bodies 里唯一：2,220,507 行 = 2,220,507
				//     个不同 request_id，所以单键不可能取到别的行）；
				//   - legacy 没配上的轮次，split 可以有正文（本次的收益），
				//     但绝不允许「串到别的轮次」——TurnNo 相等已在上面对齐。
				legacyBodies, splitBodies := 0, 0
				for i := range legacy {
					legacyHas := legacy[i].RequestDelta != nil || legacy[i].ResponseDelta != nil
					splitHas := split[i].RequestDelta != nil || split[i].ResponseDelta != nil
					if legacyHas {
						legacyBodies++
						if !splitHas {
							t.Fatalf("turn %d had a body under the tuple key but lost it under "+
								"request_id pairing — the single key must be a superset:\n legacy=%s\n split =%s",
								i, formatTurn(legacy[i]), formatTurn(split[i]))
						}
						if !reflect.DeepEqual(legacy[i], split[i]) {
							t.Fatalf("turn %d: the tuple key and the request_id key resolved to "+
								"DIFFERENT bodies, which means request_id is not unique in the "+
								"bodies store:\n legacy=%s\n split =%s",
								i, formatTurn(legacy[i]), formatTurn(split[i]))
						}
					}
					if splitHas {
						splitBodies++
					}
				}
				if splitBodies < legacyBodies {
					t.Fatalf("request_id pairing lost bodies: legacy=%d split=%d", legacyBodies, splitBodies)
				}
				t.Logf("%d turns compared; bodies legacy(tuple)=%d split(request_id)=%d (+%d)",
					len(legacy), legacyBodies, splitBodies, splitBodies-legacyBodies)
			})
		}
	}
}

// TestSessionSummaryV2FallbackBodiesStaysOnIndexPath pins the *plan*, not the
// SQL text. The shapes measured on 2026-10-01 (200 real request_ids, one
// session, same three body columns, real database — audit report §8.3):
//
//	IN (SELECT unnest($1)) → Index Scan using _2026_09_pkey          1.77~2.20 s
//	= ANY($1::text[])      → ColumnarScan, Rows Removed 2,217,398   15.85~17.43 s
//
// The SQL-text guard cannot tell the first from the second: both name the same
// view and the same columns, and both look like a batched semi-join. What
// separates them is whether the planner walks the primary key or scans columnar
// chunks — so that is what is asserted.
//
// 两条本轮实测换掉的旧认知：
//   - **计划缓存不是原因。** 同一 prepared statement 连跑 7 次（含第 7 次的
//     通用计划，Planning 0.033 ms），2026_09 分区每一次都是 ColumnarScan。
//   - **不是「EXPLAIN 插桩」也不是「投影宽度」。** 真实生产函数
//     querySessionBodiesByRequestID 连测 15.85 / 15.88 / 17.43 s，与 EXPLAIN
//     ANALYZE 同量级；而只选 request_id 不选正文列，代价几乎不变。
//     反过来 `SELECT count(*)` 包住同一个子查询只要 2.0 s —— 那是规划器把
//     投影整个消掉了，量的不是同一件事，别拿它当「其实不慢」的证据。
func TestSessionSummaryV2FallbackBodiesStaysOnIndexPath(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that phase 2 stays on the index path")
	}
	// 主机防护必须排在 EXPLAIN ANALYZE **之前**：这道门要真跑一次分析型扫描，
	// 而同族扫描在 252 上独占 IO 1h32m、把生产写链饿死（252 审计 R17 §二.5）。
	// 集成门能被一个环境变量指向任意主机，所以默认只准回环。
	if allowed, why := heavyMeasurementAllowed(dsn, os.Getenv(heavyMeasurementOptInEnv)); !allowed {
		t.Skipf("skipping the measurement-grade phase 2 plan: %s", why)
	} else if why != "" {
		t.Logf("running against a non-local host: %s", why)
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

	// EXPLAIN 的对象必须是生产真正发出的那条 phase 2 —— 2026-10-01 起是
	// request_id 单键那条（见 queryRequestLogsFallback）。EXPLAIN 一条不再
	// 执行的 SQL，等于给一个已经下线的形状做性能保证。
	// 必须带 ANALYZE：**不执行的计划证明不了性能**。
	// 原判据跑的是裸 EXPLAIN，输出里只有 cost、没有 actual rows / Buffers /
	// Execution Time，于是「这条分支多贵」这个问题根本没被问过。
	rows, err := pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sessionBodiesByRequestIDSQL, requestIDs)
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

	// 锚点必须是**计划里真实出现的字面量**。Citus 列存的节点行长这样：
	//
	//	  ->  Custom Scan (ColumnarScan) on request_logs_bodies_2026_09 request_logs_bodies ...
	//
	// 原判据找的是 "ColumnarScan on request_logs_bodies"（无括号、无空格），
	// 而真实文本是 "ColumnarScan) on ..."。**这个子串在计划里一次都不出现**，
	// 所以这道门自诞生起就是恒绿的：它既不匹配任何东西，也无从变红。
	// （用 grep 在它自己的计划输出里找那个子串，命中 0 次。）
	const columnarNodeMarker = "ColumnarScan) on request_logs_bodies"
	var columnarLines []string
	// 索引路径的正向锚点。**必须有它**，否则这道门会退化成单边断言：
	// 「没看到 ColumnarScan」在分区哪天变回 heap 时会静默成立，于是门永远绿
	// —— 那正是它过去的样子。判据要钉住「走到了哪条路」，不是「没走哪条路」。
	const indexPathMarker = "request_logs_bodies_2026_09_pkey"
	var indexPathLines []string
	for _, line := range planLines {
		if strings.Contains(line, indexPathMarker) &&
			(strings.Contains(line, "Index Scan") || strings.Contains(line, "Bitmap Index Scan")) {
			indexPathLines = append(indexPathLines, strings.TrimSpace(line))
		}
		if !strings.Contains(line, columnarNodeMarker) {
			continue
		}
		if strings.Contains(line, "never executed") {
			continue
		}
		columnarLines = append(columnarLines, strings.TrimSpace(line))
	}

	// 2026-10-01：谓词已从 `= ANY($1::text[])` 改成 `IN (SELECT unnest($1::text[]))`
	// （审计报告 §8.3.3），同一批 200 个真实 id 从 15.85~17.43 s 降到 1.77~2.20 s。
	// 所以 P0 已闭合，这道门从「显式 Skip 等拍板」翻回**红门**：列存全扫一旦回来
	// 就是回归，不该再靠人记得去看日志。实测成本仍然每次打印。
	execMs := ""
	removed := ""
	for _, line := range planLines {
		if strings.Contains(line, "Execution Time:") {
			execMs = strings.TrimSpace(line)
		}
		if strings.Contains(line, "Rows Removed by Filter:") && removed == "" {
			removed = strings.TrimSpace(line)
		}
	}
	t.Logf("phase 2 实测成本：%s | %s", execMs, removed)
	if len(columnarLines) > 0 {
		t.Fatalf("phase 2 fell back onto a columnar partition scan (audit §8.3.3):\n  %s\n  %s\n"+
			"  The = ANY($1::text[]) form makes the planner abandon the primary key "+
			"probe; IN (SELECT unnest($1::text[])) is the form that keeps it. "+
			"Measured on the same 200 ids: 15.85~17.43 s vs 1.77~2.20 s.",
			strings.Join(columnarLines, "\n  "), execMs)
	}
	if len(indexPathLines) == 0 {
		t.Fatalf("phase 2 reached no primary-key probe on %s — the bodies partitions were "+
			"neither index-probed nor columnar-scanned, so this gate's positive anchor "+
			"found nothing and the run would pass vacuously:\n%s",
			indexPathMarker, strings.Join(planLines, "\n"))
	}
	t.Logf("phase 2 走主键探针：%s", strings.Join(indexPathLines, "\n  "))
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
//   - several sessions the native source does NOT have — the population the
//     fallback exists to serve, and where defect 7 lived (see
//     TestSessionSummaryV2FallbackServesSessionsNativeSourceLacks);
//   - one sys:-prefixed session — its projected gw_session_id is CASE'd to NULL,
//     the easiest thing in this query to get wrong.
//
// 两个预算纪律，都是实测换来的：
//
//  1. **探针一律带时间窗**（probeWindow）。不带走全量 v1 视图的 GROUP BY 在
//     本机跑了 5 分半未完成（实测 pg_stat_activity 里挂着），加上窗之后 1.5s。
//     测试**发现**阶段的查询和被测查询一样会烧预算，不能当成免费的。
//  2. **轮数夹在 2~60**。legacy 单查询逐轮 ColumnarScan，约 0.8 秒/轮，
//     一个 1,365 轮的无界探针就是 18 分钟。
//
// 「有正文」那组不再与 bodies 视图做 JOIN 来挑：2.2M 行的 bodies 视图接上
// v1 视图就是一次全量哈希连接，成本和它要证明的事情不成比例。改成先按窗口
// 取候选会话，再用主键逐个点算（countTurnsAndBodies 走 (request_id, ts) 索引）。
func newFallbackProbePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []fallbackProbe {
	t.Helper()
	var out []fallbackProbe

	// 候选会话：v1 视图近窗口内、轮数适中的会话。2026-10-01 重定基线 ——
	// 探针必须取自 **v1 视图**，因为 turns 腿现在读的是 v1。此前它
	// `JOIN session_turns ON request_id AND ts`：既只看得到原生源有的会话
	// （缺陷 7 所服务那批的反面），又只挑得出 ts 恰好相等的那 11.2%。
	candidates := queryStrings(t, ctx, pool, `
		SELECT rl.gw_session_id || '|' || rl.tenant_id
		  FROM request_logs_with_current_month rl
		 WHERE rl.gw_session_id IS NOT NULL
		   AND rl.ts >= now() - `+probeWindow+`
		   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'
		 GROUP BY rl.gw_session_id, rl.tenant_id
		HAVING count(*) BETWEEN 2 AND 20
		 ORDER BY count(*) DESC
		 LIMIT 40`)

	var missProbes, hitProbes, tsMismatchProbes int
	for _, raw := range candidates {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		turns, bodies := countTurnsAndBodies(t, ctx, pool, id, tenant)
		if turns == 0 {
			continue
		}
		p := fallbackProbe{sessionID: id, tenantID: tenant, turns: turns, bodies: bodies}
		switch {
		case bodies > 0 && hitProbes < 6:
			// 有正文：命中路径，legacy 与 split 都可能取到内容。
			p.limits = []*int{nil, fallbackLimitPtr(1)}
			hitProbes++
		case bodies == 0 && missProbes < 3:
			// 元组键全 miss：这条正是本次行为变更要改善的形状。
			p.limits = []*int{nil, fallbackLimitPtr(7)}
			missProbes++
		default:
			continue
		}
		out = append(out, p)
	}

	// ts 不等组：**本次行为变更真正要覆盖的形状**。
	//
	// 2026-10-01 第一版门虽然绿了，但每一行的日志都是 "+0"——legacy(元组)
	// 与 split(单键) 取到的正文一样多。也就是说它压根没碰到被改的那个行为。
	// 原因很直白：探针是从 bodies 视图里挑的，而挑的时候条件就是
	// `(request_id, ts)` 能对上，于是按定义只挑得到那 11.2%。
	//
	// 这一组反着挑：**bodies 存在但 b.ts <> rl.ts** 的会话，正是只有单键才
	// 拿得到正文的那些轮次。缺了它，"正文只增不减"这条不变式就退化成一个
	// 恒真断言。
	for _, raw := range queryStrings(t, ctx, pool, `
		SELECT rl.gw_session_id || '|' || rl.tenant_id
		  FROM request_logs_with_current_month rl
		  JOIN request_logs_bodies_with_current_month b
		    ON b.request_id = rl.request_id
		 WHERE rl.gw_session_id IS NOT NULL
		   AND rl.ts >= now() - `+probeWindow+`
		   AND b.ts <> rl.ts
		   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'
		 GROUP BY rl.gw_session_id, rl.tenant_id
		HAVING count(*) >= 2
		 LIMIT 3`) {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		turns, bodies := countTurnsAndBodies(t, ctx, pool, id, tenant)
		if turns == 0 {
			continue
		}
		// 只跑 limit=1：这批会话轮数很大（实测 1365 / 345 / 310），而 legacy
		// 单查询约 0.8 秒/轮，跑无界分支等于拿 18 分钟换一行日志。
		out = append(out, fallbackProbe{
			sessionID: id, tenantID: tenant, turns: turns, bodies: bodies,
			limits: []*int{fallbackLimitPtr(1)},
		})
		tsMismatchProbes++
	}

	// 空集必须硬失败，不能静默跳过。
	//
	// 第一版这一组返回 0 行，门照样全绿 —— 而它正是本次行为变更唯一真正
	// 覆盖到的形状。「正文只增不减」那条不变式一旦没有 ts 不等的样本，就
	// 退化成恒真断言：所有探针的日志都是 +0，门在证明一件没发生的事。
	// 判据要钉住**扫描量**，否则空集合会让门静默通过。
	if tsMismatchProbes == 0 {
		t.Fatalf("no session in the %s window has a body whose ts differs from its v1 turn ts "+
			"(i.e. a body only the request_id key can reach). The fallback body-pairing change "+
			"is therefore NOT exercised by this run, and the 'bodies never decrease' invariant "+
			"degenerates into a tautology. Widen probeWindow or the data window before treating "+
			"a green result as evidence.", probeWindow)
	}

	// v1-only 人群：v1 视图里有轮次、session 族两张表里一行都没有。
	// 这正是 generateSummary 唯一会走 fallback 的那一类，也是缺陷 7 的靶心。
	// 此前这道门的探针**全部**取自 session_turns，于是它系统性地看不见这批
	// 会话 —— 门全绿与缺陷存在可以同时为真。
	//
	// 只跑 limit=7 的有界分支：这批会话轮数很大（实测 1365 / 345 / 310），
	// 而 legacy 单查询约 0.8 秒/轮，无界分支会单独吃满整门的预算。
	for _, raw := range queryStrings(t, ctx, pool, `
		SELECT rl.gw_session_id || '|' || rl.tenant_id
		  FROM request_logs_with_current_month rl
		 WHERE rl.gw_session_id IS NOT NULL
		   AND rl.ts >= now() - `+probeWindow+`
		   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'
		   AND NOT EXISTS (SELECT 1 FROM public.session_turns x WHERE x.session_id = rl.gw_session_id)
		   AND NOT EXISTS (SELECT 1 FROM public.session_turns_hot x WHERE x.session_id = rl.gw_session_id)
		 GROUP BY rl.gw_session_id, rl.tenant_id
		HAVING count(*) >= 3
		 ORDER BY count(*) DESC
		 LIMIT 3`) {
		id, tenant, ok := strings.Cut(raw, "|")
		if !ok {
			t.Fatalf("unexpected probe row %q", raw)
		}
		turns, bodies := countTurnsAndBodies(t, ctx, pool, id, tenant)
		if turns == 0 {
			continue
		}
		out = append(out, fallbackProbe{
			sessionID: id, tenantID: tenant, turns: turns, bodies: bodies,
			limits: []*int{fallbackLimitPtr(7)},
		})
	}

	// sys: 前缀：投影 gw_session_id 为 NULL 的那一类。
	if p, ok := pickSysPrefixed(t, ctx, pool); ok {
		p.turns = countTurns(t, ctx, pool, p.sessionID, p.tenantID)
		p.limits = []*int{fallbackLimitPtr(7)}
		out = append(out, p)
	}

	for i := range out {
		if out[i].turns == 0 {
			t.Fatalf("probe %s has no turns in the v1 view", out[i].sessionID)
		}
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
		  FROM request_logs_with_current_month rl
		  LEFT JOIN request_logs_bodies_with_current_month b
		    ON b.request_id = rl.request_id AND b.ts = rl.ts
		 WHERE rl.gw_session_id = $1 AND rl.tenant_id = $2`, sessionID, tenantID).Scan(&turns, &bodies)
	if err != nil {
		t.Fatalf("count turns for %s: %v", sessionID, err)
	}
	return turns, bodies
}

func pickSysPrefixed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (fallbackProbe, bool) {
	t.Helper()
	var p fallbackProbe
	err := pool.QueryRow(ctx, `
		SELECT gw_session_id, tenant_id FROM request_logs_with_current_month
		 WHERE gw_session_id LIKE 'sys:%'
		   AND ts >= now() - `+probeWindow+`
		 GROUP BY gw_session_id, tenant_id
		 ORDER BY gw_session_id LIMIT 1`).Scan(&p.sessionID, &p.tenantID)
	if err != nil {
		return p, false
	}
	p.turns, p.bodies = countTurnsAndBodies(t, ctx, pool, p.sessionID, p.tenantID)
	return p, true
}

const fallbackTenant = "default"

// probeWindow bounds every probe-discovery query. An unbounded GROUP BY over
// the full v1 view was measured at >5 minutes on this database (found still
// running in pg_stat_activity after the client had given up); with the window
// the same shape is 1.5s. Test *discovery* spends the same budget as the code
// under test, so "discovery is free" is not a safe assumption.
const probeWindow = "interval '1 day'"

// legacyFallbackQuery is the pre-split SINGLE-QUERY shape: turns and bodies in
// one statement, bodies attached by the (request_id, ts) tuple key.
//
// 2026-10-01 重定基线。此前这个「legacy」在 02c93d04e 里被与生产代码**改成
// 了同一个原生源**，于是这道门只证明了「两阶段拆分 ≡ 单查询」，两边共享的
// 换源对它完全不可见 —— 缺陷 7 因此在全绿状态下溜过去。现在 turns 腿改回
// v1 视图，与生产同源；保留的差异只有一处，且是本次有意为之的：**正文配对键**。
//
// 排除谓词与生产一致（db.MirrorDriftClassSQL），否则轮次集合本身就不同，
// 「轮数相等」这条断言会变成在比较两件不同的事。
func legacyFallbackQuery(sessionID, tenantID string, upToTurn *int) (string, []any) {
	query := `
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN request_logs_bodies_with_current_month rb
			ON rb.request_id = rl.request_id
			AND rb.ts = rl.ts
		WHERE rl.gw_session_id = $1
		  AND (` + db.MirrorDriftClassSQL + `) = 'genuine_loss'`
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
		SELECT rl.request_id, rl.ts
		  FROM request_logs_with_current_month rl
		 WHERE rl.gw_session_id NOT LIKE 'sys:%'
		   AND rl.ts >= now() - `+probeWindow+`
		 ORDER BY rl.ts DESC
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
