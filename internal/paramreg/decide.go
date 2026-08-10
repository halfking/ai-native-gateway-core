package paramreg

import (
	"encoding/json"
	"sort"
	"sync"
)

// RestoreAction 是对单个 Extensions 字段的还原决策。
type RestoreAction uint8

const (
	// ActionRestore 原样写入目标 body。
	ActionRestore RestoreAction = iota

	// ActionTranslate 经 FieldSpec.Translate 转换后写入。
	ActionTranslate

	// ActionDrop 裁剪并上报 protocol loss。
	ActionDrop

	// ActionSkip 表示 IR 已处理该字段，序列化器负责输出。
	// 这不是丢失，不该上报 loss。
	ActionSkip
)

// String 便于日志与测试断言。
func (a RestoreAction) String() string {
	switch a {
	case ActionRestore:
		return "restore"
	case ActionTranslate:
		return "translate"
	case ActionDrop:
		return "drop"
	case ActionSkip:
		return "skip"
	default:
		return "unknown"
	}
}

var (
	indexOnce sync.Once
	// byName 是 name 与 alias 到 spec 的索引。
	byName map[string]*FieldSpec
	// irHandled 缓存 KindIRHandled 的字段名集合。
	irHandled map[string]bool
)

func buildIndex() {
	byName = make(map[string]*FieldSpec, len(specs)*2)
	irHandled = make(map[string]bool, len(specs))

	for i := range specs {
		spec := &specs[i]
		key := normalizeKey(spec.Name)
		byName[key] = spec
		if spec.Kind == KindIRHandled {
			irHandled[key] = true
		}
		for _, alias := range spec.Aliases {
			ak := normalizeKey(alias)
			// 别名不覆盖已登记的一等字段名。max_tokens 既是 stop 的
			// 别名目标也是独立字段，先登记者优先。
			if _, exists := byName[ak]; !exists {
				byName[ak] = spec
			}
			if spec.Kind == KindIRHandled {
				irHandled[ak] = true
			}
		}
	}
}

func index() map[string]*FieldSpec {
	indexOnce.Do(buildIndex)
	return byName
}

// Lookup 返回字段的注册信息。未登记返回 nil。
func Lookup(key string) *FieldSpec {
	return index()[normalizeKey(key)]
}

// Decide 判定一个 Extensions 字段在 src → dst 方向上该如何处理。
//
// 决策优先级（自上而下短路）：
//
//  1. dst 明确 RejectedBy 该字段          → ActionDrop（硬报错防护，最高优先）
//  2. 未登记（未知字段）                   → ActionRestore（前向兼容，无条件透传）
//  3. KindIRHandled                       → ActionSkip（序列化器已处理）
//  4. KindPortable                        → ActionRestore
//  5. KindTranslatable 且有 src→dst 映射   → ActionTranslate
//  6. KindDialectOnly 且 dst 认识该字段    → ActionRestore
//  7. 其余                                → ActionDrop
//
// 第 2 条是本设计的核心：厂商每月新增参数，网关不该成为瓶颈。
// Claude Code 的 CLAUDE_CODE_EXTRA_BODY 会把任意用户 JSON 展开进 body，
// 这类字段必须能穿过网关。
func Decide(key string, src, dst Dialect) (RestoreAction, *FieldSpec) {
	spec := Lookup(key)

	if spec == nil {
		// 未知字段：无条件透传。
		//
		// 注意 Mistral 的 additionalProperties:false 会拒绝未知键，
		// 但那属于"客户端发了 Mistral 不认识的字段"，应由 provider 级
		// strip_request_fields 配置处理，而非在此静默丢弃所有人的字段。
		return ActionRestore, nil
	}

	if spec.rejects(dst) {
		return ActionDrop, spec
	}

	switch spec.Kind {
	case KindIRHandled:
		// 关键设计决策（2026-08-11）：KindIRHandled 也走 ActionRestore，不走 ActionSkip。
		//
		// 理由：Decide 只会被用于处理 Extensions 里的字段，而一个字段能进
		// Extensions 恰恰说明**某条路径上的 parser 没有消费它**。举例：
		// `reasoning` 对象只有 parse_responses 认识，parse_openai 不认识，
		// 于是 openai-chat 入向时它进了 Extensions；此时若判成 ActionSkip
		// 就会被静默丢弃。同理 thinking_budget / enable_thinking 在 P5 之前
		// 没有任何 parser 消费。
		//
		// 而 ActionRestore 在"IR 确实处理了该字段"的情况下也是安全的：
		// 调用方一律遵守"不覆盖目标已存在的键"，此时 IR 的输出已经在 body 里，
		// 还原是无操作。所以 ActionRestore 严格优于 ActionSkip。
		//
		// KindIRHandled 因此退化为纯元数据用途：文档、审计、以及
		// IRHandledFields() 驱动的 isStandardField 判定。
		return ActionRestore, spec

	case KindPortable:
		return ActionRestore, spec

	case KindTranslatable:
		if spec.Translate != nil {
			return ActionTranslate, spec
		}
		// 声明可翻译但没给函数：退化为方言判定。
		if spec.knownBy(dst) {
			return ActionRestore, spec
		}
		return ActionDrop, spec

	case KindDialectOnly:
		if spec.knownBy(dst) {
			return ActionRestore, spec
		}
		return ActionDrop, spec

	default:
		return ActionRestore, spec
	}
}

// Apply 对单个字段执行决策并返回最终写入的 key/value。
//
// ok=false 表示不应写入（ActionDrop 或 ActionSkip）。调用方需区分两者：
// ActionDrop 要上报 loss，ActionSkip 不要。
func Apply(key string, value json.RawMessage, src, dst Dialect) (outKey string, outVal json.RawMessage, action RestoreAction, spec *FieldSpec) {
	action, spec = Decide(key, src, dst)

	switch action {
	case ActionRestore:
		return key, value, action, spec

	case ActionTranslate:
		newKey, newVal, ok := spec.Translate(value, src, dst)
		if !ok {
			return "", nil, ActionDrop, spec
		}
		return newKey, newVal, action, spec

	default:
		return "", nil, action, spec
	}
}

// IRHandledFields 返回 IR 已处理的字段名集合（含别名）。
//
// 用途：替代 domains/transformation/fields.go 的 standardRequestFields。
// 两套并行清单曾漂移导致 stream_options / metadata 静默丢失
// （见 fields.go:8-13 的事故记录），由本函数统一为单一数据源。
func IRHandledFields() map[string]bool {
	indexOnce.Do(buildIndex)
	out := make(map[string]bool, len(irHandled))
	for k := range irHandled {
		out[k] = true
	}
	return out
}

// KnownFieldsForDialect 返回目标方言应当允许通过的全部字段名。
//
// 用途：替代 domains/transformation/sanitizer.go 的 alwaysKeepFieldsOpenAI
// （仅 8 个字段）与 alwaysKeepFieldsAnthropic（仅 12 个）。原实现在白名单
// 模式下会删掉 tools / tool_choice / stream_options 等关键字段。
func KnownFieldsForDialect(d Dialect) map[string]bool {
	indexOnce.Do(buildIndex)
	out := make(map[string]bool, len(byName))
	for key, spec := range byName {
		if spec.rejects(d) {
			continue
		}
		switch spec.Kind {
		case KindIRHandled, KindPortable, KindTranslatable:
			out[key] = true
		case KindDialectOnly:
			if spec.knownBy(d) {
				out[key] = true
			}
		}
	}
	return out
}

// RegisteredNames 返回全部已登记的一等字段名（不含别名），已排序。
// 供 admin 展示与测试使用。
func RegisteredNames() []string {
	out := make([]string, 0, len(specs))
	for i := range specs {
		out = append(out, specs[i].Name)
	}
	sort.Strings(out)
	return out
}
