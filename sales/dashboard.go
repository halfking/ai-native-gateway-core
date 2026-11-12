package sales

// dashboard.go —— 管理层漏斗看板（风暴档 §3.2「管理层的一天」/ 规划档 W2-T2）。
//
// 四个数（漏斗 / 审批触发率 / 丢单原因 / 毛利分布）全部现算，不建新表：
//   - 漏斗 = 头表 GROUP BY status + **expired 派生态现算**（944 CHECK 故意
//     不含 expired，读时对活单 current 版本 valid_until 判定）；
//   - 审批触发率 = DealHub 突破率自校准：进过人审的单 / 提交过的单
//     （status_log 面统计，转换表隐式定义状态机）；超过
//     sales.quote.approval_rate_alert_pct ⇒ alert=true（阈值定错的信号，
//     不是门 —— 该 settings 键自此有了消费方，不再是占位）；
//   - 触发维度分布 = 947 trigger_reasons jsonb 逐元素聚合（{dimension,
//     value,threshold} 快照可复算）；
//   - 丢单原因 = status_log to_status='lost' 的 reason 聚合（CloseQuote
//     强制回填，风暴档 §4-5）；
//   - 毛利分布 = 未 superseded 单的 current 版本 margin_pct 分桶。
//
// 实收毛利**不在本面**：default 租户按设计跳过计费（credit_ledger 恒 0），
// 「实收毛利」一栏由前端显式标「待租户计费激活」（风暴档 §3.6 诚实边界，
// 文案断言进前端测试）。

import (
	"context"
	"fmt"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ReasonCount is one bucket of a GROUP BY reason aggregate.
type ReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// SalesDashboard is the management funnel read model (computed, no new table).
type SalesDashboard struct {
	Funnel              map[string]int `json:"funnel"`           // status → 单数（含派生 expired）
	Approvals           map[string]int `json:"approvals"`        // decision(pending/approved/rejected) → 行数
	SubmittedQuotes     int            `json:"submitted_quotes"` // 提交过的单（分母）
	TriggeredQuotes     int            `json:"triggered_quotes"` // 进过人审的单（分子）
	TriggerRatePct      float64        `json:"trigger_rate_pct"`
	TriggerAlertPct     int            `json:"trigger_alert_pct"`     // settings 键读数
	TriggerRateAlert    bool           `json:"trigger_rate_alert"`    // 触发率超告警线
	TriggersByDimension map[string]int `json:"triggers_by_dimension"` // dimension → 触发行数
	LostReasons         []ReasonCount  `json:"lost_reasons"`
	MarginBuckets       map[string]int `json:"margin_buckets"` // 桶 → 单数
}

// FunnelDashboard computes the whole read model in four queries.
func (s *Service) FunnelDashboard(ctx context.Context) (*SalesDashboard, error) {
	out := &SalesDashboard{
		Funnel:              map[string]int{},
		Approvals:           map[string]int{},
		TriggersByDimension: map[string]int{},
		MarginBuckets:       map[string]int{},
		LostReasons:         []ReasonCount{},
	}

	// ① 漏斗：头表按状态 + expired 派生态（活单 current 版本过期）。
	rows, err := s.pool.Query(ctx, `
SELECT status, count(*) FROM public.sales_quotes GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("sales: dashboard funnel: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, fmt.Errorf("sales: dashboard funnel scan: %w", err)
		}
		out.Funnel[st] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sales: dashboard funnel iterate: %w", err)
	}
	var expired int
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM public.sales_quotes q
  JOIN public.sales_quote_versions v ON v.id = q.current_version_id
 WHERE q.status IN ('approved','sent') AND v.valid_until < now()`).Scan(&expired); err != nil {
		return nil, fmt.Errorf("sales: dashboard expired: %w", err)
	}
	if expired > 0 {
		// 派生态键（存储闭集故意没有它）。口径声明（2026-11-12 复审审计
		// P3-9）：expired 只数 approved/sent 的活单（draft/pending 的过期
		// 无行动意义），且它与自身状态桶**并存**（一张 sent 过期单同时计入
		// sent 与 expired）⇒ 漏斗各键是「阶段计数」不是「互斥分桶」，加和
		// 可以 > 头表总数——前端 funnelOrder 把 expired 排在末位、标签为
		// 「已过期（派生）」，语义即「其中已过期的」。
		out.Funnel["expired"] = expired
	}

	// ② 审批触发率（DealHub 突破率自校准，分母/分子都走 status_log 面）。
	if err := s.pool.QueryRow(ctx, `
SELECT count(DISTINCT quote_id) FILTER (WHERE to_status IN ('pending_approval','approved')),
       count(DISTINCT quote_id) FILTER (WHERE to_status = 'pending_approval')
  FROM public.sales_quote_status_log`).Scan(&out.SubmittedQuotes, &out.TriggeredQuotes); err != nil {
		return nil, fmt.Errorf("sales: dashboard trigger rate: %w", err)
	}
	if out.SubmittedQuotes > 0 {
		out.TriggerRatePct = float64(out.TriggeredQuotes) / float64(out.SubmittedQuotes) * 100
	}
	out.TriggerAlertPct = settings.GetPlatformInt("sales.quote.approval_rate_alert_pct", 40)
	out.TriggerRateAlert = out.TriggerAlertPct > 0 && out.TriggerRatePct > float64(out.TriggerAlertPct)
	approvalRows, err := s.pool.Query(ctx, `
SELECT decision, count(*) FROM public.sales_quote_approvals GROUP BY decision`)
	if err != nil {
		return nil, fmt.Errorf("sales: dashboard approvals: %w", err)
	}
	defer approvalRows.Close()
	for approvalRows.Next() {
		var d string
		var n int
		if err := approvalRows.Scan(&d, &n); err != nil {
			return nil, fmt.Errorf("sales: dashboard approvals scan: %w", err)
		}
		out.Approvals[d] = n
	}
	if err := approvalRows.Err(); err != nil {
		return nil, fmt.Errorf("sales: dashboard approvals iterate: %w", err)
	}

	// ③ 触发维度分布（trigger_reasons jsonb 逐元素）。
	dimRows, err := s.pool.Query(ctx, `
SELECT e->>'dimension' AS dimension, count(*)
  FROM public.sales_quote_approvals, jsonb_array_elements(trigger_reasons) e
 GROUP BY 1 ORDER BY 2 DESC`)
	if err != nil {
		return nil, fmt.Errorf("sales: dashboard dimensions: %w", err)
	}
	defer dimRows.Close()
	for dimRows.Next() {
		var d string
		var n int
		if err := dimRows.Scan(&d, &n); err != nil {
			return nil, fmt.Errorf("sales: dashboard dimensions scan: %w", err)
		}
		out.TriggersByDimension[d] = n
	}
	if err := dimRows.Err(); err != nil {
		return nil, fmt.Errorf("sales: dashboard dimensions iterate: %w", err)
	}

	// ④ 丢单原因（CloseQuote 强制回填 reason）。
	lostRows, err := s.pool.Query(ctx, `
SELECT reason, count(*) FROM public.sales_quote_status_log
 WHERE to_status = 'lost' GROUP BY reason ORDER BY count(*) DESC LIMIT 10`)
	if err != nil {
		return nil, fmt.Errorf("sales: dashboard lost reasons: %w", err)
	}
	defer lostRows.Close()
	for lostRows.Next() {
		var rc ReasonCount
		if err := lostRows.Scan(&rc.Reason, &rc.Count); err != nil {
			return nil, fmt.Errorf("sales: dashboard lost reasons scan: %w", err)
		}
		out.LostReasons = append(out.LostReasons, rc)
	}
	if err := lostRows.Err(); err != nil {
		return nil, fmt.Errorf("sales: dashboard lost reasons iterate: %w", err)
	}

	// ⑤ 毛利分布（未 superseded 单的 current 版本；margin_pct NULL ⇒ n/a）。
	marginRows, err := s.pool.Query(ctx, `
SELECT CASE
         WHEN v.margin_pct IS NULL THEN 'n/a'
         WHEN v.margin_pct < 0 THEN '<0'
         WHEN v.margin_pct < 10 THEN '0-10'
         WHEN v.margin_pct < 20 THEN '10-20'
         WHEN v.margin_pct < 30 THEN '20-30'
         WHEN v.margin_pct < 50 THEN '30-50'
         ELSE '>=50'
       END AS bucket, count(*)
  FROM public.sales_quotes q
  JOIN public.sales_quote_versions v ON v.id = q.current_version_id
 GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("sales: dashboard margin buckets: %w", err)
	}
	defer marginRows.Close()
	for marginRows.Next() {
		var b string
		var n int
		if err := marginRows.Scan(&b, &n); err != nil {
			return nil, fmt.Errorf("sales: dashboard margin buckets scan: %w", err)
		}
		out.MarginBuckets[b] = n
	}
	if err := marginRows.Err(); err != nil {
		return nil, fmt.Errorf("sales: dashboard margin buckets iterate: %w", err)
	}
	return out, nil
}
