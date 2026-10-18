// 判据：turns 页筛选可选值端点（/api/admin/turns/sessions/filter-options）。
//
// 该端点曾稳定 500@12s。根因不是「某条查询慢」，而是**结构**：
// 8 条查询（5 条 session_summaries + 3 条 session_turns）**串行**塞在一个 12s 预算里，
// 逐条实测需 ~40s ⇒ 预算不可能成立。
//
// 本文件钉住修复后的三条不变量：
//  1. 两个臂**共用同一个 30 天窗**（此前 ss 臂无窗、turns 臂有窗，语义分裂）；
//  2. 查询**并发**执行（此前是顺序 for 循环）；
//  3. 真库层：逐条取 handler 真实构造出的 SQL，在 handler 同款 12s 预算内跑完，
//     且与独立重算一致。
//
// ★ 真库层直接调用 handler 的 SQL 构造函数（`(&Handler{}).lastActiveSource(...)`），
//   **不走文本提取**——提取点会随并发合并漂移（§13.8 已栽过一次），
//   而直接调用让「测的就是 handler 会跑的那条 SQL」成为结构性事实。
package admin

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestFilterOptionsBothArmsShareTheSameWindow —— 口径不变量。
//
// 端点内部原本两个臂口径不一致：ss 臂无时间窗（给全历史选项），
// turns 臂有 30 天窗；而前端 TurnsListView 默认 timePreset='all'。
// 经 owner 裁定统一为近 30 天 ⇒ 两个臂都必须带同一个窗常量。
func TestFilterOptionsBothArmsShareTheSameWindow(t *testing.T) {
	if turnsFilterOptionsWindowDays != 30 {
		t.Fatalf("窗口常量变成 %d 天：判据与代码里的「30 天」口径脱节，"+
			"改动前先确认 owner 裁定是否还成立", turnsFilterOptionsWindowDays)
	}

	h := &Handler{}
	joinDim := " JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id"
	want := "ss.first_request_at >= NOW() - INTERVAL '30 days'"

	ssArm := h.lastActiveSource(
		"sd.task_id", joinDim, "sd.task_id IS NOT NULL AND sd.task_id != ''", "")
	if !strings.Contains(ssArm, want) {
		t.Errorf("ss 臂的 SQL 缺 30 天窗（实际：%s）：\n%s",
			want, strings.TrimSpace(ssArm))
	}

	turnArm := h.lastActiveTurnQuery(
		"t.model", "t.model IS NOT NULL AND t.model != ''", "")
	if !strings.Contains(turnArm, "INTERVAL '30 days'") {
		t.Errorf("turns 臂的 SQL 缺 30 天窗：\n%s", strings.TrimSpace(turnArm))
	}

	// ss 臂必须用的是同一个常量，而不是写死的另一个数字。
	if strings.Contains(ssArm, "INTERVAL '") &&
		!strings.Contains(ssArm, "INTERVAL '30 days'") {
		t.Errorf("ss 臂的窗口与 turnsFilterOptionsWindowDays 不一致：\n%s", strings.TrimSpace(ssArm))
	}
}

// TestFilterOptionsTagsArmIsAlsoWindowed —— tags 臂走的是
// `, LATERAL UNNEST(ss.user_tags) AS tag` + `cond="1=1"` 的形态，
// 它的窗必须落在 WHERE 里而不是被 cond 吃掉的任何位置。
func TestFilterOptionsTagsArmIsAlsoWindowed(t *testing.T) {
	h := &Handler{}
	tags := h.lastActiveSource(
		"tag", ", LATERAL UNNEST(ss.user_tags) AS tag", "1=1", "")

	if !strings.Contains(tags, "ss.first_request_at >= NOW() - INTERVAL '30 days'") {
		t.Errorf("tags 臂缺 30 天窗：\n%s", strings.TrimSpace(tags))
	}
}

// stripGoLineComments 去掉 // 行注释与 /* */ 块注释。
//
// ★ 必需：修复处的注释通篇在讨论「顺序 for 循环」「errgroup」「30 天窗」，
//   不剥离的话下面两条断言一律恒真——「必须含 errgroup」会因为注释提到它而恒真，
//   「不得含顺序循环」会因注释而误报。这与 §13.8 的注释陷阱同源。
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

// runFilterOptionQueriesSource 取出该函数体（剥注释）。
func runFilterOptionQueriesSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("turns_filter_options.go")
	if err != nil {
		t.Fatalf("read turns_filter_options.go: %v", err)
	}
	code := stripGoLineComments(string(b))
	i := strings.Index(code, "func (h *Handler) runFilterOptionQueries(")
	if i < 0 {
		t.Fatal("cannot locate runFilterOptionQueries")
	}
	j := strings.Index(code[i:], "\nfunc ")
	if j < 0 {
		return code[i:]
	}
	return code[i : i+j]
}

// TestFilterOptionsQueriesRunConcurrently —— 结构不变量：不得退回顺序执行。
//
// 修复前是顺序 for 循环，8 条之和 ~40s > 12s 预算。实测并发收益 5.3×（不是 9×，
// 因为这些查询争 CPU/buffer 而非 I/O），配合 30 天窗后墙钟落在预算内。
func TestFilterOptionsQueriesRunConcurrently(t *testing.T) {
	body := runFilterOptionQueriesSource(t)

	if !strings.Contains(body, "errgroup.WithContext") {
		t.Error("runFilterOptionQueries 不再用 errgroup：顺序执行 8 条查询需要 ~40s，"+
			"装不进 handler 的 12s 预算（实测并行 5.3×）")
	}
	if !strings.Contains(body, "g.Go(") {
		t.Error("runFilterOptionQueries 里没有 g.Go：errgroup 建了却没派发，并行没有真正发生")
	}
	// 顺序执行的特征：for 循环体内直接 h.db.Query。
	if strings.Contains(body, "for i, q := range queries {") &&
		strings.Contains(body, "h.db.Query(ctx, q, args...)") {
		t.Error("runFilterOptionQueries 又变回顺序 for 循环直接打 db.Query")
	}
}

// TestFilterOptionsRealDBWithinHandlerBudget —— 真库层。
//
// 用 handler **自己构造**的 SQL（直接调用，不做文本提取），带 handler 同款 12s
// deadline 跑 ss 臂最重的一条（tasks：有 join + 非空谓词），必须跑完；
// 结果与独立重算的「近 30 天」口径一致。
//
// 「与独立重算一致」这半条不能省：加了窗之后口径变了，若窗口谓词写错
// （比如 `<=` 或写反），查询照样跑得飞快，只是数字错了。
func TestFilterOptionsRealDBWithinHandlerBudget(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	h := &Handler{}
	joinDim := " JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id"
	q := h.lastActiveQuery(h.lastActiveSource(
		"sd.task_id", joinDim, "sd.task_id IS NOT NULL AND sd.task_id != ''", ""))
	// 判据用相对窗口形态，避免依赖 handler 的入参。
	q = strings.Replace(q, "NOW() - INTERVAL '30 days'",
		"(now() - interval '30 days')", 1)

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// 与 handler 同款的总预算（handleTurnsFilterOptions: 12s）。
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	start := time.Now()
	rows, err := pool.Query(ctx, q)
	if err != nil {
		t.Fatalf("ss 臂最重的一条在 12s 预算内没跑完：%v", err)
	}
	got := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	elapsed := time.Since(start)

	// 独立重算近 30 天的 task_id 集合，确认窗口谓词确实生效。
	var want int64
	if err := pool.QueryRow(context.Background(), `
		SELECT count(DISTINCT sd.task_id)
		  FROM session_summaries ss
		  JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id
		 WHERE sd.task_id IS NOT NULL AND sd.task_id != ''
		   AND ss.first_request_at >= now() - interval '30 days'
	`).Scan(&want); err != nil {
		t.Fatalf("独立重算失败：%v", err)
	}

	// ★ LIMIT 是**上限**不是定值：实测近 30 天只有 1 个不同的 task_id，
	//   端点就该只返回 1 个。第一版判据断言「必须等于 20」当场红了——
	//   红的是判据的前提（把上限当成了期望值），不是代码。
	exp := want
	if exp > int64(turnsFilterOptionsLimit) {
		exp = turnsFilterOptionsLimit
	}
	if int64(len(got)) != exp {
		t.Errorf("返回 %d 个选项，期望 %d（= min(LIMIT=%d, 近30天实际 %d)）："+
			"多了说明 LIMIT 没生效，少了说明有别的截断",
			len(got), exp, turnsFilterOptionsLimit, want)
	}
	t.Logf("ss 臂最重一条耗时 %v，返回 %d 个选项（近 30 天共 %d 个不同 task_id）",
		elapsed.Round(time.Millisecond), len(got), want)
}
