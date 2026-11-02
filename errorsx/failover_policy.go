package errorsx

import "time"

// failover_policy.go —— 2026-11-02 第十六轮（F-4 死代码清理）之后的**存活**部分。
//
// ★ 本文件在第十六轮之前还住着 DecideFailover / FailoverDecision /
//   IsUpstreamAuthFailure 与四个冷却常量（DefaultRateLimitCooldown /
//   DefaultTransientCooldown / DefaultFrontendWait / DefaultProbeFanout）。
//   它们**全部生产零引用**（只在 errorsx 包内被测）⇒ 从第四轮起它们就是
//   「按同口径维护着的死代码」：
//
//	维护它们是为了「将来接线时不带回头的老口径」，但代价是**每一次**改归责
//	矩阵都要同步改一份没人用的分支——第十一轮（F-C）踩的就是这个坑：
//	「回写 kind 必须逐点核对所有按 kind 字符串分档的消费点」，而这份死代码
//	本身就是其中之一（它按 kind 分档决定扇不扇出探针）。
//	⇒ 第七轮登记为死代码，第十六轮删除。
//
// ★ 留下的只有**活**的那部分：
//   · FailoverScope —— FailoverScopeOf（penalty.go）的返回类型；
//   · DefaultOverloadRetryDelay —— domains/streaming/executors/executor_chat.go
//     的过载退避默认值。
//
// ★ 「死代码不得回来」由 errorsx/dead_code_absent_test.go 钉住。
// ★ 另有一处**注册表漏登记**的死代码同轮删除：IsUpstreamAuthFailure
//   （生产零引用，只被 errorsx 自己的测试引用）。

// FailoverScope describes how much of the candidate pool an upstream failure
// should remove synchronously from the current request.
type FailoverScope string

const (
	ScopeNone       FailoverScope = "none"
	ScopeModel      FailoverScope = "credential_model"
	ScopeCredential FailoverScope = "credential"
)

// DefaultOverloadRetryDelay 是上游过载（KindUpstreamOverloaded）时的默认退避。
const DefaultOverloadRetryDelay = 5 * time.Second
