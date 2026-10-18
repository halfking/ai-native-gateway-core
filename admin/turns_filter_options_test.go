// 判据：turns 页筛选可选值端点（/api/admin/turns/sessions/filter-options）。
//
// 该端点曾稳定 500@12s。根因是结构，不是「某条查询慢」：
//   - 8 条查询**串行**填一个 12s 预算，逐条实测需 ~40s；
//   - 两个臂口径不一致（ss 臂无时间窗给全历史，turns 臂只有 30 天窗）。
//
// 本文件钉住修复后的四条不变量：
//  1. 两个臂共用同一个 30 天窗常量（owner 裁定，见 lastActiveSource 注释）；
//  2. tags 臂（UNNEST 形态，GROUPING SETS 合不进去）也必须带窗；
//  3. 两个臂各自**合并成一条** GROUPING SETS 查询；
//  4. **不得改回并发**——并发在本端点是净负，实测 68.9s vs 串行 ~19s。
//
// ★ 真库层直接调用 handler 的 SQL 构造函数（`h.ssMergedSource(...)`），
//
//	**不走文本提取**——提取点会随重构漂移（审计 §13.8 已栽过一次），
//	直接调用让「测的就是 handler 会跑的那条 SQL」成为结构性事实。
package admin

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFilterOptionsBothArmsShareTheSameWindow —— 口径不变量。
//
// 端点内部原本两个臂口径不一致：ss 臂无时间窗、turns 臂有 30 天窗，
// 而前端 TurnsListView 默认 timePreset='all'，同一个下拉框里两套语义。
// 经 owner 裁定统一为近 30 天 ⇒ 两个臂都必须带同一个窗常量。
func TestFilterOptionsBothArmsShareTheSameWindow(t *testing.T) {
	if turnsFilterOptionsWindowDays != 30 {
		t.Fatalf("窗口常量变成 %d 天：判据与代码里的「30 天」口径脱节，"+
			"改动前先确认 owner 裁定是否还成立", turnsFilterOptionsWindowDays)
	}

	h := &Handler{}
	for name, sql := range map[string]string{
		"ss 合并臂":    h.ssMergedSource(""),
		"turns 合并臂": h.turnMergedSource(""),
	} {
		if !strings.Contains(sql, "INTERVAL '30 days'") {
			t.Errorf("%s 缺 30 天窗：\n%s", name, strings.TrimSpace(sql))
		}
	}
	// tags 臂走的是另一个构造函数，也要带窗。
	tags := h.lastActiveSource(
		"tag", ", LATERAL UNNEST(ss.user_tags) AS tag", "1=1", "")
	if !strings.Contains(tags, "ss.first_request_at >= NOW() - INTERVAL '30 days'") {
		t.Errorf("tags 臂缺 30 天窗：\n%s", strings.TrimSpace(tags))
	}
}

// TestFilterOptionsArmsAreMerged —— 合并不变量。
//
// ss 臂原来 5 条查询各自做一次 776k×818k 的全量 hash join，只为取一列；
// turns 臂 3 条同理。合并成 GROUPING SETS 后（实测，加 30 天窗）：
//
//	ss 臂   5×1.5s ≈ 7.5s → 4.58s
//	turns 臂 3×3.8s ≈ 11.5s → 2.84s
func TestFilterOptionsArmsAreMerged(t *testing.T) {
	h := &Handler{}

	ss := h.ssMergedSource("")
	if !strings.Contains(ss, "GROUPING SETS ((1),(2),(3),(4),(5))") {
		t.Errorf("ss 臂没有合并成 5 个维度的 GROUPING SETS：\n%s", strings.TrimSpace(ss))
	}
	for _, col := range []string{
		"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id)",
		"sd.task_id", "sd.owner_user", "sd.client_id", "sd.application_code",
	} {
		if !strings.Contains(ss, col) {
			t.Errorf("ss 臂合并查询缺维度 %q", col)
		}
	}

	turn := h.turnMergedSource("")
	if !strings.Contains(turn, "GROUPING SETS ((1),(2),(3))") {
		t.Errorf("turns 臂没有合并成 3 个维度的 GROUPING SETS：\n%s", strings.TrimSpace(turn))
	}
	for _, col := range []string{"t.model", "t.provider", "t.status_code::text"} {
		if !strings.Contains(turn, col) {
			t.Errorf("turns 臂合并查询缺维度 %q", col)
		}
	}
}

// TestFilterOptionsStaysSequential —— 反判据：不得改回并发。
//
// 并发在这个端点是**净负**（不是「收益不够」，是更慢）：
//
//	8 条并发（加窗后）：墙钟 68,971 ms，其中两条 session_turns 各 68.9s
//	8 条串行（加窗后）：约 19s
//
// 原因：工作集（session_turns 9 月分区 2.7GB）远大于 shared_buffers，
// 并发时彼此把对方的页挤出缓存，「并行」变成「来回换页」。
// dashboardapi/session_overview.go 那句「墙钟 = max(段)」在这里**不成立**。
func TestFilterOptionsStaysSequential(t *testing.T) {
	b, err := os.ReadFile("turns_filter_options.go")
	if err != nil {
		t.Fatalf("read turns_filter_options.go: %v", err)
	}
	code := stripGoLineComments(string(b))

	if strings.Contains(code, "errgroup.WithContext") {
		t.Error("runFilterOptionQueries 又用上 errgroup 并发了：本端点实测并发 68.9s、" +
			"串行约 19s，并发是净负（工作集大于 shared_buffers，并发变成互相换页）")
	}
}

// stripGoLineComments 去掉 // 行注释与 /* */ 块注释。
//
// ★ 必需：修复处的注释通篇在讨论「errgroup」「并发 68.9s」「GROUPING SETS」，
//
//	不剥离的话「不得含 errgroup」会因为注释提到它而误报。
//	这与审计 §13.8 的注释陷阱同源。
func stripGoLineComments(src string) string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		if inBlock {
			if i := strings.Index(line, "*/"); i >= 0 {
				line = line[i+2:]
				inBlock = false
			} else {
				continue
			}
		}
		for {
			i := strings.Index(line, "/*")
			j := strings.Index(line, "*/")
			if i >= 0 && (j < 0 || i < j) {
				line = line[:i] + line[j+2:]
				inBlock = true
				continue
			}
			break
		}
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// TestFilterOptionsRealDBWithinHandlerBudget —— 真库层。
//
// 用 handler **自己构造**的合并 SQL（直接调用，不做文本提取），带 handler 同款
// 12s deadline 跑，必须跑完；并与独立重算的「近 30 天」口径一致。
//
// 一致性这半条不能省：加了窗/合并之后口径与计划都变了，若窗口谓词写错
// （写反、或 GROUPING SETS 的 dim 映射错位），查询照样跑得飞快，只是数字错了。
func TestFilterOptionsRealDBWithinHandlerBudget(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	h := &Handler{}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 与 handler 同款的总预算（handleTurnsFilterOptions: 12s）。
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	start := time.Now()
	byDim := map[string]map[string]time.Time{}
	rows, err := pool.Query(ctx, h.ssMergedSource(""))
	if err != nil {
		t.Fatalf("ss 臂合并查询在 12s 预算内没跑完：%v", err)
	}
	for rows.Next() {
		var c1, c2, c3, c4, c5 *string
		var lastAt any
		if err := rows.Scan(&c1, &c2, &c3, &c4, &c5, &lastAt); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		ts := parseFilterOptionTime(lastAt)
		for i, c := range []*string{c1, c2, c3, c4, c5} {
			if c == nil || *c == "" {
				continue
			}
			dim := strconv.Itoa(i + 1)
			if byDim[dim] == nil {
				byDim[dim] = map[string]time.Time{}
			}
			if cur, ok := byDim[dim][*c]; !ok || ts.After(cur) {
				byDim[dim][*c] = ts
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	ssElapsed := time.Since(start)

	// turns 臂合并查询，自带子预算；超时不判失败（handler 会退化成空数组）。
	tctx, tcancel := context.WithTimeout(context.Background(), turnsFilterOptionsTurnBudget)
	defer tcancel()
	startTurn := time.Now()
	var turnElapsed time.Duration
	turnRows := -1
	rows2, err := pool.Query(tctx, h.turnMergedSource(""))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			t.Logf("turns 臂在 %v 子预算内没跑完（生产会退化成空数组）：%v",
				turnsFilterOptionsTurnBudget, err)
			turnElapsed = time.Since(startTurn)
			turnRows = -1
		} else {
			t.Fatalf("turns 臂合并查询报错：%v", err)
		}
	}
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("turns 臂合并查询报错：%v", err)
	}
	if err == nil {
		turnRows = 0
		for rows2.Next() {
			var c1, c2, c3 *string
			var lastAt any
			if err := rows2.Scan(&c1, &c2, &c3, &lastAt); err != nil {
				rows2.Close()
				t.Fatalf("scan turns: %v", err)
			}
			turnRows++
		}
		rows2.Close()
		if err := rows2.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("iterate turns: %v", err)
		}
		turnElapsed = time.Since(startTurn)
	}

	// 独立重算 task 维度（dim=2），确认 GROUPING SETS 的 dim 映射没错位、
	// 窗口谓词确实生效。
	var wantTasks int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(DISTINCT sd.task_id)
		  FROM session_summaries ss
		  JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		 WHERE sd.task_id IS NOT NULL AND sd.task_id != ''
		   AND ss.first_request_at >= now() - interval '30 days'
	`).Scan(&wantTasks); err != nil {
		t.Fatalf("独立重算失败：%v", err)
	}
	gotTasks := int64(len(byDim["2"]))
	if gotTasks != wantTasks {
		t.Errorf("dim=2（task）返回 %d 个不同值，独立重算近 30 天共 %d 个：%s",
			gotTasks, wantTasks,
			map[bool]string{true: "dim 映射错位或窗口谓词失效", false: "分组结果与直接重算不符"}[gotTasks < wantTasks])
	}
	t.Logf("ss 臂合并 %v（dim1=%d dim2=%d dim3=%d dim4=%d dim5=%d），turns 臂 %v（%d 行）",
		ssElapsed.Round(time.Millisecond),
		len(byDim["1"]), len(byDim["2"]), len(byDim["3"]), len(byDim["4"]), len(byDim["5"]),
		turnElapsed.Round(time.Millisecond), turnRows)
}
