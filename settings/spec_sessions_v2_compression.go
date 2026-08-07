package settings

// SessionsV2CompressionPlatformSpecs returns the platform-scoped master switch
// for V2 session reads (compression + summarizer together).
//
// docs/omni-ref3 A1 — decision (2026-08-07): compress + summary cut over
// together, DEFAULT ON, no per-tenant canary (user count is small). This is a
// kill-switch: set it to false via admin/platform settings to hot-reload V1
// reads back immediately without a redeploy. Both shouldUseV2
// (domains/hooks/compression) and the summarizer's SetMessageSource wiring
// (cmd/gateway/main_pipeline) read this single platform key.
//
// Previously this was a tenant-scoped key defaulting to false, which (a)
// couldn't be set platform-wide and (b) left shouldUseV2 unable to honour a
// kill-switch because GetPlatformBool reads the platform scope. The tenant
// scope was never actually consumed — shouldUseV2 ignored its tenantID arg —
// so widening to platform loses nothing.
func SessionsV2CompressionPlatformSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "sessions_v2_compression_read",
			EnvName:     "LLM_GATEWAY_SESSIONS_V2_COMPRESSION_READ",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryCompression,
			Default:     true,
			Description: "Read session state (compression + summaries) from V2 (session_bodies) instead of V1 (request_logs). Default on; set false to revert to V1.",
			HotReload:   true,
			DangerLevel: Warning,
		},
	}
}

