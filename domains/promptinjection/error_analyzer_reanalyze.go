package promptinjection

// error_analyzer_reanalyze.go — 定向分析（单条）与存量结果的重解析（2026-11-25 取证轮）。
//
// ★ 为什么需要「定向分析」：
//
//	既有的 /trigger 是「按优先级领一批」，管理员在某一行上点「分析」并不能
//	保证这一行被分析到（它可能排在批之外）。这里对**指定的一行**走完整分析。
//
// ★ 为什么需要「重解析」：
//
//	旧解析器把 15 条真实分析里的 14 条判成了失败，但模型的原始响应**原样留在**
//	llm_response_raw 里 ⇒ 换代后的解析器可以直接对着存量原文重新读，不用再调
//	一次模型（零费用、毫秒级）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 定向分析 / 重解析的业务性错误（管理面据此映射 404 / 409 / 422）。
var (
	ErrQueueItemNotFound      = errors.New("promptinjection: queue item not found")
	ErrAnalysisResultNotFound = errors.New("promptinjection: analysis result not found")
	ErrAnalysisInProgress     = errors.New("promptinjection: queue item is already being analysed")
	ErrAnalyzerNotConfigured  = errors.New("promptinjection: error analyzer is not configured")
	ErrNothingToReparse       = errors.New("promptinjection: stored result has no raw response to re-parse")
	ErrStillUnparsable        = errors.New("promptinjection: stored raw response still yields no analysis")
)

// AnalyzeQueueItem 对**指定**的队列行做一次完整分析（取证 → LLM → 解析 → 落库）。
//
//	可用于 pending / failed / analyzed 的任意一行（「分析此条」与「重新分析」共用）。
//
//	★ 与批处理的三处差异（都是「这一行是管理员点名要的」带来的）：
//
//	① 已耗尽重试的 failed 行也能分析（重试计数清零）；
//	② 已有结果行时**就地更新**分析列（批处理路径是「已分析则保留旧结果」）；
//	③ 正在分析中的行（领取时间在回收阈值之内）不会被重复领取 ⇒ ErrAnalysisInProgress。
func (ea *ErrorAnalyzer) AnalyzeQueueItem(ctx context.Context, queueID int64) (*AnalysisResult, error) {
	if ea == nil || ea.db == nil || ea.collector == nil || ea.llmClient == nil {
		return nil, ErrAnalyzerNotConfigured
	}
	qe, err := ea.collector.GetQueueItem(ctx, queueID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrQueueItemNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("promptinjection: load queue item %d: %w", queueID, err)
	}
	claimed, err := ea.collector.MarkProcessing(ctx, queueID, true)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, ErrAnalysisInProgress
	}
	// 登记在途 ⇒ 并发的回收器不会在 LLM 调用期间把这一行放回 pending。
	release := ea.markInflight([]int64{queueID})
	defer release()

	result, err := ea.AnalyzeError(ctx, qe)
	if err != nil {
		slog.Warn("error_analyzer: targeted analysis failed", "queue_id", queueID, "error", err)
		// 与批处理同一套重试语义（条件更新：只有仍处于 processing 才推进）。
		if rerr := ea.collector.IncrementRetry(ctx, queueID); rerr != nil {
			slog.Error("error_analyzer: failed to increment retry", "queue_id", queueID, "error", rerr)
		}
		return nil, fmt.Errorf("promptinjection: analysis failed: %w", err)
	}
	if err := ea.SaveAnalysisResultReplace(ctx, result); err != nil {
		_ = ea.collector.UpdateStatus(ctx, queueID, QueueStatusFailed)
		return nil, err
	}
	if err := ea.collector.UpdateStatus(ctx, queueID, QueueStatusAnalyzed); err != nil {
		slog.Warn("error_analyzer: failed to update status to analyzed", "queue_id", queueID, "error", err)
	}
	slog.Info("error_analyzer: targeted analysis completed",
		"queue_id", queueID, "analysis_id", result.AnalysisID, "root_cause", result.RootCause)
	return result, nil
}

// SaveAnalysisResultReplace 落一条分析结果；该队列行已有结果时**就地更新分析列**。
//
//	★ applied_at / applied_by / auto_applied / applied_actions 四列**不更新**：
//	已应用修复的留痕与回滚依据（旧值快照）不能被一次重新分析抹掉。
//	（批处理路径的 SaveAnalysisResult 仍是「已分析则保留旧结果」。）
func (ea *ErrorAnalyzer) SaveAnalysisResultReplace(ctx context.Context, result *AnalysisResult) error {
	ruleAdjustmentJSON, err := jsonbArg(result.RuleAdjustment)
	if err != nil {
		return fmt.Errorf("failed to marshal rule_adjustment: %w", err)
	}
	codeChangesJSON, err := jsonbArg(result.CodeChanges)
	if err != nil {
		return fmt.Errorf("failed to marshal code_changes: %w", err)
	}
	similarCasesJSON, err := jsonbArg(result.SimilarCases)
	if err != nil {
		return fmt.Errorf("failed to marshal similar_cases: %w", err)
	}
	analysisDetailsJSON, err := jsonbArg(result.AnalysisDetails)
	if err != nil {
		return fmt.Errorf("failed to marshal analysis_details: %w", err)
	}

	// ON CONFLICT (queue_id) 依赖迁移 977 的唯一索引 idx_error_analysis_results_queue_uniq
	// （2026-11-25 在隔离 PG 17.10 上实测：更新分析列、applied_* 原样保留）。
	var resultID int64
	err = ea.db.QueryRowContext(ctx, `
		INSERT INTO error_analysis_results (
			queue_id, analysis_id, analyzer_model, analyzer_version,
			root_cause, error_category, is_false_positive, confidence_score,
			suggested_fix, rule_adjustment, code_changes,
			severity_level, impact_scope, affected_users_estimate,
			similar_cases, pattern_frequency,
			analysis_details, llm_response_raw,
			analysis_duration_ms, llm_tokens_used
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9,
			NULLIF($10, '')::text::jsonb, NULLIF($11, '')::text::jsonb,
			$12, $13, $14,
			NULLIF($15, '')::text::jsonb, $16,
			NULLIF($17, '')::text::jsonb, $18,
			$19, $20
		)
		ON CONFLICT (queue_id) DO UPDATE SET
			analysis_id = EXCLUDED.analysis_id,
			analyzer_model = EXCLUDED.analyzer_model,
			analyzer_version = EXCLUDED.analyzer_version,
			root_cause = EXCLUDED.root_cause,
			error_category = EXCLUDED.error_category,
			is_false_positive = EXCLUDED.is_false_positive,
			confidence_score = EXCLUDED.confidence_score,
			suggested_fix = EXCLUDED.suggested_fix,
			rule_adjustment = EXCLUDED.rule_adjustment,
			code_changes = EXCLUDED.code_changes,
			severity_level = EXCLUDED.severity_level,
			impact_scope = EXCLUDED.impact_scope,
			affected_users_estimate = EXCLUDED.affected_users_estimate,
			similar_cases = EXCLUDED.similar_cases,
			pattern_frequency = EXCLUDED.pattern_frequency,
			analysis_details = EXCLUDED.analysis_details,
			llm_response_raw = EXCLUDED.llm_response_raw,
			analysis_duration_ms = EXCLUDED.analysis_duration_ms,
			llm_tokens_used = EXCLUDED.llm_tokens_used,
			created_at = NOW()
		RETURNING id
	`, result.QueueID, result.AnalysisID, ea.analyzerModel, ea.analyzerVersion,
		result.RootCause, result.ErrorCategory, result.IsFalsePositive, result.ConfidenceScore,
		result.SuggestedFix, ruleAdjustmentJSON, codeChangesJSON,
		result.SeverityLevel, result.ImpactScope, result.AffectedUsersEstimate,
		similarCasesJSON, result.PatternFrequency,
		analysisDetailsJSON, result.LLMResponseRaw,
		result.AnalysisDurationMs, result.LLMTokensUsed,
	).Scan(&resultID)
	if err != nil {
		return fmt.Errorf("failed to upsert analysis result: %w", err)
	}
	slog.Info("error_analyzer: analysis result saved", "result_id", resultID,
		"analysis_id", result.AnalysisID, "queue_id", result.QueueID)
	return nil
}

// analyzerVersionCurrent 是当前解析器的代际标记（重解析时写进 analyzer_version）。
const analyzerVersionCurrent = "2.0.0"

// ReparseStoredResult 用当前解析器重新读取某条已落库结果的原始响应。
//
//	★ 不调模型：输入就是 error_analysis_results.llm_response_raw。
//	★ 解析不出任何内容时**不动库**（返回 ErrStillUnparsable）——不做「把一条
//	  记录改成另一条同样没内容的记录」这种无意义写入。
//	★ 只更新分析列：分析耗时/token 数/相似案例/应用留痕（applied_*）都不动。
//	  旧结论留痕在 analysis_details.reparse 里（之前的根因、之前的解析错误）。
func ReparseStoredResult(ctx context.Context, db *sql.DB, queueID int64) (*AnalysisResult, ParseDiagnostics, error) {
	if db == nil {
		return nil, ParseDiagnostics{Mode: ParseModeFailed}, ErrAnalyzerNotConfigured
	}
	var (
		resultID        int64
		raw             sql.NullString
		prevRoot        sql.NullString
		prevCategory    sql.NullString
		prevModel       sql.NullString
		prevVersion     sql.NullString
		prevDetailsJSON []byte
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, llm_response_raw, root_cause, error_category, analyzer_model, analyzer_version,
		       COALESCE(analysis_details, 'null'::jsonb)
		FROM error_analysis_results
		WHERE queue_id = $1`, queueID,
	).Scan(&resultID, &raw, &prevRoot, &prevCategory, &prevModel, &prevVersion, &prevDetailsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ParseDiagnostics{Mode: ParseModeFailed}, ErrAnalysisResultNotFound
	}
	if err != nil {
		return nil, ParseDiagnostics{Mode: ParseModeFailed}, fmt.Errorf("promptinjection: load stored result: %w", err)
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, ParseDiagnostics{Mode: ParseModeFailed}, ErrNothingToReparse
	}

	result, diag := parseAnalysisResponse(raw.String)
	if result == nil {
		return nil, diag, ErrStillUnparsable
	}
	result.QueueID = queueID
	result.AnalysisID = uuid.New().String()
	result.LLMResponseRaw = raw.String
	if result.AnalysisDetails == nil {
		result.AnalysisDetails = map[string]any{}
	}
	var prevDetails map[string]any
	if len(prevDetailsJSON) > 0 {
		_ = json.Unmarshal(prevDetailsJSON, &prevDetails)
	}
	reparse := map[string]any{
		"at":                    time.Now().UTC(),
		"previous_root_cause":   prevRoot.String,
		"previous_category":     prevCategory.String,
		"previous_model":        prevModel.String,
		"previous_version":      prevVersion.String,
		"previous_parse_error":  stringOf(prevDetails["parse_error"]),
		"previous_fix_plan_sum": firstSentence(stringOf(prevDetails["summary"]), 120),
	}
	if prevDetails["parse_diagnostics"] != nil {
		reparse["previous_parse_diagnostics"] = prevDetails["parse_diagnostics"]
	}
	pd, _ := result.AnalysisDetails["parse_diagnostics"].(map[string]any)
	if pd == nil {
		pd = map[string]any{"mode": ParseModeCanonical}
		result.AnalysisDetails["parse_diagnostics"] = pd
	}
	pd["reparse"] = reparse

	ruleAdjustmentJSON, err := jsonbArg(result.RuleAdjustment)
	if err != nil {
		return nil, diag, fmt.Errorf("failed to marshal rule_adjustment: %w", err)
	}
	codeChangesJSON, err := jsonbArg(result.CodeChanges)
	if err != nil {
		return nil, diag, fmt.Errorf("failed to marshal code_changes: %w", err)
	}
	analysisDetailsJSON, err := jsonbArg(result.AnalysisDetails)
	if err != nil {
		return nil, diag, fmt.Errorf("failed to marshal analysis_details: %w", err)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE error_analysis_results
		SET analysis_id = $2, analyzer_version = $3,
		    root_cause = $4, error_category = $5, is_false_positive = $6, confidence_score = $7,
		    suggested_fix = $8,
		    rule_adjustment = NULLIF($9, '')::text::jsonb,
		    code_changes = NULLIF($10, '')::text::jsonb,
		    severity_level = $11, impact_scope = $12, affected_users_estimate = $13,
		    analysis_details = NULLIF($14, '')::text::jsonb
		WHERE queue_id = $1 AND id = $15`,
		queueID, result.AnalysisID, analyzerVersionCurrent+"+reparse",
		result.RootCause, result.ErrorCategory, result.IsFalsePositive, result.ConfidenceScore,
		result.SuggestedFix, ruleAdjustmentJSON, codeChangesJSON,
		result.SeverityLevel, result.ImpactScope, result.AffectedUsersEstimate,
		analysisDetailsJSON, resultID,
	)
	if err != nil {
		return nil, diag, fmt.Errorf("promptinjection: update re-parsed result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// ★ RowsAffected 纪律：0 行 = 这条结果已经不在了 = 没更新成功，不能声称成功。
		return nil, diag, ErrAnalysisResultNotFound
	}
	slog.Info("error_analyzer: stored result re-parsed",
		"queue_id", queueID, "result_id", resultID, "mode", diag.Mode,
		"previous_root_cause", prevRoot.String, "root_cause", result.RootCause)
	return result, diag, nil
}

// stringOf 把 JSON 里的标量转成短文本（容器 ⇒ 空串）。
func stringOf(v any) string {
	switch t := v.(type) {
	case string:
		return truncateRunesSafe(t, 300)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return ""
}
