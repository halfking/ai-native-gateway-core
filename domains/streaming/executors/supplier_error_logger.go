// Package routing — supplier_error_logger.go
//
// 2026-09-05 审计闭环1：supplier_errors_hot / supplier_error_stats 唯一事实源。
//
// 审计结论（docs/audit-2026-09-05-ir-storage-provider.md）：task3 设计稿中的
// supplier_errors_hot 在生产写入链路中不存在，dashboard 实际查询
// session_module_executions_hot 等错误维度聚合。本文件把 supplier_errors_hot
// 的写入收敛到 CandidateFailureWriter.logFailure —— 每个失败候选一行、
// 单一写入函数、与 candidate_failure_logs_hot 同源同生命周期；
// 读端（凭据详情 / 供应商错误统计 / 趋势 API）统一走
// supplier_errors_unified / supplier_error_stats。
//
// 字段契约（低基数约束）：supplier = provider catalog code，error_type =
// errorsx.ErrorKind，stage = 失败阶段枚举；自由文本只有 error_message 与
// request_metadata，二者入库前必须经 errorsx.SanitizeErrorText 脱敏。
package executors

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// supplierErrorInsertSQL 写入 supplier_errors_hot（V371）。
// 错误维度列（supplier/error_type/stage）为低基数词表；
// error_message 与 request_metadata 为已脱敏自由文本。
const supplierErrorInsertSQL = `
	INSERT INTO supplier_errors_hot (
		occurred_at, request_id, trace_id, tenant_id, session_id,
		provider_id, supplier, credential_id, model, attempt_seq,
		error_type, error_code, http_status, error_message,
		is_retryable, stage, latency_ms, affected_users, request_metadata
	) VALUES (
		COALESCE($1, NOW()), $2, NULLIF($3, ''), $4, NULLIF($5, ''),
		$6, $7, $8, $9, $10,
		$11, $12, $13, NULLIF($14, ''),
		$15, $16, $17, 1, $18::text::jsonb
	)
`

// persistSupplierError 把已构建的候选失败行投影为 supplier_errors_hot 行并
// 写入。与 candidate_failure_logs_hot 的 INSERT 共用 3s 独立超时上下文，
// best-effort：失败只记 warn，不影响请求主链路。
//
// 事实源约定：读端（趋势 API、凭据详情、供应商错误统计）一律查
// supplier_errors_unified / supplier_error_stats；本函数是唯一写入入口。
func (w *CandidateFailureWriter) persistSupplierError(ctx context.Context, row candidateFailureLog) {
	if w == nil || w.pool == nil {
		return
	}

	// 低基数维度：supplier 来自调用方注入的 catalog code（extra context），
	// 缺省为空串（未知维度仍参与聚合，唯一键用 '' 而非 NULL）。
	supplier := lowCardinalityContextValue(row.Context, "supplier")
	stage := lowCardinalityContextValue(row.Context, "failure_stage")
	errorCode := sanitizeErrorString(contextStringValue(row.Context, "error_code"), 64)

	var occurredAt *time.Time
	if row.Context != nil {
		if v, ok := row.Context["occurred_at"].(time.Time); ok {
			occurredAt = &v
		}
	}

	latency := row.LatencyMs
	if latency == nil {
		latency = row.PerAttemptLatencyMs
	}

	metadata := supplierErrorMetadata(row)
	var metadataJSON any
	if len(metadata) > 0 {
		metadataJSON = marshalContext(metadata)
	}

	_, err := w.pool.Exec(ctx, supplierErrorInsertSQL,
		occurredAt,
		row.RequestID,
		contextStringValue(row.Context, "trace_id"),
		row.TenantID,
		row.SessionID,
		row.ProviderID,
		supplier,
		row.CredentialID,
		row.RawModelName,
		row.AttemptIndex,
		row.ErrorKind,
		errorCode,
		row.UpstreamStatusCode,
		sanitizeErrorString(row.ErrorMessage, 512),
		row.Retryable,
		stage,
		latency,
		metadataJSON,
	)
	if err != nil {
		slog.Warn("supplier_error_logger: insert failed",
			"error", err,
			"request_id", row.RequestID,
			"credential_id", row.CredentialID,
			"supplier", supplier,
		)
	}
}

// supplierErrorMetadata 构建请求元数据 JSONB。仅保留结构化诊断字段；
// 字符串值逐项脱敏，防止 extra context 里的自由文本（如 stream_reason、
// preflight_reason）把密钥或 query token 带入可查询表。
func supplierErrorMetadata(row candidateFailureLog) map[string]any {
	if len(row.Context) == 0 {
		return nil
	}
	// recoveryContext 已放入 generic_retryable/candidate_failover/
	// transparent_resume/effective_action/reason + 调用方 extras。
	// 事实源口径下 retryable 已是一级列，reason 属自由文本需脱敏。
	metadata := make(map[string]any, len(row.Context))
	for key, value := range row.Context {
		switch v := value.(type) {
		case string:
			metadata[key] = sanitizeErrorString(v, 256)
		case error:
			metadata[key] = sanitizeErrorString(v.Error(), 256)
		default:
			metadata[key] = value
		}
	}
	delete(metadata, "supplier")      // 一级列，不重复存 metadata
	delete(metadata, "failure_stage") // 一级列
	delete(metadata, "error_code")    // 一级列
	return metadata
}

// contextStringValue 读取 context 字符串值。
func contextStringValue(ctx map[string]any, key string) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx[key].(string); ok {
		return v
	}
	return ""
}

// lowCardinalityContextValue 读取低基数维度值：超长或含空白的值一律丢弃
// （词表值是短代码，二者都是自由文本混入的信号），不截断、不清洗——
// 宁可维度缺失（''），不可把自由文本写入维度列。
func lowCardinalityContextValue(ctx map[string]any, key string) string {
	v := contextStringValue(ctx, key)
	if v == "" || len(v) > 64 {
		return ""
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

// sanitizeErrorString 对自由文本做脱敏 + 截断（runes 安全截断交由
// SanitizeErrorText 的字节截断实现，这里只控制上限）。
func sanitizeErrorString(s string, limit int) string {
	if s == "" {
		return ""
	}
	return string(errorsx.SanitizeErrorText([]byte(s), limit))
}
