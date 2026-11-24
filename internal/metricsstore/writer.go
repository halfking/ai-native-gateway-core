// Package metricsstore 提供 request_metrics 表的写入和处理能力
// 负责从 usage_facts 异步填充统计专用表 request_metrics
package metricsstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	OutboundModel *string // 标准模型名称（映射后）
	CanonicalID   *int64
	RawModelName  *string // 原始模型名称（来自usage_facts.raw_model_name）

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

// BatchUpsertRequestMetrics 批量写入（同一事务、单条多行 INSERT）
//
// ★ 整个批次在**一个事务**里：批内任一条失败 ⇒ 整批回滚 ⇒ 不会留下「半批落库」。
// ★ 早期实现循环里调 w.UpsertRequestMetric（走 w.db.ExecContext）⇒ 完全绕过事务，
//
//	前面的行已提交、后面的行失败 ⇒ 库里留下半批数据。
//
// ★ 2026-11-24 吞吐优化轮：批内从「N 次单行 upsert」改为「**多行 INSERT**」。
//
//	理由：单行 upsert 每次都是一次独立网络往返（批 100 条 ⇒ 100 次往返）。
//	生产实测（211）：批 100 条单行 upsert = **18.8ms**，
//	而同样 100 行的单条多行 INSERT = **8.5ms**（含索引冲突处理）⇒ 快 2.2 倍。
//	★ 批量越大收益越明显（往返次数从 N 降到 1）。
//	★ 事务语义不变：仍是「全成或全败」。
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

	// ★ 分块写入：每块一条多行 INSERT（块内 1 次往返）。
	//
	//	分块的原因：PostgreSQL 单条语句的绑定参数上限是 65535，
	//	而每行占 43 个参数 ⇒ 65535/43 ≈ 1524 行/条语句。
	//	默认块大小 500 行（远低于硬顶，且与默认 BatchSize 对齐 ⇒ 通常只有 1 块）。
	for start := 0; start < len(metrics); start += maxBatchInsertRows {
		end := start + maxBatchInsertRows
		if end > len(metrics) {
			end = len(metrics)
		}
		chunk := metrics[start:end]

		query, args, err := buildBatchUpsert(chunk)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// maxBatchInsertRows 单条 INSERT 语句最多写多少行。
//
// ★ 上限来自 PostgreSQL 的 65535 绑定参数上限：43 参数/行 ⇒ 1524 行是硬顶，
//
//	这里取 500（远低于硬顶，且与默认 BatchSize 对齐）。
const maxBatchInsertRows = 500

// buildBatchUpsert 把一批 metric 拼成**一条**多行 INSERT 语句。
//
// ★ 为什么自己拼而不用 pq.CopyIn：COPY 不支持 ON CONFLICT DO UPDATE，
//
//	而本表必须幂等（同一 request 可能被后续批次重写，见 upsertRequestMetricSQL 注释）。
//
// ★ 参数编号：多行 INSERT 里 $1 是**整条语句**的第一个参数，
//
//	所以第 r 行（0-based）的参数区间是 [r*43+1, r*43+43]。
//	★ 关键：平移时必须**保留每个占位符后面的类型转换**
//	（$4::date、$5::date、$42::jsonb）——
//	丢掉 ::jsonb 会让 attributes 写入失败（驱动送 text，PG 端无隐式转换）。
func buildBatchUpsert(metrics []*RequestMetric) (string, []interface{}, error) {
	const paramCols = 43 // requestMetricArgs 返回的参数个数

	valuesSection, err := extractValuesSection(upsertRequestMetricSQL)
	if err != nil {
		return "", nil, err
	}

	rowTemplates := make([]string, 0, len(metrics))
	args := make([]interface{}, 0, len(metrics)*paramCols)

	for r, metric := range metrics {
		rowArgs, err := requestMetricArgs(metric)
		if err != nil {
			return "", nil, err
		}
		if len(rowArgs) != paramCols {
			return "", nil, fmt.Errorf("requestMetricArgs 返回 %d 个参数，期望 %d（占位符与参数错位）", len(rowArgs), paramCols)
		}
		// 第 r 行的参数整体后移 r*43。
		rowTemplates = append(rowTemplates, shiftParams(valuesSection, r*paramCols))
		args = append(args, rowArgs...)
	}

	return replaceValuesSection(upsertRequestMetricSQL, valuesSection, strings.Join(rowTemplates, ",\n")), args, nil
}

// extractValuesSection 取出单行 upsert SQL 的 VALUES 段（含外层括号）。
//
// ★ 用括号配对来定位结束位置 ⇒ 不受 SQL 里其它括号干扰。
func extractValuesSection(sqlText string) (string, error) {
	start := strings.Index(sqlText, "VALUES (")
	if start < 0 {
		return "", fmt.Errorf("upsertRequestMetricSQL 里找不到 VALUES 段")
	}
	open := strings.Index(sqlText[start:], "(")
	depth := 0
	for i := start + open; i < len(sqlText); i++ {
		switch sqlText[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sqlText[start+open : i+1], nil
			}
		}
	}
	return "", fmt.Errorf("upsertRequestMetricSQL 的 VALUES 段括号不配对")
}

// replaceValuesSection 把 SQL 里的 VALUES 段替换成新的内容。
func replaceValuesSection(sqlText, oldSection, newSection string) string {
	idx := strings.Index(sqlText, oldSection)
	if idx < 0 {
		return sqlText
	}
	return sqlText[:idx] + newSection + sqlText[idx+len(oldSection):]
}

// shiftParams 把 VALUES 段里的占位符编号整体后移 offset。
//
// ★ 只替换「$」后面的数字部分 ⇒ $4::date 变成 $(4+offset)::date、
//
//	$42::jsonb 变成 $(42+offset)::jsonb ⇒ 类型转换原样保留。
func shiftParams(section string, offset int) string {
	if offset == 0 {
		return section
	}
	var b strings.Builder
	b.Grow(len(section) + 8)
	for i := 0; i < len(section); i++ {
		if section[i] != '$' {
			b.WriteByte(section[i])
			continue
		}
		// 读出数字
		j := i + 1
		for j < len(section) && section[j] >= '0' && section[j] <= '9' {
			j++
		}
		if j == i+1 {
			// 不是占位符（理论上不会发生）⇒ 原样输出。
			b.WriteByte(section[i])
			continue
		}
		n, err := strconv.Atoi(section[i+1 : j])
		if err != nil {
			b.WriteByte(section[i])
			continue
		}
		b.WriteByte('$')
		b.WriteString(strconv.Itoa(n + offset))
		i = j - 1
	}
	return b.String()
}
