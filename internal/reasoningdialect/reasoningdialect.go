// Package reasoningdialect 抽象「上游 wire 方言中 reasoning 字段族的投影
// 语义」，是 SRP / OCP / 抽象工厂三原则在「多厂商适应」上的第二个落点
// （架构档 docs/architecture/2026-11-11-多厂商适应变化点隔离-第一片tooldialect.md
// 变化点 B，Slice 2；模式与 Slice 1 的 internal/tooldialect 同族）。
//
// 为什么需要它（2026-11-12 站点地图实证，internal/ir 共 21 个判定块）：
//   - 同一条「IR reasoning 三字段按优先级仲裁」的语义——Type 指令词汇 >
//     显式预算(0=关) > Effort 拼写(IsDisableEffort / EffortToBudget)——
//     曾以 4 份平行实现存在：ir.reasoningDisabled（★ 死代码，零调用方，
//     却被两处注释自称 "mirrors"）、serialize_anthropic 内联 switch、
//     serialize_gemini 平行复制 switch、serialize_openai 的
//     openAIReasoningIntent。每接一个新厂商 = 同时改 reasoncap 方言枚举、
//     reasonnorm render 分支、serialize_* 原生+派生两块、syntheticReasonCaps
//     switch、paramreg 登记——散点比站点数更多。
//   - 各家族的「按设计丢弃」散落为各自内联的 loss 上报，语义（哪族、
//     哪个字段、为何丢）没有统一载体。
//
// 形状（Go 惯用法，与 tooldialect 同族）：
//   - Arbitrate            —— 仲裁单一语义源（上面那份优先级从此只有一份；
//     各 serializer 的内联 switch 删除，家族映射保留各家策略差异：
//     anthropic 的 8192 兜底 vs gemini 的 -1 动态默认是方言语义，不是漂移）；
//   - ReasoningDialect     —— 目标厂商族方言（小接口按能力切：
//     NativeProjector / DerivedProjector / IntentProjector）；
//   - Register/ForVendorFamily —— 注册表式抽象工厂（内置种子保证
//     「未装配即正确」；未注册族回落 noop——不发射、不发明 wire 形态）；
//   - Loss                 —— 按设计丢弃的语义载体（fieldPath/reason/
//     message/metadata 四元组，metadata 只携带枚举与数值，不携带用户内容；
//     anomaly dedupKey 不含 metadata ⇒ 富化不改变去重行为）。
//
// 注册表键 = reasoncap.Dialect 词表（anthropic/gemini25/ollama/openai/
// minimax/deepseek/glm/ark/qwen …）；与 reasoncap 常量的字符串字面量对齐
// 由契约门钉住（reasoningdialect_test.go，同 Slice 1 的协议键契约门）。
package reasoningdialect

import (
	"sync"

	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// ── 仲裁：单一优先级权威 ─────────────────────────────────────────────────
//
// 取代四份平行实现（其中 reasoningDisabled 是死代码）。语义与
// serialize_anthropic / serialize_gemini 的内联 switch 逐字一致：
//   1. Type 命中三词指令词汇（enabled|disabled|adaptive）⇒ KindDirective。
//      Responses reasoning.summary 词汇（auto/concise/detailed）会撞进
//      Type 字段（parseResponsesReasoning），它们**不是**指令 ⇒ 不命中。
//   2. BudgetTokens != nil ⇒ KindBudget（0 = 显式关闭）。
//   3. Effort != "" ⇒ KindEffort（关闭族拼写或 EffortToBudget 换算）。
//   4. 全空 ⇒ KindNone（无可识别意图）。

// Kind 是仲裁结论的形态。
type Kind int8

const (
	// KindNone：无可识别意图（Type 全空 + 无预算 + 无 effort）。
	KindNone Kind = iota
	// KindDirective：Type 命中指令词汇。
	KindDirective
	// KindBudget：显式预算裁决（Budget 可为 0 = 显式关闭）。
	KindBudget
	// KindEffort：Effort 拼写裁决。
	KindEffort
)

// 指令词汇。anthropic / gemini / openai 三家 serializer 的内联实现一致。
const (
	DirectiveEnabled  = "enabled"
	DirectiveDisabled = "disabled"
	DirectiveAdaptive = "adaptive"
)

// Arbitrated 是一次仲裁的结论。字段按 Kind 各取所需：
//   - KindDirective：Directive 有效；ExplicitBudget/Budget 携带伴随的
//     显式正预算（无则 false/-1）。
//   - KindBudget：Budget 为原值（0 = 显式关闭）。
//   - KindEffort：Effort 有效；DisableEffort = 拼写属关闭族；
//     BudgetFromEffort = EffortToBudget 命中且 > 0（Budget 为换算值）。
type Arbitrated struct {
	Kind Kind

	Directive string // KindDirective: enabled | disabled | adaptive

	ExplicitBudget bool // 显式正预算是否存在（KindDirective/KindBudget）
	Budget         int  // 显式预算值 / effort 换算值（无则 -1）

	Effort           string // KindEffort
	DisableEffort    bool   // KindEffort: 拼写属关闭族（none/disabled）
	BudgetFromEffort bool   // KindEffort: EffortToBudget 命中且 > 0
}

// Arbitrate 把 IR ReasoningConfig 的三字段按单一优先级语义仲裁。
// typ/budget/effort 分别对应 ReasoningConfig.Type/.BudgetTokens/.Effort。
func Arbitrate(typ string, budget *int, effort string) Arbitrated {
	switch typ {
	case DirectiveEnabled, DirectiveDisabled, DirectiveAdaptive:
		a := Arbitrated{Kind: KindDirective, Directive: typ, Budget: -1}
		if budget != nil && *budget > 0 {
			a.ExplicitBudget = true
			a.Budget = *budget
		}
		return a
	}
	if budget != nil {
		return Arbitrated{Kind: KindBudget, Budget: *budget, ExplicitBudget: true}
	}
	if effort != "" {
		a := Arbitrated{Kind: KindEffort, Effort: effort, Budget: -1}
		if reasonnorm.IsDisableEffort(effort) {
			a.DisableEffort = true
			return a
		}
		if b, ok := reasonnorm.EffortToBudget(effort); ok && b > 0 {
			a.BudgetFromEffort = true
			a.Budget = b
		}
		return a
	}
	return Arbitrated{Kind: KindNone}
}

// ── 方言接口（小接口按能力切）───────────────────────────────────────────

// NativeRequest 是「原生 thinking 配置」的投影输入（anthropic 顶层
// thinking / gemini generationConfig.thinkingConfig 的原生形态）。
type NativeRequest struct {
	// Type: "enabled" | "disabled" | "adaptive"（原生词汇）。
	Type string
	// Budget: 显式预算（原生形态恒携带；anthropic enabled 必带，
	// adaptive/disabled 语义上不带——由方言决定是否发射）。
	Budget int
	// Model 是目标模型名（模型族门用，如 opus-5-5 拒 disabled /
	// gemini-2.5-flash 才接受 0 哨兵）。
	Model string
	// SourceProtocol 是入站协议（loss 的 fieldPath 归因用）。
	SourceProtocol string
}

// DerivedRequest 是「跨协议 ReasoningConfig 派生」的投影输入。
type DerivedRequest struct {
	Type               string
	BudgetTokens       *int
	Effort             string
	MaxReasoningTokens *int // DeepSeek/QwQ 上限帽（ollama 家族的按设计丢弃面）
	Model              string
	SourceProtocol     string

	// FinalMaxTokens 是最终出站 max_tokens（anthropic 派生 budget 的
	// 一致性钳制用；仅客户端省略 max_tokens 的形态参与钳制）。
	FinalMaxTokens int
	// ClientSetMaxTokens: 客户端显式设置了 max_tokens（钳制只作用于
	// 省略形态——显式选值逐字透传，两路对等金标准钉住）。
	ClientSetMaxTokens bool
}

// Loss 是「按设计丢弃/省略」的语义载体。字段与
// ir.ReportProtocolLoss 的形参一一对应，调用方原样转发；
// metadata 只携带枚举与数值（不携带用户内容），且 anomaly 去重键
// 不含 metadata ⇒ 富化不改变去重行为。
type Loss struct {
	FieldPath      string
	Reason         string
	Message        string
	SourceProtocol string
	TargetProtocol string
	Metadata       map[string]any
}

// Projection 是一次方言投影的结论：
//   - Wire 非 nil ⇒ 调用方把键值并进出站 map（不覆盖既有键）；
//   - Losses 按设计丢弃逐条上报（可为空）；
//     Wire 与 Losses 不互斥（发射 + 附带损失可并存）。
type Projection struct {
	Wire   map[string]any
	Losses []Loss
}

// NativeProjector 是「原生 thinking 配置 → 本族 wire」的能力。
type NativeProjector interface {
	ProjectNative(n NativeRequest) Projection
}

// DerivedProjector 是「IR ReasoningConfig → 本族 wire」的能力。
type DerivedProjector interface {
	ProjectDerived(d DerivedRequest) Projection
}

// IntentProjector 是 openai-wire thinking 家族（minimax/deepseek/glm/
// ark/qwen）的附加能力：把已提取的归一意图按本族 wire 形态渲染进出站
// map（内部走 reasonnorm.Render 单源）。返回 true 表示至少写回一个键；
// false = 本族无可验证的 openai-wire 投影（调用方保持 loss 路径）。
type IntentProjector interface {
	RenderIntent(intent reasonnorm.Intent, maxTokens int, out map[string]any) bool
}

// ReasoningDialect 是一个目标厂商族的 reasoning 方言。按能力断言取用
// （NativeProjector / DerivedProjector / IntentProjector）；Name 报告
// 方言名（= 注册键，诊断用）。
type ReasoningDialect interface {
	Name() string
}

// ── 注册表式抽象工厂 ─────────────────────────────────────────────────────
//
// 注册表键 = reasoncap.Dialect 词表。内置种子保证「未装配即正确」；
// 未注册族回落 noop 族（不发射、不发明 wire 形态、不上报——调用方的
// 既有 loss 路径保持原判）。生产装配可用 Register 覆盖（OCP：新厂商 =
// 新方言 + 一行注册，核心 serializer 零改动）。

var (
	registryMu sync.RWMutex
	registry   = newSeedRegistry()
)

// newSeedRegistry 构造内置种子（独立函数便于测试隔离读数）。
func newSeedRegistry() map[string]ReasoningDialect {
	seed := map[string]ReasoningDialect{}
	for key, d := range seedFamilies() {
		seed[key] = d
	}
	return seed
}

// Register 注册（或覆盖）某一厂商族的方言。每个方言包在自己的装配点
// 调用一次；厂商接入只触碰自己的方言包与这一行注册（OCP）。
func Register(family string, d ReasoningDialect) {
	if family == "" || d == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[family] = d
}

// ForVendorFamily 返回该厂商族的 reasoning 方言；未注册族回落 noop 族
// （语义中立：不发射、不发明 wire 形态、不上报，fail-safe 到「调用方
// 既有 loss/跳过路径」的行为）。
func ForVendorFamily(family string) ReasoningDialect {
	registryMu.RLock()
	d, ok := registry[family]
	registryMu.RUnlock()
	if ok {
		return d
	}
	return noopFamily{}
}

// noopFamily 是未注册族的兜底方言。
type noopFamily struct{}

func (noopFamily) Name() string { return "noop/reasoning" }
