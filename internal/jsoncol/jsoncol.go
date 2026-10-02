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
	"bytes"
	"encoding/json"
	"log/slog"
)

// jsonNullLiteral 是 JSON 的 null 字面量。jsonb 列存过 JSON null 后 pgx 取回
// 的字节就是它（canonical 形态无空白；TrimSpace 只是防御性兼容手拼 SQL）。
var jsonNullLiteral = []byte("null")

// Decode 把 raw 反序列化进 dst。
//
// 两种「无值」形态都**不是错误**，dst 保持调用方传入时的原值（通常是零值，
// 也可以是调用方 pre-seed 的默认值）不动：
//   - raw 为空（SQL NULL 或空串）；
//   - raw 是 JSON 字面量 null——JSON 惯例里 null 通常表示 not present，与
//     「列有内容但坏了」是两件事。订正（2026-10-01 审计）：此前实现把 null
//     当普通值 Unmarshal（对 null 恒成功），fresh 零值会覆盖 dst——调用方
//     pre-seed 的默认值被静默打回零值（受害实例：cmd/gateway 的
//     mqEnabled := true 在 settings_kv 存 null 时被打成 false；admin
//     credential_monitor 的空切片注释承诺与本实现相反）。
//
// 返回值：
//   - true  ：解析成功，或 raw 为空 / raw 为 null（三种都不是坏数据）
//   - false ：raw 非空且非 null，但解析失败，已留下 Warn 级痕迹，dst 保持
//     调用方传入时的原值
//
// op 必须能唯一定位到消费点（建议 "<包>.<函数>.<列名>"），否则日志无法
// 反查是哪条数据的哪一列出了问题。注意 op 是**列级**定位——同一列哪一
// 行坏了需调用方把行标识（如 command_id）拼进 op 才可追溯。
//
// 解码先落 fresh 再整份赋值，而不是直接 Unmarshal 进 dst：encoding/json
// 对 UnmarshalTypeError 是 save-error-then-continue 语义，坏字段跳过、
// **其余字段仍写入**——直接解进 dst 会让「部分填充的脏对象」逃逸到调用
// 方（12h 审计实测：center.Command.Args map 会被部分填充后照常下发执行）。
// fresh 形态保证失败时 dst 一个字节都不动，契约才真正成立。
func Decode[T any](op string, raw []byte, dst *T) bool {
	if len(raw) == 0 {
		return true
	}
	// JSON null 视为「无值」短路：dst 不动、无 Warn、return true。
	// （Unmarshal("null") 本身恒成功但不产出任何字段——走下面常规路径的话
	// fresh 零值会覆盖 dst，语义漂移见包文档订正注记。）
	if bytes.Equal(bytes.TrimSpace(raw), jsonNullLiteral) {
		return true
	}
	var fresh T
	if err := json.Unmarshal(raw, &fresh); err != nil {
		slog.Warn("jsonb column decode failed; caller value kept untouched",
			"op", op, "error", err, "bytes", len(raw))
		return false
	}
	*dst = fresh
	return true
}
