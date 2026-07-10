package streaming

import (
	"encoding/json"
	"log/slog"
)

// TODO(AUDIT-2026-07-11): 以下字段清单为推测，需要根据生产环境抓包结果验证和补充。
// 抓包方法：curl 真实 API 并观察响应字段。
// 参考文档：https://open.bigmodel.cn/dev/api#glm-4
var zhipuPrivateFields = []string{
	"zhipu_request_id",      // 智谱私有请求ID
	"cache_read_tokens",     // 缓存读取token数
	"cache_creation_tokens", // 缓存创建token数
	"web_search_results",    // 联网搜索结果（智谱专有功能）
	"retrieval_documents",   // 知识库检索文档（智谱专有功能）
	"model_version",         // 内部模型版本号
	"system_fingerprint",    // 系统指纹（可能包含敏感信息）
	"sensitive_word_check",  // 敏感词检查结果
	"request_id",            // 通用请求ID字段
}

func StripZhipuFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := 0
	for _, k := range zhipuPrivateFields {
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
		slog.Warn("strip_zhipu: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}
