// Package extpoints — 本仓**扩展点裁决表**（单一真值源）。
//
// # 这道改造补的是哪块缺口
//
// 2026-10-24 的 magpie 刷新轮（docs/research/2026-10-24-magpie-v0.1.1367-…md
// §3.5 / §6-P0-1）审计出本仓**一个请求面上并存 5 套扩展机制**，其中 2 套
// 在生产里分别是**死的**（HookRegistry：注册表从未被构造）和**关着的**
// （Request Pipeline：`LLM_GATEWAY_USE_V2_PIPELINE` 默认 false）：
//
//	① HTTP 中间件链        middleware/            活（启动时静态构建）
//	② Hook Registry        domains/hooks/registry.go   **死**（生产零调用方）
//	③ Request Pipeline     domains/pipeline/      **关**（v2 默认关）
//	④ 执行器生命周期钩子   executors.ExecuteHooks **死**（★ 2026-10-24 订正：
//	                       类型在、读取方在，但**零生产安装方** ⇒ 钩子从不触发）
//	⑤ 响应拦截链          domains/hooks/response/ 活（Go 类型，body 级改/拦）
//
// ★ `domains/hooks` 自述"提供统一的插件化架构，所有企业功能都可以通过 Hook
//   形式动态加载"，但它的注册表**从未被构造** ⇒ "**框架在、链路不通**"。
//
// # 本包存在的理由：把裁决写成**代码**，而不是文档里的一段话
//
// ★ 裁决表写在代码里 ⇒ 门（scripts/ursmcheck/magpie_p0_extension_adjudication_test.go）
//   可以逐条**对拍现实**：
//   「你说 HookRegistry 判死？那我去全仓扫一遍，看它是不是真的零生产调用方」；
//   「你说 ExecuteHooks 挂在 upstream 阶段？那那个 phase 必须在 pipeline 里真的存在」。
//   ⇒ **表与现实不同源就红** —— 这正是本仓 LESSONS 里反复出现的那条教训
//   （"同一口径两处实现"、"框架在、链路不通"）。
//
// # 裁决口径
//
//	VerdictMainChain   —— 请求面**只有这一条**链（v1 生产路径）
//	VerdictSkeleton    —— 保留为骨架：**阶段词表的唯一事实源**（v2 才是实链）
//	VerdictAlias       —— 降级为**别名视图**（阶段名映射到骨架的 phase，不自成一链）
//	VerdictDead        —— 判死（保留代码但**禁止**新增调用方；已有调用方必须先裁决）
//	VerdictPhaseBound  —— 显式挂到骨架的某个 phase（不再是"独立的一套"）
//
// # 引用
//
//	domains/pipeline/pipeline.go     —— 骨架的 16 个 phase（词表事实源）
//	domains/hooks/types.go           —— 别名视图的 5 个旧阶段名
//	docs/subsystems/request-surface.md —— 人读的那一页（由本表生成口径）

package extpoints

// Verdict 是扩展机制的裁决结论。
type Verdict string

const (
	// VerdictMainChain：请求面**只有这一条**链（v1 生产路径）。
	VerdictMainChain Verdict = "main_chain"
	// VerdictSkeleton：保留为骨架 —— **阶段词表的唯一事实源**。
	VerdictSkeleton Verdict = "skeleton"
	// VerdictAlias：降级为别名视图 —— 阶段名映射到骨架的 phase，**不自成一链**。
	VerdictAlias Verdict = "alias"
	// VerdictDead：判死 —— 保留代码但不再接受新的生产调用方。
	VerdictDead Verdict = "dead"
	// VerdictPhaseBound：显式挂到骨架的某个 phase —— 不再是"独立的一套"。
	VerdictPhaseBound Verdict = "phase_bound"
)

// Mechanism 是扩展机制的一行裁决。
type Mechanism struct {
	// ID 是稳定标识（门禁按它对拍现实）。
	ID string
	// Title 是中文名。
	Title string
	// Location 是代码位置（file 或 file:line）。
	Location string
	// Verdict 是裁决结论。
	Verdict Verdict
	// Production 表示该机制**当前**是否真的在生产路径上被调用。
	//
	// ★ 这是门要**独立复核**的字段 —— 它不是表说了算，是扫源码说了算。
	Production bool
	// Phase 在 VerdictPhaseBound 时给出所挂的骨架 phase；否则为空。
	Phase string
	// Evidence 是裁决依据（file:line）。
	Evidence []string
	// Note 是补充说明（尤其是"为什么这么裁"）。
	Note string
}

// 机制 ID（门禁按这些 ID 断言覆盖度）。
const (
	IDHTTPMiddlewareChain = "http_middleware_chain"
	IDRequestPipeline     = "request_pipeline"
	IDHookRegistry        = "hook_registry"
	IDHookPhaseVocabulary = "hook_phase_vocabulary"
	IDExecutorHooks       = "executor_hooks"
	IDResponseIntercept   = "response_intercept"
)

// PipelinePhaseCount 是骨架（domains/pipeline）的 phase 数。
//
// ★ 实测 16（domains/pipeline/pipeline.go:25-42）：
//
//	pre_authentication / authentication / post_authentication /
//	pre_routing / routing / post_routing / governance /
//	pre_transform / transform / post_transform /
//	pre_upstream / upstream / post_upstream /
//	pre_response / response / post_response
//
// 〔审计订正〕计划书写的是「15 阶段」，实测是 **16**（多了 governance，
// 它是 V4 的 PhaseGovernance，见 domains/pipeline/pipeline.go:31）。
const PipelinePhaseCount = 16

// HookPhaseCount 是别名视图（domains/hooks）的旧阶段数。
const HookPhaseCount = 5

// HookPhaseAliases 把 domains/hooks 的 5 个旧阶段名映射到骨架的 phase。
//
// ★ 映射是**同名**的：5 个旧阶段名**逐字**出现在骨架的 16 个 phase 里
//
//	⇒ 别名视图不需要翻译，只需要"指过去"。
//	（这正是"降级为别名视图"能成立的前提：词表已经是骨架的子集。）
var HookPhaseAliases = map[string]string{
	"pre_routing":   "pre_routing",
	"routing":       "routing",
	"pre_upstream":  "pre_upstream",
	"post_upstream": "post_upstream",
	"post_response": "post_response",
}

// Mechanisms 是全部扩展机制的裁决表。
//
// ★ 顺序即"请求面"的阅读顺序：主链 → 骨架 → 别名 → 死 → 挂在 phase 上的。
func Mechanisms() []Mechanism {
	return []Mechanism{
		{
			ID:         IDHTTPMiddlewareChain,
			Title:      "HTTP 中间件链",
			Location:   "middleware/ + cmd/gateway/main.go:7717-7738",
			Verdict:    VerdictMainChain,
			Production: true,
			Evidence: []string{
				"cmd/gateway/main.go:7717-7738（Builder 链的构建处）",
			},
			Note: "★ 请求面**只有这一条链**（v1 生产路径）：Recovery → RequestID → Logging → " +
				"Locale → CORS → Prometheus → Tracing → Auth → Origin → SecurityHeaders → mux。" +
				"其余机制要么是它的骨架、要么挂在它的 phase 上、要么判死。",
		},
		{
			ID:         IDRequestPipeline,
			Title:      "Request Pipeline（v2）",
			Location:   "domains/pipeline/",
			Verdict:    VerdictSkeleton,
			Production: false,
			Evidence: []string{
				"domains/pipeline/pipeline.go:25-42（16 个 phase 常量）",
				"cmd/gateway/main_pipeline.go:160-168（v2 开关，启动时读一次）",
			},
			Note: "★ 保留为**骨架**：`LLM_GATEWAY_USE_V2_PIPELINE` 默认 false ⇒ v2 不是生产链，" +
				"但它的 16 个 phase 是本仓**阶段词表的唯一事实源**。" +
				"⇒ 任何新扩展点都必须挂到这里的某个 phase 上，而不是另起一套阶段词表。",
		},
		{
			ID:         IDHookRegistry,
			Title:      "Hook Registry（注册表）",
			Location:   "domains/hooks/registry.go",
			Verdict:    VerdictDead,
			Production: false,
			Evidence: []string{
				"domains/hooks/registry.go:34（NewHookRegistry 的定义）",
				"domains/hooks/registry.go:96（Execute 的定义）",
			},
			Note: "★ **判死**：`NewHookRegistry` 与 `registry.Execute` **在包外零生产调用方**" +
				"（只有 registry_test.go 用它）⇒ 注册表从未被构造过。" +
				"⚠ 与 v5 里记为「待裁决」的 `internal/smartretry`（生产零引用）是同一种形态，" +
				"但这次更隐蔽 —— 它有完整的 README、覆盖率声明和 Prometheus 指标。" +
				"⇒ 判死 = 保留代码（读它的人还需要它解释历史），但**禁止新增生产调用方**；" +
				"若哪天有人要复活它，必须先改本表的裁决并让门红给你看。",
		},
		{
			ID:         IDHookPhaseVocabulary,
			Title:      "Hook 阶段词表（5 个旧阶段名）",
			Location:   "domains/hooks/types.go:13-24",
			Verdict:    VerdictAlias,
			Production: false,
			Evidence: []string{
				"domains/hooks/types.go:13-24（5 个 Phase 常量）",
				"domains/hooks/types.go:34（IsValid 的同一份清单）",
			},
			Note: "★ 降级为**别名视图**：5 个旧阶段名逐字出现在骨架的 16 个 phase 里" +
				"⇒ 不需要翻译，只需要「指过去」（HookPhaseAliases）。" +
				"⇒ 于是「阶段词表有两份」这个隐患被消掉了：**只有骨架那一份是真的**。",
		},
		{
			ID:         IDExecutorHooks,
			Title:      "执行器生命周期钩子",
			Location:   "domains/streaming/executors/executor_chat.go:60-67",
			Verdict:    VerdictDead,
			Production: false,
			Phase:      "",
			Evidence: []string{
				"domains/streaming/executors/executor_chat.go:60-67（ExecuteHooks 类型）",
				"domains/streaming/executors/executor_chat.go:70-78（WithExecuteHooks）",
				"★ 全仓 `WithExecuteHooks(` 的调用点**只有测试**（executor_chat_test.go 与 " +
					"scripts/ursmcheck 的 on/off 用例）⇒ **零生产安装方**",
			},
			Note: "★ **判死**（2026-10-24 订正，详见审计档 §3.14）：`ExecuteHooks` 类型在、" +
				"`executeOpenAI` 也**读**它（executor_chat.go:689），但**没有任何生产代码安装它** —— " +
				"`WithExecuteHooks(` 在生产代码里**零调用** ⇒ 钩子**从不触发** ⇒ " +
				"⚠ 与 HookRegistry **同一种形态**（框架在、链路不通），但**更隐蔽**：它有完整的 API、" +
				"有 P0-5 的 on/off 用例（那两条用例只证明『API 装上能用』，**不证明生产装了**）。" +
				"⇒ 此前本表把它标成 `Production=true`（靠『类型存在 + 有读取方』判定）⇒ **判据口径错了**：" +
				"★ **『类型在』≠『被安装』**。⇒ 判死 = 保留代码（读它的人还需要它），但**禁止新增生产安装方**；" +
				"若哪天有人要复活它，必须先改本表的裁决并让门红给你看。",
		},
		{
			ID:         IDResponseIntercept,
			Title:      "响应拦截链",
			Location:   "domains/hooks/response/",
			Verdict:    VerdictPhaseBound,
			Production: true,
			Phase:      "response",
			Evidence: []string{
				"domains/hooks/response/types.go:179-183（ResponseInterceptor 接口）",
				"domains/hooks/response/chain.go:18（NewInterceptorChain）",
			},
			Note: "★ 显式挂到骨架的 **response** phase：body 级改/拦（整包 + 流式 chunk + 收尾），" +
				"语义上就是「响应」这一段 ⇒ 归 response，而不是自成一链。",
		},
	}
}

// MechanismByID 按 ID 取一行。
func MechanismByID(id string) (Mechanism, bool) {
	for _, m := range Mechanisms() {
		if m.ID == id {
			return m, true
		}
	}
	return Mechanism{}, false
}

// ProductionMechanisms 返回**当前**在生产路径上被调用的机制。
func ProductionMechanisms() []Mechanism {
	var out []Mechanism
	for _, m := range Mechanisms() {
		if m.Production {
			out = append(out, m)
		}
	}
	return out
}
