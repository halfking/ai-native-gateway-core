package executors

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// The legacy redaction callback returns only bytes. Recognize its structured
// policy envelope at the write boundary so routing receives a typed terminal
// instead of committing that envelope as an HTTP 200 success.
//
// ★ 2026-11-17（可重试性审计轮）：信封里除 code 外还带 rule（见
// domains/streaming 的 policyBlockErrorExtras —— rule 放在 error 对象的
// 顶层）。此前这里只认 code ⇒ 规则名在非流式面**整段丢失** ⇒ 执行层
// 无法区分「内容策略拦截（终态）」与「格式类守卫拦下上游的畸形输出
// （可换兄弟节点重试）」⇒ 两者都被判成 KindContentFilter。
//
// 现在把 rule 带出来（*errorsx.OutputPolicyError），Unwrap 后
// errors.Is(err, ErrOutputPolicyBlocked) 仍然成立 ⇒ 既有线上的终态处理
// 全部保持不变，只有「要不要可重试」这一项获得了判据。
func outputPolicyError(body []byte) error {
	var envelope struct {
		Error struct {
			Code   string `json:"code"`
			Rule   string `json:"rule"`
			Reason string `json:"reason"`
			Field  string `json:"field"`
			Digest string `json:"digest"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Code == "output_policy_blocked" {
		if envelope.Error.Rule != "" {
			return &errorsx.OutputPolicyError{
				RuleID: envelope.Error.Rule,
				Reason: envelope.Error.Reason,
				Path:   envelope.Error.Field,
				Digest: envelope.Error.Digest,
			}
		}
		return errorsx.ErrOutputPolicyBlocked
	}
	return nil
}
