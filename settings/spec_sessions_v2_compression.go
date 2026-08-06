package settings

// SessionsV2CompressionTenantSpecs returns tenant-scoped controls for V2 compression reads.
func SessionsV2CompressionTenantSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "sessions_v2_compression_read",
			Type:        TypeBool,
			Scope:       ScopeTenant,
			Category:    CategoryCompression,
			Default:     false,
			Description: "Enable V2 session compression (read from session_turns + session_bodies)",
			HotReload:   true,
		},
	}
}
