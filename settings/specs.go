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
	return out
}

// TenantSpecs returns all tenant-scoped Specs registered in Phase 1.
func TenantSpecs() []*Spec {
	out := RateLimitTenantSpecs()
	out = append(out, SessionAnalyticsSpecs()...)
	return out
}
