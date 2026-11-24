package controller

// reconciler_budget.go —— plugin 运行时 reconciler 的「崩溃预算」配置
// （2026-11-25 12h 修订审计轮 P1-1 的落地）。
//
// # 为什么要单独抽一个文件
//
// W2 那笔改动把 `MaxCrashCount: 3` 直接写在 Start() 的调用点里 ⇒
//
//	① **没有任何钉测**钉住这个数字（改回 10 或改成别的，门一律绿）；
//	② 更要紧的是：**这个数字不只决定「崩溃几次后禁用」**，
//	  它**连带决定了退避重试预算**（见下面「连带效应」）。
//
// # ★ 连带效应（本文件存在的理由）
//
// `reconciler.NewReconciler` 在 `config.Backoff == nil`（生产路径正是这个形态）
// 时执行：
//
//	backoffCfg.MaxRetries = config.MaxCrashCount
//
// ⇒ **MaxCrashCount 从 10 改成 3，等于把退避重试预算也从 10 改成 3**：
// 一个插件在退避里会被重启的次数，从 10 次变成 3 次；3 次用尽即进入
// ActionDisable（永久禁用）。
//
// ★ 原注释只写了「3 attempts before failed」，**没有披露这条连带收紧**
//   ⇒ 只看注释的人会以为「只是把禁用门槛提前了」，不知道重启预算也一起缩了。
//   ⇒ 本文件把两件事写在同一处，并用两道钉测各自钉住（见下）。
//
// # 三条纪律
//
//	① **改一个就要改另一个**：动 MaxCrashCount 前先看这里（本文件 + reconciler
//	   那侧的耦合钉测），因为它们是一条线上的两个旋钮。
//	② **没有需求文档就不改数字**：这个 3 来自 W2 的口头「需求」，仓内无成文出处。
//	   ⇒ 本文件把「出处未成文」这件事本身写成注释，避免后来人以为它有权威依据。
//	③ **不要用「唯一」这类排他断言**描述本配置：它是**当前**取值，不是不可改的定值。

import (
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/reconciler"
)

// pluginCrashBudget 是插件在退避窗口内允许的崩溃次数上限。
//
// ★ 出处：W2 轮的口头要求「3 attempts before failed」——★ **仓内没有成文的需求文档**
//
//	（审计轮已登记该缺口）⇒ 改这个数字前，先把需求补成文。
//
// ★ 连带效应（★ 极易踩坑 ★）：它同时是**退避重试预算**
//
//	（reconciler.NewReconciler 在 Backoff==nil 时把 MaxRetries 同步成它）
//	⇒ 从默认的 10 收到 3，意味着「插件崩溃后被自动重启的次数」也从 10 降到 3。
const pluginCrashBudget = 3

// reconcilerConfig 构造 plugin 运行时 reconciler 的配置。
//
// ★ 单独抽成函数（而不是写在 NewReconciler 的实参里）是为了**可测**：
//
//	钉测能直接断言这里给出的数字，而不必去起整个 controller（要 socket、
//	要 eventbus、要容器）。
//
// ★ 附带钉住一条不变式：调用方**不得**自己塞 Backoff（Backoff != nil 时
//
//	reconciler 不再把 MaxRetries 同步成 MaxCrashCount ⇒ 两条旋钮会分叉，
//	而分叉的后果是「禁用门槛 3 次、重启预算 10 次」这种半同步状态）。
//	本函数不暴露 Backoff 字段，调用方只能拿到我们给的这份配置。
func reconcilerConfig(options Options) reconciler.Config {
	return reconciler.Config{
		Interval: options.Interval,
		// ★ 见 pluginCrashBudget 的注释：这一行同时决定「禁用门槛」与
		//   「退避重试预算」两个数（后者由 reconciler 侧同步而来）。
		MaxCrashCount: pluginCrashBudget,
	}
}
