// Package metricsstore 提供 request_metrics 表的写入和处理能力
// 负责从 usage_facts 异步填充统计专用表 request_metrics
package metricsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// RequestMetric 请求计量数据
type RequestMetric struct {
	RequestID   string
	TenantID    string
	OccurredAt  time.Time
	OccurredDate time.Time // DATE 类型
	BillingDate  time.Time // DATE 类型
	
	// 节点与服务器维度
	GatewayNodeID   *string
	GatewayInstance *string
	EndpointNode    *string
	
	// 核心维度
	ProviderID   *int64
	OutboundModel *string
	CanonicalID  *int64
	
	// 付款与使用者维度
	PayerID   *string
	PayerType *string
	UserID    *string
	
	// 会话与任务维度
	SessionID *string
	TaskID    *string
	
	// 组织结构维度
	OrgID  *string
	TeamID *int64
	
	// API 维度
	APIKeyID      *int64
	ApplicationID *int64
	
	// 计量字段
	PromptTokens     int32
	CompletionTokens int32
	CachedTokens     int32
	ReasoningTokens  int32
	TotalTokens      int32
	
	// 费用字段（分）
	CostCents       int64
	CreditsCharged  int64
	
	// 状态与类型
	Status       string
	TrafficClass string
	RequestType  *string
	ErrorKind    *string
	
	// 性能指标
	LatencyMs *int32
	TTFTMs    *int32
	
	// 扩展属性
	Attributes map[string]interface{}
	
	Source string
}

// Writer 负责写入 request_metrics
type Writer struct {
	db *sql.DB
}

// NewWriter 创建 Writer
func NewWriter(db *sql.DB) *Writer {
	return &Writer{db: db}
}

// UpsertRequestMetric 幂等写入（基于 request_id）
func (w *Writer) UpsertRequestMetric(ctx context.Context, metric *RequestMetric) error {
	// 转换 attributes 为 JSON
	var attributesJSON []byte
	var err error
	if metric.Attributes != nil && len(metric.Attributes) > 0 {
		attributesJSON, err = json.Marshal(metric.Attributes)
		if err != nil {
			return err
		}
	}
	
	query := `
		INSERT INTO request_metrics (
			request_id, tenant_id, occurred_at, occurred_date, billing_date,
			gateway_node_id, gateway_instance, endpoint_node,
			provider_id, outbound_model, canonical_id,
			payer_id, payer_type, user_id,
			session_id, task_id,
			org_id, team_id,
			api_key_id, application_id,
			prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, total_tokens,
			cost_cents, credits_charged,
			status, traffic_class, request_type, error_kind,
			latency_ms, ttft_ms,
			attributes,
			source, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10, $11,
			$12, $13, $14,
			$15, $16,
			$17, $18,
			$19, $20,
			$21, $22, $23, $24, $25,
			$26, $27,
			$28, $29, $30, $31,
			$32, $33,
			$34,
			$35, NOW(), NOW()
		)
		ON CONFLICT (request_id, occurred_at) DO UPDATE SET
			gateway_node_id = EXCLUDED.gateway_node_id,
			gateway_instance = EXCLUDED.gateway_instance,
			provider_id = EXCLUDED.provider_id,
			cost_cents = EXCLUDED.cost_cents,
			credits_charged = EXCLUDED.credits_charged,
			status = EXCLUDED.status,
			latency_ms = EXCLUDED.latency_ms,
			ttft_ms = EXCLUDED.ttft_ms,
			attributes = EXCLUDED.attributes,
			updated_at = NOW()
	`
	
	_, err = w.db.ExecContext(ctx, query,
		metric.RequestID, metric.TenantID, metric.OccurredAt, metric.OccurredDate, metric.BillingDate,
		metric.GatewayNodeID, metric.GatewayInstance, metric.EndpointNode,
		metric.ProviderID, metric.OutboundModel, metric.CanonicalID,
		metric.PayerID, metric.PayerType, metric.UserID,
		metric.SessionID, metric.TaskID,
		metric.OrgID, metric.TeamID,
		metric.APIKeyID, metric.ApplicationID,
		metric.PromptTokens, metric.CompletionTokens, metric.CachedTokens, metric.ReasoningTokens, metric.TotalTokens,
		metric.CostCents, metric.CreditsCharged,
		metric.Status, metric.TrafficClass, metric.RequestType, metric.ErrorKind,
		metric.LatencyMs, metric.TTFTMs,
		attributesJSON,
		metric.Source,
	)
	
	return err
}

// BatchUpsertRequestMetrics 批量写入
func (w *Writer) BatchUpsertRequestMetrics(ctx context.Context, metrics []*RequestMetric) error {
	if len(metrics) == 0 {
		return nil
	}
	
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	
	for _, metric := range metrics {
		if err := w.UpsertRequestMetric(ctx, metric); err != nil {
			return err
		}
	}
	
	return tx.Commit()
}
