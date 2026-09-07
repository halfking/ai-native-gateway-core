package routingopt

import (
	"context"
	"time"
)

// Options gates the individual RealOptimizer hooks and bounds their latency.
//
// 背景（2026-09-07 并发审计 P1）：ROUTING_OPT_* 子开关此前只被打印进日志，
// 从未注入优化器 —— 关掉 MODEL_RECOMMENDATION 或 FEEDBACK_INTEGRATION 后
// 对应 hook 照常执行；MaxPluginLatencyMs 无任何消费方。Options 由
// cmd/gateway 从 settings.RoutingOptFeatureFlags 映射而来，在各 hook 入口
// 短路，使 flag 语义与文档一致。
type Options struct {
	// EnableClassificationEnhancement gates PreClassify. false → 返回原始
	// signals，不触 DB。
	EnableClassificationEnhancement bool

	// EnableModelRecommendation gates RecommendModel. false → 原样返回候选。
	EnableModelRecommendation bool

	// EnableFeedbackIntegration gates RecordFeedback. false → 丢弃反馈。
	EnableFeedbackIntegration bool

	// LoadUserAffinity controls whether Enhance queries routing_user_affinity.
	// 默认 false：Classifier 目前不消费 EnhancedSignals（审计：每次 auto 请求
	// 白查一次 DB，结果被整体丢弃），待 Week 2 把增强字段接入分类器后再开。
	LoadUserAffinity bool

	// HookTimeout bounds each hook (PreClassify / PostClassify /
	// RecommendModel) via context timeout. 0 → 不加超时（沿用请求 ctx）。
	// 设计目标 P99 ≤ 10ms（ROUTING_OPT_MAX_PLUGIN_LATENCY_MS）。
	HookTimeout time.Duration

	// ControlledSyncFallback keeps the legacy synchronous feedback INSERT
	// path (P2.2 Track B): true → RecordFeedback 走旧的单条同步 INSERT；
	// false（默认）→ 非阻塞入队异步批量写入（pgx.Batch，满 100 条或每 5s
	// 刷盘，队列 10000，满则丢弃计数）。接线点：NewRealOptimizerWithOptions
	// 应把它传给 FeedbackIntegrator.SetControlledSyncFallback。
	ControlledSyncFallback bool
}

// DefaultOptions returns the flag semantics documented in
// settings.RoutingOptFeatureFlags: 子 hook 全开（跟随主开关）、不查亲和度、
// hook 超时 10ms。
func DefaultOptions() Options {
	return Options{
		EnableClassificationEnhancement: true,
		EnableModelRecommendation:       true,
		EnableFeedbackIntegration:       true,
		LoadUserAffinity:                false,
		HookTimeout:                     10 * time.Millisecond,
		ControlledSyncFallback:          false,
	}
}

// hookContext wraps ctx with the configured hook timeout (if any). The
// returned cancel is always non-nil and must be deferred by the caller.
func (o Options) hookContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if o.HookTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, o.HookTimeout)
}
