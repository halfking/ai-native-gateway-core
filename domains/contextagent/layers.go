package contextagent

import (
	"fmt"
	"sort"
	"strings"
)

// bytesPerToken 与仓内自身的 token 启发式一致（3.5 字节/token）：
//
//	domains/hooks/compression/estimator.go
//	domains/streaming/executors/context_summarize.go（heuristicCompactBytePerToken）
//
// ★ 口径纪律：本包**不**引入第二套 token 估算口径。
const bytesPerToken = 3.5

// EstimateTokens 按仓内启发式估算 token 数。
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return int(float64(len(s)) / bytesPerToken)
}

// Layer 是记忆分层的一个层。
type Layer string

const (
	// LayerKB 是知识库层（文档 / 语料）。★ 仓内目前**没有**知识库存储
	// （knowledge_bases 表是外仓 schema 残留，本仓迁移从未创建），
	// 本层只保留契约与预算位，不实现取数。
	LayerKB Layer = "kb"

	// LayerIndex 是 L0 索引层：每步一行「目标 → 结果」。
	// 权威源：public.session_request_summaries（step_index / request_summary / response_summary）。
	LayerIndex Layer = "index"

	// LayerStepDetail 是 L1 步骤明细层：每一步的完整正文。
	// 权威源：public.session_bodies（按 turn_no 精确寻址）。
	LayerStepDetail Layer = "step_detail"

	// LayerLongTerm 是 L2 长期记忆层：跨会话的长期事实 / 偏好。
	// 权威源：Memora（向量检索，**语义**寻址，不是按步寻址）。
	LayerLongTerm Layer = "long_term"

	// LayerTail 是最近若干条原始消息（永远在上下文里）。
	LayerTail Layer = "tail"
)

// LayerOrder 是装配顺序（自上而下）。
//
// ★ 索引层必须**在尾部之前、system 之后**——这正是用户要的「最上层」。
//
//	装配形状与生产既有能力一致（domains/hooks/compression/rebuilder_openai.go：
//	system → dynamic_context(摘要/索引) → 首条 user → 尾部），
//	所以本包不需要新造装配器语义。
var LayerOrder = []Layer{LayerKB, LayerIndex, LayerLongTerm, LayerStepDetail, LayerTail}

// LayerContent 是某一层取回的内容 + 它的来源说明。
type LayerContent struct {
	Layer Layer `json:"layer"`
	// Text 是注入上下文的文本。
	Text string `json:"text"`
	// Tokens 是该层的估算 token 数。
	Tokens int `json:"tokens"`
	// Sources 记录这一层的内容分别来自哪里（可寻址的证据链）。
	Sources []string `json:"sources"`
	// Truncated 表示该层因预算被截断。
	Truncated bool `json:"truncated"`
}

// Plan 是分层加载计划。
//
// ★ 计划与执行分离：计划是**纯函数**（只依赖身份 + 预算 + 现有内容量估计），
//
//	执行才发生 IO（加载）。这样计划本身可被完整单测覆盖。
type Plan struct {
	// ContextWindow 是目标模型的上下文窗口（token）；<=0 表示未知。
	ContextWindow int `json:"context_window"`
	// Budget 是本次允许占用的 token 预算（不含客户端自带的历史）。
	Budget int `json:"budget"`
	// TailMessages 是尾部保留的原始消息条数。
	TailMessages int `json:"tail_messages"`
	// Allocation 是各层的预算分配（token）。
	Allocation map[Layer]int `json:"allocation"`
	// LoadSteps 列出计划按需加载的步骤序号（升序）。
	LoadSteps []int `json:"load_steps"`
	// SearchQuery 是 L2 语义检索用的查询串（空 = 不做 L2 召回）。
	SearchQuery string `json:"search_query"`
}

// DefaultBudgetFraction 是压缩目标占窗口的比例。
//
// ★ 与仓内既有口径一致：domains/transformation/ctx_compress.go 的
//
//	providerCompressionTargetTokens 用 0.60（并对 <=1M 窗口封顶 200K）。
const DefaultBudgetFraction = 0.60

// MaxBudgetTokens 是预算硬上限（与 ctx_compress 的 200K 封顶同量级）。
const MaxBudgetTokens = 200_000

// DefaultTailMessages 是尾部默认保留条数。
const DefaultTailMessages = 4

// PlanInput 是计划阶段的输入。
type PlanInput struct {
	Identity      Identity
	ContextWindow int
	// CurrentTokens 是客户端当前请求体已占用的 token 估计。
	CurrentTokens int
	// TailMessages 覆盖默认尾部条数（<=0 用默认）。
	TailMessages int
	// SearchQuery 是 L2 召回查询；空则不做 L2。
	SearchQuery string
	// AvailableSteps 是已知的步骤序号（升序）。用于决定按需加载哪些步。
	AvailableSteps []int
	// StepTokens 是每个步骤的 token 估计（键为步骤序号）。
	StepTokens map[int]int
	// IndexTokens 是 L0 索引层的 token 估计。
	IndexTokens int
}

// MakePlan 产出分层加载计划。
//
// ★ 分配顺序（先紧后松，反映「越靠上越该保底」）：
//  1. 尾部（Tail）      —— 最近的任务必须看得见
//  2. 索引层（Index）    —— 每步一行，是「要不要加载细节」的依据
//  3. 步骤明细（StepDetail）—— 按需加载，占剩余预算
//  4. 长期记忆（LongTerm）—— 语义召回，兜底价值最低 ⇒ 最后分配
//
// ★ 预算 = min(窗口 × 60%, 200K) - 客户端已占用；不足则等比缩减。
func MakePlan(in PlanInput) Plan {
	window := in.ContextWindow
	if window <= 0 {
		window = 128_000
	}
	budget := int(float64(window) * DefaultBudgetFraction)
	if budget > MaxBudgetTokens {
		budget = MaxBudgetTokens
	}
	budget -= in.CurrentTokens
	if budget < 0 {
		budget = 0
	}

	tail := in.TailMessages
	if tail <= 0 {
		tail = DefaultTailMessages
	}

	p := Plan{
		ContextWindow: window,
		Budget:        budget,
		TailMessages:  tail,
		Allocation:    map[Layer]int{},
		SearchQuery:   strings.TrimSpace(in.SearchQuery),
	}

	if p.Budget == 0 {
		return p
	}

	// 1) 尾部：按每条消息的粗估（消息体量差异大，这里只做上限保护）。
	tailBudget := tail * 400
	if tailBudget > p.Budget/2 {
		tailBudget = p.Budget / 2
	}
	p.Allocation[LayerTail] = tailBudget

	// 2) 索引层：必须保底（它是「要不要加载细节」的依据）。
	idx := in.IndexTokens
	if idx <= 0 {
		idx = 200
	}
	if idx > p.Budget/3 {
		idx = p.Budget / 3
	}
	p.Allocation[LayerIndex] = idx

	remain := p.Budget - p.Allocation[LayerTail] - p.Allocation[LayerIndex]
	if remain < 0 {
		remain = 0
	}

	// 3) 步骤明细：按「最相关优先」装入，装不下的留到「按需加载」。
	steps := append([]int(nil), in.AvailableSteps...)
	sort.Ints(steps)
	for _, s := range steps {
		cost := in.StepTokens[s]
		if cost <= 0 {
			cost = 200
		}
		if cost > remain {
			continue
		}
		remain -= cost
		p.LoadSteps = append(p.LoadSteps, s)
	}
	p.Allocation[LayerStepDetail] = p.Budget - p.Allocation[LayerTail] - p.Allocation[LayerIndex] - remain

	// 4) 长期记忆：吃掉剩余。
	if p.SearchQuery != "" {
		p.Allocation[LayerLongTerm] = remain
	} else {
		p.Allocation[LayerLongTerm] = 0
	}

	return p
}

// BudgetFor 返回某层的预算（未分配的层返回 0）。
func (p Plan) BudgetFor(l Layer) int {
	if p.Allocation == nil {
		return 0
	}
	return p.Allocation[l]
}

// Describe 返回计划的可读描述（供日志与记录使用）。
func (p Plan) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "budget=%d window=%d tail=%d", p.Budget, p.ContextWindow, p.TailMessages)
	for _, l := range LayerOrder {
		fmt.Fprintf(&b, " %s=%d", l, p.Allocation[l])
	}
	if len(p.LoadSteps) > 0 {
		fmt.Fprintf(&b, " steps=%v", p.LoadSteps)
	}
	return b.String()
}
