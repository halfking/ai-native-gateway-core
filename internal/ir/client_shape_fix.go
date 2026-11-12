package ir

import (
	"encoding/json"
)

// client_shape_fix.go —— 客户端错误格式修正器（2026-11-11 审计第二轮 · 批 2）。
//
// ── SRP 边界 ─────────────────────────────────────────────────────────────
// 本文件承担「**单条消息 / 单个字段**」粒度的客户端畸形修正；会话级形态
// （role 交替、tool 结果配对）在 validate_and_fix.go。修正与披露的裁决
// 纪律（与第一轮审计钉测一致）：
//
//   - 无歧义的畸形 → 修正 + anomaly（修正是唯一合理解读，如空参数串）
//   - 有歧义的畸形 → 原样保留 + anomaly 披露（不猜测，如非法 JSON）
//   - 永不静默（此前 parse 层 8 类静默跳过/丢弃点的收口）
//
// anomaly 类型统一为 ir.client_shape_fix（dedup 由 reporter 承担），使
// 「客户端错误被网关修正」在生产日志面可观测、可计数 —— 此前这些事件
// 要么无痕（continue 跳过）要么混在通用 unknown_field 里。

// normalizeMessageRole 归一化缺失/空/非字符串的消息 role。
//
// 修正规则（从形态可证明，非猜测）：
//   - role 缺失/空/非字符串，但消息带非空 tool_call_id → "tool"
//     （OpenAI 形态下 tool 结果消息的唯一区别特征）
//   - 其余 → "user"（与 responses.go 手写转换对 roleless item 的既有
//     兜底同款 —— 仓库已有先例，不是新发明）
//
// 合法非空字符串 role 原样返回、零上报。
func normalizeMessageRole(msg map[string]any, sourceProtocol string) string {
	roleRaw, hasRole := msg["role"]
	role, isStr := roleRaw.(string)
	if hasRole && isStr && role != "" {
		return role
	}
	if id, ok := msg["tool_call_id"].(string); ok && id != "" {
		reportClientShapeFix(sourceProtocol, "role", "inferred_tool_from_tool_call_id",
			map[string]any{"inferred": "tool", "had_role": hasRole, "role_type": jsonTypeName(roleRaw)})
		return "tool"
	}
	reportClientShapeFix(sourceProtocol, "role", "inferred_user",
		map[string]any{"inferred": "user", "had_role": hasRole, "role_type": jsonTypeName(roleRaw)})
	return "user"
}

// inferRoleFromContentBlocks 从内容块推断 role（Anthropic 形态：tool 结果
// 是 content 块而非消息级 tool_call_id）。tool_result 块存在 → "tool"。
// 无可判形态返回 false，由调用方走 "user" 兜底。
func inferRoleFromContentBlocks(blocks []ContentBlock) (string, bool) {
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return "tool", true
		}
	}
	return "", false
}

// coerceToolCallArguments 修正 tool_calls/function_call 的参数字符串。
//
//   - "" → "{}"：无参调用的无歧义表达。OpenAI/Anthropic 上游普遍要求
//     arguments 为合法 JSON（空串会 400）；Anthropic 原生线把无参调用
//     编码为 input:"{}"。修正 + anomaly。
//   - 非法 JSON（非空）→ 原样保留 + anomaly 披露。修复方式不可从形态
//     证明（单引号？尾逗号？截断？），不猜 —— 终态守卫与上游会给出
//     类型化错误，客户端可自愈。
func coerceToolCallArguments(args string, sourceProtocol string) string {
	if args == "" {
		reportClientShapeFix(sourceProtocol, "tool_calls[].arguments", "empty_arguments_normalized",
			map[string]any{"fixed_to": "{}"})
		return "{}"
	}
	if !json.Valid([]byte(args)) {
		reportClientShapeFix(sourceProtocol, "tool_calls[].arguments", "invalid_json_disclosed",
			map[string]any{"length": len(args), "action": "preserved"})
	}
	return args
}

// reportSkippedElement 披露「数组中的非对象元素被跳过」。
// 此前 parse_openai / parse_anthropic / parse_responses 的 tool_calls 与
// content 块循环对非 map 元素一律 `continue` 静默跳过 —— 客户端的数组
// 缺陷（如元素是字符串/null）无任何痕迹。
func reportSkippedElement(sourceProtocol, what string, index int, value any) {
	reportClientShapeFix(sourceProtocol, what, "non_object_element_skipped",
		map[string]any{"index": index, "element_type": jsonTypeName(value)})
}

// geminiMalformedReason 区分 Gemini 畸形 part 的两类成因（metadata 用）。
func geminiMalformedReason(err error, keyEmpty bool) string {
	if err != nil {
		return "unmarshal_failed"
	}
	return "required_field_empty"
}

// reportClientShapeFix 是本文件所有修正/披露事件的统一出口。
// anomaly_type 沿用 ir_unknown_field（dedup/限幅面共用），reason 定制为
// "client_shape_fix.<fix>" —— 生产日志面按 reason 前缀过滤即可单独观测
// 「客户端错误被网关修正」的流量；且 dedup 键含 reason ⇒ 同一字段的不同
// 修正（如 role 推断为 tool 与 user）不会被 dedup 吞掉。
func reportClientShapeFix(sourceProtocol, fieldPath, fix string, extra map[string]any) {
	ReportAnomaly(AnomalyEvent{
		RequestID:         "unknown",
		AnomalyType:       AnomalyUnknownField,
		Severity:          SeverityLow,
		FieldPath:         fieldPath,
		SourceProtocol:    sourceProtocol,
		Reason:            "client_shape_fix." + fix,
		RawValueTruncated: true,
		Metadata:          mergeShapeFixExtra(fix, extra),
	})
}

func mergeShapeFixExtra(fix string, extra map[string]any) map[string]any {
	out := make(map[string]any, len(extra)+1)
	out["client_shape_fix"] = fix
	for k, v := range extra {
		out[k] = v
	}
	return out
}
