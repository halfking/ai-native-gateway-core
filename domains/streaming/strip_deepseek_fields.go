package streaming

import (
	"encoding/json"
	"log/slog"
)

// TODO(AUDIT-2026-07-11): 以下字段清单为推测，需要根据生产环境抓包结果验证和补充。
// 抓包方法：curl 真实 API 并观察响应字段。
// 参考文档：https://platform.deepseek.com/api-docs/
var deepseekPrivateFields = []string{
	"deepseek_request_id",        // DeepSeek私有请求ID
	"reasoning_tokens",           // 关键：R1 推理token计费字段（DeepSeek R1专有）
	"prompt_cache_hit_tokens",    // 提示缓存命中token数
	"prompt_cache_miss_tokens",   // 提示缓存未命中token数
	"model_type",                 // 内部模型类型标识
	"system_fingerprint",         // 系统指纹
	"request_id",                 // 通用请求ID字段
	"reasoning_content",          // R1推理过程内容（可能包含内部推理细节）
	"cache_hit_tokens",           // 缓存命中token（旧版字段）
	"cache_creation_input_tokens", // 缓存创建输入token
}

func StripDeepSeekFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := 0
	for _, k := range deepseekPrivateFields {
		if _, ok := raw[k]; ok {
			delete(raw, k)
			stripped++
		}
	}
	if stripped == 0 {
		return body
	}
	out, err := json.Marshal(raw)
	if err != nil {
		slog.Warn("strip_deepseek: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}
