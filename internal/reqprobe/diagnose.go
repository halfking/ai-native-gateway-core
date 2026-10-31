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
//   - kimi-k3 聚合中转 (2026-10-13 实测): "Validation: Unsupported
//     parameter(s): `web_search`" —— 名词带括号复数 (s)/(es)，两条点名
//     正则此前在此断链：param_rejected 判得出来、参数名提不出来，
//     剥参重试静默不触发，整单六候选全灭。
var namedParamPatterns = []*regexp.Regexp{
	// "Invalid value for 'reasoning_effort': 'x-high' is not one of [...]"
	regexp.MustCompile("(?:invalid|unsupported|unknown|unexpected)\\s+(?:value|type)\\s+for\\s+['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]"),
	// 2026-10-13（Vapeur gpt-5.6-sol temperature 事故）：chat 腿的值拒绝是
	// 冒号前缀形态 "Unsupported value: 'temperature' does not support 0.2
	// with this model."——上面那条要 "for"，此处无 for，此前两条都断链。
	regexp.MustCompile("(?:unsupported|invalid|unknown|unexpected)\\s+value\\s*[:：]\\s*['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]"),
	regexp.MustCompile("(?:argument|parameter|param|field|input)s?(?:\\([a-z]+\\))?[\\s:]*['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]"),
	// 同上事故：动词补 "does not support"（"does not support 0.2 with this
	// model" 的主语就是被点名的参数）。
	// 2026-10-13（Vapeur claude 家族 temperature+thinking）：Anthropic 系
	// 文案 "`temperature` may only be set to 1 when thinking is enabled"——
	// 反引号参数名 + "may only be" 动词族，补进同一条。
	regexp.MustCompile("['\"`]([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]\\s+(?:is not one of|is not supported|is not a valid|is invalid|must be|does not exist|is not recognized|is not allowed|is unknown|is unsupported|does not support|may only be|can only be)"),
	regexp.MustCompile("(?:unknown|unrecognized|unexpected|unsupported|invalid|extra|additional)\\s+(?:request\\s+)?(?:argument|parameter|field|input|property|key)s?(?:\\([a-z]+\\))?\\s*[:\\s]*['\"`]?([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]?"),
	// 2026-10-14（Bedrock 后端 effort 值拒绝，claude-opus-5-5 经 vapeur）：
	// "output_config.effort: Input should be 'low', 'medium', ..."——
	// 参数名在冒号前、无引号包裹，上面五条全断链（诊断不出 → 不记录 →
	// 不剥参重试 → 三候选连败整流 503）。
	regexp.MustCompile("([a-z_][a-z0-9_.\\-]{1,64})\\s*[:：]\\s*(?:input|effort|value)\\s+(?:should be|must be|allowed)"),
	// 同族：上游补救句点名参数（gpt-6.1-sol "set reasoning_effort to
	// 'none'"）——正文陈述里的裸词提不出来，补救句里必带引号值。
	regexp.MustCompile("set\\s+['\"`]?([A-Za-z_][A-Za-z0-9_.\\-]{1,64})['\"`]?\\s+to\\s+['\"`]"),
	// 2026-10-29（211 原厂直通生产验证轮实测）：MiniMax 参数 400 方言
	// "invalid params, invalid thinking.type: \"enabled\" (allowed:
	// adaptive, disabled) (2013)" —— 参数名在冒号前（可带命名空间点段）、
	// 值带双引号，上面七条全断链 ⇒ named 路径判不出 param_rejected ⇒
	// 原生对首发全参被拒后无剥参重试、整流 503（同请求非原生对反而被
	// 首发前改写保 200 —— 直通特性倒挂伤到它服务的客户端）。
	regexp.MustCompile("invalid\\s+([a-z_][a-z0-9_.\\-]{1,64})\\s*[:：]\\s*['\"`]"),
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
	// 2026-09-28 vapeur 轮：聚合中转的「不支持 Responses API」裁决——
	// claude 系 400「该供应商不支持 Responses API」+ code=unsupported_operation，
	// qwen/doubao 系 502 "X provider does not support the Responses API"。
	// CJK 文案的语序是「不支持」在前，英文 "responses api is not" 命不中。
	"不支持 responses api",
	"does not support the responses api",
	"responses api 不支持",
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

// chatDirectiveHints：上游明确要求切回 Chat API 的**指令句**信号。
//
// 2026-10-14（gpt-6.1-sol tools+effort 事故）：上游文案同时包含
// "… not supported … in /v1/chat/completions"（陈述语境，不是指令）与
// "use /v1/responses"（指令）。旧逻辑 chat 检查在前，裸子串
// "/v1/chat/completions" 命中陈述语境 → suggest_mode 误判为 chat。
// 规则：responses 指令句命中且 chat 只命中"提及"级信号时，方向取
// responses；chat 指令句（use/does not support responses 家族）命中则
// 维持 chat——与既有全部文案族兼容。
var chatDirectiveHints = []string{
	"use /v1/chat/completions",
	"use the chat completions",
	"use chat/completions",
	"does not support /v1/responses",
	"responses api is not",
	"不支持 responses api",
	"does not support the responses api",
	"responses api 不支持",
}

// paramRejectionHints：参数被拒的泛化信号（未点名参数时兜底）。
// thinkingUnsupportedHints 是「模型不支持 thinking」的错误文案族
// （2026-10-27 本地 ollama 实测：reasoning_effort 发给 qwen2.5:7b 等
// 非 thinking 模型 ⇒ 400 `"qwen2.5:7b" does not support thinking`）。
var thinkingUnsupportedHints = []string{
	"does not support thinking",
	"thinking is not supported",
	"does not support reasoning",
}

var paramRejectionHints = []string{
	"extra inputs are not permitted",
	"additional properties are not allowed",
	"additionalproperties",
	"invalid request error",
	"invalid_request_error",
	"invalid value for",
	"is not one of",
	"unsupported parameter",
	// 2026-10-13（Vapeur chat 腿值拒绝形态）：message 前缀
	// "Unsupported value: 'temperature' does not support ..."。
	"unsupported value",
	"unsupported_argument",
	"unrecognized request argument",
	"unexpected field",
	"extra fields",
	// 2026-10-14（Bedrock 后端 ValidationException，claude-opus-5-5 经
	// vapeur 实测）："output_config.effort: Input should be 'low', ..."。
	// 此前 hints 断链 → 整条 400 判不出 param_rejected → 不记录不剥参。
	"input should be",
	"allowed values",
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

// extractPydanticForbiddenField 从 FastAPI/pydantic 422 体提取被拒字段名。
// 两形态（raw-logs 实证）：
//  1. {"detail":[{"type":"extra_forbidden","loc":["body","service_tier"],...}]}
//  2. {"error":{"type":"pydantic","code":"extra_forbidden","loc":["body","service_tier"],...}}
//     （本仓 mock-p0 / 部分中转把 detail 收成 error 对象——2026-10-14 raw-logs
//     rid=89b4a272… 实测）。loc 末段字符串为字段名；优先返回出站体顶层键。
func extractPydanticForbiddenField(body []byte, outbound map[string]json.RawMessage) string {
	if len(body) == 0 {
		return ""
	}
	type locItem struct {
		Type string `json:"type"`
		Code string `json:"code"`
		Loc  []any  `json:"loc"`
	}
	var parsed struct {
		Detail []locItem `json:"detail"`
		Error  *locItem  `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	items := parsed.Detail
	if parsed.Error != nil {
		items = append(items, *parsed.Error)
	}
	if len(items) == 0 {
		return ""
	}
	var first string
	for _, item := range items {
		typ := strings.ToLower(strings.TrimSpace(item.Type))
		code := strings.ToLower(strings.TrimSpace(item.Code))
		// detail[] 用 type=extra_forbidden；error 对象常用 type=pydantic + code=extra_forbidden。
		okType := typ == "" || typ == "extra_forbidden" || typ == "unexpected_keyword_argument" || typ == "pydantic"
		okCode := code == "" || code == "extra_forbidden" || code == "unexpected_keyword_argument"
		if !okType || !okCode {
			continue
		}
		if typ == "pydantic" && code != "extra_forbidden" && code != "unexpected_keyword_argument" && code != "" {
			continue
		}
		field := ""
		for i := len(item.Loc) - 1; i >= 0; i-- {
			if s, ok := item.Loc[i].(string); ok {
				s = strings.TrimSpace(s)
				if s == "" || s == "body" || s == "query" || s == "path" {
					continue
				}
				field = strings.ToLower(s)
				break
			}
		}
		if field == "" {
			continue
		}
		if outbound != nil {
			if _, ok := outbound[field]; ok {
				return field
			}
		}
		if first == "" {
			first = field
		}
	}
	return first
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
				// 成员判定走别名扩展（strip.go wireKeyAliases）：上游点名
				// 的可能是命名空间形态（MiniMax "thinking.type"），我方
				// 线体顶层键是 "thinking" —— 直查顶层键会空手而归（211
				// 生产实测：原生对首发 thinking.enabled 被 MiniMax 400，
				// named 路径断链 ⇒ 无剥参重试 ⇒ client_bug 整流 503）。
				for _, key := range resolveWireKeys(cand) {
					if _, ok := outbound[key]; ok {
						return cand
					}
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

	outbound := parseOutboundTopLevel(in.OutboundBody)

	// 0) Pydantic / FastAPI detail[].loc（P0-B）：文案可能只有
	// "Extra inputs are not permitted"，参数名在 loc 数组——必须先于
	// messages 空检查，否则整段 detail 数组被 walk 成零消息后直接放弃。
	if param := extractPydanticForbiddenField(in.ErrorBody, outbound); param != "" {
		return Diagnosis{
			Trigger: TriggerParamRejected,
			Param:   param,
			Reason:  "pydantic loc extra_forbidden",
		}, true
	}

	messages := jsonErrorMessages(in.ErrorBody)
	if len(messages) == 0 {
		return Diagnosis{}, false
	}
	blob := strings.ToLower(strings.Join(messages, " \n "))

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
	// 2026-10-14（方向甄别）：responses 指令句优先于 chat 的"提及"级
	// 信号。上游文案常同时提到两个端点（"… not supported in
	// /v1/chat/completions. … use /v1/responses …"），裸子串
	// "/v1/chat/completions" 只是陈述语境；chat **指令句**（use/does
	// not support responses 家族）与 responses 指令句同时命中时维持
	// chat-first（与旧行为一致）。
	respDirective := containsAny(blob, responsesOnlyHints)
	chatDirective := containsAny(blob, chatDirectiveHints)
	if respDirective && !chatDirective {
		return Diagnosis{
			Trigger:     TriggerModeMismatch,
			SuggestMode: "responses",
			Reason:      "upstream demands responses api",
		}, true
	}
	if containsAny(blob, modeMismatchHints) {
		// chat 形态打上 responses 端点等：建议切 chat（若已在 chat 则仅记录）。
		return Diagnosis{
			Trigger:     TriggerModeMismatch,
			SuggestMode: "chat",
			Reason:      "endpoint hint in error body",
		}, true
	}
	if respDirective {
		return Diagnosis{
			Trigger:     TriggerModeMismatch,
			SuggestMode: "responses",
			Reason:      "upstream demands responses api",
		}, true
	}

	// 1.5) thinking 能力族（2026-10-27 多模态标准接口轮，本地 ollama 实测）：
	// `"qwen2.5:7b" does not support thinking`（HTTP 400）——reasoning_effort /
	// think 发给了**非 thinking 模型**。参数名不在文案里（只点名模型），
	// 按族识别：出站体带 thinking 族参数即判 param_rejected，点名剔除。
	// 调用方（网关端点/技能编排器）无法预知模型能力，这层自适应让
	// 「thinking 模型保 effort、非 thinking 模型裸发」两边都通。
	if containsAny(blob, thinkingUnsupportedHints) {
		if strippable := StrippableParamsForWire(in.OutboundBody, "reasoning_effort,think", ""); len(strippable) > 0 {
			return Diagnosis{
				Trigger: TriggerParamRejected,
				Param:   strings.Join(strippable, ","),
				Reason:  "model does not support thinking (reasoning params rejected)",
			}, true
		}
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
