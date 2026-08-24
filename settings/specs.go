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
	// 2026-08-19: read-only legacy/canonical statistics shadow comparison.
	out = append(out, StatsShadowSpecs()...)
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
	// 2026-08-07: V2 session read-path master switch (docs/omni-ref3 A1).
	// Platform-scoped, default on, kill-switch via admin/platform settings.
	out = append(out, SessionsV2CompressionPlatformSpecs()...)
	// 2026-08-11: V2 多层队列调度（模型/凭据队列 + 并发模式削峰 + 分层故障转移）。
	out = append(out, DispatchSpecs()...)
	// 2026-08-17: per-credential, per-client quota enforcement (FP-slot + concurrency).
	// Parked (AUDIT_24H_20260817.md B1): the package has zero production
	// importers and no code reads credential_client_quota.mode. Surfacing a
	// no-op knob lets operators believe enforcement is "shadow"-on when nothing
	// is recorded. Re-register when the dispatch path is actually wired.
	// out = append(out, CredentialClientQuotaSpecs()...)
	// 2026-08-20: 项目归属（LLM 推断）平台级主开关，默认关闭。
	out = append(out, ProjectAttributionSpecs()...)
	// 2026-08-24: gateway prompt budget, hot-reloadable through admin settings.
	out = append(out, GatewaySpecs()...)
	return out
}

// TenantSpecs returns all tenant-scoped Specs registered in Phase 1.
func TenantSpecs() []*Spec {
	out := RateLimitTenantSpecs()
	// 会话全景分析模块的租户级调优配置（model/strategy/cluster 等）。
	out = append(out, SessionAnalyticsTenantSpecs()...)
	return out
}
