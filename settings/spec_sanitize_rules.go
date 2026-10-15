package settings

const (
	KeySanitizeRulesRefreshIntervalSeconds = "sanitize.rules.refresh_interval_seconds"
	KeySanitizeRulesAuditEnabled           = "sanitize.rules.audit_enabled"
	KeySanitizeRulesEventLimit             = "sanitize.rules.event_limit"
)

// SanitizeRulesSpecs exposes the managed-rule operational controls in module parameters.
func SanitizeRulesSpecs() []*Spec {
	return []*Spec{
		{Key: KeySanitizeRulesRefreshIntervalSeconds, EnvName: EnvNameAuto(KeySanitizeRulesRefreshIntervalSeconds), Type: TypeInt, Scope: ScopePlatform, Category: CategoryModules, Default: 5, Min: floatPtr(1), Max: floatPtr(300), Description: "脱敏规则刷新周期", DescriptionLong: "各网关检查数据库规则版本的间隔；发布通知丢失后由周期检查补齐。修改后后台自动读取。", Unit: "秒", DangerLevel: Warning, HotReload: true, Observability: "/admin/sanitize-rules"},
		{Key: KeySanitizeRulesAuditEnabled, EnvName: EnvNameAuto(KeySanitizeRulesAuditEnabled), Type: TypeBool, Scope: ScopePlatform, Category: CategoryModules, Default: true, Description: "脱敏规则命中审计", DescriptionLong: "异步记录脱敏、阻断、语法豁免及待确认命中的结构摘要，供人工复核；不存储请求原文或秘密真值。关闭后规则仍然执行。", DangerLevel: Warning, HotReload: true, Observability: "/admin/sanitize-rules"},
		{Key: KeySanitizeRulesEventLimit, EnvName: EnvNameAuto(KeySanitizeRulesEventLimit), Type: TypeInt, Scope: ScopePlatform, Category: CategoryModules, Default: 10000, Min: floatPtr(100), Max: floatPtr(100000), Description: "脱敏规则审计保留量", DescriptionLong: "按最后命中时间保留的结构聚合记录数量；达到上限后淘汰较旧记录。人工反馈只有发布关联规则版本后才影响运行策略。", Unit: "条", DangerLevel: Warning, HotReload: true, Observability: "/admin/sanitize-rules"},
	}
}
func SanitizeRulesRefreshIntervalSeconds() int {
	return clampThresholdInt(GetPlatformInt(KeySanitizeRulesRefreshIntervalSeconds, 5), 1, 300)
}
func SanitizeRulesAuditEnabled() bool { return GetPlatformBool(KeySanitizeRulesAuditEnabled, true) }
func SanitizeRulesEventLimit() int {
	return clampThresholdInt(GetPlatformInt(KeySanitizeRulesEventLimit, 10000), 100, 100000)
}
