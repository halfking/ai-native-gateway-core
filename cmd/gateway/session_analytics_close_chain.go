// session_analytics_close_chain.go — 会话分析闭链启动器（2026-11-20 审计轮根修）。
//
// ★ 本轮审计定案的断链图（全部有 file:line 证据，见主档）：
//
//	① analysis.EventSessionClosed 无任何生产发布者（唯一 emitter
//	   clientprofile/event_emitter.go EmitSessionStarted 全仓零调用）⇒
//	   SessionSummaryWorker / SessionMetadataCloseHook / projectattr CloseHook
//	   三条 hook 订阅了一个永远不会有事件的类型，整条
//	   「提示词合并总结→元数据 final→项目归属」链生产空转；
//	② 会话关闭判定器 bg.SessionLifecycleWorker 零接线 + UPDATE 引用
//	   session_dim 不存在的列（42703）⇒ session_dim.status 全 'active'、
//	   closed_at 全 NULL（252 实测）；
//	③ projectattr hook 的三条件之一 deps.ProjectAttrResolver 无生产装配点
//	   ⇒ 即使①②修好，项目归属链仍挂不上；
//	④ 模型别名 "summary-fast" 全仓无解析表，被原样发往分析端点 ⇒ 必失败。
//
// 修法（本文件 + bg/session_lifecycle_worker.go）：
//   - 接活 SessionLifecycleWorker 作为「会话关闭」的事实判定器：扫 session_dim
//     闲置/超期会话 → soft_close（session_dim 元数据修正）→ 经
//     sessionClosedEventBridge 发布 analysis.EventSessionClosed；
//   - 闭链消费：RunLoop(summaryWorker) → GenerateRollingSummary（提示词合并总结
//   - 标题同步）→ closeHooks（sessionmeta final + projectattr 归属）；
//   - 启动位与 LLM_GATEWAY_USE_V2_PIPELINE 解耦（211 生产实测 V2 关闭——此前
//     整条分析链随 V2 块一起不存在）。V2 已启动同款消费循环时只补挂
//     lifecycle worker，不双跑。
//
// 门控（全部 settings/env 级，默认行为不变）：
//   - session_analytics.enabled（settings，默认 false，热更）总闸；
//   - LLM_GATEWAY_ANALYSIS_BASE_URL / _API_KEY 缺失 ⇒ 摘要层降级关闭，
//     但 元数据 final / 项目归属 / 会话关闭 三层照常工作（规则层零 LLM）。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/bg"
	"github.com/kaixuan/llm-gateway-go/domain/analysis" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	sessionanalytics "github.com/kaixuan/llm-gateway-go/domains/analysis"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/bus"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/projectattr"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/sessionmeta"
	"github.com/kaixuan/llm-gateway-go/domains/analysis/workers"
	"github.com/kaixuan/llm-gateway-go/domains/sessionsummary"
	"github.com/kaixuan/llm-gateway-go/settings"
)

// sessionAnalyticsAutoResolver 把「auto」解析为字面 "auto"：分析端点默认就是
// 网关自身（loopback 或公网自地址），网关原生支持 model="auto" 路由。此前的
// DefaultModelResolver 返回 "summary-fast"/"intent-fast" 等内置别名，而全仓
// 不存在这些别名的解析表 ⇒ 别名被原样塞进请求的 model 字段 ⇒ 分析端点
// （无论自地址还是外部）都不认识 ⇒ 每次调用必失败。显式配置的具体模型名
// 不经过本 resolver（stage_config.ModelFor 只在 settings 值为 "auto" 时询问）。
type sessionAnalyticsAutoResolver struct{}

func (sessionAnalyticsAutoResolver) ResolveModel(_ sessionanalytics.Stage) string { return "auto" }

// sessionClosedEventBridge 把 bg.SessionLifecycleWorker 的回收结果桥接为
// analysis.EventSessionClosed 事件落 analysis_events 表。
//
// EventID 用 (tenant|session|lastActiveUnix) 的确定性哈希：多实例同时扫描
// 时对同一会话产出的两条事件共享同一 EventID，由 PGPublisher 的
// ON CONFLICT (event_id) DO NOTHING 收敛成一条（幂等去重在发布层完成，
// 不依赖消费侧去重）。
type sessionClosedEventBridge struct {
	pub bus.Publisher
}

func (b sessionClosedEventBridge) PublishSessionClosed(ctx context.Context, tenantID, gwSessionID, reason string, lastActiveAt time.Time, idleFor time.Duration) error {
	if b.pub == nil {
		return nil
	}
	// ★ 两行扇出（EventSessionClosedSummary 常量头注）：legacy session.closed
	//   行归 clientprofile 画像 worker；session.closed.summary 行归摘要链，
	//   两类消费者互不抢认领。EventID 后缀保证两行确定性可去重且互不相同。
	base := fmt.Sprintf("session.closed|%s|%s|%d",
		tenantID, gwSessionID, lastActiveAt.Unix())
	sum := sha256.Sum256([]byte(base))
	payload := map[string]any{
		"reason":         reason,
		"idle_for":       idleFor.String(),
		"last_active_at": lastActiveAt.Format(time.RFC3339),
		"source":         "session_lifecycle_worker",
		"schema_version": "session.closed/v1",
	}
	profileEvt := analysis.AnalysisEvent{
		EventID:    hex.EncodeToString(sum[:]) + "-p",
		Type:       analysis.EventSessionClosed,
		TenantID:   tenantID,
		SessionID:  gwSessionID,
		OccurredAt: time.Now(),
		Payload:    payload,
	}
	if err := b.pub.Publish(ctx, profileEvt); err != nil {
		return err
	}
	summarySum := sha256.Sum256([]byte(base + "|summary"))
	summaryEvt := analysis.AnalysisEvent{
		EventID:    hex.EncodeToString(summarySum[:]) + "-s",
		Type:       analysis.EventSessionClosedSummary,
		TenantID:   tenantID,
		SessionID:  gwSessionID,
		OccurredAt: time.Now(),
		Payload:    payload,
	}
	return b.pub.Publish(ctx, summaryEvt)
}

// normalizeAnalysisModelAlias 把分析阶段模型的**幻别名**归一为 "auto"。
//
// ★ 2026-11-20 211/154 活体打回：stage_config.ModelFor 的 settings 未命中路径
//
//	直接回落 stage.defaultModel()（"summary-fast"/"intent-fast"/"embedding-fast"），
//	而 EffectiveValue 对未设置的键不合成 spec 默认值 ⇒ "auto" 透传 resolver
//	根本没机会生效 ⇒ 幻别名被原样发往分析端点（154 派发日志实录：
//	model=summary-fast + provider=0 + 上游 400）。本函数在**调用点**兜底：
//	已知幻别名一律改写为 "auto"（分析端点=网关自地址，原生支持 auto 路由），
//	显式配置的真实模型名逐字透传。
func normalizeAnalysisModelAlias(model string) string {
	switch model {
	case "summary-fast", "intent-fast", "embedding-fast":
		return "auto"
	default:
		return model
	}
}

// newAnalysisLLMClient 构造分析端点客户端 + stage 配置（auto 透传 resolver）。
// 返回 (nil, nil) 的条件：settings 总闸关 / env 缺失 / 端点校验失败。
// V2 块（SetV2DispatchAnalysisResources）与独立闭链共用这一个入口。
func newAnalysisLLMClient() (*sessionanalytics.OpenAIClient, *sessionanalytics.LLMStageConfig) {
	if !sessionanalytics.NewLLMStageConfig(nil).Enabled() {
		return nil, nil
	}
	analysisBaseURL := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_BASE_URL"))
	analysisAPIKey := strings.TrimSpace(os.Getenv("LLM_GATEWAY_ANALYSIS_API_KEY"))
	allowInsecureLocal := os.Getenv("LLM_GATEWAY_ANALYSIS_ALLOW_INSECURE_LOCAL") == "true"
	if analysisBaseURL == "" || analysisAPIKey == "" {
		return nil, nil
	}
	validatedURL, err := validateAnalysisEndpoint(analysisBaseURL, allowInsecureLocal)
	if err != nil {
		slog.Error("session analytics model client disabled", "error", err)
		return nil, nil
	}
	slog.Info("session analytics model client enabled", "base_url", validatedURL)
	client := sessionanalytics.NewOpenAIClientWithNetworkPolicy(validatedURL, analysisAPIKey, 30*time.Second, allowInsecureLocal)
	return client, sessionanalytics.NewLLMStageConfig(sessionAnalyticsAutoResolver{})
}

// buildSessionSummarizer 构造会话总结器（V2 块与独立闭链共用）。
// 返回 nil = 摘要层降级关闭（元数据/归属/关闭三层不依赖 LLM，照常工作）。
func buildSessionSummarizer(pool *pgxpool.Pool, redisClient *redis.Client) workers.SessionSummarizer {
	if pool == nil {
		return nil
	}
	analysisClient, cfg := newAnalysisLLMClient()
	if analysisClient == nil {
		return nil
	}
	summaryService := sessionsummary.NewSummarizer(pool, redisClient, sessionSummaryLLMAdapter{client: analysisClient})
	summaryService.SetModel(normalizeAnalysisModelAlias(cfg.ModelFor(sessionanalytics.StageSummary)))
	// docs/omni-ref3 A1：读 session_bodies（V2 源，默认开）；per-turn digest
	// 层开启时摘要输入改为逐轮人读摘要。
	if settings.GetPlatformBool("sessions_v2_compression_read", true) {
		summaryService.SetMessageSource(sessionsummary.NewPerTurnDigestSource(pool))
	}
	return sessionSummaryWorkerAdapter{summarizer: summaryService}
}

// assembleSessionCloseHooks 挂载总结完成后的扩展钩子（元数据 final + 项目归属）。
// 归属钩子受 project_attribution.enabled 门控（默认关）；resolver 由 pool 现建
// ——2026-11-20 审计确认此前 deps.ProjectAttrResolver 全仓无装配点，
// 三级推断（rule→inherit→llm）在生产不可达。
func assembleSessionCloseHooks(sumWorker *workers.SessionSummaryWorker, pool *pgxpool.Pool) {
	if sumWorker == nil || pool == nil {
		return
	}
	// 会话元数据 final（session_analysis_metadata status=final；迁移 567 §7）。
	msgSource := sessionsummary.NewV2SessionBodiesSource(pool)
	metaHook := workers.NewSessionMetadataCloseHook(
		sessionmeta.NewMetadataStore(pool),
		msgSource,
		slog.Default(),
	)
	sumWorker.AddCloseHook(metaHook)

	// 项目归属（三级降级推断；写 session_project_attribution，绝不碰计费列）。
	if settings.GetPlatformBool("project_attribution.enabled", false) {
		resolver := projectattr.NewProjectResolver(projectattr.NewPGStoreFromPool(pool))
		hook := projectattr.NewCloseHook(projectattr.NewPGStoreFromPool(pool), nil, slog.Default())
		hook.AttributorFor = func(ctx context.Context, tenantID, gwSessionID string) (*projectattr.Attributor, error) {
			return resolver.BuildAttributor(ctx, tenantID)
		}
		sumWorker.AddCloseHook(hook)
		slog.Info("session analytics close chain: project attribution hook enabled")
	}
	slog.Info("session analytics close chain: session metadata final hook enabled")
}

// startSessionClosedRunLoop 启动 session.closed 事件消费循环。
func startSessionClosedRunLoop(ctx context.Context, pool *pgxpool.Pool, sumWorker *workers.SessionSummaryWorker) {
	if ctx == nil || pool == nil || sumWorker == nil {
		return
	}
	interval := envDuration("LLM_GATEWAY_V2_ANALYSIS_INTERVAL", 5*time.Second)
	batch := envInt("LLM_GATEWAY_V2_ANALYSIS_BATCH", 10)
	poll := bus.NewPGPollFunc(bus.AsPGDB(pool), sumWorker.SubscribedTypes(), batch)
	mark := bus.NewPGMarkFunc(bus.AsPGDB(pool), slog.Default())
	go bus.RunLoop(ctx, sumWorker, poll, mark, bus.LoopConfig{
		Interval:  interval,
		BatchSize: batch,
		Logger:    slog.Default(),
	})
	slog.Info("session analytics close chain: session_summary_worker loop started",
		"interval", interval.String(), "batch_size", batch)
}

// lifecycleWorkerFromSettings 按 session_inspector.* settings 构造回收 worker。
func lifecycleWorkerFromSettings(pool *pgxpool.Pool, publisher bg.SessionClosedPublisher) *bg.SessionLifecycleWorker {
	opts := []bg.SessionLifecycleWorkerOption{
		bg.WithRecycleConfig(
			parseDurationSetting("session_inspector.idle.timeout", 30*time.Minute),
			parseDurationSetting("session_inspector.idle.absolute_max_lifetime", 168*time.Hour),
			parseDurationSetting("session_inspector.idle.cleanup_interval", 5*time.Minute),
			settings.GetPlatformInt("session_inspector.idle.cleanup_batch_size", 500),
			settings.GetPlatformString("session_inspector.idle.recycle_action", "soft_close"),
		),
		bg.WithTenantLimits(
			settings.GetPlatformInt("session_inspector.lifecycle.max_sessions_per_tenant", 1000),
			settings.GetPlatformString("session_inspector.lifecycle.eviction_policy", "lru"),
		),
	}
	if publisher != nil {
		opts = append(opts, bg.WithSessionClosedPublisher(publisher))
	}
	return bg.NewSessionLifecycleWorker(pool, opts...)
}

// parseDurationSetting 读 duration 型 settings（如 "30m"/"168h"），非法值回落 def。
func parseDurationSetting(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(settings.GetPlatformString(key, def.String()))
	if raw == "" {
		return def
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return def
}

// StartSessionAnalyticsCloseChain 启动会话分析闭链。v2ChainStarted=true 表示
// V2 块已启动同款 session.closed 消费循环（不双跑，只补挂 lifecycle worker）。
//
// 返回 lifecycle worker 供优雅停机 Stop；未启动时返回 nil。
func StartSessionAnalyticsCloseChain(pool *pgxpool.Pool, redisClient *redis.Client, v2ChainStarted bool) *bg.SessionLifecycleWorker {
	if pool == nil {
		return nil
	}
	if !settings.IsSessionAnalyticsEnabled() {
		slog.Info("session analytics close chain disabled (session_analytics.enabled=false)")
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	if !v2ChainStarted {
		summarizer := buildSessionSummarizer(pool, redisClient)
		if summarizer == nil {
			slog.Info("session analytics close chain: summary layer disabled (no analysis LLM endpoint); metadata/attribution layers stay active")
		}
		// summarizer 为 nil 时 worker 仅执行 closeHooks（SessionSummaryWorker
		// 2026-11-20 起支持无 summarizer 跑钩子）——元数据 final 与项目归属
		// 不依赖 LLM。
		sumWorker := workers.NewSessionSummaryWorker(summarizer, slog.Default())
		assembleSessionCloseHooks(sumWorker, pool)
		startSessionClosedRunLoop(ctx, pool, sumWorker)
	}

	// 会话关闭判定器（事实源）：session_dim 闲置/超期 → soft_close → 发
	// session.closed。与 MV refresher 同款「所有实例可跑 + 发布层幂等」形态：
	// UPDATE 带 status='active' 守卫幂等，EventID 确定性去重。
	publisher := sessionClosedEventBridge{pub: bus.NewPGPublisher(pool, slog.Default())}
	lifecycle := lifecycleWorkerFromSettings(pool, publisher)
	lifecycle.Start(ctx)
	slog.Info("session analytics close chain: session lifecycle worker started",
		"idle_timeout", parseDurationSetting("session_inspector.idle.timeout", 30*time.Minute).String(),
		"summary_lookback", bg.DefaultSummaryLookback.String())

	// 停机句柄：cancel 与 worker.Stop 挂到包级 shutdown 序列（由 main.go 调）。
	sessionCloseChainCancel = cancel
	return lifecycle
}

// sessionCloseChainCancel 是闭链 context 的停机句柄（StartSessionAnalyticsCloseChain
// 赋值；v2ShutdownPipeline 同期调用）。
var sessionCloseChainCancel context.CancelFunc

// StopSessionAnalyticsCloseChain 优雅停机闭链（main.go shutdown 序列调用）。
func StopSessionAnalyticsCloseChain(lifecycle *bg.SessionLifecycleWorker) {
	if sessionCloseChainCancel != nil {
		sessionCloseChainCancel()
		sessionCloseChainCancel = nil
	}
	if lifecycle != nil {
		lifecycle.Stop()
	}
}
