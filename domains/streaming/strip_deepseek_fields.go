package streaming

import (
	"encoding/json"
	"log/slog"
)

// TODO(AUDIT-2026-07-11): 以下字段清单为推测，需要根据生产环境抓包结果验证和补充。
// 抓包方法：curl 真实 API 并观察响应字段。
// 参考文档：https://platform.deepseek.com/api-docs/
//
// 2026-07-12 audit fix (audit-09): 移除官方/账单/usage 字段。
// - reasoning_tokens 是 R1 推理计费字段，必须保留
// - prompt_cache_hit_tokens / prompt_cache_miss_tokens 是账单对账字段，必须保留
// - reasoning_content 是语义输出，保留供输出侧处理
// - system_fingerprint 是 OpenAI compatible 标准字段，不应清理
// - request_id 保留作为通用追踪字段（但不在 std OpenAI 字段中）
var deepseekPrivateFields = []string{
	"deepseek_request_id", // DeepSeek私有请求ID
	"model_type",          // 内部模型类型标识
	"cache_hit_tokens",    // 缓存命中token（旧版字段，已被 prompt_cache_hit_tokens 替代）
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
