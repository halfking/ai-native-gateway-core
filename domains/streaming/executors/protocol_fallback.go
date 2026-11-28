package executors

import (
	"strings"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// protocolUnsupportedStatuses 是可能表示"协议不支持"的 HTTP 状态码集合。
// 参考 chatRequiresResponses 的同款守门逻辑：400/404/405 是协议/端点不匹配的
// 典型信号。
var protocolUnsupportedStatuses = map[int]bool{
	400: true, // Bad Request（协议格式不对）
	404: true, // Not Found（端点路径不存在）
	405: true, // Method Not Allowed（端点存在但方法不对）
}

// isProtocolUnsupportedError 判断上游错误是否为"协议不支持"。
// 守门条件（三合一）：
//   1. HTTP 状态码在 protocolUnsupportedStatuses 集合中
//   2. 错误体包含协议/端点不匹配的关键词
//   3. 候选协议不是基协议（base protocol = openai-completions）
//
// 注意：只在非基协议时才判定为"协议不支持"，避免误判。如果连基协议都失败，
// 那是供应商整体不可用或配置错误，不应触发协议回退。
//
// 参考 chatRequiresResponses (responses_mode_bridge.go:109) 的设计模式。
func isProtocolUnsupportedError(candidateProtocol string, httpStatus int, body string) bool {
	// 守门 1：候选协议必须不是基协议（否则无回退空间）
	if candidateProtocol == providercatalog.ProtocolOpenAICompletions {
		return false
	}

	// 守门 2：HTTP 状态码必须在已知的协议不匹配集合中
	if !protocolUnsupportedStatuses[httpStatus] {
		return false
	}

	// 守门 3：错误体必须包含协议/端点不匹配的关键词
	b := strings.ToLower(strings.TrimSpace(body))
	if b == "" {
		return false
	}

	// 协议/端点不支持的典型错误消息模式（参考 vapEUR gpt-5.3-codex 事故）：
	// - "unsupported"（通用）
	// - "not found"（404 路径不存在）
	// - "invalid endpoint"（端点错误）
	// - "unknown model"（模型不存在于该协议）
	// - "method not allowed"（405）
	//
	// ★ 明确排除参数形状错误（与 chatRequiresResponses 同口径）：
	// - "unsupported parameter" / "不支持该参数" 是请求体格式bug，不是协议问题
	if strings.Contains(b, "unsupported parameter") ||
		strings.Contains(b, "不支持该参数") ||
		strings.Contains(b, "unsupported param") {
		return false // 参数错误，不是协议不支持
	}

	// 正向匹配：协议/端点不支持的关键词
	keywords := []string{
		"unsupported",
		"not found",
		"not supported",
		"invalid endpoint",
		"unknown endpoint",
		"unknown model",
		"method not allowed",
		"route not found",
		"endpoint does not exist",
	}

	for _, kw := range keywords {
		if strings.Contains(b, kw) {
			return true
		}
	}

	return false
}
