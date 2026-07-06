// Package analysis — OptimizationAdviser: 优化建议引擎。
//
// 规则检测（无 LLM 成本），产出 OptimizationSuggestion + 量化潜在节省。
// 检测项：
//  1. 上下文冗余（prompt/completion 比值过高）→ 建议压缩
//  2. 模型过剩（简单意图用昂贵模型）→ 建议降级
//  3. 重试浪费（同 session 多次 error）→ 建议 prompt/凭证检查
//  4. 缓存利用率低（cache_read_tokens 占比低）→ 建议启用 prompt cache
//  5. 压缩机会（未压缩但 outbound_token_est 高）→ 建议压缩
//  6. 模型频繁切换（model_switch_count 高）→ 建议稳定路由
package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// OptimizationAdviser 检测会话可优化空间并写入建议。
type OptimizationAdviser struct {
	db     DB
	config *LLMStageConfig
	logger *slog.Logger
}

// NewOptimizationAdviser 构造 adviser。
func NewOptimizationAdviser(db DB, config *LLMStageConfig, logger *slog.Logger) *OptimizationAdviser {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		config = NewLLMStageConfig(nil)
	}
	return &OptimizationAdviser{db: db, config: config, logger: logger}
}

// sessionStatsForOpt 是 adviser 需要的统计字段。
type sessionStatsForOpt struct {
	GwSessionID       string
	TenantID          string
	RequestCount      int
	ErrorCount        int
	TotalPromptTokens int64
	TotalCompletionTokens int64
	TotalCostUSD      float64
	InputCostUSD      float64
	UserIntent        *string
	PrimaryModel      *string
	ModelSwitchCount  int
	// 聚合的 cache/compression 统计
	CacheReadTokens   int64
	CompressedCount   int
	OutboundTokenEst  int64
}

// suggestion 计算结果（内部，写入前构造）。
type suggestion struct {
	Category        string
	Severity        string
	Title           string
	Description     string
	PotentialTokens int64
	PotentialCost   float64
	Evidence        map[string]any
}

// Advise 检测某会话并持久化建议。
func (a *OptimizationAdviser) Advise(ctx context.Context, tenantID, gwSessionID string) error {
	if a.db == nil || !a.config.OptimizationEnabled() {
		return nil
	}
	stats, err := a.loadStats(ctx, tenantID, gwSessionID)
	if err != nil {
		return fmt.Errorf("optimizer: load stats: %w", err)
	}
	if stats == nil || stats.RequestCount == 0 {
		return nil
	}
	suggestions := a.detect(stats)
	for _, s := range suggestions {
		if err := a.save(ctx, stats, s); err != nil {
			a.logger.Warn("optimizer: save failed",
				"gw_session_id", gwSessionID, "title", s.Title, "error", err)
		}
	}
	return nil
}

// detect 运行所有规则检测，返回命中建议。
func (a *OptimizationAdviser) detect(s *sessionStatsForOpt) []suggestion {
	var out []suggestion

	// 1. 上下文冗余：prompt/completion > 5:1
	if s.TotalCompletionTokens > 0 {
		ratio := float64(s.TotalPromptTokens) / float64(s.TotalCompletionTokens)
		if ratio > 5 {
			savings := int64(float64(s.TotalPromptTokens) * 0.5) // 估计可压缩 50%
			out = append(out, suggestion{
				Category:        "prompt",
				Severity:        "warn",
				Title:           "上下文冗余，建议启用压缩",
				Description:     fmt.Sprintf("prompt/completion = %.1f:1，输入上下文过长。启用会话压缩可显著降低成本。", ratio),
				PotentialTokens: savings,
				PotentialCost:   float64(savings) * 0.00001, // 粗略单价
				Evidence:        map[string]any{"ratio": ratio, "prompt_tokens": s.TotalPromptTokens},
			})
		}
	}

	// 2. 模型过剩：chat 意图用昂贵模型（>某阈值成本）
	if s.UserIntent != nil && *s.UserIntent == "chat" && s.PrimaryModel != nil {
		avgCost := s.TotalCostUSD / float64(s.RequestCount)
		if avgCost > 0.05 { // 单请求均价 > $0.05 对 chat 偏高
			cheaper := avgCost * 0.2 // 假设降级到轻量模型省 80%
			out = append(out, suggestion{
				Category:        "model",
				Severity:        "info",
				Title:           "简单意图使用昂贵模型",
				Description:     fmt.Sprintf("chat 意图平均 $%.4f/请求，可降级到轻量模型。", avgCost),
				PotentialTokens: 0,
				PotentialCost:   cheaper * float64(s.RequestCount),
				Evidence:        map[string]any{"avg_cost": avgCost, "model": *s.PrimaryModel},
			})
		}
	}

	// 3. 重试浪费：错误率 > 30%
	if s.RequestCount > 3 && s.ErrorCount > 0 {
		errorRate := float64(s.ErrorCount) / float64(s.RequestCount)
		if errorRate > 0.3 {
			out = append(out, suggestion{
				Category:        "session",
				Severity:        "action_required",
				Title:           "高错误率，建议检查 prompt 或凭证",
				Description:     fmt.Sprintf("错误率 %.0f%%（%d/%d 请求失败），造成 token 与时间浪费。", errorRate*100, s.ErrorCount, s.RequestCount),
				PotentialTokens: 0,
				PotentialCost:   s.InputCostUSD * errorRate,
				Evidence:        map[string]any{"error_rate": errorRate, "error_count": s.ErrorCount},
			})
		}
	}

	// 4. 缓存利用率低
	totalTokens := s.TotalPromptTokens + s.TotalCompletionTokens
	if totalTokens > 10000 && s.CacheReadTokens == 0 {
		out = append(out, suggestion{
			Category:        "policy",
			Severity:        "info",
			Title:           "未使用 prompt cache",
			Description:     "会话 token 量较大但无缓存命中，启用 prompt cache 可降低重复前缀成本。",
			PotentialTokens: int64(float64(s.TotalPromptTokens) * 0.3),
			PotentialCost:   float64(s.TotalPromptTokens) * 0.3 * 0.000005,
			Evidence:        map[string]any{"total_tokens": totalTokens, "cache_read": 0},
		})
	}

	// 5. 压缩机会
	if s.CompressedCount == 0 && s.OutboundTokenEst > 20000 {
		out = append(out, suggestion{
			Category:        "prompt",
			Severity:        "warn",
			Title:           "存在压缩空间",
			Description:     fmt.Sprintf("出站 token 估计 %d，但未启用压缩。", s.OutboundTokenEst),
			PotentialTokens: int64(float64(s.OutboundTokenEst) * 0.4),
			PotentialCost:   float64(s.OutboundTokenEst) * 0.4 * 0.00001,
			Evidence:        map[string]any{"outbound_token_est": s.OutboundTokenEst},
		})
	}

	// 6. 模型频繁切换
	if s.ModelSwitchCount > 3 {
		out = append(out, suggestion{
			Category:        "model",
			Severity:        "info",
			Title:           "模型频繁切换",
			Description:     fmt.Sprintf("会话内切换模型 %d 次，建议稳定路由策略以减少波动。", s.ModelSwitchCount),
			PotentialTokens: 0,
			PotentialCost:   0,
			Evidence:        map[string]any{"switch_count": s.ModelSwitchCount},
		})
	}

	return out
}

// loadStats 读取会话统计（session_summaries + request_logs 聚合）。
func (a *OptimizationAdviser) loadStats(ctx context.Context, tenantID, gwSessionID string) (*sessionStatsForOpt, error) {
	query := `
		SELECT ss.session_key, ss.tenant_id,
		       ss.request_count, ss.error_count,
		       ss.total_prompt_tokens, ss.total_completion_tokens,
		       ss.total_cost_usd, ss.input_cost_usd,
		       ss.user_intent, ss.primary_model, ss.model_switch_count,
		       COALESCE((SELECT SUM(COALESCE(cache_read_tokens,0)) FROM request_logs WHERE gw_session_id = ss.session_key), 0),
		       COALESCE((SELECT COUNT(*) FROM request_logs WHERE gw_session_id = ss.session_key AND compression_strategy IS NOT NULL AND compression_strategy != ''), 0),
		       COALESCE((SELECT SUM(COALESCE(outbound_token_est,0)) FROM request_logs WHERE gw_session_id = ss.session_key), 0)
		FROM session_summaries ss
		WHERE ss.session_key = $1`
	args := []any{gwSessionID}
	if tenantID != "" {
		query += " AND ss.tenant_id = $2"
		args = append(args, tenantID)
	}
	var s sessionStatsForOpt
	err := a.db.QueryRow(ctx, query, args...).Scan(
		&s.GwSessionID, &s.TenantID,
		&s.RequestCount, &s.ErrorCount,
		&s.TotalPromptTokens, &s.TotalCompletionTokens,
		&s.TotalCostUSD, &s.InputCostUSD,
		&s.UserIntent, &s.PrimaryModel, &s.ModelSwitchCount,
		&s.CacheReadTokens, &s.CompressedCount, &s.OutboundTokenEst,
	)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

// save 持久化一条建议。
func (a *OptimizationAdviser) save(ctx context.Context, s *sessionStatsForOpt, sug suggestion) error {
	evidenceJSON, _ := json.Marshal(sug.Evidence)
	_, err := a.db.Exec(ctx, `
		INSERT INTO session_optimization_suggestions
			(gw_session_id, tenant_id, category, severity, title, description,
			 potential_savings_tokens, potential_savings_cost, evidence)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		s.GwSessionID, s.TenantID, sug.Category, sug.Severity, sug.Title, sug.Description,
		sug.PotentialTokens, sug.PotentialCost, evidenceJSON)
	return err
}
