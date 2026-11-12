package sanitize

// media_restore.go — 2026-11-13 脱敏域逐条审计 P2 收口。
//
// /v1/images/generations 与 /v1/videos/generations 是非流式 JSON 应答
// （如 {created, data:[{url, b64_json, revised_prompt}]}），既不是 native
// Messages/Responses 形态，也没有 legacy choices 数组——此前它们不经过
// 响应侧还原拦截链（media handler 直接写 respBody），输入侧补上脱敏后
// 这里必须补上对称的还原出口，否则「占位符出得去、原文回不来」。
//
// 语义对齐 InterceptNonStream 的「未识别信封」契约（smart_sani_guard.go
// maskUnrecognizedBody 分支）：
//   - 映射表来自请求 Context（中间件在 Wrap 尾部放入的完整活字典，
//     已含 Redis 会话装载与本次新增映射）——不再读 Redis；
//   - 已知占位符逐叶还原（restoreNativeNestedValue 的通用树行走）；
//   - 未知/内部保留标记打码（[REDACTED] 语义，RestoreOutputOrMask）；
//   - 还原+打码后仍有残余保留标记 ⇒ fail-closed（ErrMediaRestoreBlocked，
//     由 handler 转 502，绝不把内部标记漏给客户端）；
//   - 无映射且无标记 ⇒ 直通（media 面在未启用脱敏时零开销）。

import (
	"context"
	"encoding/json"
	"errors"
)

// ErrMediaRestoreBlocked 由 media handler 消费：还原通道 fail-closed 时
// 响应不得下发，handler 应以 502 拒绝而非把含内部标记的原文写给客户端。
var ErrMediaRestoreBlocked = errors.New("sanitize restore: media body carries unrestorable reserved marker")

// RestoreMediaBody 还原/打码一个非流式 media generation 响应体。
//
// 返回 (restoredBody, changed, error)：
//   - changed=false 且 error=nil ⇒ 原样直通（响应不含占位符/保留标记）；
//   - changed=true ⇒ restoredBody 是已还原的 JSON（字节序可能重排）；
//   - error=ErrMediaRestoreBlocked（或其他内部错误）⇒ handler 必须 fail-closed。
func (it *SanitizeRestoreInterceptor) RestoreMediaBody(ctx context.Context, body []byte) ([]byte, bool, error) {
	if it == nil || it.sanitizer == nil || len(body) == 0 {
		return nil, false, nil
	}
	sm, _ := SanitizeMapFromContext(ctx)
	root := mustDecodeJSONMap(body)
	if root == nil {
		// 截断/非法 JSON 且带保留标记 ⇒ 绝不下发；干净的非 JSON
		// （理论上不该出现，media 上游恒为 JSON）直通。
		if hasReservedPlaceholder(ctx, body) {
			return nil, false, ErrMediaRestoreBlocked
		}
		return nil, false, nil
	}
	if len(sm) == 0 && !hasReservedPlaceholder(ctx, body) {
		return nil, false, nil
	}
	restored, changed, err := restoreNativeNestedValue(ctx, it.sanitizer, root, sm, 0)
	if err != nil {
		// 行走器错误（键名带标记 / 嵌套超限 / 不完整占位符）= fail-closed。
		return nil, false, err
	}
	out, marshalErr := json.Marshal(restored)
	if marshalErr != nil {
		return nil, false, ErrMediaRestoreBlocked
	}
	if hasReservedPlaceholder(ctx, out) {
		// 打码后仍残余 ⇒ 不可还原的未知形态（如 b64 串内伪造标记），
		// 与 InterceptNonStream 的 residual 契约同判：拦下，不半还原下发。
		return nil, false, ErrMediaRestoreBlocked
	}
	if !changed {
		return nil, false, nil
	}
	return out, true, nil
}
