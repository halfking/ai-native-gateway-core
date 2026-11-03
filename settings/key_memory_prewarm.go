package settings

// memora 上下文预热注入（P1-2③）的 settings 键。
// 读者：internal/memoraprewarm/wiring.go（SyncGateFromSettings）。
const (
	// KeyMemoryPrewarmEnabled 是平台级总开关（默认 false）。
	KeyMemoryPrewarmEnabled = "memory.prewarm.enabled"
	// KeyMemoryPrewarmTenantAllowlist 是租户灰度名单（逗号分隔；空=全平台）。
	KeyMemoryPrewarmTenantAllowlist = "memory.prewarm.tenant_allowlist"
)
