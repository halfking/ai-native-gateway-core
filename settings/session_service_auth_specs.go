package settings

// SessionServiceAuthSpecs returns the platform-scoped service-auth gate for
// session-manager -> Gateway analytics calls.
func SessionServiceAuthSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "session_service_auth.enabled",
			EnvName:         "LLM_GATEWAY_SESSION_SERVICE_JWT_ENABLED",
			Type:            TypeBool,
			Scope:           ScopePlatform,
			Category:        CategorySession,
			Default:         false,
			Description:     "启用会话服务 JWT",
			DescriptionLong: "启用后 session analytics 路由接受 Gateway 专用 service JWT；关闭或缺少 secret 时回退原 Admin middleware。",
			HotReload:       true,
			DangerLevel:     Dangerous,
		},
	}

}
