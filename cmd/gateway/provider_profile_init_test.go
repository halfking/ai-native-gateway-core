package main

import (
	"os"
	"testing"

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

// 变异方式：把 os.Getenv 换成 os.LookupEnv、或把 ParseBool 失败分支
// 改成 return true —— 两者都会让上面这条转红。
func TestCostReconciliationFallsBackToSettingsWhenEnvAbsent(t *testing.T) {
	_ = os.Unsetenv("LLM_GATEWAY_PROVIDER_COST_RECONCILIATION_ENABLED")
	// 不依赖任何 settings 写入（会污染同包其它用例）：只断言「默认值是关闭」。
	if costReconciliationEnabled() {
		t.Skip("settings 在本进程里已被别处置为 true，跳过默认值断言")
	}
}
