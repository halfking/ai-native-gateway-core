package settings

// PlatformSpecs returns all platform-scoped Specs registered in Phase 1.
// New Specs added in Phase 2 (continuous migration) are appended here.
func PlatformSpecs() []*Spec {
	out := []*Spec{}
	out = append(out, CompressionSpecs()...)
	out = append(out, DisguiseSpecs()...)
	out = append(out, SessionSpecs()...)
	out = append(out, RateLimitPlatformSpecs()...)
	out = append(out, PassthroughSpecs()...)
	out = append(out, ModuleSpecs()...)
	out = append(out, LogSpecs()...)
	out = append(out, StorageSpecs()...)
	out = append(out, SessionAuditSpecs()...)
	out = append(out, ProbeSpecs()...)
	out = append(out, SelfCheckSpecs()...)
	// 2026-07-13: 错误触发的主动探测（连续失败立即直连上游探测）
	out = append(out, ErrorProbeSpecs()...)
	// 2026-07-15: shadow-only routing state, probe, and capability coordinators.
	out = append(out, RoutingStateSpecs()...)
	out = append(out, LifecycleSpecs()...)
	out = append(out, SessionAnalyticsSpecs()...)
	out = append(out, DashboardSpecs()...)
	// 2026-07-17: Sessions V2 feature flags (Migration 430)
	out = append(out, SessionsV2Specs()...)
	// 2026-07-18: dedicated session-manager service JWT gate.
	out = append(out, SessionServiceAuthSpecs()...)
	// 2026-07-26: Provider Profile System (供应商画像系统) - 7-dimension provider quality scoring.
	out = append(out, ProviderProfileSpecs()...)
	// 2026-08-06: 即时会话总结 pipeline 的运行时常量
	out = append(out, AutoSummarySpecs()...)
	// 2026-08-06: Model Quality Monitoring (模型质量监控) - MMLU benchmark testing for featured models
	out = append(out, ModelQualitySpecs()...)
	return out
}

// TenantSpecs returns all tenant-scoped Specs registered in Phase 1.
func TenantSpecs() []*Spec {
	out := RateLimitTenantSpecs()
	// 会话全景分析模块的租户级调优配置（model/strategy/cluster 等）。
	out = append(out, SessionAnalyticsTenantSpecs()...)
	return out
}
