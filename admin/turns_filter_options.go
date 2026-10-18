package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ★ 2026-10-18：总预算 12s → 20s，三条臂各有界。
	//
	// 12s 是「只有 turns 臂有子预算」时的取值，当时 ss / tags 是**无界**的——
	// ss 冷缓存时能独占整个 12s，ctx 一到就 500（线上连出 3 次 500@12.05s）。
	//
	// 20s 的依据（EXPLAIN ANALYZE / BUFFERS 实测，非估算）：
	//
	//	ss 臂     69,912 buffers  热态 1.5~2.1s  换计划变体首读可达 9s+
	//	          （Parallel Hash Join：session_dim 全扫 828k 行 / 23,854 buffers
	//	            + session_summaries 全扫 423k 行、过滤掉 155,775 行）
	//	tags 臂        ~0.5s（走 LATERAL UNNEST，合不进 GROUPING SETS）
	//	turns 臂  159,153 buffers  热态 1.5~2.5s  换计划变体首读 6.3s
	//
	// 20s = ss 8s + tags 2s + turns 8s + 2s 余量。
	// **热态总耗时仍是 4~5s**，预算只在慢的时候才起作用，所以抬高它
	// 不会让正常情况变慢；而 nginx 侧 proxy_read_timeout 是 30s，留得住 10s 余量。
	turnsFilterOptionsBudget     = 20 * time.Second
	turnsFilterOptionsWindowDays = 30
	turnsFilterOptionsLimit      = 20

	// turnsFilterOptionsTurnBudget 是 session_turns 臂的子预算。
	// 8s：实测最慢的「换计划变体首读」是 6.3s，8s 能把它包住；
	// 再高就会把 ss 的余量吃掉，而 ss 才是那条真正结构性的查询。
	turnsFilterOptionsTurnBudget = 8 * time.Second

	// ★ 2026-10-18 新增：turnsFilterOptionsSsBudget 是 session_summaries 臂的子预算。
	//
	// 这一条是被线上打出来的，不是设计出来的。2705-9b586d21 部署后立刻连着
	// 3 次 500@12.05s，journalctl 的原因写在 handler 里：
	//
	//	filter-options: ss 维度查询失败
	//	  err=query filter option groups failed: timeout: context deadline exceeded
	//
	// ⇒ 与 turns 臂同一套降级语义：**超时只让 ss 的 5 个下拉空着，端点仍返 200。**
	//
	// 取舍如实记录：冷缓存那一次，projects/tasks/owners/clients 会空。
	// 这比整页 500 好（500 时前端报错、整页筛选不可用；降级时页面照常渲染，
	// 只是这四个下拉没选项，刷新一次就好），但它确实是拿可用性换了这四个下拉。
	//
	// ★ 已否证的三条路线（不要再重试）：
	//  1. 给 session_summaries 加覆盖索引让 planner 走 Index Only Scan：
	//     实测 buffers 69,912 → 168,993（**2.4 倍**），堆取 172,001 次。
	//     墙钟 3,432ms → 2,490ms 看着变快，但 IO 量翻三倍、冷缓存会更糟。
	//     墙钟变快是假象，buffers 才是确定量。
	//  2. 改读 session_dim.last_active_at 的现成索引（371 buffers / 0.7ms，快 200 倍）：
	//     **口径变了**，且实测不收敛——N=60,000 时仍缺 20 个、多 13 个值。
	//     两个排序口径（MAX(first_request_at) vs last_active_at）本质不同，
	//     不是同一个下拉。属口径变更，需 owner 裁定，不在性能修复里做。
	//  3. 给 session_turns 加 (tenant_id, ts DESC) INCLUDE (model, provider, status_code)：
	//     buffers 159,153 → 127,888（**仅 −20%**）。原因：九月分区
	//     （483,570 行）仍被 planner 选去用既有的
	//     session_turns_2026_09_heap_tenant_id_ts_idx，单独就占 118,030 buffers
	//     （占总量 84%）。要真拿到收益得让 planner 改选，那已经不是一条
	//     「加个索引」能定的事，且要与并发会话抢迁移编号。不做。
	turnsFilterOptionsSsBudget = 8 * time.Second

	// tags 臂走 LATERAL UNNEST，合不进 GROUPING SETS，只能单独一条。
	// 实测 ~0.5s；给它 2s 上界，保证它也不会无界地吃总预算。
	turnsFilterOptionsTagsBudget = 2 * time.Second
)

// resetFilterOptionGroups 把一组维度目标置成空数组（而不是留 nil）。
//
// 响应字段是 []string，nil 会序列化成 `null`，前端按数组渲染会拿到 null。
// 降级路径必须给出 `[]`，这样「这一格没数据」和「这一格是空列表」在
// JSON 上有区别，也让 trimFilterOptions 之后的长度断言有意义。
func resetFilterOptionGroups(groups map[string]*[]string) {
	for _, dst := range groups {
		if *dst == nil {
			*dst = []string{}
		}
	}
}

// TurnsFilterOptionsResponse 是各筛选维度的热门可选值。
type TurnsFilterOptionsResponse struct {
	Projects    []string `json:"projects"`
	Tasks       []string `json:"tasks"`
	Owners      []string `json:"owners"`
	Clients     []string `json:"clients"`
	Tags        []string `json:"tags"`
	Models      []string `json:"models"`
	Providers   []string `json:"providers"`
	StatusCodes []string `json:"status_codes"`
}

// handleTurnsFilterOptions 处理 GET /api/admin/turns/sessions/filter-options。
func (h *Handler) handleTurnsFilterOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), turnsFilterOptionsBudget)
	defer cancel()

	tenantID := ""
	if IsTenantAdmin(r) {
		tenantID = GetTenantID(r)
	} else {
		tenantID = tenantFromQueryOrContext(r)
	}

	tenantWhere := ""
	tenantArgs := []any{}
	if tenantID != "" {
		tenantWhere = " AND ss.tenant_id = $1"
		tenantArgs = []any{tenantID}
	}

	resp := &TurnsFilterOptionsResponse{}

	// ★ 2026-10-18：ss 臂的 5 个维度改成**一条 GROUPING SETS 查询**。
	//
	// 为什么合并（实测，同一 30 天窗口、无并发干扰）：
	//   5 条分跑（每条一次全量 join）1.49~1.54s × 5  ≈ 7.5s
	//   合并成 1 条                              4.58s
	// ⚠ 这个方向**依赖 30 天窗**。无窗时合并实测 35.4s，比分跑的总和 15.8s
	//   还慢一倍多（审计 §14.2）。加窗把输入从 776,465 行压到 312,457 行之后，
	//   合并才由负转正。⇒「合并成一条是负收益」只在**没加窗**时成立。
	ssGroups := map[string]*[]string{
		"1": &resp.Projects, "2": &resp.Tasks, "3": &resp.Owners,
		"4": &resp.Clients, "5": &resp.Clients,
	}
	// ★ ss 臂自带子预算，超时**降级**而不是 500（理由见 turnsFilterOptionsSsBudget）。
	//   修复前这里直接用总预算 ctx，冷缓存时整页 500@12.05s（线上 3 次连续命中）。
	//   与 turns 臂保持同一套降级语义：失败只影响自己那几格，不作废已算对的维度。
	sctx, scancel := context.WithTimeout(ctx, turnsFilterOptionsSsBudget)
	_, err := h.queryFilterOptionGroups(
		sctx, h.ssMergedSource(tenantWhere), ssGroups, tenantArgs)
	scancel()
	if err != nil {
		// 必须记 err：修复前这一分支只有 writeError，journalctl 里查不到任何
		// 原因，只能看到 HTTP 层的 500，排查时完全瞎的（2026-10-18 实际踩过）。
		slog.Warn("filter-options: ss 维度不可用，已退化为空",
			"budget", turnsFilterOptionsSsBudget, "tenant", tenantID, "err", err)
		resetFilterOptionGroups(ssGroups)
	}
	for _, dst := range ssGroups {
		trimFilterOptions(dst)
	}

	// tags 走 LATERAL UNNEST，GROUPING SETS 合不进去，单独一条。
	// 也给一条子预算：它是端点里最后一条无界查询。
	tagctx, tagcancel := context.WithTimeout(ctx, turnsFilterOptionsTagsBudget)
	ssTags := h.lastActiveQuery(h.lastActiveSource(
		"tag", ", LATERAL UNNEST(ss.user_tags) AS tag", "1=1", tenantWhere))
	terr := h.runFilterOptionQueries(tagctx, []string{ssTags},
		tenantArgs, []*[]string{&resp.Tags})
	tagcancel()
	if terr != nil {
		slog.Warn("filter-options: tags 维度不可用，已退化为空",
			"budget", turnsFilterOptionsTagsBudget, "tenant", tenantID, "err", terr)
		if resp.Tags == nil {
			resp.Tags = []string{}
		}
	}

	// ★ 基于 session_turns 的 3 个维度：合并成一条 GROUPING SETS，并**自带子预算**。
	//
	// 合并收益（实测，同一 30 天窗）：
	//   3 条分跑 3×3.8s ≈ 11.5s  →  合并成 1 条 2.8s
	//
	// ⚠ 但这条查询的耗时**天然不稳定**，同一会话里测到 2.8s / 26.1s / 68.9s
	//   三个值（工作集是 session_turns_2026_09 的 2.7GB，远大于 shared_buffers，
	//   页是否驻留决定它是 2.8s 还是 26s）。加覆盖索引没能改变计划（审计 §14.6），
	//   也没有任何滚存带 model/provider/status_code 维度。
	//   ⇒ 剩下的唯一稳妥做法是给它**有界子预算**，超时则安静退化：
	//   ss 臂 + tags 稳定在 ~3.1s，优先保证；三个下拉空着比整页 500 好。
	//
	// 这与 cache-economics 的压缩计数是同一套降级语义（失败只影响它自己，
	// 不让已算对的维度一起作废）。响应里三个字段本来就是数组，前端按空数组渲染。
	turnGroups := map[string]*[]string{
		"1": &resp.Models, "2": &resp.Providers, "3": &resp.StatusCodes,
	}
	tctx, tcancel := context.WithTimeout(ctx, turnsFilterOptionsTurnBudget)
	defer tcancel()
	// 传 tenantID 本身，不传成串谓词（理由见 turnMergedSource）。
	// 租户参数与非租户形态**完全一致**：tenantArgs 早就带上了 $1，两处共用。
	if _, terr := h.queryFilterOptionGroups(
		tctx, h.turnMergedSource(tenantID), turnGroups, tenantArgs); terr != nil {
		resetFilterOptionGroups(turnGroups)
		slog.Warn("filter-options: turns 维度不可用，已退化为空",
			"budget", turnsFilterOptionsTurnBudget, "tenant", tenantID, "err", terr)
	}
	for _, dst := range turnGroups {
		trimFilterOptions(dst)
	}

	writeJSON(w, http.StatusOK, resp)
}

// lastActiveSource 组装基于 session_summaries 的热门值内层子查询：
//
//	SELECT <valueSel> AS v, MAX(ss.first_request_at) AS last_at
//	FROM session_summaries ss<fromSuffix>
//	WHERE <cond> AND ss.first_request_at >= NOW() - INTERVAL '30 days'<tenantWhere>
//	GROUP BY 1
//
// ★ 2026-10-18：这里原本**没有时间窗**，与本文件头「基于近 30 天实际使用数据」
// 和 turns 臂的 30 天窗都不一致——端点内部两个臂口径不同，而前端
// TurnsListView 默认 timePreset='all'，于是 ss 臂给出全历史选项、turns 臂只给
// 近 30 天，同一个下拉框里两套语义。
//
// 经 owner 裁定：**统一为近 30 天**（与头注和 turns 臂一致）。代价如实记录：
// 超过 30 天没用过的 project/task/owner/client/tag 不再出现在下拉里。
//
// 顺带的性能收益（实测，同一 30 天窗口、无并发干扰）：
//
//	projects  3.92s → 1.36s（−65%）
//	tasks     7.03~9.66s → 3.55s（−60%）
//
// 原因是 session_summaries 共 776,465 行而近 30 天只有 312,457 行（40%），
// 无窗时每次分组都要扫全量历史。
func (h *Handler) lastActiveSource(valueSel, fromSuffix, cond, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT %s AS v, MAX(ss.first_request_at) AS last_at FROM session_summaries ss%s"+
			" WHERE %s AND ss.first_request_at >= NOW() - INTERVAL '%d days'%s GROUP BY 1",
		valueSel, fromSuffix, cond, turnsFilterOptionsWindowDays, tenantWhere)
}

// lastActiveQuery 包成外层：SELECT v FROM (<source>) t ORDER BY last_at DESC LIMIT K。
func (h *Handler) lastActiveQuery(source string) string {
	return "SELECT v FROM (" + source + ") t ORDER BY last_at DESC LIMIT " + fmt.Sprint(turnsFilterOptionsLimit)
}

// lastActiveTurnQuery 组装基于 session_turns 的热门值查询。
func (h *Handler) lastActiveTurnQuery(valueExpr, filter, tenantWhere string) string {
	return fmt.Sprintf(
		"SELECT v FROM (SELECT %s AS v, MAX(t.ts) AS last_at FROM public.session_turns_with_current_month t"+
			" WHERE %s AND t.ts > NOW() - INTERVAL '%d days'%s GROUP BY 1)"+
			" t2 ORDER BY last_at DESC LIMIT %d",
		valueExpr, filter, turnsFilterOptionsWindowDays, tenantWhere, turnsFilterOptionsLimit)
}

// runFilterOptionQueries **顺序**执行一组热门值查询并写入对应目标切片。
//
// ★ 2026-10-18：这里一度改成 errgroup 并发，实测是**净负**，已改回顺序。
//
//	8 条并发（加 30 天窗后）：墙钟 68,971 ms
//	8 条串行（加 30 天窗后）：约 19s
//	⇒ 并发把最慢的 `session_turns` 两条从 ~3.8s 推到 68.9s。
//
// 原因：这几条的工作集（session_turns 9 月分区 2.7GB）远大于 shared_buffers，
// 并发时彼此把对方的页挤出缓存，于是「并行」变成「来回换页」。
// 审计 §14.7 记了这次否证。dashboardapi/session_overview.go 那句
// 「墙钟 = max(段)」在这里**不成立**。
func (h *Handler) runFilterOptionQueries(ctx context.Context, queries []string, args []any, targets []*[]string) error {
	if len(queries) != len(targets) {
		return fmt.Errorf("query/target count mismatch: %d vs %d", len(queries), len(targets))
	}
	for i, q := range queries {
		vals, err := h.queryFilterOptionValues(ctx, q, args)
		if err != nil {
			return fmt.Errorf("query filter options [%d] failed: %w", i, err)
		}
		*targets[i] = vals
	}
	return nil
}

// queryFilterOptionGroups 跑一条 GROUPING SETS 查询，按 dim 分桶取每个维度
// 最近活跃的前 N 个值。
//
// 合并查询返回 (dim, v, last_at)，同一个 v 可能出现在多个维度
// （例如 client_id 与 application_code 同值），所以按 v 取 MAX(last_at) 收口，
// 与修复前 clients 那条 `UNION` 的意图一致（同一个下拉框里只出现一次）。
func (h *Handler) queryFilterOptionGroups(ctx context.Context, query string,
	groups map[string]*[]string, args []any) (map[string][]string, error) {

	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query filter option groups failed: %w", err)
	}
	defer rows.Close()

	nDims := numFilterOptionDims(query)
	var hits []filterOptionHit
	for rows.Next() {
		// ★ 必须是 **string 而不是 *string：GROUPING SETS 的每个 grouping set
		//   只让对应那一列非空，其余全是 NULL，而 pgx 拒绝把 NULL 扫进
		//   非空的 *string（`cannot scan NULL into *string`）。
		//   ⇒ 用 *string 的话**每一行都会失败**，端点立刻 500。
		//   这个 bug 能溜过去，是因为当时的判据自己手写了一遍扫描
		//   （用的是 **string），没有调用本函数——判据复刻了「扫描这件事」
		//   而不是「同一个函数」。真库层必须直接调 queryFilterOptionGroups。
		cols := make([]*string, nDims)
		holders := make([]any, nDims)
		for i := range cols {
			holders[i] = &cols[i]
		}
		var lastAt any
		dest := append(holders, &lastAt)
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("scan filter option groups failed: %w", err)
		}
		ts := parseFilterOptionTime(lastAt)
		for i, c := range cols {
			if c == nil || *c == "" {
				continue
			}
			hits = append(hits, filterOptionHit{dim: strconv.Itoa(i + 1), v: *c, lastAt: ts})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate filter option groups failed: %w", err)
	}

	// written 记录哪些目标切片已经初始化过，避免同一字段被重复重置。
	// 键必须是**目标指针**而不是 dim：dim4 与 dim5 指向同一个字段，
	// 按 dim 去重会让第二个维度把第一个的结果重置掉。
	written := map[string]struct{}{}

	// 按 (dim, v) 收口：同 v 取最近活跃时间。
	best := map[string]map[string]time.Time{}
	for _, h1 := range hits {
		if h1.v == "" {
			continue
		}
		if best[h1.dim] == nil {
			best[h1.dim] = map[string]time.Time{}
		}
		if cur, ok := best[h1.dim][h1.v]; !ok || h1.lastAt.After(cur) {
			best[h1.dim][h1.v] = h1.lastAt
		}
	}
	out := map[string][]string{}
	for dim, byV := range best {
		type pair struct {
			v string
			t time.Time
		}
		ps := make([]pair, 0, len(byV))
		for v, t := range byV {
			ps = append(ps, pair{v, t})
		}
		sort.Slice(ps, func(a, b int) bool {
			if ps[a].t.Equal(ps[b].t) {
				return ps[a].v < ps[b].v
			}
			return ps[a].t.After(ps[b].t)
		})
		if len(ps) > turnsFilterOptionsLimit {
			ps = ps[:turnsFilterOptionsLimit]
		}
		for _, p := range ps {
			out[dim] = append(out[dim], p.v)
		}
	}
	// 写进调用方给的切片指针。
	//
	// ★ 多个维度可能映射到**同一个**字段（client_id 与 application_code 都进
	//   Clients），所以这里必须**合并**而不是逐个覆盖——直接 `*dst = out[dim]`
	//   会让后一个维度把前一个的结果整个抹掉（实测 clients 只剩 13 个值，
	//   client_id 那一维的 193 个全丢了，而 map 遍历顺序随机 ⇒ 丢多少还不确定）。
	for dim, dst := range groups {
		if dst == nil {
			continue
		}
		key := fmt.Sprintf("%p", dst)
		if _, seen := written[key]; !seen {
			*dst = nil
			written[key] = struct{}{}
		}
		*dst = append(*dst, out[dim]...)
	}
	for _, dst := range groups {
		if dst != nil && *dst == nil {
			*dst = []string{}
		}
	}
	return out, nil
}

// parseFilterOptionTime 把 last_at 列转成 time.Time；NULL/未知形态给零值。
func parseFilterOptionTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case *time.Time:
		if t == nil {
			return time.Time{}
		}
		return *t
	default:
		return time.Time{}
	}
}

// trimFilterOptions 把超出 LIMIT 的切片截断，并保证非 nil。
func trimFilterOptions(dst *[]string) {
	if *dst == nil {
		*dst = []string{}
		return
	}
	if len(*dst) > turnsFilterOptionsLimit {
		*dst = (*dst)[:turnsFilterOptionsLimit]
	}
}

// windowClause 是 ss 臂统一的 30 天窗（两个臂必须同一个常量，见 lastActiveSource）。
func windowClause() string {
	return fmt.Sprintf(" AND ss.first_request_at >= NOW() - INTERVAL '%d days'", turnsFilterOptionsWindowDays)
}

// intStrings 把 []int 渲染成 "1, 2, 3"。
func intStrings(xs []int) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

// queryFilterOptionValues 跑一条热门值查询并把结果收成字符串切片。
func (h *Handler) queryFilterOptionValues(ctx context.Context, query string, args []any) ([]string, error) {
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	vals := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return vals, nil
}

// ssMergedSource 是 ss 臂 5 个维度合并后的单条 GROUPING SETS 查询。
//
// ★ 维度列必须排在 SELECT 列表**最前面**：`GROUP BY GROUPING SETS ((1),(2),…)`
//
//	里的数字是**输出列位置**。第一版把维度编号常量放在最前，于是 (1)…(5)
//	指向的是那五个常量而不是维度列，PG 直接报
//	`column "ss.gw_project_id" must appear in the GROUP BY clause`
//	（SQLSTATE 42803）。⇒ 合并查询里「输出列顺序」不是格式问题，是语义。
//
// 每个 grouping set 产出的行里只有对应那一列非空，其余为 NULL，
// 所以 Go 侧靠「哪一列非空」识别维度（见 queryFilterOptionGroups）。
// 原先各维度自带的 `IS NOT NULL AND != ”` 谓词不再下推——
// 合并不可能让 5 条谓词各管一列；空串/NULL 在 Go 侧按 v == "" 跳过。
func (h *Handler) ssMergedSource(tenantWhere string) string {
	joinDim := " JOIN session_dim sd ON sd.gw_session_id = ss.session_key AND sd.tenant_id = ss.tenant_id"
	return fmt.Sprintf(
		"SELECT s.c1, s.c2, s.c3, s.c4, s.c5, s.last_at FROM ("+
			"SELECT %s, MAX(ss.first_request_at) AS last_at"+
			" FROM session_summaries ss%s"+
			" WHERE 1=1%s GROUP BY GROUPING SETS ((1),(2),(3),(4),(5))) s",
		strings.Join([]string{
			"COALESCE(NULLIF(ss.gw_project_id, ''), sd.project_id) AS c1",
			"sd.task_id AS c2", "sd.owner_user AS c3",
			"sd.client_id AS c4", "sd.application_code AS c5",
		}, ", "),
		joinDim, tenantWhere+windowClause())
}

// turnMergedSource 是 turns 臂 3 个维度合并后的单条 GROUPING SETS 查询。
// 维度列排在 SELECT 最前（理由见 ssMergedSource：GROUPING SETS 里的数字是输出列位置）。
// 1=model 2=provider 3=status_code。
//
// ★ 2026-10-18：**不走 `session_turns_with_current_month` 视图，改读 hot∪父表两腿。**
//
// 实测（同一 30 天窗口，无并发干扰，返回行数都是 961）：
//
//	session_turns_with_current_month（视图）  44,564 ms
//	public.session_turns（父表单腿）            1,895 ms   —— 23×
//	父表 ∪ hot 两腿                            2,048 / 2,814 ms（两次）
//
// 视图慢在**它定义的东西**，不是数据量：那个视图给每个请求补 canonical 列、
// 反连 session_dim 等，代价与「本端点只要 MAX(ts) 分组」的需求差着数量级。
// 与 cache-economics 读 `_request_logs` 母表是同一类判断。
//
// 为什么是**两腿**而不是裸父表：最近 8h 的行只在 `session_turns_hot`
// （hot 默认仅保留 8h），裸父表读对「近 30 天」是真盲区。两腿 UNION ALL 与
// 视图同形，且本查询**不是计数**（每个维度取 MAX(ts) 后按值去重），
// 所以不存在 promote 重复计数的问题——与 cache-economics 那条计数查询的
// 单腿取舍刻意不同，理由记在这里。
// 入参是 **tenantID 本身**，不是 ss 臂那种 ` AND ss.tenant_id = $1` 的成串谓词。
//
// ★ 2026-10-18 修复一个 100% 必现的语法错。上一版让本函数接收 ss 臂的
// tenantWhere（`" AND t.tenant_id = $1"`）再**用字符串手术**去掉表别名：
//
//	return " AND " + strings.TrimPrefix(strings.Replace(tenantWhere, "t.", "", 1), " ")
//
//	输入 " AND t.tenant_id = $1"
//	  → Replace      → " AND tenant_id = $1"
//	  → TrimPrefix " " → "AND tenant_id = $1"
//	  → 前缀拼 " AND " → " AND AND tenant_id = $1"   ← 42601 syntax error
//
// 线上证据（154，2705-9b586d21，23:33~23:34 连续 7 次）：
//
//	filter-options: turns 维度不可用，已退化为空
//	  err=ERROR: syntax error at or near "AND" (SQLSTATE 42601)
//
// 而它上一轮是**查得出来的**：见 tenantIDResolution 的说明——生产上 tenantID
// 永远非空（GetTenantID 兜底返回 "default"），所以空串分支根本走不到。
func (h *Handler) turnMergedSource(tenantID string) string {
	win := fmt.Sprintf("ts > NOW() - INTERVAL '%d days'", turnsFilterOptionsWindowDays)
	// 两腿都在 FROM 里直接写表名、没有别名，所以租户列不能带 `t.` 前缀。
	tc := ""
	if tenantID != "" {
		tc = " AND tenant_id = $1"
	}
	return fmt.Sprintf(
		"SELECT s.c1, s.c2, s.c3, s.last_at FROM ("+
			"SELECT %s, MAX(u.ts) AS last_at FROM ("+
			"SELECT model AS c1, provider AS c2, status_code::text AS c3, ts"+
			" FROM public.session_turns WHERE %s%s"+
			" UNION ALL"+
			" SELECT model, provider, status_code::text, ts"+
			" FROM public.session_turns_hot WHERE %s%s"+
			") u GROUP BY GROUPING SETS ((1),(2),(3))) s",
		strings.Join([]string{"u.c1 AS c1", "u.c2 AS c2", "u.c3 AS c3"}, ", "),
		win, tc,
		win, tc)
}

// filterOptionHit 是一条「维度 → 值 → 最近活跃」的中间结果。
type filterOptionHit struct {
	dim    string
	v      string
	lastAt time.Time
}

// numFilterOptionDims 从合并查询的输出列数推出维度数（= 列数 − 1 个 last_at）。
func numFilterOptionDims(query string) int {
	// 取最外层 SELECT 的列数：s.c1, s.c2, ... , s.last_at
	open := strings.Index(query, "SELECT ")
	if open < 0 {
		return 3
	}
	rest := query[open+len("SELECT "):]
	close := strings.Index(rest, " FROM (")
	if close < 0 {
		return 3
	}
	n := 0
	for _, part := range strings.Split(rest[:close], ",") {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	if n < 2 {
		return 3
	}
	return n - 1 // 减去 last_at
}
