package providerprofile

import (
	"context"
	"fmt"
	"time"
)

// ComputeDiffRate 计算网关统计值与供应商账单值的差异率：
//
//	(gateway - provider) / provider
//
// provider 为 0 时分母无意义，返回 0（调用方以“未定义差异”处理，
// 不触发告警）。结果保留 4 位小数，与表的 numeric(5,4) 精度对齐。
func ComputeDiffRate(gateway, provider float64) float64 {
	if provider == 0 {
		return 0
	}
	return roundTo(gateway-provider, provider)
}

// roundTo 计算 diff/provider 并四舍五入到 4 位小数。
func roundTo(diff, provider float64) float64 {
	rate := diff / provider
	// 四舍五入到 1e-4 精度。
	const prec = 10000
	if rate >= 0 {
		return float64(int64(rate*prec+0.5)) / prec
	}
	return float64(int64(rate*prec-0.5)) / prec
}

// GatewayMonthlyUsage 网关侧某供应商单月用量聚合（来自 request_logs /
// request_logs_hot，按 provider_id + 月份聚合）。
type GatewayMonthlyUsage struct {
	ProviderID   int64
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	TotalCost    float64
}

// ProviderBill 供应商账单（人工导入或未来 API 自动获取）。
type ProviderBill struct {
	ProviderID   int64
	Month        time.Time // 月份首日
	InputTokens  int64
	OutputTokens int64
	TotalTokens  int64
	TotalCost    float64
	DataSource   string // "manual" / "api"
	Notes        string
}

// ReconciliationRecord 映射 provider_cost_reconciliation 一行。
type ReconciliationRecord struct {
	ID                   int64     `json:"id"`
	ProviderID           int64     `json:"provider_id"`
	Month                time.Time `json:"reconciliation_month"`
	GatewayTotalTokens   int64     `json:"gateway_total_tokens"`
	GatewayInputTokens   int64     `json:"gateway_input_tokens"`
	GatewayOutputTokens  int64     `json:"gateway_output_tokens"`
	GatewayTotalCost     float64   `json:"gateway_total_cost"`
	ProviderTotalTokens  int64     `json:"provider_total_tokens"`
	ProviderInputTokens  int64     `json:"provider_input_tokens"`
	ProviderOutputTokens int64     `json:"provider_output_tokens"`
	ProviderTotalCost    float64   `json:"provider_total_cost"`
	TokenDiffRate        float64   `json:"token_diff_rate"`
	CostDiffRate         float64   `json:"cost_diff_rate"`
	DataSource           string    `json:"data_source"`
	Notes                string    `json:"notes"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// HasBill 报告该记录是否已有供应商账单数据（tokens 或 cost 任一非零）。
func (r *ReconciliationRecord) HasBill() bool {
	return r.ProviderTotalTokens != 0 || r.ProviderTotalCost != 0
}

// GatewayUsageSource 网关侧月度用量数据源。
type GatewayUsageSource interface {
	// MonthlyUsageByProvider 返回 month（月份首日）内各供应商的用量聚合。
	MonthlyUsageByProvider(ctx context.Context, month time.Time) ([]GatewayMonthlyUsage, error)
}

// ReconciliationStore provider_cost_reconciliation 表的持久化接口。
type ReconciliationStore interface {
	// UpsertGatewayUsage 写入网关侧聚合值（只更新 gateway_* 列，保留账单列）。
	UpsertGatewayUsage(ctx context.Context, month time.Time, usage []GatewayMonthlyUsage) error
	// SaveRecord 全量 upsert 一条对账记录（按 provider_id + 月份）。
	SaveRecord(ctx context.Context, rec *ReconciliationRecord) error
	// Get 读取指定供应商+月份的记录；不存在时返回 (nil, nil)。
	Get(ctx context.Context, providerID int64, month time.Time) (*ReconciliationRecord, error)
	// ListByMonth 列出某月的全部对账记录。
	ListByMonth(ctx context.Context, month time.Time) ([]ReconciliationRecord, error)
}

// ReconciliationAlertSink 对账差异告警出口（复用供应商画像的
// provider_events 事件流，见 PGReconciliationEventSink）。
type ReconciliationAlertSink interface {
	EmitCostDiffAlert(ctx context.Context, rec *ReconciliationRecord) error
}

// DiffThresholds 差异告警阈值（相对比率）。
type DiffThresholds struct {
	Cost  float64 // 成本差异率阈值，默认 0.05
	Token float64 // token 差异率阈值，默认 0.05
}

// DefaultDiffThresholds 返回默认阈值（5%）。
func DefaultDiffThresholds() DiffThresholds {
	return DiffThresholds{Cost: 0.05, Token: 0.05}
}

// DiffAlertReason 判断记录的差异率是否超过阈值；超过时返回 true 及
// 人类可读的原因描述。
func DiffAlertReason(rec *ReconciliationRecord, th DiffThresholds) (bool, string) {
	if cost := rec.CostDiffRate; cost > th.Cost || cost < -th.Cost {
		return true, fmt.Sprintf("cost diff rate %.4f exceeds threshold %.4f (gateway %.4f vs provider %.4f)",
			cost, th.Cost, rec.GatewayTotalCost, rec.ProviderTotalCost)
	}
	if tok := rec.TokenDiffRate; tok > th.Token || tok < -th.Token {
		return true, fmt.Sprintf("token diff rate %.4f exceeds threshold %.4f (gateway %d vs provider %d)",
			tok, th.Token, rec.GatewayTotalTokens, rec.ProviderTotalTokens)
	}
	return false, ""
}

// CostReconciler 对账编排器：月度聚合网关用量 → 导入供应商账单 →
// 差异超阈值时通过告警 sink 发出事件。
type CostReconciler struct {
	source     GatewayUsageSource
	store      ReconciliationStore
	sink       ReconciliationAlertSink
	thresholds DiffThresholds
}

// NewCostReconciler 创建对账编排器。source 可为 nil（仅用于账单导入
// 场景，不跑月度聚合）。
func NewCostReconciler(source GatewayUsageSource, store ReconciliationStore, sink ReconciliationAlertSink, thresholds DiffThresholds) *CostReconciler {
	return &CostReconciler{source: source, store: store, sink: sink, thresholds: thresholds}
}

// ImportBill 导入供应商账单：合并已有网关聚合值计算差异率，持久化，
// 并在差异超阈值时发出告警。
func (r *CostReconciler) ImportBill(ctx context.Context, bill ProviderBill) (*ReconciliationRecord, error) {
	rec, err := r.store.Get(ctx, bill.ProviderID, bill.Month)
	if err != nil {
		return nil, fmt.Errorf("load existing reconciliation record: %w", err)
	}
	if rec == nil {
		rec = &ReconciliationRecord{ProviderID: bill.ProviderID, Month: bill.Month}
	}
	rec.ProviderInputTokens = bill.InputTokens
	rec.ProviderOutputTokens = bill.OutputTokens
	rec.ProviderTotalTokens = bill.TotalTokens
	rec.ProviderTotalCost = bill.TotalCost
	rec.DataSource = bill.DataSource
	rec.Notes = bill.Notes
	r.applyDiff(rec)
	if err := r.store.SaveRecord(ctx, rec); err != nil {
		return nil, fmt.Errorf("save reconciliation record: %w", err)
	}
	if err := r.alertIfBreached(ctx, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// AggregateMonth 聚合 month（月份首日）的网关侧用量写入对账表，
// 并对已有账单的记录刷新差异率、按阈值发出告警。
func (r *CostReconciler) AggregateMonth(ctx context.Context, month time.Time) error {
	if r.source == nil {
		return fmt.Errorf("cost reconciler: usage source is nil")
	}
	usage, err := r.source.MonthlyUsageByProvider(ctx, month)
	if err != nil {
		return fmt.Errorf("query monthly usage: %w", err)
	}
	if err := r.store.UpsertGatewayUsage(ctx, month, usage); err != nil {
		return fmt.Errorf("upsert gateway usage: %w", err)
	}

	// 已有账单的记录：刷新 diff 率并评估告警。聚合值每天刷新，diff 率
	// 随之变化；告警 sink 侧按 (provider, month) 幂等去重。
	recs, err := r.store.ListByMonth(ctx, month)
	if err != nil {
		return fmt.Errorf("list reconciliation records: %w", err)
	}
	for i := range recs {
		rec := &recs[i]
		if !rec.HasBill() {
			continue
		}
		r.applyDiff(rec)
		if err := r.store.SaveRecord(ctx, rec); err != nil {
			return fmt.Errorf("save reconciliation record: %w", err)
		}
		if err := r.alertIfBreached(ctx, rec); err != nil {
			return err
		}
	}
	return nil
}

// ListByMonth 读取某月的全部对账记录（供管理端查询）。
func (r *CostReconciler) ListByMonth(ctx context.Context, month time.Time) ([]ReconciliationRecord, error) {
	return r.store.ListByMonth(ctx, month)
}

// applyDiff 依据当前两侧数值重算差异率列。
func (r *CostReconciler) applyDiff(rec *ReconciliationRecord) {
	rec.TokenDiffRate = ComputeDiffRate(float64(rec.GatewayTotalTokens), float64(rec.ProviderTotalTokens))
	rec.CostDiffRate = ComputeDiffRate(rec.GatewayTotalCost, rec.ProviderTotalCost)
}

// alertIfBreached 差异超阈值时通过 sink 发出告警；sink 为 nil 时跳过。
func (r *CostReconciler) alertIfBreached(ctx context.Context, rec *ReconciliationRecord) error {
	if r.sink == nil {
		return nil
	}
	if breached, _ := DiffAlertReason(rec, r.thresholds); breached {
		if err := r.sink.EmitCostDiffAlert(ctx, rec); err != nil {
			return fmt.Errorf("emit cost diff alert: %w", err)
		}
	}
	return nil
}
