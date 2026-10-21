package settings

// UrsmSpecs 返回 URSM v2（上游路由状态机）相关的平台级设置。
//
// ★ 2026-10-21 R72f（属主决策项 #10 的前半）新增：
// `ursm.healthy_reconcile_interval_seconds` 此前**只出现在读方**
// （bg/ursm_healthy_reconcile.go:95，键名是包内常量
// `ursmHealthyReconcileSettingsKey`）⇒ 从未 Register 成 spec
// ⇒ ⇒ `settings.Global.Spec(key)` 返回 nil ⇒ `GetPlatformInt` 直接回退
// ⇒ ⇒ **往 settings_kv 里写这个键完全无效**（静默无效，不报错）。
// ⇒ ⇒ 而它是**运维 kill switch**（值 ≤ 0 ⇒ 协调器停机）⇒ ⇒ 一个
// 「写不进去的 kill switch」比「没有 kill switch」更危险。
func UrsmSpecs() []*Spec {
	return []*Spec{
		{
			Key:         "ursm.healthy_reconcile_interval_seconds",
			Type:        TypeInt,
			EnvName:     EnvNameAuto("ursm.healthy_reconcile_interval_seconds"),
			Scope:       ScopePlatform,
			Default:     60,
			Min:         floatPtr(0),
			Max:         floatPtr(86400),
			Category:    CategoryGeneral,
			HotReload:   true,
			Description: "URSM 健康键协调器周期（秒），≤0 表示停用",
			DescriptionLong: "URSM v2 的 healthy-key reconciler 周期（秒）。" +
				"它周期性把「DB 判定为健康的路由对」恢复到节点状态。" +
				"★ **运维 kill switch**：把本键设成 0 或负数 ⇒ 协调器停机" +
				"（Start 会打日志并直接返回）。" +
				"★ 读方：bg/ursm_healthy_reconcile.go:95（fallback 60 秒）。",
			Unit: "秒",
		},
	}
}
