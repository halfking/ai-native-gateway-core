// Package jsoncol 提供 jsonb 列反序列化的统一失败语义（R67）。
//
// 背景：`SELECT jsonb_column` 取回的字节若反序列化失败，惯用写法是
//
//	_ = json.Unmarshal(argsJSON, &cmd.Args)
//
// 这与 pgx 的 rows 迭代截断是**同一族静默数据丢失**（R66 收的是后者）：
// 调用方拿到零值对象，且**无法区分「这行本来就是空的」与「这行的数据
// 坏了」**。在命令审计、消息正文、评审结果这类完整性通道上，这个区分
// 直接决定运维能不能查出问题。
//
// 语义选择：留痕后按零值继续，**不上抛**。
// 单行 jsonb 损坏而上抛会把整个列表端点变成 500——那是把「一行数据坏了」
// 升格成「接口不可用」，与 R66 定下的「不要影响客户端感观」原则相反。
// 正确做法是让端点照常返回，同时留下能定位到具体行与列的服务端痕迹。
//
// 与 dbrows 的分工：dbrows 管「行有没有读全」，本包管「读到的列能不能
// 解析」。两者都是完整性通道上的同类问题，但发生在不同层。
package jsoncol

import (
	"encoding/json"
	"log/slog"
)

// Decode 把 raw 反序列化进 dst。
//
// raw 为空（SQL NULL 或空串）时**不是错误**：直接返回 false，dst 保持
// 零值——这与「列有内容但解析失败」是两件事，调用方需要能分开看。
//
// 返回值：
//   - true  ：解析成功，或 raw 为空（两种都不是坏数据）
//   - false ：raw 非空但解析失败，已留下 Warn 级痕迹，dst 是零值
//
// op 必须能唯一定位到消费点（建议 "<包>.<函数>.<列名>"），否则日志无法
// 反查是哪条数据的哪一列出了问题。
func Decode[T any](op string, raw []byte, dst *T) bool {
	if len(raw) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		slog.Warn("jsonb column decode failed; zero value substituted",
			"op", op, "error", err, "bytes", len(raw))
		return false
	}
	return true
}
