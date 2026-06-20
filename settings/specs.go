package settings

// PlatformSpecs returns all platform-scoped Specs registered in Phase 1.
// New Specs added in Phase 2 (continuous migration) are appended here.
func PlatformSpecs() []*Spec {
	out := []*Spec{}
	out = append(out, CompressionSpecs()...)
	out = append(out, RateLimitPlatformSpecs()...)
	return out
}

// TenantSpecs returns all tenant-scoped Specs registered in Phase 1.
func TenantSpecs() []*Spec {
	return RateLimitTenantSpecs()
}
