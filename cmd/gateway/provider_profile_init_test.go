package main

import (
	"os"
	"testing"

	"github.com/kaixuan/llm-gateway-go/settings"
)

// ⚠️ 这组判据钉的是 2026-10-13 真机发现的「费用对账页永远空」：
//   对帐装配被写在 initProviderProfile 内部，而该函数在
//   `LLM_GATEWAY_PROVIDER_PROFILE_ENABLED` 未开时整个返回 nil
//   ⇒ `/api/admin/provider-cost-reconciliation*` **永远不注册**
//   ⇒ 移动端那一页对 super_admin 一直可见、点进去必然 404。
//
// 修法是对账**只**受自己的子开关控制（见 initCostReconciliation）。
// 这组测试能守住的是「开关语义」；**守不住的**是「main.go 是否在
// RegisterRoutes 之前调用了它」—— 那是 cmd/gateway 的装配顺序，
// 没有 DB 就测不了，只能靠部署后 curl 端点做端到端判据。
// 这一点写在这里，免得下一个人误以为单测已经覆盖了装配。

func TestCostReconciliationEnabledPrefersEnvOverSettings(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED", "true")
	if !costReconciliationEnabled() {
		t.Fatal("env=true 时 costReconciliationEnabled() 应为 true")
	}

	t.Setenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED", "false")
	if costReconciliationEnabled() {
		t.Fatal("env=false 时 costReconciliationEnabled() 应为 false（env 必须压过 settings）")
	}
}

// 无效取值必须**退回关闭**，不能当成开启。
// ⚠️ 这条不是洁癖：开关一旦解析失败被当成 true，就会启动一个会写
// provider_cost_reconciliation、并在导入账单后发 diff 告警的后台 worker。
func TestCostReconciliationEnabledRejectsGarbageValue(t *testing.T) {
	t.Setenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED", "yes-please")
	if costReconciliationEnabled() {
		t.Fatal("无法解析的取值必须按关闭处理")
	}
}

// ⚠️ 这条判据在 2026-10-13 被**推翻重写**过。原来的写法是：
//
//	_ = os.Unsetenv("…ENABLED")
//	if costReconciliationEnabled() { t.Skip("settings 被别处置为 true，跳过") }
//
// 它有**两个致命问题**，都不是「写得不够严」而是「没有失败模式」：
//
//  1. **结构上不可能变红**：唯一的分支是 t.Skip，没有 Fatal/Error。
//     实测把 `costReconciliationEnabled` 末尾的 settings 回退整段换成
//     `return false`，它照样 `ok`。
//  2. **名不副实**：名字叫 FallsBackToSettingsWhenEnvAbsent，
//     原注释却明说「不依赖任何 settings 写入」—— 它**从没测过 settings 回退**，
//     只测了「默认值恰好是 false」。
//
// ⇒ 一条恒真的判据比没有判据更坏：它会让人以为「settings 优先级已被守住」。
//
// 重写后钉的是真正想守住的东西：**env 缺席时，取值必须来自 settings**。
// 注入口用 `settings.Global` 的 Spec.Default（settings.EffectiveValue 的
// 第三级回落，见 settings/spec.go:284），不碰 DB backend，因此不污染同包用例；
// 但仍用 t.Cleanup 还原 Global，避免影响后续用例。
func TestCostReconciliationFallsBackToSettingsWhenEnvAbsent(t *testing.T) {
	const key = "provider_profile.cost_reconciliation.enabled"

	prev := settings.Global
	t.Cleanup(func() { settings.Global = prev })

	cases := []struct {
		name        string
		settingsDef bool
		want        bool
	}{
		// ⭐ 这两格是判据的牙：settings 侧给什么，开关就必须是什么。
		// 只有「settings 回退被摘掉」或「读错 key」才会让它们转红。
		{name: "settings 开启 ⇒ 开关开启", settingsDef: true, want: true},
		{name: "settings 关闭 ⇒ 开关关闭", settingsDef: false, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := settings.NewRegistry()
			registry.MustRegisterSpec(&settings.Spec{
				Key:     key,
				Scope:   settings.ScopePlatform,
				Type:    settings.TypeBool,
				Default: tc.settingsDef,
			})
			settings.Global = registry

			// env 必须缺席，否则根本走不到 settings 这一级。
			t.Setenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED", "")
			if got := os.Getenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED"); got != "" {
				t.Fatalf("前置条件不成立：env 未清空（%q），本用例会被 env 抢走", got)
			}
			// 真正清掉：t.Setenv 设空值后 LookupEnv 仍然返回 ok=true，
			// 而实现用的是 LookupEnv（存在即优先），所以必须显式 Unsetenv。
			os.Unsetenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED")

			if got := costReconciliationEnabled(); got != tc.want {
				t.Fatalf("env 缺席、settings 默认 %v 时，开关应为 %v，实际 %v",
					tc.settingsDef, tc.want, got)
			}
		})
	}
}
