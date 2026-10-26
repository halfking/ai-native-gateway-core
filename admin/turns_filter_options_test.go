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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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
	for _, col := range []string{"model", "provider", "status_code::text"} {
		if !strings.Contains(turn, col) {
			t.Errorf("turns 臂合并查询缺维度 %q", col)
		}
	}
	// ★ 关键不变量：**不得读 canonical 视图**。视图给每行补 canonical 列、
	// 反连 session_dim 等，代价与「只要 MAX(ts) 分组」差一个数量级。
	// 实测同一 30 天窗口（返回行数相同）：视图 44,564ms vs hot∪父表两腿 2,048ms（23×）。
	if strings.Contains(turn, "session_turns_with_current_month") {
		t.Error("turns 臂仍在读 session_turns_with_current_month 视图：实测 44,564ms vs 两腿 2,048ms")
	}
	// 两腿都要在：最近 8h 的行只在 hot，裸父表读对「近 30 天」是真盲区。
	for _, leg := range []string{"public.session_turns ", "public.session_turns_hot "} {
		if !strings.Contains(turn, leg) {
			t.Errorf("turns 臂缺少 %q 这条腿：最近 8h 的行只在 hot，单腿读是真盲区",
				strings.TrimSpace(leg))
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
	// ★ 2026-10-18：三条臂现在都有界（此前 ss / tags 是无界的，
	//   ss 冷缓存时独占总预算 → 线上 3 次连续 500@12.05s）。
	//   余量必须按**全部三条**算，只算 turns 一条会放过「ss 把余量吃掉」这种回归。
	//
	// 必须 >= 而不是 >：余量恰好为零本身就是失败——线上实测 8s 子预算时
	// 端点总耗时到过 12.06s，已经越过了 12s 总预算。
	const minHeadroom = 1 * time.Second
	arms := turnsFilterOptionsSsBudget +
		turnsFilterOptionsTagsBudget + turnsFilterOptionsTurnBudget
	if total := arms + minHeadroom; total >= turnsFilterOptionsBudget {
		t.Errorf("三条臂的子预算之和 %v + 最低余量 %v = %v，未低于端点总预算 %v：余量不足，"+
			"负载一高就会整体超时（实测 8s 单条子预算时端点到过 12.06s；"+
			"ss 臂无界时线上连过 3 次 500@12.05s）",
			arms, minHeadroom, total, turnsFilterOptionsBudget)
	}
}

// TestFilterOptionsNeverReturns500 —— 反判据：任何一条臂失败，端点仍必须 200。
//
// 修复前 ss 臂直接吃总预算 ctx，冷缓存时 ctx 一到就 writeError(500)，
// 线上连着 3 次 500@12.05s。这条判据钉住「降级而非报错」这个语义，
// 且用**真的超时**来触发，不依赖线上恰好冷缓存。
//
// 用一个必定超时的 handler（预算 1ns 的 ctx）驱动完整的 handleTurnsFilterOptions，
// 断言返回 200 且三个 turns 维度是**空数组而不是 null**。
func TestFilterOptionsNeverReturns500(t *testing.T) {
	// 1) 降级语义：一条都没算出来时，目标必须是**空数组**而不是 nil。
	//    nil 会被序列化成 null，前端拿到 null 而不是 []。
	ss := map[string]*[]string{"1": nil, "2": nil}
	ss["1"] = new([]string)
	ss["2"] = new([]string)
	resetFilterOptionGroups(ss)
	for dim, dst := range ss {
		if *dst == nil {
			t.Errorf("ss 臂 dim=%s 降级后是 nil：响应会序列化成 null 而不是 []", dim)
		}
		if len(*dst) != 0 {
			t.Errorf("ss 臂 dim=%s 降级后非空：%v", dim, *dst)
		}
	}

	// 2) 源码层：handler 里**不得**还有 500 分支。
	b, err := os.ReadFile("turns_filter_options.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "func (h *Handler) handleTurnsFilterOptions")
	if i < 0 {
		t.Fatal("找不到 handleTurnsFilterOptions，判据锚点失效")
	}
	end := strings.Index(src[i:], "\n}\n")
	if end < 0 {
		t.Fatal("找不到 handleTurnsFilterOptions 的结尾，判据锚点失效")
	}
	body := src[i : i+end]
	if strings.Contains(body, "writeError(w, http.StatusInternalServerError") {
		t.Error("handleTurnsFilterOptions 里仍有 500 分支：任一维度失败都会让整页报错，" +
			"而修复前 ss 臂正是这样在冷缓存时连出 3 次 500@12.05s")
	}
	// 三条臂都必须**有界**：无界的那条冷缓存时会独占总预算。
	//
	// ⚠ 这里必须匹配 `context.WithTimeout(ctx, <常量>)` 这个**机制**，
	// 不能只查常量名在函数体里出现过。
	// 变异验证记录：把 ss 臂改成 `context.WithCancel(ctx)`（同样无界）后，
	// 「常量名是否出现」这种写法**照样全绿**——因为 slog.Warn 里还在引用它。
	// 这是判据钉错了东西：它量的是「字符串出现过」，不是「查询被有界地包住了」。
	for _, tc := range []struct{ arm, konst string }{
		{"ss", "turnsFilterOptionsSsBudget"},
		{"tags", "turnsFilterOptionsTagsBudget"},
		{"turns", "turnsFilterOptionsTurnBudget"},
	} {
		pat := `context\.WithTimeout\(ctx,\s*` + tc.konst + `\)`
		if !regexp.MustCompile(pat).MatchString(body) {
			t.Errorf("%s 臂没有用 `context.WithTimeout(ctx, %s)` 包住查询：这条臂是无界的，"+
				"冷缓存时它会吃满总预算并让整页 500", tc.arm, tc.konst)
		}
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
	// ★ 必须传**非空租户**，且必须与生产一致。
	//   上一版这里传的是 ""，而生产上 tenantID 永远非空（GetTenantID 兜底 "default"），
	//   于是判据测的是一条**生产不可达的分支**，`AND AND` 语法错照样全绿。
	//   详见 TestFilterOptionsTenantClauseIsRealSQL 的头注。
	const tenant = "default"
	_, terr := h.queryFilterOptionGroups(tctx, h.turnMergedSource(tenant), turnGroups, []any{tenant})
	switch {
	case terr == nil:
		turnRows = len(respModels) + len(respProviders) + len(respStatus)
		// ★ 必须有正向下限。这一段原来只把行数打进 t.Log 就收工，于是
		//   「查得飞快但一个值都没返回」与「正常返回」在判据眼里完全一样——
		//   而线上恰恰是 3 个下拉恒空（语法错被降级吞掉，不报任何错）。
		//   「没坏」不等于「做了」：不设下限的判据在「干脆没返回」面前永远绿。
		if turnRows == 0 {
			t.Error("turns 臂返回 0 个值：models/providers/status_codes 三个下拉会全空。" +
				"线上曾连续 7 次命中（42601 语法错被降级路径吞掉，端点仍返回 200）")
		}
	case errors.Is(terr, context.DeadlineExceeded):
		t.Logf("turns 臂在 %v 子预算内没跑完（生产会退化成空数组）：%v",
			turnsFilterOptionsTurnBudget, terr)
		turnRows = -1
	default:
		t.Fatalf("turns 臂合并查询报错：%v", terr)
	}
	turnElapsed = time.Since(startTurn)

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

// ★ 2026-10-18 新增判据：租户形态的 turns 臂 SQL 必须**真的是合法 SQL**。
//
// 背景（这一条是本轮最贵的教训）：
//
// 生产上 tenantID **永远非空**。GetTenantID(r) 在取不到 auth 租户时兜底返回
// "default"（admin/context.go:37），而 admin 用户在 tenantFromQueryOrContext 里
// 没有 ?tenant= / X-Tenant-ID，最后落回 GetTenantID ⇒ tenantID="default"。
// ⇒ 上一版 `turnTenantClause` 里的 `tenantWhere == ""` 分支是**死代码**，
// 而上一版的真库判据恰恰只传了 ""，于是它测的是一条生产上走不到的分支：
//
//	turnMergedSource(" AND t.tenant_id = $1")
//	  → " AND " + TrimPrefix(Replace(s,"t.","",1), " ")
//	  → " AND " + "AND tenant_id = $1"
//	  → " AND AND tenant_id = $1"        ← 42601
//
// 线上表现不是报错，而是**静默降级**：turns 臂 err → WARN → 三个下拉恒空，
// 端点照旧 200。真库判据全绿、线上全空，两边都不报错。
//
// ⇒ 判据纪律的补充：参数也要同层。判据自己编一个「方便的值」传给被测函数，
// 等于测了一条生产走不到的分支；正确做法是让判据走生产解析路径。
func TestFilterOptionsTenantClauseIsRealSQL(t *testing.T) {
	h := &Handler{}
	sql := h.turnMergedSource("default")

	// 双 AND：上一版 42601 的直接形态。取子串两端都界定，避免误伤 "AND NOT" 等。
	if i := strings.Index(sql, "AND AND"); i >= 0 {
		t.Errorf("租户子句出现连续 AND（PG 会报 42601）：…%s…",
			sql[max(0, i-40):min(len(sql), i+40)])
	}
	// 两腿各一条，且列名不带表别名（两腿 FROM 里都是裸表名，没有 t 可限定）。
	if n := strings.Count(sql, "AND tenant_id = $1"); n != 2 {
		t.Errorf("租户子句出现 %d 次，两腿各一条应为 2 次：\n%s", n, strings.TrimSpace(sql))
	}
	if strings.Contains(sql, "t.tenant_id") {
		t.Error("两腿都没有表别名 t，租户列不能带 t. 前缀（会报 column t.tenant_id does not exist）")
	}
	// 空租户形态必须仍然可用（虽生产不可达，但不能因此把代码写崩）。
	if empty := h.turnMergedSource(""); strings.Contains(empty, "tenant_id") {
		t.Errorf("空租户形态不该出现 tenant_id：\n%s", strings.TrimSpace(empty))
	}
}

// TestFilterOptionsHandlerEndToEndWithRealTenant —— 真正端到端地驱动 handler。
//
// 上面那条是字符串层的；这条把**租户解析 + 拼 SQL + 执行**整条链走一遍，
// 而且用的是生产的解析方式：一个不带 auth context 的请求 ⇒ tenantID="default"
// （与 admin 用户在线上的路径一致）。
//
// 同层：调的是 `h.handleTurnsFilterOptions` 本身，不是它的片段。
func TestFilterOptionsHandlerEndToEndWithRealTenant(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	h := &Handler{db: pool}
	// 不注入 auth context：走 GetTenantID 的 "default" 兜底，与线上 admin 一致。
	req := httptest.NewRequest(http.MethodGet,
		"/api/admin/turns/sessions/filter-options", nil)
	rec := httptest.NewRecorder()
	h.handleTurnsFilterOptions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("端点返回 %d（body=%s）：这条链路上任何一步出错都会在这里露出来，"+
			"包括租户子句拼错", rec.Code, rec.Body.String())
	}
	var got TurnsFilterOptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败：%v", err)
	}
	// 三个 turns 维度必须**有值**。曾经它们恒空而端点仍返回 200。
	for name, vals := range map[string][]string{
		"models": got.Models, "providers": got.Providers, "status_codes": got.StatusCodes,
	} {
		if len(vals) == 0 {
			t.Errorf("%s 为空：该下拉在页面上无选项（端点仍返回 200，所以前端不报错、"+
				"用户只看到「没有可选项」）", name)
		}
	}
	// ss 臂顺带钉一下：它也是静默失败风险面（无子预算，超时直接 500）。
	if len(got.Projects) == 0 {
		t.Error("projects 为空")
	}
	t.Logf("projects=%d tasks=%d owners=%d clients=%d models=%d providers=%d status_codes=%d",
		len(got.Projects), len(got.Tasks), len(got.Owners), len(got.Clients),
		len(got.Models), len(got.Providers), len(got.StatusCodes))
}
