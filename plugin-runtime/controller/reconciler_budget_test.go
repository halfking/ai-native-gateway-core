package controller

// reconciler_budget_test.go —— 插件「崩溃预算」配置的钉测
// （2026-11-25 12h 修订审计轮 P1-1 的落地）。
//
// ★ 这道门守的是**生产装配面**的那一个数字：
//   `pluginCrashBudget`（当前 3）——它是「插件崩几次后被禁用」，
//   并且**连带**决定「插件在退避里被重启几次」（见 reconciler_budget.go）。
//
// ★ 为什么这道门必须有（审计轮的原始发现）：
//
//	该数字原先**只存在于 Start() 的调用点实参里**，全仓没有任何断言钉住它
//	⇒ 有人把它改回 10、或改成别的值，**门一律绿** ⇒ 生产行为静默变化。
//
// ★ 变异的自证（审计轮已实跑）：把 pluginCrashBudget 改成 10 ⇒ 本门红。

import "testing"

// TestPluginCrashBudgetIsThree —— 钉住崩溃预算的**当前取值**。
//
// ★ 为什么「3」这个数也要钉：它来自口头要求、无成文需求文档
//
//	⇒ 一旦有人「顺手优化」成别的值（例如为了减少禁用而调大），
//	**没有任何机制会告诉他这一改同时把退避重试预算也改了**。
//	⇒ 把值钉住，是为了让「改这个数」变成一个**必须显式改门**的动作。
func TestPluginCrashBudgetIsThree(t *testing.T) {
	if pluginCrashBudget != 3 {
		t.Fatalf("插件崩溃预算 = %d，期望 3 —— 改这个数会同时改「禁用门槛」与"+
			"「退避重试预算」两个旋钮（见 reconciler_budget.go），必须同步改本钉测与成文需求",
			pluginCrashBudget)
	}
}

// TestReconcilerConfigCarriesCrashBudget —— 钉住**配置真的被装配出去**。
//
// ★ 只断言常量是不够的：常量对了、但 Start() 没用它（比如调用方另写了一份
//
//	reconciler.Config），门照样绿 ⇒ 必须在「配置构造函数」这一层断言。
func TestReconcilerConfigCarriesCrashBudget(t *testing.T) {
	cfg := reconcilerConfig(Options{})
	if cfg.MaxCrashCount != pluginCrashBudget {
		t.Errorf("reconcilerConfig 的 MaxCrashCount = %d，期望 %d",
			cfg.MaxCrashCount, pluginCrashBudget)
	}
}

// TestReconcilerConfigPassesInterval —— 钉住 interval 仍然由调用方决定。
//
// ★ 这是「不许把配置写死」的对侧钉测：只钉 MaxCrashCount 的话，
//
//	有人把 Interval 也写死成常量，本门不会发现 ⇒ 而 Interval 是
//	**运维可调**的（Options.Interval）。
func TestReconcilerConfigPassesInterval(t *testing.T) {
	const interval = 7
	cfg := reconcilerConfig(Options{Interval: interval})
	if cfg.Interval != interval {
		t.Errorf("reconcilerConfig 的 Interval = %v，期望 %v", cfg.Interval, interval)
	}
}
