package settings

// SessionSpecs contains session-related platform settings.
func SessionSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "session.id_body_keys",
			Type:            TypeString,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         "",
			Description:     "请求体会话别名键",
			DescriptionLong: "用于从前端请求体中提取 session_id 的附加 key 名称，逗号分隔；会与默认别名合并，修改后立即生效。",
			Unit:            "csv",
			DangerLevel:     Warning,
			HotReload:       true,
			Observability:   "/api/admin/settings/session.id_body_keys",
		},
	}
}
