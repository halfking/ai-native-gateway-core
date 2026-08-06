package settings

// SessionsV2CompressionSpecs returns the feature flag for V2 session compression reads.
//
// This is platform-scoped because SessionCompressor reads it via the
// platform-level settings.GetPlatformBool helper. Promoting it to tenant
// scope requires a per-tenant resolver wired into the compressor hot path.
func SessionsV2CompressionSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "sessions_v2_compression_read",
			Type:        TypeBool,
			Scope:       ScopePlatform,
			Category:    CategoryCompression,
			Default:     false,
			Description: "Enable V2 session compression (read from session_turns + session_bodies)",
			HotReload:   true,
		},
	}
}
