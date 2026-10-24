// Package phasepolicy — **失败策略表**：每个 phase 失败时是「终止」还是「继续」。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §5.1 / §6-P1-5）指出：magpie 的每个扩展点都有**独立的失败策略**
// （观察者「永不失败请求」／策略「失败开放」／中间件「失败开放」／遮蔽「失败关闭」／
// 预算「失败关闭」），而本仓：
//
//	① domains/hooks/registry.go 的 isCriticalPhase **硬编码**了
//	   「pre_routing 与 routing 是关键阶段，其余失败只记录并继续」；
//	② domains/pipeline 的 OnError 是**逐 hook 自实现**，没有集中表；
//	③ 两套机制的「哪些阶段算关键」**互不相干** ⇒ 同一件事有两套口径。
//
// ⇒ 本包把「失败是终止还是继续」**写死在一张表里**（代码即真值源），
//   并让 domains/hooks **引用**它 ⇒ 口径只有一份。
//
// # 三种策略（沿用 magpie 的语义命名）
//
//	PolicyTerminate —— 失败 ⇒ **终止请求**（失败关闭）
//	PolicyContinue  —— 失败 ⇒ **记录并继续**（失败开放）
//	PolicySuspend   —— 失败 ⇒ **挂起**（待人工/审批介入）
//
// # 为什么「终止」的只有两个阶段
//
// 认证与路由是**请求能不能成立**的判定：这两步错了，后面的阶段没有意义
// （拿一个没鉴权的请求去记账，或让一个没路由的请求去打上游，都是错的）。
// 其余阶段（转换、上游、响应…）的失败**不该**把一个本来能成功的请求打死
// ⇒ 失败开放。
//
// # 引用
//
//	domains/hooks/registry.go   —— isCriticalPhase 改为**引用**本表
//	internal/extpoints           —— 扩展点裁决表（挂到哪个 phase）
//	docs/subsystems/request-surface.md —— 请求面设计页

package phasepolicy

// FailurePolicy 是一个 phase 的失败策略。
type FailurePolicy string

const (
	// PolicyTerminate：失败 ⇒ 终止请求（失败关闭）。
	PolicyTerminate FailurePolicy = "terminate"
	// PolicyContinue：失败 ⇒ 记录并继续（失败开放）。
	PolicyContinue FailurePolicy = "continue"
	// PolicySuspend：失败 ⇒ 挂起（待审批/人工介入）。
	PolicySuspend FailurePolicy = "suspend"
)

// PhasePolicy 是某个 phase 的失败策略条目。
type PhasePolicy struct {
	// Phase 是阶段名（与 domains/pipeline 的 Phase 常量同名）。
	Phase string
	// Policy 是失败策略。
	Policy FailurePolicy
	// Reason 写明「为什么是这个策略」（一句，面向审计阅读）。
	Reason string
}

// 骨架的 16 个 phase 名（domains/pipeline/pipeline.go 的 Phase 常量值）。
//
// ★ 这里**逐字**列出而不是从 domains/pipeline 导入 —— 因为 domains/pipeline
//
//	会依赖本包（hooks 引用本表）⇒ 反向 import 会成环。
//	⇒ 两份清单必须一致，由门禁
//	（scripts/ursmcheck/magpie_p1_failure_policy_test.go）**对拍**。
const (
	PhasePreAuthentication  = "pre_authentication"
	PhaseAuthentication     = "authentication"
	PhasePostAuthentication = "post_authentication"
	PhasePreRouting         = "pre_routing"
	PhaseRouting            = "routing"
	PhasePostRouting        = "post_routing"
	PhaseGovernance         = "governance"
	PhasePreTransform       = "pre_transform"
	PhaseTransform          = "transform"
	PhasePostTransform      = "post_transform"
	PhasePreUpstream        = "pre_upstream"
	PhaseUpstream           = "upstream"
	PhasePostUpstream       = "post_upstream"
	PhasePreResponse        = "pre_response"
	PhaseResponse           = "response"
	PhasePostResponse       = "post_response"
)

// Phases 返回骨架的全部 phase 名（有序，与 domains/pipeline.AllPhases 同序）。
func Phases() []string {
	return []string{
		PhasePreAuthentication, PhaseAuthentication, PhasePostAuthentication,
		PhasePreRouting, PhaseRouting, PhasePostRouting, PhaseGovernance,
		PhasePreTransform, PhaseTransform, PhasePostTransform,
		PhasePreUpstream, PhaseUpstream, PhasePostUpstream,
		PhasePreResponse, PhaseResponse, PhasePostResponse,
	}
}

// Policies 返回**失败策略表**（单一真值源）。
//
// ★ 顺序与 Phases() 一致 ⇒ 便于逐条对读。
func Policies() []PhasePolicy {
	return []PhasePolicy{
		{
			Phase:  PhasePreAuthentication,
			Policy: PolicyContinue,
			Reason: "鉴权前置（拿 key 之类）：失败说明请求还没进到能被判定的状态 " +
				"⇒ 记录并继续，由后续认证阶段给出真正的结论。",
		},
		{
			Phase:  PhaseAuthentication,
			Policy: PolicyTerminate,
			Reason: "★ 认证是「请求能不能成立」的判定 ⇒ 失败必须终止 " +
				"（放行一个未鉴权的请求比失败严重得多）。",
		},
		{
			Phase:  PhasePostAuthentication,
			Policy: PolicyContinue,
			Reason: "认证后的记账/审计：失败不该把已认证的请求打死 ⇒ 失败开放。",
		},
		{
			Phase:  PhasePreRouting,
			Policy: PolicyTerminate,
			Reason: "★ 路由前置（模型解析/规则）：没有可路由的模型就没有请求 ⇒ 终止。",
		},
		{
			Phase:  PhaseRouting,
			Policy: PolicyTerminate,
			Reason: "★ 路由本身：选不出候选就没有请求 ⇒ 终止 " +
				"（本仓既有行为：domains/hooks/registry.go 的 isCriticalPhase）。",
		},
		{
			Phase:  PhasePostRouting,
			Policy: PolicyContinue,
			Reason: "路由后的记账/亲和缓存：失败开放。",
		},
		{
			Phase:  PhaseGovernance,
			Policy: PolicySuspend,
			Reason: "治理阶段（审批/拦截策略）：失败**挂起**而不是放行 —— " +
				"放行等于绕过策略，挂起等于把决定权交回给人。",
		},
		{
			Phase:  PhasePreTransform,
			Policy: PolicyContinue,
			Reason: "转换前置：失败开放（转换失败通常有降级路径）。",
		},
		{
			Phase:  PhaseTransform,
			Policy: PolicyContinue,
			Reason: "★ 协议转换：失败开放 —— 但不是「直接发原始体」那种放行，" +
				"而是让调用方拿到明确错误（本仓既有 OnError 语义）。",
		},
		{
			Phase:  PhasePostTransform,
			Policy: PolicyContinue,
			Reason: "转换后的记账/缓存：失败开放。",
		},
		{
			Phase:  PhasePreUpstream,
			Policy: PolicyContinue,
			Reason: "上游前置（压缩/脱敏）：失败开放（压缩可跳过）。",
		},
		{
			Phase:  PhaseUpstream,
			Policy: PolicyContinue,
			Reason: "★ 上游调用：失败**不**终止整个 pipeline —— 换候选/重试是" +
				"「路由层」的职责（见 errorsx 的归责矩阵），不是 pipeline 的。",
		},
		{
			Phase:  PhasePostUpstream,
			Policy: PolicyContinue,
			Reason: "上游后（缓存/脱敏还原）：失败开放。",
		},
		{
			Phase:  PhasePreResponse,
			Policy: PolicyContinue,
			Reason: "响应前置：失败开放。",
		},
		{
			Phase:  PhaseResponse,
			Policy: PolicyContinue,
			Reason: "响应阶段：失败开放 —— 响应拦截链的失败策略由「拦截器自己」声明 " +
				"（ResponseInterceptor 的 FailClosed 标记），phase 级默认是放行。",
		},
		{
			Phase:  PhasePostResponse,
			Policy: PolicyContinue,
			Reason: "响应后（异步记账/审计）：失败开放 " +
				"（本仓既有行为：domains/hooks/audit 的 hook 走 post_response 异步路径）。",
		},
	}
}

// PolicyOf 返回某个 phase 的失败策略。
func PolicyOf(phase string) (FailurePolicy, bool) {
	for _, p := range Policies() {
		if p.Phase == phase {
			return p.Policy, true
		}
	}
	return "", false
}

// IsTerminating 报告某个 phase 失败时**是否终止**。
//
// ★ 这是 domains/hooks/registry.go 的 isCriticalPhase 的**唯一口径来源**。
func IsTerminating(phase string) bool {
	policy, ok := PolicyOf(phase)
	return ok && policy == PolicyTerminate
}

// TerminatingPhases 返回所有「失败即终止」的 phase（有序）。
func TerminatingPhases() []string {
	var out []string
	for _, p := range Policies() {
		if p.Policy == PolicyTerminate {
			out = append(out, p.Phase)
		}
	}
	return out
}
