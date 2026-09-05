package settings

// CredentialClientQuotaSpecs returns the platform-scoped rollout mode for
// per-credential, per-client quota enforcement. Default is shadow: every
// dispatch decision records would-block without refusing requests.
func CredentialClientQuotaSpecs() []*Spec {
	return []*Spec{
		{
			Key:             "credential_client_quota.mode",
			Type:            TypeEnum,
			Scope:           ScopePlatform,
			Category:        CategoryRateLimit,
			Options:         []string{"off", "shadow", "enforce"},
			Default:         "shadow",
			Description:     "凭证客户端配额运行模式",
			DescriptionLong: "off=关闭；shadow=只计算并记录命中、不拒绝请求；enforce=按凭证与客户端类型强制执行并发和 FpSlot 配额。",
			DangerLevel:     Dangerous,
			HotReload:       true,
		},
	}
}
