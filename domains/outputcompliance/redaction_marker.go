package outputcompliance

import "strings"

// RedactionMarker 返回输出侧的遮蔽标记：`[REDACTED:phone]`、`[REDACTED:secret]`……
//
// 为什么不再用 `*********` 代替原文（2026-10-10）
//
// 旧实现用固定长度的星号串顶替命中值（`138****8000`、`a***@e***.com`、
// 与原文等长的 `*********`）。那个形状有三个问题，且都不是审美问题：
//
//  1. **不可还原**。星号串没有编码、不进任何映射，命中即永久丢失。调用方
//     拿到的是一段坏文本，而不是「这里原本有什么」。
//  2. **长度泄漏 + 假精确**。与原文等长的星号串暴露了原值长度；`138****8000`
//     这种「保留首尾」的形状则暗示保留了可用的部分，实际上它是一个网关自己
//     编造的、与原值无对应关系的字符串——运维按它去查会得到错误结论。
//  3. **不可辨识**。裸 `*********` 不说明遮的是什么，客户端无从判断这是
//     密钥、手机号还是地址。
//
// 带类型的标记把「遮掉了哪一类」写进输出：类型名是**规则的名字**，不是它匹配
// 到的值，因此不构成泄漏（与 security/sanitize 的 RedactionMarker 同一论证）。
//
// r1038（2026-10-20）：类型**不可知**时也不退回裸形态，而是写成
// `[REDACTED:unknown]`。用户口径「原则上每个占位符都有特殊的标记」——
// 客户端看到的遮蔽物必须自证遮掉了哪一类；「unknown」是一句关于**这次遮蔽**
// 的实话（类型没解析出来），不是对被遮内容的猜测。
//
// 与 security/sanitize.RedactionMarker 形状必须一致，但**不能直接复用**：
// sanitize 依赖本包（output_sensitive.go 里 outputSensitiveChecker 调用
// outputcompliance 的类型），反向 import 会成环。两处的形状由
// domains/outputcompliance/r1013_marker_contract_test.go 对齐。
func RedactionMarker(kind string) string {
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		return "[REDACTED:unknown]"
	}
	// 类型名来自 DB 规则 / 内建常量，但仍按标识符收紧一次：标记要出现在
	// JSON 与 SSE 文本里，不能让一个手滑写进 pii_patterns.pattern_type 的
	// 值带出换行或尖括号。
	var b strings.Builder
	b.Grow(len(kind) + len("[REDACTED:]"))
	b.WriteString("[REDACTED:")
	for _, r := range kind {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	b.WriteString("]")
	return b.String()
}
