package ir

import (
	"encoding/json"
)

// RISK-2（2026-10-29）：工具顶层非标准键的无损往返。
//
// 缺陷（原厂直通轮 RISK-2 登记）：工具对象的顶层非标准键
// （如 zcode 的 vendor_tool_flag、Anthropic 的 cache_control）在三个
// 重建站点被丢弃 —— streaming.NormalizeOpenAIToolDefinitions（只写
// type+function）、streaming.SanitizeAnthropicToolDefinitions（只写
// name/description/input_schema）、IR 的 parse→serialize 往返
// （ToolDefinition 只承载 name/description/parameters）。
// 修法对所有客户端一致（不在原生对下特判）：parse 时把消费集之外的
// 顶层键按**原始字节**收进 ToolDefinition.Extra，serialize 时并回输出
// 顶层（不覆盖已消费键）。与 paramreg 的未知字段 fail-open 哲学一致：
// 网关不做静默裁剪，上游拒绝的回退由 reqprobe 学习承担。
//
// 边界（如实登记）：function **对象内部**的非标准键（如 OpenAI 的
// strict）不在本轮修 —— 本轮的 Extra 只收工具顶层；streaming normalizer
// 对 function 内键天然保真（fn 整体复用），丢它的是 IR 往返，需要独立
// 载体，登记为后续工程。

// irToolKnownKeys* 各 parser 的消费集：这些键已被结构化消费（或按
// provider-specific Raw 分支整体捕获），不进 Extra。
var (
	irToolKnownKeysOpenAI = map[string]bool{
		"type": true, "function": true, "name": true,
		"description": true, "input_schema": true, "parameters": true,
	}
	irToolKnownKeysAnthropic = map[string]bool{
		"type": true, "name": true,
		"description": true, "input_schema": true, "parameters": true,
	}
)

// splitToolRaw 把单个工具的原始字节拆成「消费视图」（给既有 parse 逻辑）
// 与「剩余键视图」（按字节保真，不经过 float64 —— 与 preservedRawValue
// 同一保真纪律）。
func splitToolRaw(raw json.RawMessage) (map[string]any, map[string]json.RawMessage, bool) {
	var consumed map[string]any
	if err := json.Unmarshal(raw, &consumed); err != nil || consumed == nil {
		return nil, nil, false
	}
	var leftovers map[string]json.RawMessage
	if err := json.Unmarshal(raw, &leftovers); err != nil {
		return consumed, nil, true
	}
	return consumed, leftovers, true
}

// toolExtrasFrom 把剩余键视图里消费集之外的键收成一个 JSON 对象。
// 键序由 map marshal 决定（字典序，确定性）；空集返回 nil。
func toolExtrasFrom(leftovers map[string]json.RawMessage, known map[string]bool) json.RawMessage {
	if len(leftovers) == 0 {
		return nil
	}
	var extra map[string]json.RawMessage
	for k, v := range leftovers {
		if known[k] {
			continue
		}
		if extra == nil {
			extra = make(map[string]json.RawMessage, 2)
		}
		extra[k] = v
	}
	if len(extra) == 0 {
		return nil
	}
	b, err := json.Marshal(extra)
	if err != nil {
		return nil
	}
	return b
}

// mergeToolExtras 把 ToolDefinition.Extra 里的顶层键并回输出 map
//（不覆盖已写入的键 —— 既有「IR 序列化器输出优先」语义）。
func mergeToolExtras(out map[string]any, extra json.RawMessage) {
	if len(out) == 0 || len(extra) == 0 {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(extra, &m) != nil {
		return
	}
	for k, v := range m {
		if _, exists := out[k]; exists {
			continue
		}
		out[k] = preservedRawValue(v)
	}
}
