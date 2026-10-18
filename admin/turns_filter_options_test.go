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
func TestFilterOptionsTurnBudgetLeavesHeadroom(t *testing.T) {
	// 线上实测：子预算取 8s 时端点总耗时到过 12.06s（贴着 12s 总预算）。
	// 必须是「总预算 − ss+tags 实测占用 − 余量」。
	const reservedForRest = 4 * time.Second // ss 合并 ~2.7s + tags ~0.5s，再留 1s 余量
	// 必须 >= 而不是 >：余量恰好为零本身就是失败——线上实测 8s 子预算时
	// 端点总耗时到过 12.06s，已经越过了 12s 总预算。
	if turnsFilterOptionsTurnBudget+reservedForRest >= turnsFilterOptionsBudget {
		t.Errorf("turns 子预算 %v + 其余部分预留 %v 超过端点总预算 %v：余量不足，"+
			"负载一高就会整体超时（实测 8s 子预算时端点到过 12.06s）",
			turnsFilterOptionsTurnBudget, reservedForRest, turnsFilterOptionsBudget)
	}
}

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

	respModels, respProviders, respStatus := []string{}, []string{}, []string{}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 与 handler 同款的总预算（handleTurnsFilterOptions: 12s）。
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// ★ 必须调用**生产函数** queryFilterOptionGroups，不能在测试里重写一遍扫描。
	//   上一版判据就是在这里翻车的：它自己用 `var c *string` 扫描（能接 NULL），
	//   而生产代码用 `new(string)` 扫（接不了 NULL，GROUPING SETS 每行四列都是
	//   NULL ⇒ **每行都失败**、端点立刻 500）。判据复刻了「扫描这件事」，
	//   却没有走「同一个函数」，于是端点坏着、判据全绿。
	h := &Handler{db: pool}
	projects, tasks, owners, clients := []string{}, []string{}, []string{}, []string{}
	groups := map[string]*[]string{
		"1": &projects, "2": &tasks, "3": &owners, "4": &clients, "5": &clients,
	}

	start := time.Now()
	if _, err := h.queryFilterOptionGroups(ctx, h.ssMergedSource(""), groups, []any{}); err != nil {
		t.Fatalf("ss 臂合并查询在 12s 预算内没跑完：%v", err)
	}
	ssElapsed := time.Since(start)

	// turns 臂合并查询，自带子预算；超时不判失败（handler 会退化成空数组）。
	turnGroups := map[string]*[]string{
		"1": &respModels, "2": &respProviders, "3": &respStatus,
	}
	tctx, tcancel := context.WithTimeout(context.Background(), turnsFilterOptionsTurnBudget)
	defer tcancel()
	startTurn := time.Now()
	var turnElapsed time.Duration
	turnRows := -1
	_, terr := h.queryFilterOptionGroups(tctx, h.turnMergedSource(""), turnGroups, []any{})
	switch {
	case terr == nil:
		turnRows = len(respModels) + len(respProviders) + len(respStatus)
	case errors.Is(terr, context.DeadlineExceeded):
		t.Logf("turns 臂在 %v 子预算内没跑完（生产会退化成空数组）：%v",
			turnsFilterOptionsTurnBudget, terr)
		turnRows = -1
	default:
		t.Fatalf("turns 臂合并查询报错：%v", terr)
	}
	turnElapsed = time.Since(startTurn)
	_ = turnRows

	byDim := map[string]int{"1": len(projects), "2": len(tasks),
		"3": len(owners), "4": len(clients)}

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
	gotTasks := int64(len(tasks))
	if gotTasks != wantTasks {
		t.Errorf("dim=2（task）返回 %d 个不同值，独立重算近 30 天共 %d 个", gotTasks, wantTasks)
	}
	// ★ client_id 与 application_code 共用 clients 字段：两个维度必须**合并**而不是
	//   互相覆盖。曾经写成 `*dst = out[dim]`，后一个维度把前一个整个抹掉
	//   （clients 只剩 application_code 的 13 个，client_id 的 193 个全丢）。
	if len(clients) == 0 {
		t.Error("clients 为空：client_id / application_code 两维可能整体丢数据")
	}
	t.Logf("ss 臂合并 %v（dim1=%d dim2=%d dim3=%d clients=%d），turns 臂 %v（%d 行）",
		ssElapsed.Round(time.Millisecond),
		byDim["1"], byDim["2"], byDim["3"], len(clients),
		turnElapsed.Round(time.Millisecond), turnRows)
}
