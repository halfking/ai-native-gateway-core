package streaming

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// requestShapeRejection decides whether an executor failure is a **request
// shape** rejection（请求本身畸形 ⇒ 换候选/重试都不会变好），并给出该协议面
// 应当回给客户端的 4xx 内容。
//
// 2026-11-11（claude-fable-5-2 事故轮 · 第三层）：2026-11-10 的 P1-B 审计把
// 「格式门 fail-closed ⇒ 必须回 4xx」只加在了 **chat 面**（handler.go 的
// KindClientBug 分支），而 anthropic-messages 面（messages.go）与 responses
// 面（responses.go）没有对应分支 ⇒ 一个**永久畸形**的请求在这两个面上拿到
// 503「All providers unavailable」：
//
//	★ 客户端 SDK 按 5xx 退避重试一个**永远**不会成功的请求（放大无效流量）；
//	★ 落库 error_kind 还被记成 model_not_found（误导性分类——真实原因是
//	  「你发的请求不合法」，不是「这个模型不存在」）。
//
// 211 真机实测（2.5.9-532f5fa2 在役版本）：
//
//	POST /v1/messages {"model":"claude-fable-5-2","max_tokens":1024,
//	                    "messages":[{"content":"no role here"}]}
//	⇒ 503 {"error":{"message":"All providers unavailable","type":"overloaded_error"}}
//	⇒ request_logs.error_kind = model_not_found
//
// 对照组：入口层校验（缺 max_tokens）⇒ 400 invalid_request，
// 落库 error_kind=missing_max_tokens（精确）。
//
// 本函数是 anthropic-messages 面与 responses 面的**唯一**判定实现：
//   - ok=true  ⇒ 调用方必须回 4xx（status / errType / message / code 均取自
//     本函数的返回值）；
//   - ok=false ⇒ 该失败**不是**请求形态问题 ⇒ 调用方保持原路径
//     （零回归面：transient / network / auth / quota / content_filter 等
//     一律不受影响）。
//
// ★ 为什么判定放在协议面层而不是 executor 层：executor 层只认 errorsx kind
//
//	（它不该知道 HTTP），而三个协议面的错误信封形状不同（chat /
//	anthropic-messages / responses）⇒ 共享「是不是请求形态问题」这一个判定，
//	由各面按自己的信封形状渲染。
func requestShapeRejection(execErr error) (status int, errType, message, code string, ok bool) {
	ee, isExecErr := execErr.(*executors.ExecuteError)
	if !isExecErr || ee == nil || ee.LastKind != errorsx.KindClientBug {
		return 0, "", "", "", false
	}
	msg := "request rejected: the request body failed the provider wire format validation"
	if reason := extractUpstreamReason(execErr); reason != "" {
		msg = msg + " Reason: " + reason
	}
	return http.StatusBadRequest, "invalid_request_error", msg, "client_bug", true
}
