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
// 等价。这里把**单查询形状**保留为 oracle，对同一批真实会话跑 oracle/拆分两条
// 路径，比较**端点真正吐出去的** turns（TurnNo + 两个正文解码值），而不是
// 中间行 —— 中间行相等但合并错了，端点照样坏。
//
// ★ R68 改瞄（2026-10-21，v1 族退役）：**对照面整个换了一代**。
//
//	旧：oracle = v1 视图 + v1 bodies 视图，正文按 **(request_id, ts) 元组键**配对。
//	    890 之后 v1 族整体 DROP ⇒
//	      · `request_logs_bodies_with_current_month` 不存在 ⇒ oracle 直接 42P01；
//	      · 会话族正文源 `db.SessionFamilyBodiesSourceSQL()` **不投影 ts**
//	        （db/request_logs_view_schema.go 写明：「加上它会把一个解析期错误
//	         换成一个静默的行数变化。响亮失败优于静默改行」）
//	        ⇒ **元组键形态不再可能**（那类 JOIN 会在解析期 42703）。
//	新：oracle = canonical 视图（已是会话族）+ 会话族正文源，正文按 **request_id 单键**配对。
//
// ⇒ 「元组键 vs 单键」这条方向性不变式**随之失效**：两条路径现在用的是**同一个
// 配对键与同一个源** ⇒ 拆分只可能与 oracle **相等**，不可能「多取」。
//
//	⇒ 方向性断言（「正文只增不减」）被**退役**，换成**相等性**断言
//	  （相等才是现在唯一正确的期望；留着方向性就会把「多取」当成优点）。
//
// ⚠ 这不是把门放松：轮次集合、轮次编号、正文内容三条**相等性**不变式仍然
//
//	全在，而且它们正是这道门当初要抓的东西（「中间行相等但合并错了，端点照样坏」）。
//
// Run with a real database:
//
//	TEST_PG_URL='postgres://postgres@127.0.0.1:5432/llm_gateway' \
//	  go test -tags=integration ./admin/ -run TestSessionSummaryV2Fallback
func TestSessionSummaryV2FallbackMatchesLegacyQueryOnRealRows(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that the split preserved the single-query rows")
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
	// 轮数必须小：逐轮扫描（v1 时代是逐轮 ColumnarScan）很贵，跑 200 轮就是
	// 分钟级；第一版把 53,851 轮的 sys:probe 会话丢进无界分支，整门跑满
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
				oracle := queryOracleFallbackTurns(t, ctx, pool, probe.sessionID, limit)
				split := querySplitFallbackTurns(t, ctx, pool, probe.sessionID, limit)
				if len(oracle) == 0 {
					t.Fatalf("probe returned no rows — an empty comparison proves nothing")
				}
				// 不变式 1：轮次集合。phase 1 拥有 ORDER BY / LIMIT，phase 2 只
				// 供正文，所以拆分不得丢轮、不得改序、不得改编号。
				if len(oracle) != len(split) {
					t.Fatalf("turn count diverged: oracle=%d split=%d", len(oracle), len(split))
				}
				for i := range oracle {
					if oracle[i].TurnNo != split[i].TurnNo {
						t.Fatalf("turn %d numbering diverged: oracle=%d split=%d",
							i, oracle[i].TurnNo, split[i].TurnNo)
					}
				}
				// 不变式 2：正文**逐轮相等**。
				//
				// ★ R68：原断言是「方向性」的（legacy 配上的轮次 split 必须配上
				// 同一份；legacy 没配上的轮次 split 允许有正文）。那是**元组键
				// vs 单键**的产物，两条路径的配对键不同才可能「多取」。
				// 890 之后两条路径用**同一个 request_id 单键 + 同一个源**
				// ⇒ 唯一正确的期望是**相等**（多取同样是 bug：它会把别的轮次的
				// 正文串进来）。
				oracleBodies, splitBodies := 0, 0
				for i := range oracle {
					oracleHas := oracle[i].RequestDelta != nil || oracle[i].ResponseDelta != nil
					splitHas := split[i].RequestDelta != nil || split[i].ResponseDelta != nil
					if oracleHas {
						oracleBodies++
					}
					if splitHas {
						splitBodies++
					}
					if oracleHas != splitHas {
						t.Fatalf("turn %d: body presence diverged between the single query and the split "+
							"(both use the request_id single key and the same source ⇒ they must agree):\n oracle=%s\n split =%s",
							i, formatTurn(oracle[i]), formatTurn(split[i]))
					}
					if oracleHas && !reflect.DeepEqual(oracle[i], split[i]) {
						t.Fatalf("turn %d: the single query and the split resolved to DIFFERENT bodies, "+
							"which means request_id is not unique in the bodies store:\n oracle=%s\n split =%s",
							i, formatTurn(oracle[i]), formatTurn(split[i]))
					}
				}
				if splitBodies != oracleBodies {
					t.Fatalf("body count diverged: oracle=%d split=%d", oracleBodies, splitBodies)
				}
				t.Logf("%d turns compared; bodies oracle=%d split=%d", len(oracle), oracleBodies, splitBodies)
			})
		}
	}
}

// TestSessionSummaryV2FallbackBodiesStaysOnIndexPath pins the *plan*, not the
// SQL text: phase 2 must not fall back onto a **columnar full scan** of the
// bodies relation.
//
// 2026-08-26 实测的原始动机（审计报告 §8.3）：v1 的 bodies 月分区是 Citus
// columnar，`IN (SELECT unnest($1))` 走主键探针 1.77~2.20 s，而
// `= ANY($1::text[])` 退化成 ColumnarScan 15.85~17.43 s ⇒ 谓词形态决定代价。
//
// ★ R68 改瞄（2026-10-21，v1 族退役）：
//
//	· 原判据的**锚点是 v1 专有的**：`request_logs_bodies_2026_09_pkey` 与
//	  `ColumnarScan) on _request_logs_bodies`。890 之后 v1 bodies 分区不存在
//	  ⇒ 这两个子串在计划里**永远不会出现** ⇒ 原判据的「正向锚点」永远找不到
//	  ⇒ 它一旦真跑起来（不是被 skip 掉）就会以「vacuous pass」的红脸失败。
//	· ⇒ 锚点改成**族无关**的形态：
//	    正向：计划里必须出现针对 session_bodies* 的扫描（触达目标了）；
//	    负向：不得出现针对 session_bodies* 的 ColumnarScan（列存全扫回归）。
//	  「走主键探针」这个更具体的期望**保留为日志**（不作为断言）——
//	  会话族的 hot 腿是 heap 小表，规划器选 Seq Scan 也是合理的，
//	  拿 v1 列存分区的 pkey 当硬断言会制造假红。
func TestSessionSummaryV2FallbackBodiesStaysOnIndexPath(t *testing.T) {
	dsn := os.Getenv("TEST_PG_URL")
	if dsn == "" {
		t.Skip("TEST_PG_URL unset — skipping does NOT constitute evidence that phase 2 stays off the columnar path")
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

	// EXPLAIN 的对象必须是生产真正发出的那条 phase 2（session_bodies_batch.go 的
	// sessionBodiesByRequestIDSQL，当前读会话族源）。必须带 ANALYZE：
	// **不执行的计划证明不了性能**。
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

	// 负向锚点：列存全扫。★ 锚点必须**族无关**：v1 的 bodies 分区已不存在，
	// 沿用旧锚点会让这条负向断言恒绿（它只会在 v1 上红）。
	var columnarLines []string
	var touchedLines []string
	var indexPathLines []string
	for _, line := range planLines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(line, "session_bodies") {
			continue
		}
		touchedLines = append(touchedLines, trimmed)
		if strings.Contains(line, "ColumnarScan") && !strings.Contains(line, "never executed") {
			columnarLines = append(columnarLines, trimmed)
		}
		if strings.Contains(line, "Index Scan") || strings.Contains(line, "Bitmap Index Scan") {
			indexPathLines = append(indexPathLines, trimmed)
		}
	}

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
		t.Fatalf("phase 2 fell back onto a columnar scan of the bodies relation (audit §8.3.3):\n  %s\n  %s\n"+
			"  The = ANY($1::text[]) form makes the planner abandon the primary key "+
			"probe; IN (SELECT unnest($1::text[])) is the form that keeps it.",
			strings.Join(columnarLines, "\n  "), execMs)
	}
	// 正向锚点：**必须触达** bodies 关系。否则这条门会在「什么都没扫」的情况下
	// 静默通过（vacuous pass）—— 那正是它过去的样子（锚点写死成 v1 分区的
	// pkey 名，在会话族计划里一次都不出现）。
	if len(touchedLines) == 0 {
		t.Fatalf("phase 2 never touched the bodies relation — this gate's anchors found nothing and the "+
			"run would pass vacuously:\n%s", strings.Join(planLines, "\n"))
	}
	t.Logf("phase 2 触达 bodies 关系的扫描节点 %d 个（其中索引探针 %d 个）", len(touchedLines), len(indexPathLines))
	for _, l := range indexPathLines {
		t.Logf("  索引探针：%s", l)
	}
}

func fallbackLimitPtr(i int) *int { return &i }

// fallbackProbe is one session to compare the oracle and split paths on,
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
//     the body-hit path;
//   - one multi-turn session with no bodies at all — the consecutive-miss path;
//   - several sessions whose body lives **only in the parent tier**
//     (`session_bodies` without a `session_bodies_hot` counterpart) — the cold
//     side of the session family's two-leg body source.
//
// ★ R68 退役了两组探针（前提随 890 一起消失，理由逐条写在下）：
//
//	① **ts 不等组**（原来挑「body 的 ts ≠ 轮次 ts」的会话）：
//	   它存在的前提是「正文按 (request_id, ts) 元组键配对 ⇒ ts 不等就取不到」。
//	   890 之后会话族正文源**不投影 ts** ⇒ 元组键形态在解析期就不存在（42703）
//	   ⇒ 「ts 不等」这个形状**不再有可达性含义**。替换成「父表腿专有正文」组
//	   （见上），它守的是同一个现实问题：**冷腿可达**。
//
//	② **v1-only 人群**（原来挑「v1 视图里有轮次、session 族两张表里一行都没有」
//	   的会话）：890 之后 canonical 视图**本身就是会话族**
//	   （`session_turns_hot ∪ session_turns`）⇒ 这个人群**按定义为空**
//	   ⇒ 该组不再贡献任何样本，删掉（留着的效果是每轮多跑一条注定 0 行的查询）。
//
// 两个预算纪律，都是实测换来的：
//
//  1. **探针一律带时间窗**（probeWindow）。不带走全量视图的 GROUP BY 在
//     本机跑了 5 分半未完成（实测 pg_stat_activity 里挂着），加上窗之后 1.5s。
//     测试**发现**阶段的查询和被测查询一样会烧预算，不能当成免费的。
//  2. **轮数夹在 2~60**。逐轮扫描很贵，一个 1,365 轮的无界探针就是分钟级。
func newFallbackProbePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []fallbackProbe {
	t.Helper()
	var out []fallbackProbe

	// 候选会话：canonical 视图近窗口内、轮数适中的会话。
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

	var missProbes, hitProbes int
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
			// 有正文：命中路径，oracle 与 split 都取到内容。
			p.limits = []*int{nil, fallbackLimitPtr(1)}
			hitProbes++
		case bodies == 0 && missProbes < 3:
			// 全 miss：正文腿返回空。
			p.limits = []*int{nil, fallbackLimitPtr(7)}
			missProbes++
		default:
			continue
		}
		out = append(out, p)
	}

	// 父表腿专有正文组（R68 新增，取代已退役的 ts 不等组）：
	// 「body 只在 session_bodies（父表）、不在 session_bodies_hot」的会话。
	// 这组守的是**冷腿可达**：会话族正文源是 hot ∪ parent 两条腿，
	// 只有 hot 腿命中的样本证明不了 parent 腿也被真正走到。
	//
	// ⚠ 这组**不做硬失败**：新装库里可能一条都没有（父表腿刚被写入才有）。
	//   硬失败会把「环境里没这种数据」读成「回归」⇒ 只记日志。
	parentOnlyProbes := 0
	for _, raw := range queryStrings(t, ctx, pool, `
		SELECT rl.gw_session_id || '|' || rl.tenant_id
		  FROM request_logs_with_current_month rl
		  JOIN `+db.SessionFamilyBodiesSourceSQL()+` b
		    ON b.request_id = rl.request_id
		 WHERE rl.gw_session_id IS NOT NULL
		   AND rl.ts >= now() - `+probeWindow+`
		   AND (`+db.MirrorDriftClassSQL+`) = 'genuine_loss'
		   AND NOT EXISTS (
		       SELECT 1 FROM public.session_bodies_hot h
		        WHERE h.request_id = b.request_id)
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
		out = append(out, fallbackProbe{
			sessionID: id, tenantID: tenant, turns: turns, bodies: bodies,
			limits: []*int{fallbackLimitPtr(1)},
		})
		parentOnlyProbes++
	}
	if parentOnlyProbes == 0 {
		t.Logf("no session with parent-tier-only bodies in the %s window — the cold leg of the "+
			"session family's body source is not exercised by this run (this is a coverage note, "+
			"not a failure: a fresh database legitimately has no parent-tier bodies yet)",
			probeWindow)
	}

	for i := range out {
		if out[i].turns == 0 {
			t.Fatalf("probe %s has no turns in the canonical view", out[i].sessionID)
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
	// ★ R68：bodies 腿从 v1 视图（+ ts 元组键）换成**会话族正文源**（request_id 单键），
	// 与产品 phase 2 同源 ⇒ 「有几轮有正文」这个数才有可比性。
	err := pool.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(b.request_id)::int
		  FROM request_logs_with_current_month rl
		  LEFT JOIN `+db.SessionFamilyBodiesSourceSQL()+` b
		    ON b.request_id = rl.request_id
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
// the full view was measured at >5 minutes on this database (found still
// running in pg_stat_activity after the client had given up); with the window
// the same shape is 1.5s. Test *discovery* spends the same budget as the code
// under test, so "discovery is free" is not a safe assumption.
const probeWindow = "interval '1 day'"

// oracleFallbackQuery is the SINGLE-QUERY shape: turns and bodies in one
// statement, bodies attached by the **request_id single key**.
//
// ★ R68 改瞄（2026-10-21，v1 族退役）：原来的 oracle 是
//
//	FROM request_logs_with_current_month rl
//	LEFT JOIN request_logs_bodies_with_current_month rb
//	  ON rb.request_id = rl.request_id AND rb.ts = rl.ts
//
// 890 之后 `request_logs_bodies_with_current_month` 不存在 ⇒ 42P01；
// 且会话族正文源**不投影 ts** ⇒ `rb.ts` 这个形态在解析期就 42703。
// ⇒ oracle 改为：canonical 视图（已是会话族）+ 会话族正文源 + request_id 单键。
//
// 排除谓词与生产一致（db.MirrorDriftClassSQL），否则轮次集合本身就不同，
// 「轮数相等」这条断言会变成在比较两件不同的事。
func oracleFallbackQuery(sessionID, tenantID string, upToTurn *int) (string, []any) {
	query := `
		SELECT rl.request_id,
		       rl.ts,
		       rb.request_body,
		       rb.response_body
		FROM request_logs_with_current_month rl
		LEFT JOIN (` + db.SessionFamilyBodiesSourceSQL() + `) rb
			ON rb.request_id = rl.request_id
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

// queryOracleFallbackTurns produces what the single-query shape returns.
func queryOracleFallbackTurns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sessionID string, upToTurn *int) []turnForSummary {
	t.Helper()
	query, args := oracleFallbackQuery(sessionID, fallbackTenant, upToTurn)
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		t.Fatalf("oracle query: %v", err)
	}
	defer rows.Close()
	var out []turnForSummary
	for rows.Next() {
		var requestID string
		var ts time.Time
		var reqRaw, respRaw []byte
		if err := rows.Scan(&requestID, &ts, &reqRaw, &respRaw); err != nil {
			t.Fatalf("oracle scan: %v", err)
		}
		out = append(out, turnForSummary{
			TurnNo:        len(out) + 1,
			RequestDelta:  decodeStoredJSON("request_body", requestID, reqRaw),
			ResponseDelta: decodeStoredJSON("response_body", requestID, respRaw),
		})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("oracle rows: %v", err)
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
