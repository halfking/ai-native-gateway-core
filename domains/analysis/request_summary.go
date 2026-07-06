// Package analysis — RequestSummarizer: 逐步请求/回复摘要。
//
// 默认规则模式（零 LLM 成本）：截取 request_preview/response_preview，
// 结合 tool_calls/transform_summary 抽取关键信息。
// 可选 LLM 模式（request_summary_mode=llm/hybrid）：单次 LLM 精炼为一句话。
//
// 输出写入 session_request_summaries（按 step_index 排序）。
package analysis

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// RequestSummarizer 生成每一步请求/回复的摘要。
type RequestSummarizer struct {
	db        DB
	config    *LLMStageConfig
	llmClient LLMCompleteClient // 可选；nil 时强制规则模式
	logger    *slog.Logger
}

// LLMCompleteClient 是单次补全的最小接口（与 sessionsummary.LLMClient 对齐）。
type LLMCompleteClient interface {
	Complete(ctx context.Context, model, prompt string) (string, error)
}

// requestLogRow 是 summarizer 需要的 request_logs 字段投影。
type requestLogRow struct {
	RequestID         string
	PromptTokens      int
	CompletionTokens  int
	CostUSD           float64
	LatencyMs         int
	RequestPreview    *string
	ResponsePreview   *string
	TransformSummary  *string
	ToolCallsJSON     *[]byte // jsonb
	CompressionStrategy *string
}

// NewRequestSummarizer 构造 summarizer。
func NewRequestSummarizer(db DB, config *LLMStageConfig, llm LLMCompleteClient, logger *slog.Logger) *RequestSummarizer {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		config = NewLLMStageConfig(nil)
	}
	return &RequestSummarizer{db: db, config: config, llmClient: llm, logger: logger}
}

// SummarizeSession 生成某会话所有请求步骤的摘要。
// 幂等：已存在的步骤（按 request_id）跳过。
func (r *RequestSummarizer) SummarizeSession(ctx context.Context, tenantID, gwSessionID string) error {
	if r.db == nil {
		return nil
	}
	rows, err := r.loadRequestLogs(ctx, tenantID, gwSessionID)
	if err != nil {
		return fmt.Errorf("request_summary: load logs: %w", err)
	}

	mode := r.config.RequestSummaryMode()
	useLLM := (mode == "llm") || (mode == "hybrid" && len(rows) > 20)

	for i, row := range rows {
		exists, err := r.summaryExists(ctx, gwSessionID, row.RequestID)
		if err != nil {
			r.logger.Warn("request_summary: exists check failed", "request_id", row.RequestID, "error", err)
			continue
		}
		if exists {
			continue
		}
		reqSummary, resSummary := r.ruleExtract(row)
		isLLM := false
		if useLLM && r.llmClient != nil {
			if refined := r.llmRefine(ctx, row, reqSummary, resSummary); refined != "" {
				resSummary = refined
				isLLM = true
			}
		}
		if err := r.saveSummary(ctx, tenantID, gwSessionID, row, i+1, reqSummary, resSummary, isLLM); err != nil {
			r.logger.Warn("request_summary: save failed", "request_id", row.RequestID, "error", err)
		}
	}
	return nil
}

// ruleExtract 规则模式：截取首尾，提取关键信息。
func (r *RequestSummarizer) ruleExtract(row requestLogRow) (reqSummary, resSummary string) {
	reqSummary = truncatePtr(row.RequestPreview, 200)
	resSummary = truncatePtr(row.ResponsePreview, 300)

	// 附加 transform_summary（转换摘要，如裁剪/格式化）
	if row.TransformSummary != nil && *row.TransformSummary != "" {
		resSummary = strings.TrimSpace(resSummary + " [" + *row.TransformSummary + "]")
	}
	// 附加压缩标记
	if row.CompressionStrategy != nil && *row.CompressionStrategy != "" {
		reqSummary = strings.TrimSpace(reqSummary + " (compressed:" + *row.CompressionStrategy + ")")
	}
	return reqSummary, resSummary
}

// llmRefine LLM 模式：把规则摘要精炼为一句话。
func (r *RequestSummarizer) llmRefine(ctx context.Context, row requestLogRow, reqSummary, resSummary string) string {
	model := r.config.ModelFor(StageRequestSummary)
	prompt := fmt.Sprintf(`用一句话（30字以内）概括这一步的请求与回复：
请求摘要: %s
回复摘要: %s
仅输出概括，不要前缀。`, reqSummary, resSummary)
	out, err := r.llmClient.Complete(ctx, model, prompt)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// loadRequestLogs 读取某会话的所有请求（按时间排序）。
func (r *RequestSummarizer) loadRequestLogs(ctx context.Context, tenantID, gwSessionID string) ([]requestLogRow, error) {
	query := `
		SELECT request_id, COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0),
		       COALESCE(cost_usd,0), COALESCE(latency_ms,0),
		       request_preview, response_preview, transform_summary,
		       tool_calls, compression_strategy
		FROM request_logs
		WHERE gw_session_id = $1`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY ts ASC LIMIT 100"

	rows, err := r.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []requestLogRow
	for rows.Next() {
		var row requestLogRow
		if err := rows.Scan(
			&row.RequestID, &row.PromptTokens, &row.CompletionTokens,
			&row.CostUSD, &row.LatencyMs,
			&row.RequestPreview, &row.ResponsePreview, &row.TransformSummary,
			&row.ToolCallsJSON, &row.CompressionStrategy,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// summaryExists 检查某步骤是否已生成摘要。
func (r *RequestSummarizer) summaryExists(ctx context.Context, gwSessionID, requestID string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM session_request_summaries WHERE gw_session_id=$1 AND request_id=$2)`,
		gwSessionID, requestID).Scan(&exists)
	return exists, err
}

// saveSummary 持久化一步摘要。
func (r *RequestSummarizer) saveSummary(ctx context.Context, tenantID, gwSessionID string, row requestLogRow, step int, reqSummary, resSummary string, isLLM bool) error {
	toolCallsSummary := summarizeToolCalls(row.ToolCallsJSON)
	_, err := r.db.Exec(ctx, `
		INSERT INTO session_request_summaries
			(gw_session_id, request_id, tenant_id, step_index,
			 request_summary, response_summary, is_llm_generated,
			 prompt_tokens, completion_tokens, cost_usd, latency_ms, tool_calls_summary)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (gw_session_id, request_id) DO UPDATE SET
			request_summary = EXCLUDED.request_summary,
			response_summary = EXCLUDED.response_summary,
			is_llm_generated = EXCLUDED.is_llm_generated,
			tool_calls_summary = EXCLUDED.tool_calls_summary`,
		gwSessionID, row.RequestID, tenantID, step,
		reqSummary, resSummary, isLLM,
		row.PromptTokens, row.CompletionTokens, row.CostUSD, row.LatencyMs, toolCallsSummary)
	return err
}

// truncatePtr 截取 *string 到 maxLen。
func truncatePtr(s *string, maxLen int) string {
	if s == nil || *s == "" {
		return ""
	}
	v := *s
	v = strings.ReplaceAll(v, "\n", " ")
	v = strings.TrimSpace(v)
	// 按 rune 截取避免截断 UTF-8
	runes := []rune(v)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return v
}

// summarizeToolCalls 从 tool_calls jsonb 提取摘要（如 "calc(x,y); search(q)"）。
func summarizeToolCalls(raw *[]byte) string {
	if raw == nil || len(*raw) == 0 {
		return ""
	}
	// 轻量解析：提取所有 "name" 字段值
	src := string(*raw)
	var names []string
	// 简单正则避免引入 json 解析开销；格式为数组或对象含 name 字段
	idx := 0
	for {
		pos := strings.Index(src[idx:], `"name"`)
		if pos < 0 {
			break
		}
		pos += idx + len(`"name"`)
		rest := src[pos:]
		colon := strings.Index(rest, ":")
		if colon < 0 {
			break
		}
		rest = rest[colon+1:]
		quote := strings.Index(rest, `"`)
		if quote < 0 {
			break
		}
		end := strings.Index(rest[quote+1:], `"`)
		if end < 0 {
			break
		}
		names = append(names, rest[quote+1:quote+1+end])
		idx = pos + colon + 1 + quote + 1 + end
	}
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, "; ")
}
