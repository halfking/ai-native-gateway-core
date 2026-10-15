package transformation

import (
	"bytes"
	"encoding/json"
)

// NormalizeResponsesTools 把 Responses 请求体里 chat 嵌套形的 tools 归一化为
// Responses 平铺形。
//
// r1024（2026-10-13，vapeur grok-4.6 生产复现）：客户端把 Chat Completions 的
// 工具形状（{"type":"function","function":{"name":...}}）直接发到 /v1/responses
// 时，网关原样透传，上游以 "Missing required parameter: 'tools[0].name'" 拒绝
// （vapeur 以 502 信封回包，被归类 transient 触发无意义重试与凭据连坐）。
// Responses 线格式的工具是平铺的（{"type":"function","name":...}），两种形状
// 语义等价、信息无损，归一化优于透传畸形——与 paramreg「翻译而非拒绝」的
// 教义同向。
//
// 只在有嵌套形态时改写；已是平铺形（或无 tools）时原字节返回。
func NormalizeResponsesTools(body []byte) []byte {
	if len(body) == 0 || !bytes.Contains(body, []byte(`"tools"`)) {
		return body
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	rawTools, ok := obj["tools"]
	if !ok {
		return body
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(rawTools, &tools); err != nil {
		return body
	}
	changed := false
	for i, rawTool := range tools {
		var tool map[string]json.RawMessage
		if err := json.Unmarshal(rawTool, &tool); err != nil {
			continue
		}
		nested, hasFunction := tool["function"]
		if !hasFunction {
			continue
		}
		var fn map[string]json.RawMessage
		if err := json.Unmarshal(nested, &fn); err != nil || fn == nil {
			continue
		}
		if _, hasName := fn["name"]; !hasName {
			continue
		}
		flat := map[string]json.RawMessage{"type": []byte(`"function"`)}
		// 平铺后 name/description/parameters/strict 提升到顶层；顶层原有
		// 的其它字段（如客户端附带的元数据键）保留。
		for _, key := range []string{"name", "description", "parameters", "strict"} {
			if v, ok := fn[key]; ok {
				flat[key] = v
			}
		}
		for k, v := range tool {
			if k == "function" || k == "type" {
				continue
			}
			if _, exists := flat[k]; !exists {
				flat[k] = v
			}
		}
		encoded, err := json.Marshal(flat)
		if err != nil {
			continue
		}
		tools[i] = encoded
		changed = true
	}
	if !changed {
		return body
	}
	encodedTools, err := json.Marshal(tools)
	if err != nil {
		return body
	}
	obj["tools"] = encodedTools
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
