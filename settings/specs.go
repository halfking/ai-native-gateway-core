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
	// 会话全景分析模块主开关（admin/modules 统一管理）→ platform 范围。
	out = append(out, SessionAnalyticsSpecs()...)
	// 2026-07-13: 数据生命周期相关配置
	out = append(out, LifecycleSpecs()...)
	return out
}

// TenantSpecs returns all tenant-scoped Specs registered in Phase 1.
func TenantSpecs() []*Spec {
	out := RateLimitTenantSpecs()
	// 会话全景分析模块的租户级调优配置（model/strategy/cluster 等）。
	out = append(out, SessionAnalyticsTenantSpecs()...)
	return out
}
