package reconciler

// crash_budget_coupling_test.go —— 「崩溃预算」与「退避重试预算」的耦合钉测
// （2026-11-25 12h 修订审计轮 P1-1 的落地）。
//
// ★ 这道门守的是**耦合本身**：
//
//	`NewReconciler` 在 `config.Backoff == nil` 时执行
//	`backoffCfg.MaxRetries = config.MaxCrashCount`
//	⇒ 改 MaxCrashCount 会**连带**改掉退避重试预算。
//
// ★ 为什么这道门必须存在（审计轮的原始发现）：
//
//	生产装配面（plugin-runtime/controller/reconciler_budget.go）把
//	MaxCrashCount 从默认的 10 收成 3，**注释只说了「3 attempts before failed」**
//	⇒ 看注释的人不会知道**重启预算也从 10 变成了 3**。
//	⇒ 于是「改 MaxCrashCount」这个动作在两处旋钮上同时生效，而只有一处有注释。
//	⇒ 本门把耦合**写成可执行的断言**：谁想拆掉这个耦合（让两者可独立设置），
//	先来改本门，并同时把 reconciler_budget.go 的注释改对。
//
// ★ 变异的自证（审计轮已实跑）：把 `backoffCfg.MaxRetries = config.MaxCrashCount`
//   改成写死 10 ⇒ 本门红。

import (
	"context"
	"testing"
)

// TestMaxCrashCountDrivesBackoffBudget —— 钉住耦合：MaxCrashCount 即重试预算。
func TestMaxCrashCountDrivesBackoffBudget(t *testing.T) {
	const budget = 3
	r := NewReconciler(&Config{MaxCrashCount: budget}, newMockSupervisor(), newMockRegistry()).(*reconciler)
	if got := r.backoffTracker.config.MaxRetries; got != budget {
		t.Errorf("退避重试预算 = %d，期望 %d —— ★ MaxCrashCount 与 MaxRetries 是同一条线上的"+
			"两个旋钮（Backoff==nil 时后者被同步成前者）★ 拆开它们之前，"+
			"请先确认「禁用门槛」与「重启预算」不需要再各自独立",
			got, budget)
	}
}

// TestExplicitBackoffKeepsItsOwnRetries —— 钉住「显式给了 Backoff 时不参与同步」。
//
// ★ 这是耦合的另一半：不设 Backoff 时两者同步；显式设了 Backoff 时
//
//	MaxRetries 以 Backoff 自己的值为准（见 NewReconciler 的 if 分支）。
//	⇒ 钉住它，是为了防止「后来有人把 else 分支也改成同步」造成
//	**显式配置被静默覆盖**（那种改法会让调用方以为自己设了 20 次重试，
//	实际却是 3 次）。
func TestExplicitBackoffKeepsItsOwnRetries(t *testing.T) {
	const explicit = 20
	r := NewReconciler(&Config{
		MaxCrashCount: 3,
		Backoff:       &BackoffConfig{Policy: BackoffExponential, InitialWait: 1, MaxWait: 2, MaxRetries: explicit},
	}, newMockSupervisor(), newMockRegistry()).(*reconciler)
	if got := r.backoffTracker.config.MaxRetries; got != explicit {
		t.Errorf("显式 Backoff 的 MaxRetries = %d，期望 %d —— 显式配置不得被 MaxCrashCount 覆盖",
			got, explicit)
	}
}

// TestDefaultCrashBudgetRemainsTen —— 钉住「默认仍是 10」。
//
// ★ 为什么连默认值也要钉：插件运行时 controller 那一侧已经显式设成 3，
//
//	但 reconciler 包的**默认值**仍被别处使用（任何直接 new Reconciler 的调用方）。
//	⇒ 若有人把默认值改成 3，会连带改掉那些没显式配置的调用方的行为
//	（**隐式行为变更**，没有任何调用点会亮红）。
func TestDefaultCrashBudgetRemainsTen(t *testing.T) {
	r := NewReconciler(&Config{}, newMockSupervisor(), newMockRegistry()).(*reconciler)
	if got := r.config.MaxCrashCount; got != 10 {
		t.Errorf("默认 MaxCrashCount = %d，期望 10 —— 改默认值会让所有未显式配置的调用方隐式变化",
			got)
	}
	if got := r.backoffTracker.config.MaxRetries; got != 10 {
		t.Errorf("默认退避重试预算 = %d，期望 10", got)
	}
}

// TestCrashBudgetUnusedContextGuard —— 保证本文件用到 context（编译期自检）。
//
// ★ 纯占位：确保 import 不被裁掉；若将来本文件新增需要 ctx 的断言，
//
//	这一行可以直接复用。
var _ = context.Background
