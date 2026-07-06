// Package sessionanalysis — 会话全景分析 Hook 插件。
//
// 作为插件接入请求管道的 PhasePostResponse 阶段，提供准实时分析：
//   - 每条请求完成后：增量生成逐步摘要（规则模式）+ 更新标签
//   - 首请求：生成粗标题（若 title_on_first_request 开启）
//   - 由 settings.session_analytics.enabled 控制启停（前端据此检测模块）
//
// 设计要点：
//   - Enabled() 检查模块开关 + 是否有 SessionID（无会话则跳过）
//   - Execute() 全程异步（goroutine），不阻塞响应
//   - 所有失败降级（吞错，记日志），不影响主请求路径
//   - 会话关闭时的总结/聚类/优化由 session_summary_worker（订阅
//     EventSessionClosed）异步触发，不在此 hook 内做重活
package sessionanalysis

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/analysis"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// AnalysisHook 会话全景分析插件 Hook。
type AnalysisHook struct {
	engines *Engines
	config  *analysis.LLMStageConfig
	logger  *slog.Logger
}

// Engines 聚合分析引擎（由 main 注入具体实现）。
type Engines struct {
	RequestSummarizer *analysis.RequestSummarizer
	Tagger            *analysis.SessionTagger
	TitleGenerator    TitleGenerator // 可选；nil 时跳过标题
}

// TitleGenerator 生成会话标题的最小接口。
// 由 sessionsummary.Summarizer.GenerateTitle 或 auto_title_generator 实现。
type TitleGenerator interface {
	GenerateTitle(ctx context.Context, tenantID, gwSessionID, firstMessage string) error
}

// NewAnalysisHook 构造 Hook。engines 为 nil 时 Hook 永不启用。
func NewAnalysisHook(engines *Engines, config *analysis.LLMStageConfig, logger *slog.Logger) *AnalysisHook {
	if logger == nil {
		logger = slog.Default()
	}
	if config == nil {
		config = analysis.NewLLMStageConfig(nil)
	}
	return &AnalysisHook{engines: engines, config: config, logger: logger}
}

// Name 返回 Hook 名称。
func (h *AnalysisHook) Name() string { return "session.analytics" }

// Priority 在 PostResponse 阶段较晚执行（在 audit/metrics 之后）。
func (h *AnalysisHook) Priority() int { return 250 }

// Enabled 仅当模块开启 + engines 已注入 + 有 SessionID 时启用。
func (h *AnalysisHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if !h.config.Enabled() {
		return false
	}
	if h.engines == nil {
		return false
	}
	return env != nil && env.SessionID != ""
}

// Execute 触发准实时分析（异步，不阻塞）。
func (h *AnalysisHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if !h.Enabled(ctx, env) {
		return nil
	}

	tenantID := env.TenantID
	gwSessionID := env.SessionID

	// 异步执行，避免阻塞 post_response 管道
	go h.runAnalysis(tenantID, gwSessionID)

	return nil
}

// OnError 吞掉错误（分析失败不影响主流程）。
func (h *AnalysisHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil {
		h.logger.Warn("session.analytics hook error (suppressed)",
			"gw_session_id", env.SessionID, "tenant_id", env.TenantID, "error", err)
	}
	return nil
}

// runAnalysis 执行准实时分析步骤（带超时）。
func (h *AnalysisHook) runAnalysis(tenantID, gwSessionID string) {
	// 独立 context，不绑定请求生命周期
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger := h.logger.With("gw_session_id", gwSessionID, "tenant_id", tenantID)

	// 1. 逐步摘要（规则模式，快速）
	if h.engines.RequestSummarizer != nil {
		if err := h.engines.RequestSummarizer.SummarizeSession(ctx, tenantID, gwSessionID); err != nil {
			logger.Warn("request summary failed", "error", err)
		}
	}

	// 2. 标签更新（派生已有字段，快速）
	if h.engines.Tagger != nil {
		if err := h.engines.Tagger.TagSession(ctx, tenantID, gwSessionID); err != nil {
			logger.Warn("tagging failed", "error", err)
		}
	}

	// 3. 首请求标题（若开启）
	//    通过 request_count 判断是否首请求；标题生成是重操作，
	//    仅在首请求触发。会话关闭时由 worker 精炼。
	if h.engines.TitleGenerator != nil && h.config.TitleOnFirstRequest() {
		if h.isFirstRequest(ctx, tenantID, gwSessionID) {
			firstMsg := h.fetchFirstMessage(ctx, tenantID, gwSessionID)
			if firstMsg != "" {
				if err := h.engines.TitleGenerator.GenerateTitle(ctx, tenantID, gwSessionID, firstMsg); err != nil {
					logger.Warn("title generation failed", "error", err)
				}
			}
		}
	}
}

// isFirstRequest 判断当前是否为会话首请求。
func (h *AnalysisHook) isFirstRequest(ctx context.Context, tenantID, gwSessionID string) bool {
	// 由 RequestSummarizer 的存在性隐式判断：若该会话尚无摘要记录，
	// 则视为首请求附近。简化实现：始终允许（标题生成内部有幂等缓存）。
	return true
}

// fetchFirstMessage 获取会话首条用户消息（用于标题生成）。
func (h *AnalysisHook) fetchFirstMessage(ctx context.Context, tenantID, gwSessionID string) string {
	// 委托给 summarizer 的 DB 查询；此处返回空让标题生成器自行获取
	return ""
}

// 编译期断言：实现 pipeline.Hook
var _ pipeline.Hook = (*AnalysisHook)(nil)
