package streaming

import (
	"encoding/json"
	"log/slog"
)

// TODO(AUDIT-2026-07-11): 以下字段清单为推测，需要根据生产环境抓包结果验证和补充。
// 抓包方法：curl 真实 API 并观察响应字段。
// 参考文档：https://www.volcengine.com/docs/82379/1099475
var doubaoPrivateFields = []string{
	"doubao_request_id",      // 豆包私有请求ID
	"seeddance_request_id",   // 火山引擎内部请求ID（豆包母公司）
	"content_safety_score",   // 内容安全评分（可能包含敏感检测信息）
	"model_endpoint",         // 内部模型端点标识
	"ab_test_group",          // A/B测试分组标识
	"seed_token_usage",       // 火山引擎内部token计费字段
	"system_fingerprint",     // 系统指纹
	"request_id",             // 通用请求ID字段
	"volc_request_id",        // 火山引擎请求ID（可能的别名）
	"internal_model_version", // 内部模型版本号
	"sensitive_check",        // 敏感词检查结果
}

func StripDoubaoFieldsBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	stripped := 0
	for _, k := range doubaoPrivateFields {
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
		slog.Warn("strip_doubao: marshal failed, returning original body", "error", err)
		return body
	}
	return out
}
