// Package admin — 用量趋势序列端点（2026-10-02 看板「按模型分线」轮）。
//
//	GET /api/admin/usage/trend-series?days|start&end&tenant_id&provider_id&api_key_id&model&top
//	GET /api/admin/usage/trend-models?…同上过滤条件
//
// model 支持多选：重复 query 参数（model=a&model=b），单值形式向后兼容；
// 上限 usageTrendMaxModels，超出截断（多选已定时三档都不再折叠长尾）。
//
// trend-series 返回按模型拆分的时间序列（每点含 requests/tokens/credits/cost_usd
// 四指标，指标过滤由前端选列）；trend-models 返回当前过滤条件下的模型选项及
// 各自总量（供全页视图的模型下拉）。
//
// 数据面两档（2026-10-02 实测本地库后定形；模型名口径统一 outbound 优先、
// client_model 兜底、'__unknown__' 垫底，与 rollup writer 同式）：
//   - dim 档（无 provider/api_key 过滤）：request_stats_dim_minute
//     dim_type='model'，单表索引扫描（今天窗 22ms / 7d 1.2s 实测）。
//   - detail 档（带 provider 或 api_key 过滤）：**会话族原生源**
//     db.SessionFamilyTurnsSourceSQL()
//     （★ 2026-10-20：原读面 request_logs_with_current_month_without_customer_id
//     直读 v1 族、v1 停写后恒 0 行 ⇒ 本档此前恒空；890 删族后会变 42P01 ⇒ 改指会话族）
//
// ★★ 2026-11-23：provider 过滤**也从 provider 档并入 detail 档** ⇒ ★★
// 原 provider 档（request_stats_minute JOIN provider_models 取展示名）**在生产
// 已整体退化**：该表只有 provider_id × canonical_id，没有模型名字段 ⇒ 模型身份
// 只能靠 canonical_id 反查；而 **211 真库实测请求日志的 canonical_id 全为 NULL**
// （近 3 天按 provider 分组：with_canon 恒 0，provider 14/587/18/32… 无一例外）
// ⇒ rollup 写面 COALESCE(r.canonical_id,0) ⇒ request_stats_minute 的 canonical_id
// **恒为 0** ⇒ provider_models 的 canonical_id 映射**永远 JOIN 不上** ⇒
// 兜底标签 'model#'||canonical_id ⇒ **所有行塌成一行 `model#0`** ⇒
// 供应商过滤下的图表/明细表**恒为一行、且名字是内部哨兵**。
// ★ 也就是说：那一档**不是"更快的同一答案"，是"更快的错误答案"** ——
// 快在读汇总表，慢在它把「模型身份」寄托在一个已经不再被写入的列上。
// ★ 通用式：**读面选择在数据形态变化后会静默退化**（v1 族停写 ⇒ canonical_id
// 不再落库 ⇒ 依赖它的读面不报错、不空表，只是给出一份看起来正常的错答案）。
// ⇒ 供应商过滤改走明细档：模型名直接来自 outbound_model/client_model，
// **且与同屏下半部分的排行卡片（同一明细读面）口径一致**。
// ★ 代价（实测，211 生产库，provider=apiclaude 近 7 天）：明细聚合 374ms。
//
// 唯一含 api_key_id 的读面；无 api_key 索引的旧分区上长窗会慢，前端对超时
//
//	给出「缩短时间范围」提示。
//
// 折叠：两档统一「单次扫描出 (model,bucket) 组 → Go 侧按窗口总量取 top-N、
// 其余聚合 '__others__'」。不做 SQL 内 CTE 折叠——detail 档双扫实测 75s，
// 单扫砍半；dim 档模型基数（本地 649）让 Go 折叠零成本。
package admin

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/maas"
)

const usageTrendOthersKey = "__others__"

// usageTrendMaxModels 是 model 多选的上限：多选已定时三档读面都不折叠长尾，
// 每个选中模型各成一条线，无上限会让序列数失控。与 top 上限 20 对齐。
const usageTrendMaxModels = 20

// 与 bg/stats_minute_rollup.go 的 model 维度同式；detail 档 SQL 中多处引用必须一致。
//
// ★ 2026-11-23 深链钉住轮：模型口径表达式按**表别名**生成（明细排行用 rl、
// 趋势序列用 r）⇒ 两处口径共用同一份模板，改一处不会漏另一处
// （★ 之前两份各写一遍，改了一边忘了另一边就是「卡片 14,674 / 点进去 0 条」）。
const usageTrendModelExprTemplate = `COALESCE(NULLIF(%s.outbound_model, ''), NULLIF(%s.client_model, ''), '__unknown__')`

var usageTrendModelExpr = fmt.Sprintf(usageTrendModelExprTemplate, "r", "r")

// usageTrendModelExprRL 是同口径的 rl 别名版本（top-rankings 明细查询用）。
var usageTrendModelExprRL = fmt.Sprintf(usageTrendModelExprTemplate, "rl", "rl")

type usageTrendPoint struct {
	Bucket   string  `json:"bucket"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

type usageTrendSeries struct {
	Model         string            `json:"model"`
	TotalRequests int64             `json:"total_requests"`
	TotalTokens   int64             `json:"total_tokens"`
	TotalCredits  int64             `json:"total_credits"`
	TotalCostUSD  float64           `json:"total_cost_usd"`
	Points        []usageTrendPoint `json:"points"`
}

type usageTrendSeriesResponse struct {
	Start         string             `json:"start"`
	End           string             `json:"end"`
	BucketMinutes int                `json:"bucket_minutes"`
	Top           int                `json:"top"`
	Source        string             `json:"source"`
	Series        []usageTrendSeries `json:"series"`
	// 恒发（无 omitempty）：空序列与「真的没有用量」在图上同形。
	// 全新安装没迁移 rollup 时返回的是降级，不是零用量。
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
	MissingView    string `json:"missing_view,omitempty"`
	Hint           string `json:"hint,omitempty"`
}

type usageTrendModelEntry struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	Tokens   int64   `json:"tokens"`
	Credits  int64   `json:"credits"`
	CostUSD  float64 `json:"cost_usd"`
}

type usageTrendModelsResponse struct {
	Start  string                 `json:"start"`
	End    string                 `json:"end"`
	Source string                 `json:"source"`
	Models []usageTrendModelEntry `json:"models"`
	// 恒发（无 omitempty）：模型下拉空列表与「真的没有模型」同形。
	Degraded       bool   `json:"degraded"`
	DegradedReason string `json:"degraded_reason,omitempty"`
	MissingView    string `json:"missing_view,omitempty"`
	Hint           string `json:"hint,omitempty"`
}

// usageTrendFilters 是三档查询共享的过滤参数。models 为空 = 不过滤；
// 非空时（1 个或多个）三档统一不折叠长尾（用户已显式圈定模型集合）。
type usageTrendFilters struct {
	tenantID   string
	providerID int64
	apiKeyID   int64
	models     []string
	top        int
}

// usageTrendModelsWhere 生成模型多选谓词片段（" AND <expr> = ANY($n)"）并追加
// 参数；modelExpr 是三档各自的模型列表达式（dim_key / provider 展示名 / 明细
// COALESCE 口径）。models 为空返回空片段（不过滤）。
func usageTrendModelsWhere(modelExpr string, models []string, args []any) (string, []any) {
	if len(models) == 0 {
		return "", args
	}
	args = append(args, models)
	return fmt.Sprintf(" AND %s = ANY($%d)", modelExpr, len(args)), args
}

func usageTrendFiltersFromRequest(r *http.Request) usageTrendFilters {
	f := usageTrendFilters{
		tenantID:   statsTenantScope(r),
		providerID: int64(queryInt(r, "provider_id", 0)),
		apiKeyID:   int64(queryInt(r, "api_key_id", 0)),
		top:        queryInt(r, "top", 8),
	}
	// 重复 query 参数多选（model=a&model=b）；单值（model=a）是长度 1 的退化
	// 形式，向后兼容。空值/重复值剔除，超上限截断。
	seen := make(map[string]bool)
	for _, m := range r.URL.Query()["model"] {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		f.models = append(f.models, m)
		if len(f.models) >= usageTrendMaxModels {
			break
		}
	}
	if f.top < 1 {
		f.top = 1
	}
	if f.top > 20 {
		f.top = 20
	}
	return f
}

// usageTrendSource 挑数据档：provider / api_key 过滤都走明细读面。
//
// ★ 2026-11-23：provider 过滤原本单独走 provider 档（request_stats_minute），
// 但那一档在生产**恒返回单行 `model#0`**（canonical_id 全为 0 ⇒ 无模型身份，
// 详见文件头「provider 档已整体退化」的说明）⇒ provider 与 api_key 现在
// 合并为同一条明细档 ⇒ 顺带让「图表」与「同屏排行卡片」口径一致（两者
// 都是明细读面）。
func usageTrendSource(f usageTrendFilters) string {
	switch {
	case f.apiKeyID > 0, f.providerID > 0:
		return "request_logs_with_current_month"
	default:
		return "request_stats_dim_minute"
	}
}

func (h *Handler) usageTrendSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tr, rangeErr := boardTimeRangeFromRequest(r)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	f := usageTrendFiltersFromRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	source := usageTrendSource(f)
	var rows []usageTrendRow
	var err error
	switch source {
	case "request_logs_with_current_month":
		rows, err = h.queryUsageTrendDetail(ctx, tr, f)
	default:
		rows, err = h.queryUsageTrendRollup(ctx, tr, f)
	}
	if err != nil {
		if IsMissingRelationError(err) {
			// 全新安装可能还没有 rollup 表/当月视图 —— 返回空序列而不是 500。
			// 2026-10-03: 原为空序列无标记，图上与「这段时间真的零用量」同形。
			view := ReportMissingRelation(slog.Default(), "usageTrendSeries", err)
			writeJSON(w, http.StatusOK, usageTrendSeriesResponse{
				Start:          tr.Start.UTC().Format(time.RFC3339),
				End:            tr.End.UTC().Format(time.RFC3339),
				BucketMinutes:  tr.trendBucketMinutes(),
				Top:            f.top,
				Source:         source,
				Series:         []usageTrendSeries{},
				Degraded:       true,
				DegradedReason: view,
				MissingView:    view,
				Hint:           missingRelationHint(view),
			})
			return
		}
		writeInternalErr(w, "usage trend-series query failed", err)
		return
	}

	series := pivotUsageTrendRows(foldUsageTrendRows(rows, f.top, len(f.models) > 0))
	// '__others__' 固定排最后，其余按总请求数降序（与折叠排序一致）。
	sort.SliceStable(series, func(i, j int) bool {
		if series[i].Model == usageTrendOthersKey {
			return false
		}
		if series[j].Model == usageTrendOthersKey {
			return true
		}
		return series[i].TotalRequests > series[j].TotalRequests
	})
	if series == nil {
		series = []usageTrendSeries{}
	}

	writeJSON(w, http.StatusOK, usageTrendSeriesResponse{
		Start:         tr.Start.UTC().Format(time.RFC3339),
		End:           tr.End.UTC().Format(time.RFC3339),
		BucketMinutes: tr.trendBucketMinutes(),
		Top:           f.top,
		Source:        source,
		Series:        series,
	})
}

func (h *Handler) usageTrendModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tr, rangeErr := boardTimeRangeFromRequest(r)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	f := usageTrendFiltersFromRequest(r)

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	source := usageTrendSource(f)
	var models []usageTrendModelEntry
	var err error
	switch source {
	case "request_logs_with_current_month":
		models, err = h.queryUsageTrendModelsDetail(ctx, tr, f)
	default:
		models, err = h.queryUsageTrendModelsRollup(ctx, tr, f)
	}
	if err != nil {
		if IsMissingRelationError(err) {
			view := ReportMissingRelation(slog.Default(), "usageTrendModels", err)
			writeJSON(w, http.StatusOK, usageTrendModelsResponse{
				Start:          tr.Start.UTC().Format(time.RFC3339),
				End:            tr.End.UTC().Format(time.RFC3339),
				Source:         source,
				Models:         []usageTrendModelEntry{},
				Degraded:       true,
				DegradedReason: view,
				MissingView:    view,
				Hint:           missingRelationHint(view),
			})
			return
		}
		writeInternalErr(w, "usage trend-models query failed", err)
		return
	}
	if models == nil {
		models = []usageTrendModelEntry{}
	}
	writeJSON(w, http.StatusOK, usageTrendModelsResponse{
		Start:  tr.Start.UTC().Format(time.RFC3339),
		End:    tr.End.UTC().Format(time.RFC3339),
		Source: source,
		Models: models,
	})
}

// usageTrendRow 是三档路径统一的 (model, bucket) 聚合行。
type usageTrendRow struct {
	Model    string
	Bucket   time.Time
	Requests int64
	Tokens   int64
	Credits  int64
	CostUSD  float64
}

// queryUsageTrendRollup 读 request_stats_dim_minute（dim_type='model'）。
func (h *Handler) queryUsageTrendRollup(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendRow, error) {
	where := "m.dim_type = 'model' AND m.bucket >= $1 AND m.bucket < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND m.tenant_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere("m.dim_key", f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}
	query := fmt.Sprintf(`
		SELECT m.dim_key AS model,
			%s,
			COALESCE(SUM(m.requests), 0),
			COALESCE(SUM(m.total_tokens), 0),
			COALESCE(SUM(m.credits_charged), 0),
			COALESCE(SUM(m.cost_usd), 0)
		FROM request_stats_dim_minute m
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 2, 1
	`, sqlTrendBucket("m.bucket", tr.trendBucketMinutes()), where)
	return h.scanUsageTrendRows(ctx, query, args)
}

// queryUsageTrendDetail 读 _request_logs 视图族（带 api_key_id 过滤；provider 过滤
// 可同时下推）。credits 口径与 rollup writer 一致（COALESCE(credits_charged, 估算)）。
func (h *Handler) queryUsageTrendDetail(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendRow, error) {
	query, args := usageTrendDetailSeriesSQL(tr, f)
	return h.scanUsageTrendRows(ctx, query, args)
}

// usageTrendDetailSeriesSQL 拼明细档的序列 SQL（**抽成纯函数**：位置参数
// 错位过一次 ⇒ 整条 SQL 变成 `FROM <credits 表达式> r` ⇒ 42P01「missing
// FROM-clause entry for table r」⇒ 端点静默降级成空序列 ⇒ **页面显示「没有
// 用量」**，而真相是「查询本身写错了」）。
// ★ 通用式：**同类 SQL 片段超过三处**（模型口径 / 时间桶 / credits / FROM 源）
// 时，位置参数清单迟早会错位 ⇒ ⇒ 把「哪个片段进哪个槽」写成**具名局部变量**
// （而不是靠 fmt 的参数顺序对齐）⇒ 错位在编译期/读代码期就能看见。
func usageTrendDetailSeriesSQL(tr boardTimeRange, f usageTrendFilters) (string, []any) {
	creditsExpr := maas.RequestLogCreditsSQL("r", f.tenantID == "" || f.tenantID == "default")
	where := "r.request_status IN ('success', 'failure', 'rate_limited') AND r.ts >= $1 AND r.ts < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += " AND r.tenant_id = $3"
	}
	if f.providerID > 0 {
		args = append(args, f.providerID)
		where += fmt.Sprintf(" AND r.provider_id = $%d", len(args))
	}
	if f.apiKeyID > 0 {
		args = append(args, f.apiKeyID)
		where += fmt.Sprintf(" AND r.api_key_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere(usageTrendModelExpr, f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}

	// 具名片段 ⇒ 槽位与片段一一对应（不再靠参数顺序对齐）。
	modelExpr := usageTrendModelExpr
	bucketExpr := sqlTrendBucket("r.ts", tr.trendBucketMinutes())
	fromExpr := dbpkg.SessionFamilyTurnsSourceSQL()

	query := fmt.Sprintf(`
		SELECT %s AS model,
			%s,
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(COALESCE(r.total_tokens, 0)), 0),
			COALESCE(SUM(%s), 0),
			COALESCE(SUM(COALESCE(r.cost_usd, 0)), 0)
		FROM %s r
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 2, 1
	`, modelExpr, bucketExpr, creditsExpr, fromExpr, where)
	return query, args
}

func (h *Handler) scanUsageTrendRows(ctx context.Context, query string, args []any) ([]usageTrendRow, error) {
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usageTrendRow
	for rows.Next() {
		var row usageTrendRow
		if err := rows.Scan(&row.Model, &row.Bucket, &row.Requests, &row.Tokens, &row.Credits, &row.CostUSD); err != nil {
			warnRowSkip("usage trend-series", err)
			continue
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// foldUsageTrendRows 把单扫出的 (model,bucket) 行按窗口总量取 top-N，其余聚合为
// '__others__'；模型集合已定时 WHERE 已收敛到选中模型，直接透传不折叠。
func foldUsageTrendRows(rows []usageTrendRow, top int, modelFiltered bool) []usageTrendRow {
	if modelFiltered || len(rows) == 0 {
		return rows
	}
	totals := map[string]int64{}
	for _, row := range rows {
		totals[row.Model] += row.Requests
	}
	type modelTotal struct {
		model string
		reqs  int64
	}
	ranked := make([]modelTotal, 0, len(totals))
	for m, reqs := range totals {
		ranked = append(ranked, modelTotal{m, reqs})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].reqs > ranked[j].reqs })
	if len(ranked) <= top {
		return rows
	}
	picked := make(map[string]bool, top)
	for i := 0; i < top && i < len(ranked); i++ {
		picked[ranked[i].model] = true
	}
	// 未入选模型的行改写为 '__others__'（保留桶与指标），pivot 时自然聚成一条线。
	out := make([]usageTrendRow, 0, len(rows))
	for _, row := range rows {
		if !picked[row.Model] {
			row.Model = usageTrendOthersKey
		}
		out = append(out, row)
	}
	return out
}

// pivotUsageTrendRows 把 (model, bucket) 行透视为每模型一条序列，保持桶序。
// 折叠产生的 '__others__' 行在此自然聚成一条序列。
func pivotUsageTrendRows(rows []usageTrendRow) []usageTrendSeries {
	byModel := map[string]*usageTrendSeries{}
	var order []string
	for _, row := range rows {
		s, ok := byModel[row.Model]
		if !ok {
			s = &usageTrendSeries{Model: row.Model}
			byModel[row.Model] = s
			order = append(order, row.Model)
		}
		s.Points = append(s.Points, usageTrendPoint{
			Bucket:   row.Bucket.UTC().Format(time.RFC3339),
			Requests: row.Requests,
			Tokens:   row.Tokens,
			Credits:  row.Credits,
			CostUSD:  row.CostUSD,
		})
		s.TotalRequests += row.Requests
		s.TotalTokens += row.Tokens
		s.TotalCredits += row.Credits
		s.TotalCostUSD += row.CostUSD
	}
	series := make([]usageTrendSeries, 0, len(order))
	for _, model := range order {
		series = append(series, *byModel[model])
	}
	return series
}

func (h *Handler) queryUsageTrendModelsRollup(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendModelEntry, error) {
	where := "m.dim_type = 'model' AND m.bucket >= $1 AND m.bucket < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND m.tenant_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere("m.dim_key", f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}
	rows, err := h.db.Query(ctx, `
		SELECT m.dim_key,
			COALESCE(SUM(m.requests), 0),
			COALESCE(SUM(m.total_tokens), 0),
			COALESCE(SUM(m.credits_charged), 0),
			COALESCE(SUM(m.cost_usd), 0)
		FROM request_stats_dim_minute m
		WHERE `+where+`
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 100
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageTrendModelEntries(rows)
}

func (h *Handler) queryUsageTrendModelsDetail(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendModelEntry, error) {
	query, args := usageTrendDetailModelsSQL(tr, f)
	rows, err := h.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageTrendModelEntries(rows)
}

// usageTrendDetailModelsSQL 拼明细档的模型清单 SQL（同样是**具名片段**拼装；
// 它的位置参数同样错过一次位 ⇒ 与序列 SQL 是同一个病根）。
func usageTrendDetailModelsSQL(tr boardTimeRange, f usageTrendFilters) (string, []any) {
	creditsExpr := maas.RequestLogCreditsSQL("r", f.tenantID == "" || f.tenantID == "default")
	where := "r.request_status IN ('success', 'failure', 'rate_limited') AND r.ts >= $1 AND r.ts < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += " AND r.tenant_id = $3"
	}
	if f.providerID > 0 {
		args = append(args, f.providerID)
		where += fmt.Sprintf(" AND r.provider_id = $%d", len(args))
	}
	if f.apiKeyID > 0 {
		args = append(args, f.apiKeyID)
		where += fmt.Sprintf(" AND r.api_key_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere(usageTrendModelExpr, f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}

	modelExpr := usageTrendModelExpr
	fromExpr := dbpkg.SessionFamilyTurnsSourceSQL()

	query := fmt.Sprintf(`
		SELECT %s,
			COUNT(*),
			COALESCE(SUM(COALESCE(r.total_tokens, 0)), 0),
			COALESCE(SUM(%s), 0),
			COALESCE(SUM(COALESCE(r.cost_usd, 0)), 0)
		FROM %s r
		WHERE %s
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 100
	`, modelExpr, creditsExpr, fromExpr, where)
	return query, args
}

func scanUsageTrendModelEntries(rows pgx.Rows) ([]usageTrendModelEntry, error) {
	var out []usageTrendModelEntry
	for rows.Next() {
		var e usageTrendModelEntry
		if err := rows.Scan(&e.Model, &e.Requests, &e.Tokens, &e.Credits, &e.CostUSD); err != nil {
			warnRowSkip("usage trend-models", err)
			continue
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
