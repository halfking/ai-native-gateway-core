// report.go —— 对账报表的区间汇总读面：只读 report_snapshots 日快照行，
// 按任意区间（日 / 周 / 月 / 自定义）二次汇总成总览 + 按供应商 / 按租户 /
// 按人员 / 按模型 / 按天多组行，不回扫 usage_facts（每日凌晨聚合一次落库
// 的意义所在）。
//
// 过滤口径一致性：总计与按天序列始终从「与过滤条件匹配的最细 scope」
// 折叠——无过滤时 provider 视角用 daily_total（全流量单行）、internal
// 视角折叠 internal_tenant；按模型过滤时两边都改从模型粒度行折叠，保证
// 总计 = Σ分组行（否则过滤后总计仍是无过滤值，报表自相矛盾）。
package reportrollup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// View 视角：provider = 供应商对帐（全流量、成本口径）；internal =
// 内部对帐（business 流量、积分/内部价口径）。
type View string

const (
	ViewProvider View = "provider"
	ViewInternal View = "internal"
)

// Valid 校验视图字符串。
func (v View) Valid() bool { return v == ViewProvider || v == ViewInternal }

// Totals 一组聚合指标。ErrorCount = RequestCount - SuccessCount（终态
// success 之外一律计失败，含 rate_limited）。
type Totals struct {
	RequestCount       int64   `json:"request_count"`
	SuccessCount       int64   `json:"success_count"`
	ErrorCount         int64   `json:"error_count"`
	ErrorRate          float64 `json:"error_rate"`
	InputTokens        int64   `json:"input_tokens"`
	OutputTokens       int64   `json:"output_tokens"`
	CacheReadTokens    int64   `json:"cache_read_tokens"`
	CacheWriteTokens   int64   `json:"cache_write_tokens"`
	TotalTokens        int64   `json:"total_tokens"`
	EstimatedCostCents int64   `json:"estimated_cost_cents"`
	Currency           string  `json:"currency"`
	CreditsCharged     int64   `json:"credits_charged"`
	// InternalCostCents 内部金额（分）= Σ(credits × 快照冻结的
	// cents_per_credit)；仅 internal 视角有值。
	InternalCostCents float64 `json:"internal_cost_cents"`
	InternalCurrency  string  `json:"internal_currency,omitempty"`
	// CacheHitRatio = Σcache_read / (Σinput + Σcache_read)；分母 0 → nil。
	CacheHitRatio *float64 `json:"cache_hit_ratio"`
	// LatencyP50/P95 跨天折叠时按成功请求加权平均（分位数不可折叠，
	// 近似值仅用于报表展示）。
	LatencyP50Ms float64 `json:"latency_p50_ms"`
	LatencyP95Ms float64 `json:"latency_p95_ms"`
}

// ProviderRow 供应商汇总行。
type ProviderRow struct {
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	// QualityScore 供应商综合评分（0-100，一位小数）：成功率 × 时效因子
	// （P95 ≤ 5s 不扣分，超时线性惩罚）。读面现算不落快照——公式演进
	// 可重算历史，不违背快照冻结语义（2026-09-26 审计轮，goal #2 评分项）。
	QualityScore   float64          `json:"quality_score"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
}

// ModelRow 模型汇总行（provider 视角 = provider×出站模型；internal 视角
// = 业务流量按模型全租户合并）。
type ModelRow struct {
	ProviderID     *int64           `json:"provider_id,omitempty"`
	ProviderName   string           `json:"provider_name,omitempty"`
	RawModelName   string           `json:"raw_model_name"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
}

// TenantRow 租户汇总行。
type TenantRow struct {
	TenantID       string           `json:"tenant_id"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
}

// PersonRow 人员汇总行（person = end_user_id，缺失回落 person:hash）。
type PersonRow struct {
	TenantID       string           `json:"tenant_id"`
	Person         string           `json:"person"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
}

// DayRow 单日汇总行。
type DayRow struct {
	Date   string `json:"date"`
	Totals Totals `json:"totals"`
}

// RangeReport 区间汇总结果。
type RangeReport struct {
	Start          time.Time        `json:"start"`
	End            time.Time        `json:"end"`
	View           View             `json:"view"`
	Totals         Totals           `json:"totals"`
	ErrorBreakdown map[string]int64 `json:"error_breakdown"`
	Days           []DayRow         `json:"days"`
	Providers      []ProviderRow    `json:"providers,omitempty"`
	Models         []ModelRow       `json:"models"`
	Tenants        []TenantRow      `json:"tenants,omitempty"`
	Persons        []PersonRow      `json:"persons,omitempty"`
	// SnapshotDates 已落快照的日期集合（缺失日期 = worker 未跑或当日
	// 无流量，供前端标注覆盖率）。
	SnapshotDates []string `json:"snapshot_dates"`
}

// RangeFilter 可选过滤维度（零值 = 不过滤）。
type RangeFilter struct {
	ProviderID *int64
	TenantID   string
	Model      string
}

func (f RangeFilter) empty() bool {
	return f.ProviderID == nil && f.TenantID == "" && f.Model == ""
}

// acc 聚合累加器：跨天快照行 → 单组指标。
type acc struct {
	tot Totals
	br  map[string]int64
	// successW 参与 latency 加权的成功请求权重（仅 latency>0 的行计入）。
	successW int64
}

func (a *acc) add(s Snapshot) {
	t := &a.tot
	t.RequestCount += s.RequestCount
	t.SuccessCount += s.SuccessCount
	t.InputTokens += s.InputTokens
	t.OutputTokens += s.OutputTokens
	t.CacheReadTokens += s.CacheReadTokens
	t.CacheWriteTokens += s.CacheWriteTokens
	t.CreditsCharged += s.CreditsCharged
	t.EstimatedCostCents += s.EstimatedCostCents
	if s.Currency != "" {
		t.Currency = s.Currency
	}
	if cents := internalCents(s); cents > 0 {
		t.InternalCostCents += cents
		t.InternalCurrency = "CNY"
	}
	if s.LatencyP50Ms > 0 || s.LatencyP95Ms > 0 {
		w := s.SuccessCount
		if w == 0 {
			w = 1
		}
		a.successW += w
		t.LatencyP50Ms += float64(s.LatencyP50Ms) * float64(w)
		t.LatencyP95Ms += float64(s.LatencyP95Ms) * float64(w)
	}
	a.br = mergeBreakdown(a.br, s.ErrorKindBreakdown)
}

// addAcc 把另一个累加器并入（内部总计折叠 internal_tenant 分组用）。
func (a *acc) addAcc(b *acc) {
	if b == nil {
		return
	}
	a.tot.RequestCount += b.tot.RequestCount
	a.tot.SuccessCount += b.tot.SuccessCount
	a.tot.InputTokens += b.tot.InputTokens
	a.tot.OutputTokens += b.tot.OutputTokens
	a.tot.CacheReadTokens += b.tot.CacheReadTokens
	a.tot.CacheWriteTokens += b.tot.CacheWriteTokens
	a.tot.CreditsCharged += b.tot.CreditsCharged
	a.tot.EstimatedCostCents += b.tot.EstimatedCostCents
	a.tot.InternalCostCents += b.tot.InternalCostCents
	if b.tot.Currency != "" {
		a.tot.Currency = b.tot.Currency
	}
	if b.tot.InternalCurrency != "" {
		a.tot.InternalCurrency = b.tot.InternalCurrency
	}
	a.successW += b.successW
	a.tot.LatencyP50Ms += b.tot.LatencyP50Ms
	a.tot.LatencyP95Ms += b.tot.LatencyP95Ms
	a.br = mergeBreakdown(a.br, b.br)
}

// finalize 补齐派生列；每个累加器只允许调用一次（分位加权除法不可重复）。
func (a *acc) finalize() Totals {
	t := a.tot
	t.ErrorCount = t.RequestCount - t.SuccessCount
	if t.RequestCount > 0 {
		t.ErrorRate = float64(t.ErrorCount) / float64(t.RequestCount)
	}
	t.TotalTokens = t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheWriteTokens
	denom := t.InputTokens + t.CacheReadTokens
	if denom > 0 {
		r := float64(t.CacheReadTokens) / float64(denom)
		t.CacheHitRatio = &r
	}
	switch {
	case a.successW > 0:
		t.LatencyP50Ms /= float64(a.successW)
		t.LatencyP95Ms /= float64(a.successW)
	default:
		t.LatencyP50Ms, t.LatencyP95Ms = 0, 0
	}
	return t
}

// internalCents 读快照冻结的 cents_per_credit 并折算该行内部金额（分）。
// 无冻结价（maas 缺表时的空快照）返回 0。
func internalCents(s Snapshot) float64 {
	if s.CreditsCharged == 0 {
		return 0
	}
	switch v := s.PriceSnapshot["cents_per_credit"].(type) {
	case float64:
		return float64(s.CreditsCharged) * v
	case int64:
		return float64(s.CreditsCharged) * float64(v)
	default:
		return 0
	}
}

// QualityLatencyBaselineMs 评分时效基准：P95 不超过该值不扣分，超出按
// baseline/p95 线性折减（10s ≈ 半分）。展示口径，非路由决策依据。
const QualityLatencyBaselineMs = 5000.0

// ProviderQualityScore 供应商综合评分 = 100 × 成功率 × 时效因子
// （min(1, baseline/p95)）；无成功延迟数据（p95=0）不做时效惩罚，
// 无请求行返回 0。纯函数，供读面与测试复用。
func ProviderQualityScore(t Totals) float64 {
	if t.RequestCount <= 0 {
		return 0
	}
	score := 100 * float64(t.SuccessCount) / float64(t.RequestCount)
	if t.LatencyP95Ms > 0 {
		score *= min(1, QualityLatencyBaselineMs/float64(t.LatencyP95Ms))
	}
	return math.Round(score*10) / 10
}

// BuildRangeReport 汇总 [start, end]（UTC 日闭区间）内 view 视角的日快照。
// 周报/月报 = 同一入口传对应区间。providerNames 供应商 id→名称映射（可
// nil，名称列留空）。
func BuildRangeReport(ctx context.Context, q Querier, start, end time.Time, view View, filter RangeFilter, providerNames map[int64]string) (*RangeReport, error) {
	if !view.Valid() {
		return nil, fmt.Errorf("unknown view %q", view)
	}
	day0 := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	end0 := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	if end0.Before(day0) {
		return nil, fmt.Errorf("end date before start date")
	}

	scopes, err := viewScopes(view)
	if err != nil {
		return nil, err
	}
	snaps, err := loadSnapshots(ctx, q, day0, end0, scopes, filter)
	if err != nil {
		return nil, err
	}

	rep := &RangeReport{
		Start:          day0,
		End:            end0,
		View:           view,
		ErrorBreakdown: map[string]int64{},
		SnapshotDates:  []string{},
	}

	type keyed struct {
		meta Snapshot
		a    *acc
	}

	// ---- 模型粒度行（双视角的 Models 分组；质量 sheet 数据源）----
	modelsByKey := map[string]*keyed{}
	// ---- 总计/按天用：与过滤匹配的最细 scope 行，按日分组 ----
	daysByDate := map[string]*acc{}
	// ---- 供应商/租户分组（model 过滤时从模型行二次分组）----
	provByKey := map[int64]*acc{}
	tenantByKey := map[string]*acc{}
	// ---- 人员分组（internal 视角）----
	personsByKey := map[string]*keyed{}

	dates := map[string]bool{}
	for _, s := range snaps {
		dates[s.ReportDate.Format("2006-01-02")] = true
		switch s.Scope {
		case ScopeDailyByModel:
			// 防御性复检：SQL 已按 raw_model_name 过滤（loadSnapshots），
			// 此处兜底保证「过滤开启时模型行只含匹配行」不依赖 SQL 正确性。
			if filter.Model != "" && s.RawModelName != filter.Model {
				continue
			}
			key := fmt.Sprintf("%d\x00%s", derefI(s.ProviderID), s.RawModelName)
			k := modelsByKey[key]
			if k == nil {
				k = &keyed{meta: s, a: &acc{}}
				modelsByKey[key] = k
			}
			k.a.add(s)
			if filter.Model != "" {
				addToDay(daysByDate, s)
				if s.ProviderID != nil {
					groupAcc(provByKey, *s.ProviderID).add(s)
				}
			}
		case ScopeInternalModel:
			key := "\x00" + s.RawModelName
			k := modelsByKey[key]
			if k == nil {
				k = &keyed{meta: s, a: &acc{}}
				modelsByKey[key] = k
			}
			k.a.add(s)
			if filter.Model != "" {
				addToDay(daysByDate, s)
				if s.TenantID != nil {
					groupAccStr(tenantByKey, *s.TenantID).add(s)
				}
			}
		case ScopeDailyTotal:
			if filter.empty() {
				addToDay(daysByDate, s)
			}
		case ScopeDailyByProvider:
			// 模型过滤时供应商分组改由模型行二次聚合（见 daily_by_model
			// case），此处不再累加，否则同一请求双计。
			if filter.Model == "" && s.ProviderID != nil {
				groupAcc(provByKey, *s.ProviderID).add(s)
			}
			if filter.ProviderID != nil && filter.Model == "" {
				addToDay(daysByDate, s)
			}
		case ScopeInternalTenant:
			// R65：tenant 过滤存在时也可进按天序列——SQL WHERE tenant_id
			// 已收窄行集，无双计；此前仅无过滤时填充，导致
			// view=internal&tenant_id=X 的 days 恒为空。
			if filter.Model == "" {
				addToDay(daysByDate, s)
			}
			if filter.Model == "" {
				groupAccStr(tenantByKey, s.ScopeKey).add(s)
			}
		case ScopeInternalPerson:
			key := deref(s.TenantID) + "\x00" + s.ScopeKey
			k := personsByKey[key]
			if k == nil {
				k = &keyed{meta: s, a: &acc{}}
				personsByKey[key] = k
			}
			k.a.add(s)
		}
	}

	// ---- 总计 + 错误透视 ----
	total := &acc{}
	if view == ViewProvider && filter.empty() {
		// 全流量单行 scope：每日一行，全部相加。
		for _, a := range daysByDate {
			total.addAcc(a)
		}
	} else if filter.Model != "" {
		// 模型过滤：总计 = Σ 模型行。
		for _, k := range modelsByKey {
			total.addAcc(k.a)
		}
	} else if view == ViewProvider && filter.ProviderID != nil {
		if a := provByKey[*filter.ProviderID]; a != nil {
			total.addAcc(a)
		}
	} else {
		// internal 视角（含 tenant 过滤）：总计 = Σ 租户行。
		for _, a := range tenantByKey {
			total.addAcc(a)
		}
	}
	rep.Totals = total.finalize()
	rep.ErrorBreakdown = total.br
	if rep.ErrorBreakdown == nil {
		rep.ErrorBreakdown = map[string]int64{}
	}

	// ---- 按天 ----
	for date, a := range daysByDate {
		rep.Days = append(rep.Days, DayRow{Date: date, Totals: a.finalize()})
	}
	sort.Slice(rep.Days, func(i, j int) bool { return rep.Days[i].Date < rep.Days[j].Date })

	// ---- 按供应商（provider 视角）----
	if view == ViewProvider {
		for pid, a := range provByKey {
			row := ProviderRow{
				ProviderID:     pid,
				ProviderName:   providerNames[pid],
				Totals:         a.finalize(),
				ErrorBreakdown: a.br,
			}
			row.QualityScore = ProviderQualityScore(row.Totals)
			rep.Providers = append(rep.Providers, row)
		}
		sort.Slice(rep.Providers, func(i, j int) bool {
			return rep.Providers[i].Totals.RequestCount > rep.Providers[j].Totals.RequestCount
		})
	}

	// ---- 按模型 ----
	for _, k := range modelsByKey {
		row := ModelRow{RawModelName: k.meta.RawModelName, Totals: k.a.finalize(), ErrorBreakdown: k.a.br}
		if k.meta.ProviderID != nil {
			pid := *k.meta.ProviderID
			row.ProviderID = &pid
			row.ProviderName = providerNames[pid]
		}
		rep.Models = append(rep.Models, row)
	}
	sort.Slice(rep.Models, func(i, j int) bool {
		return rep.Models[i].Totals.RequestCount > rep.Models[j].Totals.RequestCount
	})

	// ---- 按租户 / 按人员（internal 视角）----
	if view == ViewInternal {
		for tid, a := range tenantByKey {
			rep.Tenants = append(rep.Tenants, TenantRow{TenantID: tid, Totals: a.finalize(), ErrorBreakdown: a.br})
		}
		sort.Slice(rep.Tenants, func(i, j int) bool {
			return rep.Tenants[i].Totals.RequestCount > rep.Tenants[j].Totals.RequestCount
		})
		for _, k := range personsByKey {
			// R65：scope_key 已编码租户（internalPersonScopeKey），展示名
			// 取分隔符后的 person 部分。
			_, personName := splitInternalPersonScopeKey(k.meta.ScopeKey)
			rep.Persons = append(rep.Persons, PersonRow{
				TenantID:       deref(k.meta.TenantID),
				Person:         personName,
				Totals:         k.a.finalize(),
				ErrorBreakdown: k.a.br,
			})
		}
		sort.Slice(rep.Persons, func(i, j int) bool {
			return rep.Persons[i].Totals.RequestCount > rep.Persons[j].Totals.RequestCount
		})
	}

	for date := range dates {
		rep.SnapshotDates = append(rep.SnapshotDates, date)
	}
	sort.Strings(rep.SnapshotDates)
	return rep, nil
}

func groupAcc(m map[int64]*acc, pid int64) *acc {
	a := m[pid]
	if a == nil {
		a = &acc{}
		m[pid] = a
	}
	return a
}

func groupAccStr(m map[string]*acc, key string) *acc {
	a := m[key]
	if a == nil {
		a = &acc{}
		m[key] = a
	}
	return a
}

func addToDay(m map[string]*acc, s Snapshot) {
	date := s.ReportDate.Format("2006-01-02")
	a := m[date]
	if a == nil {
		a = &acc{}
		m[date] = a
	}
	a.add(s)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefI(i *int64) int64 {
	if i == nil {
		return 0
	}
	return *i
}

// viewScopes 返回视角涉及的 scope 集合。
func viewScopes(view View) ([]Scope, error) {
	switch view {
	case ViewProvider:
		return []Scope{ScopeDailyTotal, ScopeDailyByProvider, ScopeDailyByModel}, nil
	case ViewInternal:
		return []Scope{ScopeInternalTenant, ScopeInternalPerson, ScopeInternalModel}, nil
	default:
		return nil, fmt.Errorf("unknown view %q", view)
	}
}

// loadSnapshots 拉取区间内的日快照行（含可选维度过滤，全部参数化绑定）。
func loadSnapshots(ctx context.Context, q Querier, start, end time.Time, scopes []Scope, filter RangeFilter) ([]Snapshot, error) {
	scopeNames := make([]string, len(scopes))
	for i, s := range scopes {
		scopeNames[i] = string(s)
	}
	args := []any{start, end, scopeNames}
	where := `report_date >= $1 AND report_date <= $2 AND scope = ANY($3)`
	if filter.ProviderID != nil {
		args = append(args, *filter.ProviderID)
		where += fmt.Sprintf(" AND provider_id = $%d", len(args))
	}
	if filter.TenantID != "" {
		args = append(args, filter.TenantID)
		where += fmt.Sprintf(" AND tenant_id = $%d", len(args))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		where += fmt.Sprintf(" AND raw_model_name = $%d", len(args))
	}
	rows, err := q.Query(ctx, `
		SELECT scope, scope_key, report_date, raw_model_name,
		       request_count, success_count, error_count,
		       input_tokens, output_tokens, cache_read_tokens, cache_write_tokens,
		       error_kind_breakdown, cache_hit_ratio,
		       estimated_cost_cents, currency, price_snapshot,
		       provider_id, canonical_id, tenant_id,
		       credits_charged, latency_p50_ms, latency_p95_ms, updated_at
		FROM report_snapshots WHERE `+where+`
		ORDER BY report_date, scope, scope_key`, args...)
	if err != nil {
		return nil, fmt.Errorf("load report_snapshots: %w", err)
	}
	defer rows.Close()

	out := make([]Snapshot, 0, 64)
	for rows.Next() {
		var s Snapshot
		var scopeName string
		var breakdown, price []byte
		var currency string
		// 可空列走 sql.Null* 中转：pgx 直扫 **T 对非 NULL 值的兼容面窄
		//（pgxmock 实测不支持），中转写法两侧通用。
		var ratio sql.NullFloat64
		var providerID, canonicalID sql.NullInt64
		var tenantID sql.NullString
		if err := rows.Scan(&scopeName, &s.ScopeKey, &s.ReportDate, &s.RawModelName,
			&s.RequestCount, &s.SuccessCount, &s.ErrorCount,
			&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens,
			&breakdown, &ratio,
			&s.EstimatedCostCents, &currency, &price,
			&providerID, &canonicalID, &tenantID,
			&s.CreditsCharged, &s.LatencyP50Ms, &s.LatencyP95Ms, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan report_snapshots: %w", err)
		}
		s.Scope = Scope(scopeName)
		s.Currency = currency
		if ratio.Valid {
			v := ratio.Float64
			s.CacheHitRatio = &v
		}
		if providerID.Valid {
			v := providerID.Int64
			s.ProviderID = &v
		}
		if canonicalID.Valid {
			v := canonicalID.Int64
			s.CanonicalID = &v
		}
		if tenantID.Valid {
			v := tenantID.String
			s.TenantID = &v
		}
		if err := decodeBreakdown(breakdown, &s.ErrorKindBreakdown); err != nil {
			return nil, err
		}
		if err := decodePrice(price, &s.PriceSnapshot); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func decodePrice(raw []byte, out *map[string]any) error {
	m := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("decode price_snapshot %q: %w", string(raw), err)
		}
	}
	*out = m
	return nil
}
