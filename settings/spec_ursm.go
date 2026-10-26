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
			Key:     "ursm.healthy_reconcile_interval_seconds",
			Type:    TypeInt,
			EnvName: EnvNameAuto("ursm.healthy_reconcile_interval_seconds"),
			Scope:   ScopePlatform,
			Default: 60,
			// ★ 2026-10-22 12h 审计轮（B2）：Min 由 0 改为**不设下限**。
			//   文档与读方都写「设成 0 **或负数** ⇒ 停机」，而 `Min: floatPtr(0)`
			//   会让 `Spec.Validate` 拒掉一切负值（admin/settings.go 的 PUT 校验）
			//   ⇒ ⇒ 文档承诺的「负数」那条路**在管理端不可达**。
			//   ⇒ ⇒ 与读方语义对齐：协调器判的是 `<= 0` ⇒ 管理端也必须能写进去。
			Min:      nil,
			Max:      floatPtr(86400),
			Category: CategoryGeneral,
			// ★★ 2026-10-22 12h 审计轮（B2）：**HotReload 由 true 改为 false**。
			//
			//   原判词「HotReload: true」是**假的**：读方
			//   `bg/ursm_healthy_reconcile.go` 只在**构造时**读一次
			//   （NewUrsmlHealthyKeyReconciler ⇒ time.NewTicker(r.interval)），
			//   运行中的 ticker **不会**再读这个键。
			//   ⇒ ⇒ 管理端 PUT 成功、UI 显示「可热重载」，但**协调器要等进程重启才停**
			//   ⇒ ⇒ 而这正是 kill switch 存在的场景（154 全量停摆事故）。
			//   ⇒ ⇒ 诚实的做法是**如实标注**为「需重启」，而不是给一个假的承诺。
			//   ★ 真正的热重载修法（按 tick 重读，与 resolvePromoteConfig 同款）本轮未做，
			//     登记为待办 —— 那会改运行中 ticker 的周期，属于行为变更。
			HotReload:   false,
			Description: "URSM 健康键协调器周期（秒），≤0 表示停用",
			DescriptionLong: "URSM v2 的 healthy-key reconciler 周期（秒）。" +
				"它周期性把「DB 判定为健康的路由对」恢复到节点状态。" +
				"★ **运维 kill switch**：把本键设成 0 或负数 ⇒ 协调器停机" +
				"（Start 会打日志并直接返回）。" +
				"★ 读方：bg/ursm_healthy_reconcile.go:95（fallback 60 秒）。" +
				"★★ **改动需重启进程才生效**（协调器只在启动时读一次 ⇒ 不热重载）。",
			Unit: "秒",
		},
	}
}
