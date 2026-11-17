package maas

// pricing_suggestion_data.go —— 定价建议的数据取数与「一键执行」（2026-11-17）。
//
// 与 pricing_suggestion.go 同轮落地：
//   · 取数：行情事实（970 按平台代表价 + 940 综合地板）+ 我方现价；
//   · 执行：把建议价写进 model_credit_rates（manual 覆盖）⇒ 期次账自动留痕；
//   · 批次：pricing_suggestion_batches（973 迁移）记「谁、何时、什么策略、
//     改了几个、跳过几个」⇒ 审计面唯一（期次账 = 模型级，批次表 = 批次级）。
//
// ★ 执行的红线：
//   ① 必须 confirm=true（防误触）；② 必须 superAdmin（admin 层闸）；
//   ③ 只写**零售价**（model_credit_rates），不碰供应商实付价、不碰路由权重；
//   ④ 每次写入都经 upsertModelRateSource ⇒ model_price_versions 自动留期次；
//   ⑤ 期次 note 带批次 id ⇒ 可从期次反查批次，从批次反查模型。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// pricingSuggestionCurrentPrices —— 我方现价（credits）。
type pricingSuggestionCurrentPrices struct {
	In  *int64
	Out *int64
}

// pricingSuggestionScope —— 定价建议的作用域（canonical 名列表）。
//
// ★ 口径：
//   · 指定了 canonical_ids ⇒ 只看这些模型（按 id 解析成名字，解析不到就跳过）；
//   · 未指定 ⇒ 取该 dimension 下**有 canonical_id 的**行情模型
//     （canonical_id 非空 = 已与网关身份对齐 ⇒ 才是「可比」的）。
func (s *Service) pricingSuggestionScope(ctx context.Context, req PricingSuggestionRequest, dim string) []string {
	if len(req.CanonicalIDs) > 0 {
		rows, err := s.pool.Query(ctx,
			`SELECT canonical_name FROM public.models_canonical WHERE id = ANY($1)`, req.CanonicalIDs)
		if err != nil {
			return nil
		}
		defer rows.Close()
		out := make([]string, 0, len(req.CanonicalIDs))
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err == nil {
				out = append(out, n)
			}
		}
		return out
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT m.canonical_name
		  FROM public.market_price_reference m
		 WHERE m.dimension = $1 AND m.canonical_id IS NOT NULL
		 ORDER BY 1`, dim)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]string, 0, 512)
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// pricingSuggestionMarketSQLv2 —— 行情事实聚合（按策略一次算齐）。
const pricingSuggestionMarketSQLv2 = `
WITH rep AS (
    SELECT DISTINCT ON (q.canonical_name, q.platform_id)
           q.canonical_name, q.platform_id, q.is_vendor,
           q.cny_in_per_1m, q.cny_out_per_1m, q.run_date
      FROM public.market_price_platform_quotes q
     WHERE q.dimension = $1
       AND (q.cny_in_per_1m IS NOT NULL OR q.cny_out_per_1m IS NOT NULL)
     ORDER BY q.canonical_name, q.platform_id, (q.tier = '') DESC,
              q.cny_out_per_1m NULLS LAST
)
SELECT r.canonical_name,
       mpr.canonical_id,
       count(DISTINCT r.platform_id) FILTER (WHERE NOT r.is_vendor) AS sample_count,
       avg(r.cny_in_per_1m)  FILTER (WHERE NOT r.is_vendor) AS avg_in,
       avg(r.cny_out_per_1m) FILTER (WHERE NOT r.is_vendor) AS avg_out,
       min(r.cny_in_per_1m)  FILTER (WHERE NOT r.is_vendor) AS min_in,
       min(r.cny_out_per_1m) FILTER (WHERE NOT r.is_vendor) AS min_out,
       max(r.cny_in_per_1m)  FILTER (WHERE r.platform_id = $2) AS plat_in,
       max(r.cny_out_per_1m) FILTER (WHERE r.platform_id = $2) AS plat_out,
       max(r.run_date) AS run_date,
       mpr.market_min_cny_in_per_1m AS floor_in,
       mpr.market_min_cny_out_per_1m AS floor_out
  FROM rep r
  LEFT JOIN public.market_price_reference mpr
         ON mpr.dimension = $1 AND lower(mpr.canonical_name) = lower(r.canonical_name)
 WHERE ($3 = '' OR r.canonical_name = ANY($4))
 GROUP BY r.canonical_name, mpr.canonical_id,
          mpr.market_min_cny_in_per_1m, mpr.market_min_cny_out_per_1m
`

// loadPricingSuggestionMarket 取行情事实。
func (s *Service) loadPricingSuggestionMarket(ctx context.Context, dim string, req PricingSuggestionRequest, names []string) (map[string]pricingSuggestionMarketRow, error) {
	out := map[string]pricingSuggestionMarketRow{}
	scope := ""
	if len(names) > 0 {
		scope = "scoped"
	}
	rows, err := s.pool.Query(ctx, pricingSuggestionMarketSQLv2, dim, req.PlatformID, scope, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r pricingSuggestionMarketRow
		var cid *int64
		var avgIn, avgOut, minIn, minOut, platIn, platOut *float64
		var floorIn, floorOut *float64
		var runDate *time.Time
		if err := rows.Scan(&r.CanonicalName, &cid, &r.SampleCount,
			&avgIn, &avgOut, &minIn, &minOut, &platIn, &platOut,
			&runDate, &floorIn, &floorOut); err != nil {
			return nil, err
		}
		r.CanonicalID = cid
		r.AvgCNYIn, r.AvgCNYOut = avgIn, avgOut
		r.MinCNYIn, r.MinCNYOut = minIn, minOut
		r.PlatformCNYIn, r.PlatformCNYOut = platIn, platOut
		r.FloorCNYIn, r.FloorCNYOut = floorIn, floorOut
		if runDate != nil {
			r.RunDate = runDate.Format("2006-01-02")
		}
		out[r.CanonicalName] = r
	}
	return out, rows.Err()
}

// loadPricingSuggestionCurrent 取我方现价（credits，manual 优先 → 全局基准）。
func (s *Service) loadPricingSuggestionCurrent(ctx context.Context, names []string) (map[string]pricingSuggestionCurrentPrices, error) {
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	global := globalEffective(settings)
	out := map[string]pricingSuggestionCurrentPrices{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT mc.canonical_name,
		       mcr.credits_per_1m_in, mcr.credits_per_1m_out,
		       COALESCE(mcr.manual_in, FALSE), COALESCE(mcr.manual_out, FALSE)
		  FROM public.models_canonical mc
		  LEFT JOIN public.model_credit_rates mcr ON mcr.canonical_id = mc.id
		 WHERE mc.canonical_name = ANY($1)`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var in, outV *int64
		var manualIn, manualOut bool
		if err := rows.Scan(&name, &in, &outV, &manualIn, &manualOut); err != nil {
			return nil, err
		}
		out[name] = pricingSuggestionCurrentPrices{
			In:  pickCreditRateFlagged(manualIn, in, global.In),
			Out: pickCreditRateFlagged(manualOut, outV, global.Out),
		}
	}
	return out, rows.Err()
}

// ApplyPricingSuggestions —— 「一键执行」：把建议价写进本地零售价。
//
// ★ 幂等：同一建议重复执行不会产生重复期次（syncModelPriceVersion 对
//   「与现行期相同」的写入是 no-op）。
// ★ 留痕：① model_credit_rates（manual 覆盖）② model_price_versions 期次
//   （source=market_suggestion + note 带依据与批次 id）③ pricing_suggestion_batches。
func (s *Service) ApplyPricingSuggestions(ctx context.Context, req PricingSuggestionRequest) (PricingSuggestionResult, error) {
	out := PricingSuggestionResult{}
	if !s.Enabled() {
		return out, errors.New("maas service disabled")
	}
	if !req.Confirm {
		return out, errors.New("执行定价建议必须显式确认（confirm=true）")
	}
	if err := validatePricingStrategy(req); err != nil {
		return out, err
	}
	// 预览（同一份计算口径 ⇒ 预览与执行不会漂移）
	preview, err := s.SuggestPricing(ctx, req)
	if err != nil {
		return out, err
	}
	out = preview

	// 批次留痕（先落批次头，再逐个写价 ⇒ 批次 id 能进每行的 note）
	batchID, err := s.insertPricingSuggestionBatch(ctx, req, len(preview.Items), true)
	if err != nil {
		return out, fmt.Errorf("写批次留痕失败：%w", err)
	}
	out.BatchID = batchID

	applied := 0
	skipped := 0
	detail := make([]map[string]any, 0, len(preview.Items))
	for _, it := range preview.Items {
		rec := map[string]any{
			"canonical_name": it.CanonicalName,
			"applicable":    it.Applicable,
		}
		if !it.Applicable || it.CanonicalID <= 0 {
			skipped++
			rec["skipped_reason"] = it.SkipReason
			detail = append(detail, rec)
			continue
		}
		upsert := ModelRateUpsert{
			CreditsPer1MIn:  derefInt64(it.SuggestedCreditsIn),
			CreditsPer1MOut: derefInt64(it.SuggestedCreditsOut),
			ManualIn:        it.SuggestedCreditsIn != nil,
			ManualOut:       it.SuggestedCreditsOut != nil,
		}
		note := pricingSuggestionNote(req, batchID, it)
		if err := s.upsertModelRateSource(ctx, int(it.CanonicalID), upsert,
			RateSourceMarketSuggestion, note); err != nil {
			// 单个失败不吞：记进批次 detail，继续处理其余模型。
			slog.Warn("pricing suggestion: apply failed",
				"canonical_id", it.CanonicalID, "error", err)
			rec["error"] = err.Error()
			skipped++
			detail = append(detail, rec)
			continue
		}
		applied++
		rec["applied"] = true
		rec["credits_in"] = it.SuggestedCreditsIn
		rec["credits_out"] = it.SuggestedCreditsOut
		rec["delta_pct_in"] = it.DeltaPctIn
		rec["delta_pct_out"] = it.DeltaPctOut
		detail = append(detail, rec)
	}
	if err := s.updatePricingSuggestionBatch(ctx, batchID, applied, skipped, detail); err != nil {
		slog.Warn("pricing suggestion: batch tail update failed", "batch_id", batchID, "error", err)
	}
	out.Summary.Applied = applied
	out.Summary.Skipped = skipped
	out.Warnings = append(out.Warnings,
		fmt.Sprintf("已执行：写入 %d 个模型，跳过 %d 个（批次 #%d，可从价格变更历史按批次反查）",
			applied, skipped, batchID))
	return out, nil
}

// pricingSuggestionNote 期次 note：策略 + 依据 + 批次 id（审计可反查）。
func pricingSuggestionNote(req PricingSuggestionRequest, batchID int64, it PricingSuggestionRow) string {
	var b strings.Builder
	b.WriteString("market_suggestion")
	b.WriteString("|strategy=")
	b.WriteString(req.Strategy)
	if req.PlatformID != "" {
		b.WriteString("|platform=")
		b.WriteString(req.PlatformID)
	}
	if req.MarkupPct != nil {
		b.WriteString(fmt.Sprintf("|markup=%.2f%%", *req.MarkupPct))
	}
	b.WriteString(fmt.Sprintf("|batch=%d", batchID))
	b.WriteString(fmt.Sprintf("|basis=%s", it.Basis))
	b.WriteString(fmt.Sprintf("|platforms=%d", it.BasisPlatforms))
	if it.RunDate != "" {
		b.WriteString("|run_date=")
		b.WriteString(it.RunDate)
	}
	return b.String()
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// insertPricingSuggestionBatch 写批次留痕头。
func (s *Service) insertPricingSuggestionBatch(ctx context.Context, req PricingSuggestionRequest, requested int, dryRun bool) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO public.pricing_suggestion_batches
			(strategy, platform_id, dimension, markup_pct, operator, dry_run, requested)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		req.Strategy, req.PlatformID, req.Dimension, req.MarkupPct,
		req.Operator, dryRun, requested).Scan(&id)
	return id, err
}

// updatePricingSuggestionBatch 回填批次尾（应用数 / 跳过数 / 逐模型明细）。
func (s *Service) updatePricingSuggestionBatch(ctx context.Context, batchID int64, applied, skipped int, detail []map[string]any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE public.pricing_suggestion_batches
		   SET applied = $2, skipped = $3, detail = $4::jsonb
		 WHERE id = $1`, batchID, applied, skipped, string(payload))
	return err
}

// ListPricingSuggestionBatches —— 批次留痕读面（审计分析）。
func (s *Service) ListPricingSuggestionBatches(ctx context.Context, limit int) ([]PricingSuggestionBatchRow, error) {
	if !s.Enabled() {
		return nil, errors.New("maas service disabled")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, created_at, strategy, platform_id, dimension, markup_pct,
		       operator, dry_run, requested, applied, skipped, detail
		  FROM public.pricing_suggestion_batches
		 ORDER BY id DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PricingSuggestionBatchRow, 0, 16)
	for rows.Next() {
		var r PricingSuggestionBatchRow
		var created time.Time
		var detail []byte
		if err := rows.Scan(&r.ID, &created, &r.Strategy, &r.PlatformID, &r.Dimension,
			&r.MarkupPct, &r.Operator, &r.DryRun, &r.Requested, &r.Applied, &r.Skipped,
			&detail); err != nil {
			return nil, err
		}
		r.CreatedAt = created.UTC().Format(time.RFC3339)
		if len(detail) > 0 {
			_ = json.Unmarshal(detail, &r.Detail)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
