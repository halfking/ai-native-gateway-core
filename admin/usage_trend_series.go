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
// 数据面三档（2026-10-02 实测本地库后定形；模型名口径统一 outbound 优先、
// client_model 兜底、'__unknown__' 垫底，与 rollup writer 同式）：
//   - dim 档（无 provider/api_key 过滤）：request_stats_dim_minute
//     dim_type='model'，单表索引扫描（今天窗 22ms / 7d 1.2s 实测）。
//   - provider 档（仅 provider 过滤）：request_stats_minute（自带 provider_id ×
//     canonical_id）JOIN provider_models 取展示名。不扫明细日志。
//   - detail 档（带 api_key 过滤）：request_logs_with_current_month_without_customer_id
     // 唯一含 api_key_id 的读面；无 api_key 索引的旧分区上长窗会慢，前端对超时
//     给出「缩短时间范围」提示。
//
// 折叠：三档统一「单次扫描出 (model,bucket) 组 → Go 侧按窗口总量取 top-N、
// 其余聚合 '__others__'」。不做 SQL 内 CTE 折叠——detail 档双扫实测 75s，
// 单扫砍半；dim 档模型基数（本地 649）让 Go 折叠零成本。
package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/maas"
)

const usageTrendOthersKey = "__others__"

// usageTrendMaxModels 是 model 多选的上限：多选已定时三档读面都不折叠长尾，
// 每个选中模型各成一条线，无上限会让序列数失控。与 top 上限 20 对齐。
const usageTrendMaxModels = 20

// 与 bg/stats_minute_rollup.go 的 model 维度同式；detail 档 SQL 中多处引用必须一致。
const usageTrendModelExpr = `COALESCE(NULLIF(r.outbound_model, ''), NULLIF(r.client_model, ''), '__unknown__')`

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

// usageTrendSource 挑数据档：api_key 过滤只有明细读面有该列；provider 过滤走
// minute 汇总（自带 provider_id×canonical_id），避免明细分区慢扫。
func usageTrendSource(f usageTrendFilters) string {
	switch {
	case f.apiKeyID > 0:
		return "request_logs_with_current_month"
	case f.providerID > 0:
		return "request_stats_minute"
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
	case "request_stats_minute":
		rows, err = h.queryUsageTrendProvider(ctx, tr, f)
	default:
		rows, err = h.queryUsageTrendRollup(ctx, tr, f)
	}
	if err != nil {
		if IsMissingRelationError(err) {
			// 全新安装可能还没有 rollup 表/当月视图 —— 返回空序列而不是 500。
			writeJSON(w, http.StatusOK, usageTrendSeriesResponse{
				Start:         tr.Start.UTC().Format(time.RFC3339),
				End:           tr.End.UTC().Format(time.RFC3339),
				BucketMinutes: tr.trendBucketMinutes(),
				Top:           f.top,
				Source:        source,
				Series:        []usageTrendSeries{},
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
	case "request_stats_minute":
		models, err = h.queryUsageTrendModelsProvider(ctx, tr, f)
	default:
		models, err = h.queryUsageTrendModelsRollup(ctx, tr, f)
	}
	if err != nil {
		if IsMissingRelationError(err) {
			writeJSON(w, http.StatusOK, usageTrendModelsResponse{
				Start:  tr.Start.UTC().Format(time.RFC3339),
				End:    tr.End.UTC().Format(time.RFC3339),
				Source: source,
				Models: []usageTrendModelEntry{},
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

// providerModelsNameJoin 把 canonical_id 映射成展示名（同 provider 下每 canonical
// 取一条，优先 outbound_model_name）。request_stats_minute 档专用。
const providerModelsNameJoin = `
		LEFT JOIN (
			SELECT DISTINCT ON (provider_id, canonical_id)
				provider_id, canonical_id,
				COALESCE(NULLIF(outbound_model_name, ''), raw_model_name) AS model_name
			FROM provider_models
			ORDER BY provider_id, canonical_id, id
		) pm ON pm.provider_id = m.provider_id AND pm.canonical_id = m.canonical_id`

const providerModelsNameExpr = `COALESCE(NULLIF(pm.model_name, ''), 'model#' || m.canonical_id::text)`

// queryUsageTrendProvider 读 request_stats_minute + provider_models 展示名（仅 provider 过滤）。
func (h *Handler) queryUsageTrendProvider(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendRow, error) {
	where := "m.bucket >= $1 AND m.bucket < $2 AND m.provider_id = $3"
	args := []any{tr.Start, tr.End, f.providerID}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND m.tenant_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere(providerModelsNameExpr, f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}
	query := fmt.Sprintf(`
		SELECT %s AS model,
			%s,
			COALESCE(SUM(m.requests), 0),
			COALESCE(SUM(m.total_tokens), 0),
			COALESCE(SUM(m.credits_charged), 0),
			COALESCE(SUM(m.cost_usd), 0)
		FROM request_stats_minute m
		%s
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 2, 1
	`, providerModelsNameExpr, sqlTrendBucket("m.bucket", tr.trendBucketMinutes()), providerModelsNameJoin, where)
	return h.scanUsageTrendRows(ctx, query, args)
}

// queryUsageTrendDetail 读 request_logs 视图族（带 api_key_id 过滤；provider 过滤
// 可同时下推）。credits 口径与 rollup writer 一致（COALESCE(credits_charged, 估算)）。
func (h *Handler) queryUsageTrendDetail(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendRow, error) {
	creditsExpr := maas.RequestLogCreditsSQL("r", f.tenantID == "" || f.tenantID == "default")
	where := "r.request_status IN ('success', 'failure', 'rate_limited') AND r.ts >= $1 AND r.ts < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND r.tenant_id = $%d", len(args))
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
	query := fmt.Sprintf(`
		SELECT %s AS model,
			%s,
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(COALESCE(r.total_tokens, 0)), 0),
			COALESCE(SUM(%s), 0),
			COALESCE(SUM(COALESCE(r.cost_usd, 0)), 0)
		FROM request_logs_with_current_month_without_customer_id r
		WHERE %s
		GROUP BY 1, 2
		ORDER BY 2, 1
	`, usageTrendModelExpr, sqlTrendBucket("r.ts", tr.trendBucketMinutes()), creditsExpr, where)
	return h.scanUsageTrendRows(ctx, query, args)
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

func (h *Handler) queryUsageTrendModelsProvider(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendModelEntry, error) {
	where := "m.bucket >= $1 AND m.bucket < $2 AND m.provider_id = $3"
	args := []any{tr.Start, tr.End, f.providerID}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND m.tenant_id = $%d", len(args))
	}
	if clause, nextArgs := usageTrendModelsWhere(providerModelsNameExpr, f.models, args); clause != "" {
		where, args = where+clause, nextArgs
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COALESCE(SUM(m.requests), 0),
			COALESCE(SUM(m.total_tokens), 0),
			COALESCE(SUM(m.credits_charged), 0),
			COALESCE(SUM(m.cost_usd), 0)
		FROM request_stats_minute m
		%s
		WHERE %s
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 100
	`, providerModelsNameExpr, providerModelsNameJoin, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageTrendModelEntries(rows)
}

func (h *Handler) queryUsageTrendModelsDetail(ctx context.Context, tr boardTimeRange, f usageTrendFilters) ([]usageTrendModelEntry, error) {
	creditsExpr := maas.RequestLogCreditsSQL("r", f.tenantID == "" || f.tenantID == "default")
	where := "r.request_status IN ('success', 'failure', 'rate_limited') AND r.ts >= $1 AND r.ts < $2"
	args := []any{tr.Start, tr.End}
	if f.tenantID != "" {
		args = append(args, f.tenantID)
		where += fmt.Sprintf(" AND r.tenant_id = $%d", len(args))
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
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s,
			COUNT(*),
			COALESCE(SUM(COALESCE(r.total_tokens, 0)), 0),
			COALESCE(SUM(%s), 0),
			COALESCE(SUM(COALESCE(r.cost_usd, 0)), 0)
		FROM request_logs_with_current_month_without_customer_id r
		WHERE %s
		GROUP BY 1
		ORDER BY 2 DESC
		LIMIT 100
	`, usageTrendModelExpr, creditsExpr, where), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsageTrendModelEntries(rows)
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
