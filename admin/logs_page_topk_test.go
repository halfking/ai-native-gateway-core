package admin

// logs_page_topk_test.go —— 2026-11-25 request-logs「超时 ＋ 切换时间范围后无数据」
// 根修的门。
//
// # 修了什么（三条，各有实证）
//
//	① 超时：月分区统计陈旧 ⇒ details 连接退化成 Join Filter（26~38s / 条）。
//	   查询形状根修在 db 包（details 单行点查），这里钉**分页不再物化整个窗口**：
//	   每臂先取 top-K 再归并。
//	② 无数据：上一版的「探测流量窗口自动截断到 12h」以 `end` 为锚，而日历型
//	   预设的 `end` 是当天结束时刻（未来）⇒ 窗口落在未来 ⇒ 必然 0 行。撤掉。
//	③ 历史探测车道被上一版换成三臂全量源（显示全部流量）。恢复。
//
// ⚠ 门禁纪律：真库测试没配 TEST_PG_URL 时 t.Skip，跳过**不构成**证据。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
)

// ── 纯函数 ──────────────────────────────────────────────────────────────────

func TestListLogsPageInnerSQL_PerArmTopK(t *testing.T) {
	got := listLogsPageInnerSQL(listLogsPageQuery{
		Arms:      []string{"(ARM_A)", "(ARM_B)", "(ARM_C)"},
		Where:     "rl.ts >= $3 AND rl.ts <= $4 AND rl.success = $5",
		OrderBy:   "rl.ts DESC",
		LimitIdx:  7, // limit = $7；每臂上界 K 占它前一位 $6
		OffsetIdx: 8, // offset = $8
	})
	// 每臂一个子查询：过滤谓词原样、按同一序、上界 $K。
	for i, arm := range []string{"ARM_A", "ARM_B", "ARM_C"} {
		// 臂自带括号（db.SessionFamilyTurnsWindowedArms 的约定），builder 直接接在 FROM 后。
		want := fmt.Sprintf(`(SELECT rl.* FROM (%s) rl WHERE rl.ts >= $3 AND rl.ts <= $4 AND rl.success = $5 ORDER BY rl.ts DESC LIMIT $6)`, arm)
		if !strings.Contains(got, want) {
			t.Fatalf("臂 %d 的 top-K 子查询缺失或形状不对，期望含：\n%s\n实际：\n%s", i, want, got)
		}
	}
	if strings.Count(got, "LIMIT $6)") != 3 {
		t.Fatalf("三个臂必须各带一次 LIMIT $K，得到 %d", strings.Count(got, "LIMIT $6)"))
	}
	if strings.Count(got, "UNION ALL") != 2 {
		t.Fatalf("三臂应以两个 UNION ALL 接起来")
	}
	// 归并层：整体排序后按 limit / offset 截页。
	if !strings.Contains(got, ") rl\n\t\tORDER BY rl.ts DESC\n\t\tLIMIT $7 OFFSET $8") {
		t.Fatalf("归并层必须是整体 ORDER BY + LIMIT $7 OFFSET $8：\n%s", got)
	}
	// 归并层不再出现过滤谓词（已在每臂施加；重复施加只是白花 CPU）。
	if strings.Count(got, "rl.success = $5") != 3 {
		t.Fatalf("过滤谓词应恰好出现在三个臂里各一次")
	}
}

func TestListLogsPageInnerSQL_WholeSourceKeepsChronoShape(t *testing.T) {
	trace := ", ROW_NUMBER() OVER (ORDER BY rl.ts ASC) AS trace_seq"
	got := listLogsPageInnerSQL(listLogsPageQuery{
		WholeSource:   "(WHOLE) rl",
		Where:         "rl.ts >= $3",
		OrderBy:       "rl.ts ASC",
		TraceSeqInner: trace,
		LimitIdx:      5,
		OffsetIdx:     6,
	})
	// 脉络模式：trace_seq 是全局名次 ⇒ 必须先有完整行集再编号 ⇒ 不许按臂截断。
	for _, want := range []string{
		"SELECT rl.*" + trace + "\n\t\tFROM (WHOLE) rl\n\t\tWHERE rl.ts >= $3\n\t\tORDER BY rl.ts ASC\n\t\tLIMIT $5 OFFSET $6",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("整体形态不符，期望含：\n%s\n实际：\n%s", want, got)
		}
	}
	if strings.Contains(got, "LIMIT $4)") || strings.Contains(got, "UNION ALL") {
		t.Fatalf("整体形态里不得出现按臂 top-K：%s", got)
	}
}

func TestListLogsUsesProbeArm(t *testing.T) {
	cases := []struct {
		lane         string
		includeProbe bool
		want         bool
		why          string
	}{
		{"", false, false, "默认业务口径：两臂"},
		{"", true, true, "显式 include_probe"},
		{"all", false, true, "lane=all 全量"},
		{"ALL", false, false, "lane 的大小写归一在调用方（strings.ToLower）——本函数按原样比较"},
		{"probe", false, false, "历史探测：两臂业务源 + 过滤，不带探测臂"},
		{"probe_history", false, false, "同上"},
		{"business", false, false, "业务口径"},
	}
	for _, tc := range cases {
		if got := listLogsUsesProbeArm(tc.lane, tc.includeProbe); got != tc.want {
			t.Errorf("lane=%q include_probe=%v ⇒ %v, want %v（%s）", tc.lane, tc.includeProbe, got, tc.want, tc.why)
		}
	}
}

func TestListLogsQueryTimeout(t *testing.T) {
	if got := listLogsQueryTimeout(false); got != 60*time.Second {
		t.Errorf("业务口径预算 = %s, want 60s", got)
	}
	if got := listLogsQueryTimeout(true); got != 120*time.Second {
		t.Errorf("含探测预算 = %s, want 120s", got)
	}
}

func TestListLogsTimeoutMessage(t *testing.T) {
	biz := listLogsTimeoutMessage(60*time.Second, 29545, false)
	for _, want := range []string{"60秒", "29545条", "缩小时间窗口", "减少过滤条件"} {
		if !strings.Contains(biz, want) {
			t.Errorf("业务超时文案缺 %q：%s", want, biz)
		}
	}
	probe := listLogsTimeoutMessage(120*time.Second, 7, true)
	if !strings.Contains(probe, "120秒") || !strings.Contains(probe, "包含探测流量") {
		t.Errorf("含探测超时文案应报实际预算并给出关闭探测的建议：%s", probe)
	}
	// 不许再出现「用完整窗口」这类把截窗当成既成事实的措辞。
	if strings.Contains(biz+probe, "override_window_limit") || strings.Contains(biz+probe, "完整窗口") {
		t.Errorf("超时文案不得再引用已撤销的自动截窗：%s | %s", biz, probe)
	}
}

// TestListLogs_NoSilentWindowClamp 钉住「请求的窗口就是查询的窗口」。
//
// listLogs 里只允许两处窗口钳制，都是**明示的安全上限**（366 天封顶、非 default
// 租户 72 小时），由 clampQueryWindowForTenant 统一做；上一版的「探测流量自动
// 截断到 12h」让今天 / 昨天 / 近 N 天 / 本月恒为空（211 日志逐字坐实）。
func TestListLogs_NoSilentWindowClamp(t *testing.T) {
	raw, err := os.ReadFile("logs.go")
	if err != nil {
		t.Fatalf("read logs.go: %v", err)
	}
	src := string(raw)
	for _, banned := range []string{"override_window_limit", "window_clamped", "windowClamped", "auto-clamped", "maxProbeWindow"} {
		if strings.Contains(src, banned) {
			t.Errorf("logs.go 不得再出现 %q——自动截窗以未来时刻 end 为锚，会让日历型预设恒为空", banned)
		}
	}
	start := strings.Index(src, "func (h *Handler) listLogs(")
	if start < 0 {
		t.Fatal("listLogs not found")
	}
	body := src[start:]
	if end := strings.Index(body[1:], "\nfunc "); end > 0 {
		body = body[:end+1]
	}
	if strings.Count(body, "start, end = clampQueryWindowForTenant(start, end, GetTenantID(r))") != 1 {
		t.Error("listLogs 必须经 clampQueryWindowForTenant 做（且只做）明示的安全上限钳制")
	}
}

// ── 真库 ────────────────────────────────────────────────────────────────────

type pageKeyRow struct {
	RequestID string    `json:"request_id"`
	Ts        time.Time `json:"ts"`
}

// fetchPageKeys 取一页的 (request_id, ts)。extraWhere 接在 ts 窗口之后。
func fetchPageKeys(t *testing.T, pool *pgxpool.Pool, inner string, args []any) []pageKeyRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rows, err := pool.Query(ctx, "SELECT rl.request_id, rl.ts FROM ("+inner+") rl", args...)
	if err != nil {
		t.Fatalf("page query: %v\nargs=%#v\ninner tail: %s", err, args, inner[max(len(inner)-260, 0):])
	}
	defer rows.Close()
	out := make([]pageKeyRow, 0, 64)
	for rows.Next() {
		var k pageKeyRow
		if err := rows.Scan(&k.RequestID, &k.Ts); err != nil {
			t.Fatalf("scan page row: %v", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("page rows: %v", err)
	}
	return out
}

// assertSamePage 对拍两页。ts 相同的行之间的先后本就不确定（ORDER BY 只按 ts），
// 所以判据是「对任何合法分页都成立的不变式」，而不是逐行相等：
//
//	① 行数相同；② 页内最大 / 最小 ts 相同；
//	③ ts **严格落在** (min, max) 之间的内部行，两边逐行相同（按 ts, request_id 排序）。
//
// 边界上并列的行可以互换，内部行不行——内部行不同就是真的漏行 / 多行。
func assertSamePage(t *testing.T, label string, a, b []pageKeyRow) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: 行数不同（top-K=%d, 整体=%d）", label, len(a), len(b))
	}
	if len(a) == 0 {
		return
	}
	minTS, maxTS := a[0].Ts, a[0].Ts
	for _, r := range a {
		if r.Ts.Before(minTS) {
			minTS = r.Ts
		}
		if r.Ts.After(maxTS) {
			maxTS = r.Ts
		}
	}
	for _, r := range b {
		if r.Ts.Before(minTS) || r.Ts.After(maxTS) {
			t.Fatalf("%s: 整体形态的页里有行的 ts (%s) 落在 top-K 页的 [min,max]=[%s,%s] 之外", label, r.Ts, minTS, maxTS)
		}
	}
	interior := func(rows []pageKeyRow) []string {
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			if r.Ts.After(minTS) && r.Ts.Before(maxTS) {
				out = append(out, fmt.Sprintf("%s|%s", r.Ts.UTC().Format(time.RFC3339Nano), r.RequestID))
			}
		}
		sort.Strings(out)
		return out
	}
	ia, ib := interior(a), interior(b)
	if len(ia) != len(ib) {
		t.Fatalf("%s: 页内（不含边界并列）行数不同：%d vs %d", label, len(ia), len(ib))
	}
	for i := range ia {
		if ia[i] != ib[i] {
			t.Fatalf("%s: 页内第 %d 行不同：%s vs %s", label, i, ia[i], ib[i])
		}
	}
}

// TestListLogs_PageTopKMatchesWholeSourcePages 是分页改写的**行为等价**对拍：
// 同一窗口 / 同一组过滤下，「每臂 top-K 再归并」与「整体排序截页」逐页一致。
//
// 覆盖：整表窗口、近 7 天窗口、带状态过滤（过滤必须发生在截断之前）、
// 以及含探测臂的三臂形态。
func TestListLogs_PageTopKMatchesWholeSourcePages(t *testing.T) {
	pool, _ := logsRealDBPoolWithTracer(t)

	const pdFrom, pdTo = "'2000-01-01'", "'2999-12-31'"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var maxTS time.Time
	if err := pool.QueryRow(ctx, "SELECT max(ts) FROM public.session_turns").Scan(&maxTS); err != nil || maxTS.IsZero() {
		t.Skipf("session_turns 无数据（err=%v）——空集对空集不构成证据", err)
	}

	businessArms := db.SessionFamilyTurnsWindowedArms(pdFrom, pdTo)
	businessWhole := db.SessionFamilyTurnsSourceWindowedSQL(pdFrom, pdTo) + " rl"
	allArms := db.SessionFamilyTurnsAllWindowedArms(pdFrom, pdTo, "'2000-01-01'", "'2999-12-31'")
	allWhole := db.SessionFamilyTurnsSourceWindowedAllSQL(pdFrom, pdTo, "'2000-01-01'", "'2999-12-31'") + " rl"

	type variant struct {
		name  string
		arms  []string
		whole string
	}
	variants := []variant{
		{"business", businessArms, businessWhole},
		{"with_probe_arm", allArms, allWhole},
	}
	windows := []struct {
		name       string
		start, end time.Time
	}{
		{"whole_table", maxTS.AddDate(0, 0, -400), maxTS.Add(time.Hour)},
		{"last_7d", maxTS.Add(-7 * 24 * time.Hour), maxTS.Add(time.Hour)},
		{"last_24h", maxTS.Add(-24 * time.Hour), maxTS.Add(time.Hour)},
	}
	const pageSize = 100
	for _, v := range variants {
		for _, w := range windows {
			for _, withFilter := range []bool{false, true} {
				where := "rl.ts >= $1 AND rl.ts <= $2"
				baseArgs := []any{w.start.UTC(), w.end.UTC()}
				label := fmt.Sprintf("%s/%s/filter=%v", v.name, w.name, withFilter)
				if withFilter {
					where += fmt.Sprintf(" AND (%s) = $3", requestLogStatusExpr)
					baseArgs = append(baseArgs, "failure")
				}
				argIdx := len(baseArgs) + 1

				// 先数命中总数（整体形态、不带 LIMIT）。
				var total int
				if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+v.whole+" WHERE "+where, baseArgs...).Scan(&total); err != nil {
					t.Fatalf("%s: count: %v", label, err)
				}
				if total == 0 && withFilter {
					continue // 该窗口没有失败请求 ⇒ 过滤变体无从对拍
				}
				pages := (total + pageSize - 1) / pageSize
				if pages == 0 {
					pages = 1
				}
				if pages > 6 {
					pages = 6 // 前 6 页 + 末页
				}
				tryPages := make([]int, 0, pages+1)
				for p := 1; p <= pages; p++ {
					tryPages = append(tryPages, p)
				}
				if last := (total + pageSize - 1) / pageSize; last > pages {
					tryPages = append(tryPages, last)
				}
				for _, page := range tryPages {
					offset := (page - 1) * pageSize
					// top-K 形态多一个参数 K，排在 limit / offset 之前。
					topK := listLogsPageInnerSQL(listLogsPageQuery{
						Arms: v.arms, Where: where, OrderBy: "rl.ts DESC",
						LimitIdx: argIdx + 1, OffsetIdx: argIdx + 2,
					})
					topKArgs := append(append([]any{}, baseArgs...), offset+pageSize, pageSize, offset)
					whole := listLogsPageInnerSQL(listLogsPageQuery{
						WholeSource: v.whole, Where: where, OrderBy: "rl.ts DESC",
						LimitIdx: argIdx, OffsetIdx: argIdx + 1,
					})
					wholeArgs := append(append([]any{}, baseArgs...), pageSize, offset)
					a := fetchPageKeys(t, pool, topK, topKArgs)
					b := fetchPageKeys(t, pool, whole, wholeArgs)
					assertSamePage(t, fmt.Sprintf("%s/page=%d", label, page), a, b)
					// 页内必须按 ts DESC。
					for i := 1; i < len(a); i++ {
						if a[i-1].Ts.Before(a[i].Ts) {
							t.Fatalf("%s/page=%d: top-K 页未按 ts DESC 排序", label, page)
						}
					}
				}
				t.Logf("%s: total=%d, pages checked=%v", label, total, tryPages)
			}
		}
	}
}

// TestListLogs_ServesRequestedWindowAsIs 钉住②：日历型预设的 `to` 是**当天结束
// 时刻**（相对 now 可能在未来），handler 必须原样查询 [from, to]，不许以 `to`
// 为锚把窗口挪走。
//
// 用真库里「最新有数据的那一天」做窗口（含探测口径 ⇒ 上一版会触发 12h 截断）：
// 响应回传的 query_window_start / end 必须逐字等于请求值，且 count > 0。
func TestListLogs_ServesRequestedWindowAsIs(t *testing.T) {
	pool, _ := logsRealDBPoolWithTracer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var maxTS time.Time
	if err := pool.QueryRow(ctx, "SELECT max(ts) FROM public.session_turns").Scan(&maxTS); err != nil || maxTS.IsZero() {
		t.Skipf("session_turns 无数据（err=%v）——空集不构成证据", err)
	}
	// 与前端「今天 / 昨天」预设同形：本地日历日 00:00:00.000 ~ 23:59:59.999，
	// 经 Date.prototype.toISOString() 发出（毫秒精度、UTC、Z）。
	loc := time.FixedZone("CST", 8*3600)
	local := maxTS.In(loc)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	dayEnd := dayStart.Add(24*time.Hour - time.Millisecond)
	from := dayStart.UTC().Format("2006-01-02T15:04:05.000Z")
	to := dayEnd.UTC().Format("2006-01-02T15:04:05.000Z")

	h := &Handler{db: pool}
	for _, extra := range []string{"", "&include_probe=1"} {
		q := fmt.Sprintf("/api/logs?from=%s&to=%s&page=1&page_size=50%s", from, to, extra)
		rec := httptest.NewRecorder()
		h.listLogs(rec, httptest.NewRequest(http.MethodGet, q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("listLogs(%q) status = %d, want 200: %s", extra, rec.Code, rec.Body.String())
		}
		var resp struct {
			Count            int       `json:"count"`
			QueryWindowStart time.Time `json:"query_window_start"`
			QueryWindowEnd   time.Time `json:"query_window_end"`
			WindowClamped    *bool     `json:"window_clamped"`
			Items            []any     `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if resp.WindowClamped != nil {
			t.Errorf("响应不得再带 window_clamped 字段（自动截窗已撤销）")
		}
		if !resp.QueryWindowStart.Equal(dayStart) || !resp.QueryWindowEnd.Equal(dayEnd) {
			t.Fatalf("查询窗口被改动了：请求 [%s, %s]，实际 [%s, %s]",
				dayStart.Format(time.RFC3339), dayEnd.Format(time.RFC3339),
				resp.QueryWindowStart.Format(time.RFC3339), resp.QueryWindowEnd.Format(time.RFC3339))
		}
		if resp.Count == 0 || len(resp.Items) == 0 {
			t.Fatalf("该日历日有数据却返回空：count=%d items=%d", resp.Count, len(resp.Items))
		}
	}
}

// TestListLogs_ProbeHistoryLaneReadsBusinessSourceOnly 钉住③：lane=probe /
// probe_history 是「业务表上的历史探测行」——读两臂业务源 ＋ 探测过滤，
// 不带探测臂（session_turns_probe_hot）。
func TestListLogs_ProbeHistoryLaneReadsBusinessSourceOnly(t *testing.T) {
	pool, tracer := logsRealDBPoolWithTracer(t)
	h := &Handler{db: pool}
	end := time.Now().UTC()
	start := end.Add(-30 * 24 * time.Hour)
	for _, lane := range []string{"probe", "probe_history"} {
		q := fmt.Sprintf("/api/logs?from=%s&to=%s&page=1&page_size=5&lane=%s",
			start.Format(time.RFC3339), end.Format(time.RFC3339), lane)
		rec := httptest.NewRecorder()
		h.listLogs(rec, httptest.NewRequest(http.MethodGet, q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("lane=%s status = %d, want 200: %s", lane, rec.Code, rec.Body.String())
		}
		sawSource := false
		for _, s := range tracer.snapshot() {
			if !isLogsSourceMarker(s) {
				continue
			}
			sawSource = true
			if strings.Contains(s, "session_turns_probe_hot") {
				t.Fatalf("lane=%s 不得读探测臂（那是当前探测，不是历史探测）", lane)
			}
			if !strings.Contains(s, "'probe_triggered'") {
				t.Fatalf("lane=%s 必须带历史探测过滤谓词", lane)
			}
		}
		if !sawSource {
			t.Fatalf("lane=%s 没有发出读日志源的语句", lane)
		}
	}
}
