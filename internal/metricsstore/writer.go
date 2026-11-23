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
	RequestID    string
	TenantID     string
	OccurredAt   time.Time
	OccurredDate time.Time // DATE 类型（UTC 日历日）
	BillingDate  time.Time // DATE 类型（UTC 日历日）

	// 节点与服务器维度
	GatewayNodeID   *string
	GatewayInstance *string
	EndpointNode    *string

	// 核心维度
	ProviderID    *int64
	OutboundModel *string  // 标准模型名称（映射后）
	CanonicalID   *int64
	RawModelName  *string  // 原始模型名称（来自usage_facts.raw_model_name）

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

	// 多模态与协议维度
	Modality        *string
	Protocol        *string
	AttachmentCount int32
	AttachmentSize  int64

	// 计量字段
	PromptTokens     int32
	CompletionTokens int32
	CachedTokens     int32
	ReasoningTokens  int32
	TotalTokens      int32

	// 多模态专用 token 计量
	ImageTokens int32
	AudioTokens int32
	VideoTokens int32

	// 费用字段（分）
	CostCents      int64
	CreditsCharged int64

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

// execer 抽象 *sql.DB 与 *sql.Tx —— 两者都提供 ExecContext。
//
// ★ 批量写入必须走同一个 *sql.Tx，否则批内失败时「前面已成功的行」会留在库里
//
//	（半批落库）。抽象出这一个接口，就是为了让单条写入与批量写入共用同一份 SQL 与参数构造，
//	从根上杜绝「循环里又换回 w.db.ExecContext」这类写法。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
}

// upsertRequestMetricSQL 单条幂等写入。
//
// ★ 冲突键 = (request_id, occurred_at)（与迁移 983 的主键一致）。
// ★ DO UPDATE 更新**所有**业务列：同一请求可能被后续批次用更新后的维度/计量值重写，
//
//	只更新部分列会让旧值长期残留。
//
// ★ attributes 显式 ::jsonb 转换：驱动把 Go 字符串按 text 送过来，
//
//	靠 PG 端的显式转换解析成 JSON（bytea→jsonb 没有可用转换）。
const upsertRequestMetricSQL = `
	INSERT INTO request_metrics (
		request_id, tenant_id, occurred_at, occurred_date, billing_date,
		gateway_node_id, gateway_instance, endpoint_node,
		provider_id, outbound_model, canonical_id, raw_model_name,
		payer_id, payer_type, user_id,
		session_id, task_id,
		org_id, team_id,
		api_key_id, application_id,
		modality, protocol, attachment_count, attachment_size,
		prompt_tokens, completion_tokens, cached_tokens, reasoning_tokens, total_tokens,
		image_tokens, audio_tokens, video_tokens,
		cost_cents, credits_charged,
		status, traffic_class, request_type, error_kind,
		latency_ms, ttft_ms,
		attributes,
		source, created_at, updated_at
	) VALUES (
		$1, $2, $3, $4::date, $5::date,
		$6, $7, $8,
		$9, $10, $11, $12,
		$13, $14, $15,
		$16, $17,
		$18, $19,
		$20, $21,
		$22, $23, $24, $25,
		$26, $27, $28, $29, $30,
		$31, $32, $33,
		$34, $35,
		$36, $37, $38, $39,
		$40, $41,
		$42::jsonb,
		$43, NOW(), NOW()
	)
	ON CONFLICT (request_id, occurred_at) DO UPDATE SET
		tenant_id = EXCLUDED.tenant_id,
		occurred_date = EXCLUDED.occurred_date,
		billing_date = EXCLUDED.billing_date,
		gateway_node_id = EXCLUDED.gateway_node_id,
		gateway_instance = EXCLUDED.gateway_instance,
		endpoint_node = EXCLUDED.endpoint_node,
		provider_id = EXCLUDED.provider_id,
		outbound_model = EXCLUDED.outbound_model,
		canonical_id = EXCLUDED.canonical_id,
		raw_model_name = EXCLUDED.raw_model_name,
		payer_id = EXCLUDED.payer_id,
		payer_type = EXCLUDED.payer_type,
		user_id = EXCLUDED.user_id,
		session_id = EXCLUDED.session_id,
		task_id = EXCLUDED.task_id,
		org_id = EXCLUDED.org_id,
		team_id = EXCLUDED.team_id,
		api_key_id = EXCLUDED.api_key_id,
		application_id = EXCLUDED.application_id,
		modality = EXCLUDED.modality,
		protocol = EXCLUDED.protocol,
		attachment_count = EXCLUDED.attachment_count,
		attachment_size = EXCLUDED.attachment_size,
		prompt_tokens = EXCLUDED.prompt_tokens,
		completion_tokens = EXCLUDED.completion_tokens,
		cached_tokens = EXCLUDED.cached_tokens,
		reasoning_tokens = EXCLUDED.reasoning_tokens,
		total_tokens = EXCLUDED.total_tokens,
		image_tokens = EXCLUDED.image_tokens,
		audio_tokens = EXCLUDED.audio_tokens,
		video_tokens = EXCLUDED.video_tokens,
		cost_cents = EXCLUDED.cost_cents,
		credits_charged = EXCLUDED.credits_charged,
		status = EXCLUDED.status,
		traffic_class = EXCLUDED.traffic_class,
		request_type = EXCLUDED.request_type,
		error_kind = EXCLUDED.error_kind,
		latency_ms = EXCLUDED.latency_ms,
		ttft_ms = EXCLUDED.ttft_ms,
		attributes = EXCLUDED.attributes,
		source = EXCLUDED.source,
		updated_at = NOW()
`

// requestMetricArgs 构造 upsertRequestMetricSQL 的参数（顺序与 SQL 占位符一一对应）。
func requestMetricArgs(metric *RequestMetric) ([]interface{}, error) {
	// attributes：JSON 文本；空则写 NULL（::jsonb 转换对 NULL 安全）。
	var attributesJSON interface{}
	if len(metric.Attributes) > 0 {
		raw, err := json.Marshal(metric.Attributes)
		if err != nil {
			return nil, err
		}
		attributesJSON = string(raw)
	}

	return []interface{}{
		metric.RequestID,
		metric.TenantID,
		metric.OccurredAt,
		formatDate(metric.OccurredDate),
		formatDate(metric.BillingDate),
		metric.GatewayNodeID,
		metric.GatewayInstance,
		metric.EndpointNode,
		metric.ProviderID,
		metric.OutboundModel,
		metric.CanonicalID,
		metric.RawModelName,
		metric.PayerID,
		metric.PayerType,
		metric.UserID,
		metric.SessionID,
		metric.TaskID,
		metric.OrgID,
		metric.TeamID,
		metric.APIKeyID,
		metric.ApplicationID,
		metric.Modality,
		metric.Protocol,
		metric.AttachmentCount,
		metric.AttachmentSize,
		metric.PromptTokens,
		metric.CompletionTokens,
		metric.CachedTokens,
		metric.ReasoningTokens,
		metric.TotalTokens,
		metric.ImageTokens,
		metric.AudioTokens,
		metric.VideoTokens,
		metric.CostCents,
		metric.CreditsCharged,
		metric.Status,
		metric.TrafficClass,
		metric.RequestType,
		metric.ErrorKind,
		metric.LatencyMs,
		metric.TTFTMs,
		attributesJSON,
		metric.Source,
	}, nil
}

// formatDate 把日期列格式化成 YYYY-MM-DD 文本。
//
// ★ DATE 列不能直接塞 time.Time：驱动按 timestamptz 送，PG 端再按**会话时区**
//
//	截断成 date ⇒ 同一个时间在不同 TimeZone 下落到不同日期（跨时区偏一天）。
//	送 YYYY-MM-DD 文本 + 端上 ::date 就没有这层歧义。
func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.DateOnly)
}

// UpsertRequestMetric 幂等写入（基于 request_id + occurred_at）
func (w *Writer) UpsertRequestMetric(ctx context.Context, metric *RequestMetric) error {
	return w.upsertRequestMetric(ctx, w.db, metric)
}

// upsertRequestMetric 在指定执行器（db 或 tx）上写入一条。
func (w *Writer) upsertRequestMetric(ctx context.Context, ex execer, metric *RequestMetric) error {
	args, err := requestMetricArgs(metric)
	if err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx, upsertRequestMetricSQL, args...)
	return err
}

// BatchUpsertRequestMetrics 批量写入（同一事务）
//
// ★ 整个批次在**一个事务**里：批内任一条失败 ⇒ 整批回滚 ⇒ 不会留下「半批落库」。
// ★ 早期实现循环里调 w.UpsertRequestMetric（走 w.db.ExecContext）⇒ 完全绕过事务，
//
//	前面的行已提交、后面的行失败 ⇒ 库里留下半批数据。
func (w *Writer) BatchUpsertRequestMetrics(ctx context.Context, metrics []*RequestMetric) error {
	if len(metrics) == 0 {
		return nil
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// 正常路径走到 Commit 后 defer 里的 Rollback 会返回「事务已结束」错误，忽略即可。
	defer func() { _ = tx.Rollback() }()

	for _, metric := range metrics {
		if err := w.upsertRequestMetric(ctx, tx, metric); err != nil {
			return err
		}
	}

	return tx.Commit()
}
