package reqprobe

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Diagnosis 是 Diagnose 的结论。
type Diagnosis struct {
	Trigger Trigger
	// Param 是从错误文案中解析出的被点名参数（可能与出站体顶层键对上，
	// 也可能为空）。仅 TriggerParamRejected 有意义。
	Param string
	// SuggestMode 是建议切换的请求形态："chat" 表示当前 responses 形态
	// 不被接受、应回退 chat/completions；"responses" 表示当前 chat 形态
	// 被告知要用 Responses API（仅记录建议，不自动切换）。
	SuggestMode string
	// Reason 是判定依据的短描述（落日志，不落库）。
	Reason string
}

// Input 是 Diagnose 的输入。
type Input struct {
	// HTTPStatus 是上游响应状态码（4xx 家族才会被判为请求侧问题）。
	HTTPStatus int
	// ErrorBody 是上游错误响应体（截断后的 4xx body）。
	ErrorBody []byte
	// OutboundBody 是发往上游的请求体（JSON），用于核对点名参数是否
	// 真实存在、以及判断可剔除参数。
	OutboundBody []byte
	// ErrorKind 是 errorsx 低基数分类（"client_bug" / "unsupported_feature"…）。
	ErrorKind string
	// Protocol 是出站协议（cand.Protocol：openai-chat / openai-responses /
	// anthropic-messages…）。
	Protocol string
	// NativeResponses 表示当前是否走了原生 Responses 传输（openai-responses
	// 候选且 SupportsNativeResponses(Stream) 开启）。
	NativeResponses bool
}

// namedParamPatterns 从错误文案中提取被点名的参数名。
// 覆盖已见过的各家文案：
//   - OpenAI:  "Unrecognized request argument supplied: reasoning_effort"
//     / "Unknown parameter: 'foo'."
//   - Anthropic: "unexpected field `thinking`" / "temperature: field required"
//   - xAI/Grok: "Invalid value for 'reasoning_effort': 'x-high' is not one of ..."
//   - 中转通用: "Extra inputs are not permitted" (无点名，靠键名交集)
var namedParamPatterns = []*regexp.Regexp{
	// "Invalid value for 'reasoning_effort': 'x-high' is not one of [...]"
	regexp.MustCompile("(?:invalid|unsupported|unknown|unexpected)\\s+(?:value|type)\\s+for\\s+['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]"),
	regexp.MustCompile("(?:argument|parameter|param|field|input)s?[\\s:]*['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]"),
	regexp.MustCompile("['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]\\s+(?:is not one of|is not supported|is not a valid|is invalid|must be|does not exist|is not recognized|is not allowed|is unknown|is unsupported)"),
	regexp.MustCompile("(?:unknown|unrecognized|unexpected|unsupported|invalid|extra|additional)\\s+(?:request\\s+)?(?:argument|parameter|field|input|property|key)\\s*[:\\s]*['\"`]?([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]?"),
}

// modeMismatchHints：端点/形态不匹配的文案信号（小写子串匹配）。
var modeMismatchHints = []string{
	"use /v1/chat/completions",
	"use the chat completions",
	"use chat/completions",
	"chat completions endpoint",
	"/v1/chat/completions",
	"not found. check the url", // xai: "The model ... or the url is incorrect"
	"unknown path",
	"unknown url",
	"invalid url",
	"no route for path",
	"unrecognized request url",
	"endpoint does not exist",
	"endpoint is not supported",
	"does not support /v1/responses",
	"responses api is not",
}

// responsesOnlyHints：上游明确要求 Responses API 的信号（当前 chat 形态
// 打过去被判"类型不对"）。仅记录建议，executor 不自动切换。
var responsesOnlyHints = []string{
	"use the responses api",
	"use /v1/responses",
	"requires the responses api",
	"only supported via the responses api",
	"this model only supports the responses",
	"must use the responses api",
}

// paramRejectionHints：参数被拒的泛化信号（未点名参数时兜底）。
var paramRejectionHints = []string{
	"extra inputs are not permitted",
	"additional properties are not allowed",
	"additionalproperties",
	"invalid request error",
	"invalid_request_error",
	"invalid value for",
	"is not one of",
	"unsupported parameter",
	"unsupported_argument",
	"unrecognized request argument",
	"unexpected field",
	"extra fields",
}

// jsonErrorMessages 提取错误体里各家常见的 message 字段（error.message /
// message / detail / error 顶层字符串）。非 JSON body（纯文本 4xx，或被
// 截断的非法 JSON）整体作为一条 message 处理。
func jsonErrorMessages(body []byte) []string {
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		if s := strings.TrimSpace(string(body)); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, 3)
	var walk func(node any, depth int)
	walk = func(node any, depth int) {
		if depth > 3 {
			return
		}
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				lk := strings.ToLower(k)
				if s, ok := child.(string); ok &&
					(lk == "message" || lk == "detail" || lk == "msg" || lk == "error" || lk == "status_msg") {
					out = append(out, s)
				} else {
					walk(child, depth+1)
				}
			}
		case []any:
			for _, child := range v {
				walk(child, depth+1)
			}
		}
	}
	walk(parsed, 0)
	return out
}

// extractNamedParam 从文案提取参数名，并优先返回确实存在于出站体顶层
// 的那个（防止把 "x-high" 这类值当参数名）。
func extractNamedParam(messages []string, outbound map[string]json.RawMessage) string {
	for _, re := range namedParamPatterns {
		for _, msg := range messages {
			for _, m := range re.FindAllStringSubmatch(strings.ToLower(msg), -1) {
				cand := strings.ToLower(m[1])
				if outbound == nil {
					return cand
				}
				if _, ok := outbound[cand]; ok {
					return cand
				}
			}
		}
	}
	return ""
}

// parseOutboundTopLevel 解析出站体顶层键（失败返回 nil）。
func parseOutboundTopLevel(body []byte) map[string]json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil
	}
	return obj
}

// Diagnose 判断一个 4xx 是否请求侧可归类/可尝试的问题。第二个返回值为
// false 表示"不感兴趣"（鉴权/限流/配额/内容过滤/上下文长度等都有专属
// 通道，不在这里重复记账）。
func Diagnose(in Input) (Diagnosis, bool) {
	if in.HTTPStatus < 400 || in.HTTPStatus >= 500 {
		return Diagnosis{}, false
	}
	switch in.HTTPStatus {
	case 401, 402, 403, 407, 408, 429:
		return Diagnosis{}, false
	}
	// 这些 kind 有各自的处置通道（mnf 重试 / 上下文压缩 / 内容过滤），
	// 不属于请求参数/形态问题。
	switch in.ErrorKind {
	case "model_not_found", "model_deprecated", "context_length_exceeded",
		"content_filter", "rate_limit", "auth", "auth_revoked", "quota",
		"quota_periodic", "quota_balance", "quota_permanent", "concurrent",
		"canceled", "timeout":
		return Diagnosis{}, false
	}

	messages := jsonErrorMessages(in.ErrorBody)
	if len(messages) == 0 {
		return Diagnosis{}, false
	}
	blob := strings.ToLower(strings.Join(messages, " \n "))
	outbound := parseOutboundTopLevel(in.OutboundBody)

	// 1) 形态不匹配优先：端点级 404/405，或文案明确指向端点/形态。
	if in.HTTPStatus == 404 || in.HTTPStatus == 405 {
		if in.NativeResponses {
			return Diagnosis{
				Trigger:     TriggerModeMismatch,
				SuggestMode: "chat",
				Reason:      "endpoint-level status on native responses transport",
			}, true
		}
		if containsAny(blob, modeMismatchHints) {
			return Diagnosis{
				Trigger:     TriggerModeMismatch,
				SuggestMode: "chat",
				Reason:      "endpoint hint in error body",
			}, true
		}
	}
	if containsAny(blob, modeMismatchHints) {
		// chat 形态打上 responses 端点等：建议切 chat（若已在 chat 则仅记录）。
		return Diagnosis{
			Trigger:     TriggerModeMismatch,
			SuggestMode: "chat",
			Reason:      "endpoint hint in error body",
		}, true
	}
	if containsAny(blob, responsesOnlyHints) {
		return Diagnosis{
			Trigger:     TriggerModeMismatch,
			SuggestMode: "responses",
			Reason:      "upstream demands responses api",
		}, true
	}

	// 2) 参数被拒：点名参数优先。
	if param := extractNamedParam(messages, outbound); param != "" {
		return Diagnosis{
			Trigger: TriggerParamRejected,
			Param:   param,
			Reason:  "named param in error text",
		}, true
	}
	// 兜底信号（未点名的 invalid_request 家族）——仅当出站体里确实存在
	// 可剔除参数时才算 param_rejected，否则按 upstream_error 记录。
	if containsAny(blob, paramRejectionHints) {
		if strippable := StrippableParams(in.OutboundBody, ""); len(strippable) > 0 {
			return Diagnosis{
				Trigger: TriggerParamRejected,
				Param:   strings.Join(strippable, ","),
				Reason:  "generic param rejection with strippable params present",
			}, true
		}
		return Diagnosis{
			Trigger: TriggerUpstreamError,
			Reason:  "generic invalid request, nothing strippable",
		}, true
	}

	// 3) 400/422 + client_bug/unsupported_feature kind 但无明确信号：
	// 归 upstream_error 供人工分类（ occurrences 高时人工再细分）。
	if in.ErrorKind == "client_bug" || in.ErrorKind == "unsupported_feature" {
		return Diagnosis{
			Trigger: TriggerUpstreamError,
			Reason:  "client-bug kind without param/mode signal",
		}, true
	}
	return Diagnosis{}, false
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
