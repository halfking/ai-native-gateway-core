// Package tooldialect 抽象「上游 wire 方言中工具参数流的语义」，是
// SRP / OCP / 抽象工厂三原则在「多厂商适应」上的第一个落点。
//
// 为什么需要它（2026-11-11 211 生产实证）：
//   - 同一条 anthropic wire 语义——content_block_start.tool_use.input 的 `{}`
//     是占位符、不得进入参数流——曾散落三处实现且有一处缺失：IR 解析器
//     内联注释（builtin.guard.tool_json_invalid 事故根因）、anthropic→chat
//     桥的 `!= "{}"` 守卫、anthropic→responses 桥（缺失，事故现场）。
//   - 厂商方言差异（占位符约定、续拼校验、修复策略）被复制进每个消费方，
//     新厂商/新协议接入 = 改所有桥（OCP 反例）。
//
// 形状（Go 惯用法）：
//   - Dialect          —— 单一方言实现（SRP：每个方言独占其全部参数流语义）；
//   - Register/ForProtocol —— 注册表式抽象工厂（OCP：新厂商 = 注册新方言，
//     核心解析器与桥零改动；按 protocol 键取用）；
//   - Anthropic builtin —— 内置种子方言（无外部依赖，json.Valid 兜底），
//     生产装配可注入更强的整文档校验/修复器
//     （domains/transformation/anthropic 的实现）。
package tooldialect

import (
	"encoding/json"
	"strings"
	"sync"
)

// Dialect 是一种上游 wire 方言的工具参数流语义。
type Dialect interface {
	// Name 报告方言名（诊断用）。
	Name() string
	// StartArguments 返回「工具调用首帧」携带的初始参数片段。
	// 返回 "" 表示首帧只携带占位符（如 anthropic 的 input:{}），该占位符
	// 不得作为参数片段下发或累计——真实参数由后续增量帧提供。
	StartArguments(raw []byte) string
	// MergeContinuation 把「首帧已下发的参数 start」与「后续增量拼接的
	// buffered」合成为终态整文档。ok=false 表示尾部非法且应被丢弃
	//（保留已下发的 start，不回撤）；返回的 merged 在 ok=true 时是
	// 校验（或保守修复）后的整文档，调用方自行切出超出 start 的增量。
	MergeContinuation(start, buffered string) (merged string, ok bool)
}

// Validator 校验（并尽力保守修复）终态整文档。ok=false 表示非法且不可修复。
// 生产装配注入 domains/transformation/anthropic.ValidateStreamingToolArgs。
type Validator func(doc string) (validated string, ok bool, err error)

// AnthropicDialect 实现 anthropic-messages 方言：
//   - 首帧 input 恒为 `{}` 占位符（真实参数走 input_json_delta 增量）；
//   - 非标准中继把真实半截参数放进 input 的形状保留（非 `{}` 即初始片段）；
//   - 终态整文档校验可注入；未注入时用 json.Valid 兜底（非法即丢弃尾部，
//     与「不回撤已发字节」的契约一致）。
type AnthropicDialect struct {
	// validate 由生产装配注入；nil 走 json.Valid 兜底。
	validate Validator
}

// NewAnthropic 构造 anthropic 方言；validate 可为 nil。
func NewAnthropic(validate Validator) *AnthropicDialect {
	return &AnthropicDialect{validate: validate}
}

// Name implements Dialect.
func (d *AnthropicDialect) Name() string { return "anthropic-messages/tool-args" }

// StartArguments implements Dialect：`{}`（含空白等价形）是占位符 ⇒ ""；
// 其余原文即初始参数片段（含空输入）。
func (d *AnthropicDialect) StartArguments(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	// 与历史守卫逐字对齐：仅排除字面 "{}"（anthropic 官方流恒为该形态；
	// 带空白变体按「真实参数」处理，宁下发不误吞）。
	if string(raw) == "{}" {
		return ""
	}
	return string(raw)
}

// MergeContinuation implements Dialect。
func (d *AnthropicDialect) MergeContinuation(start, buffered string) (string, bool) {
	if buffered == "" {
		return start, true
	}
	doc := start + buffered
	// 2026-11-11（48h 审计 P2）：delta 位置的占位符回声。标准 Anthropic 上游
	// 永远不会把 "{}" 作为 input_json_delta 发出（占位符住在
	// content_block_start.input，StartArguments 已清空）；start 为空且 buffered
	// 以 "{}" 开头还有后续内容 ⇒ 非标准中继把占位符回声进了增量流 ⇒ 终态拼出
	// "{}{...}" 非法 JSON（与 9381d13a4 修的原始事故同形态）。剥掉这个回声。
	// "{}" 单独出现是零参工具的合法终态 ⇒ 保留。
	if start == "" && strings.HasPrefix(buffered, "{}") && len(buffered) > 2 {
		doc = buffered[2:]
	}
	if d.validate != nil {
		validated, _, err := d.validate(doc)
		if err != nil {
			// 尾部非法：保留已下发的 start（no-worse-than-before），
			// 调用方据 ok=false 丢弃尾部并留痕。
			return start, false
		}
		return validated, true
	}
	if json.Valid([]byte(doc)) {
		return doc, true
	}
	return start, false
}

// ── 注册表式抽象工厂 ──────────────────────────────────────────────────────
//
// 协议键与 internal/ir 的 Protocol* 常量同值（字符串字面量对齐，避免
// 循环依赖；tooldialect 是叶子包）。任何一侧改名，契约门会抓住（见
// tooldialect_test.go 的常量对齐断言）。

// 协议键：与 internal/ir.ProtocolAnthropicMessages 同值。
const ProtocolAnthropicMessages = "anthropic-messages"

var (
	registryMu sync.RWMutex
	// 内置种子保证「未装配即正确」：任何消费方（含不经过 cmd/gateway
	// 装配的单元测试）按协议取方言都拿到保守但正确的实现——尤其
	// anthropic 的 `{}` 占位符规则不得依赖装配顺序（2026-11-11 事故的
	// 教训：语义靠「下游会滤掉」的假设会静默失效）。生产装配用 Register
	// 覆盖为带完整校验/修复器的方言。
	registry = map[string]Dialect{
		ProtocolAnthropicMessages: NewAnthropic(nil),
	}
)

// Register 注册（或覆盖）某一协议的方言。每个方言包在自己的装配点调用
// 一次；厂商接入只触碰自己的方言包与这一行注册（OCP）。
func Register(protocol string, d Dialect) {
	if protocol == "" || d == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[protocol] = d
}

// ForProtocol 返回该协议的工具参数流方言；未注册协议回落到直通方言
// （不吞任何首帧输入、整文档校验只认 json.Valid），fail-safe 到
// 「与历史通用路径一致」的行为。
func ForProtocol(protocol string) Dialect {
	registryMu.RLock()
	d, ok := registry[protocol]
	registryMu.RUnlock()
	if ok {
		return d
	}
	return passthrough{}
}

// passthrough 是未注册协议的兜底方言：语义中立，不替任何厂商做假设。
type passthrough struct{}

func (passthrough) Name() string                     { return "passthrough/tool-args" }
func (passthrough) StartArguments(raw []byte) string { return string(raw) }
func (passthrough) MergeContinuation(start, buffered string) (string, bool) {
	doc := start + buffered
	return doc, json.Valid([]byte(doc))
}
