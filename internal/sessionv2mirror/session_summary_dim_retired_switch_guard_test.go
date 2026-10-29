//go:build !integration

package sessionv2mirror

import (
	"os"
	"strings"
	"testing"
)

// session_summary_dim_retired_switch_guard_test.go — 退役开关不得支配活写入
// （2026-10-29）。
//
// # 这道门守什么
//
// `session_summaries` 的聚合列（request_count / success_count / error_count /
// total_cost_usd / tokens / latency / models_used …）在 v1 族退役后的**唯一
// 写方**是 `SessionSummaryDimWriter`。2026-10-29 之前，它被 v1 退役开关
// `storage.request_logs_write_enabled` 门住：开关为 true ⇒ 整体 no-op。
//
// 那道门曾经是合理的双喂防护（v1 停写前 `trg_update_session_summary` 还在
// 计数），但它的两个前提都已随 v1 族退役消失：
//
//	① PG 侧 v1 写入分支已从 telemetry/client.go 整段删除 ⇒ 本二进制不写
//	   `_request_logs_hot`，触发器无输入可触发（灰度窗口内库未升级也成立）；
//	② 890 物理删除了 v1 族、两个触发器与 `update_session_summary()`。
//
// 于是门只剩「静默停摆」这一个效果：运维把一个控制「v1 写不写」的退役开关
// 拨到 true，用户面（admin 会话列表/详情、会话分析、usage_enhanced 意图列）
// 在新会话上永远不出现，且**无日志、无错误**。
//
// # 为什么是静态门（而不是再翻一次设置）
//
// 行为侧的「开关为 true ⇒ 照写」已经由真库门
// TestSessionSummaryDimWriter_S4HandoffSemantics 的 C 段钉住。但行为门
// 需要一个 seam 去翻那个开关——而 seam 本身就是「退役开关还能被读」的证据。
// 所以这里用**源码级**判据：源文件**不再引用**该开关 ⇒ 该开关在物理上无法
// 支配本写入器。静态判据零依赖、零假绿，且变异臂天然可做（把引用塞回去
// ⇒ 立刻报红）。
//
// 同族先例：`settings/key_request_logs_write_enabled_test.go`（键字面量
// 只拼一次）、`admin/request_logs_control_plane_gated_flag_test.go`
// （调用点钉死）。
func TestSessionSummaryDimWriter_IgnoresRetiredV1Switch(t *testing.T) {
	source, err := os.ReadFile("session_summary_dim.go")
	if err != nil {
		t.Fatalf("read session_summary_dim.go: %v", err)
	}
	text := string(source)

	// ★ 判据落在**语句**上，不是整份文件：文件头的注释里必须能自由讨论
	// 「这道门为什么被删」（含开关名），否则注释会被迫自我审查、最后连
	// 「曾经有这道门」都不敢写。因此只查可执行代码里的标识符出现。
	code := stripCommentsAndStrings(text)

	forbidden := []string{
		"v1WritesEnabled",              // 已删除的 seam 名
		"RequestLogsWriteEnabled",      // settings 侧读函数
		"KeyRequestLogsWriteEnabled",   // settings 侧键常量
		"storage.request_logs_write_enabled", // 键字面量
	}
	for _, needle := range forbidden {
		if strings.Contains(code, needle) {
			t.Fatalf("session_summary_dim.go 的可执行代码里出现 %q："+
				"退役开关不得再支配 session_summaries 聚合写入"+
				"（本写入器是聚合列的唯一写方，门只剩静默停摆一个效果）", needle)
		}
	}

	// 该文件也不应再依赖 settings 包（删门后已无用途；留着会诱使后来者
	// 「顺手」把开关接回来）。两种写法都要抓：import 行本身，以及调用点。
	for _, needle := range []string{
		"\"github.com/kaixuan/llm-gateway-go/settings\"",
		"settings.",
	} {
		if strings.Contains(code, needle) {
			t.Fatalf("session_summary_dim.go 的可执行代码仍引用 settings 包（%q）："+
				"删门后已无用途，留着等于给「把退役开关接回来」留一个把手", needle)
		}
	}

	// ★ 第二颗牙：门不只可能是「退役开关那扇」。任何在**写入之前**的
	// 条件返回，都会让聚合停摆（而且必然静默）。所以判据升级为结构性：
	// UpsertSessionDim 在第一次 pool.Exec 之前**只允许两个 return** ——
	// entry 为空的空操作、pool 为 nil 的装配错误。
	//
	// 为什么用「Exec 之前的 return 数」而不是「return 总数」：Exec 之后的
	// `if err != nil { return err }` 与末尾的 `return nil` 是正常返回路径，
	// 数总数会把正常代码当成门。
	body := extractFuncBody(code, "func (w *SessionSummaryDimWriter) UpsertSessionDim(")
	if body == "" {
		t.Fatalf("找不到 UpsertSessionDim 函数体（签名变更？）")
	}
	execIdx := strings.Index(body, "w.pool.Exec")
	if execIdx < 0 {
		t.Fatalf("UpsertSessionDim 里找不到 w.pool.Exec（写入路径被挪走？）")
	}
	preExec := body[:execIdx]
	gateCount := strings.Count(preExec, "return")
	if gateCount != 2 {
		t.Fatalf("UpsertSessionDim 在写入前有 %d 个 return（应为 2：entry 空 / pool nil）："+
			"聚合写入前的任何条件返回都是一扇能让 session_summaries 静默停摆的门", gateCount)
	}
}

// extractFuncBody 按**大括号深度**取出函数体（跳过签名后第一个 '{' 起算，
// 深度归零处收尾）。
//
// 为什么不用正则：函数体里有字符串/注释里的花括号时，正则会数错；而且本仓
// 已吃过「用第一个闭合标签配对 ⇒ 文件后半段成盲区」的亏（LESSONS §419）。
// 这里刻意在**剥掉注释与字符串之后**再数深度，判据落在代码骨架上。
func extractFuncBody(code, signature string) string {
	idx := strings.Index(code, signature)
	if idx < 0 {
		return ""
	}
	open := strings.Index(code[idx:], "{")
	if open < 0 {
		return ""
	}
	start := idx + open
	depth := 0
	for i := start; i < len(code); i++ {
		switch code[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return code[start+1 : i]
			}
		}
	}
	return ""
}

// stripCommentsAndStrings 去掉注释与字符串字面量，只留可执行代码骨架。
//
// 为什么需要它：判据是「源文件不得读这个开关」，而源文件的**注释**必须能
// 自由提到这个开关（解释它为什么被删）。若不剥注释，这道门会逼着作者
// 闭嘴——那是把治理变成自我审查。
//
// 实现要点：逐字符扫描，处理 `//` 行注释、`/* */` 块注释、`"` 双引号字符串、
// 反引号原始字符串、`'` 字符字面量。转义在字符串/字符字面量内按 `\X` 跳过。
func stripCommentsAndStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	const (
		stateCode = iota
		stateLineComment
		stateBlockComment
		stateString
		stateRawString
		stateChar
	)
	state := stateCode
	for i := 0; i < len(src); i++ {
		c := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch state {
		case stateCode:
			switch {
			case c == '/' && next == '/':
				state = stateLineComment
				i++
			case c == '/' && next == '*':
				state = stateBlockComment
				i++
			case c == '"':
				state = stateString
			case c == '`':
				state = stateRawString
			case c == '\'':
				state = stateChar
			default:
				out.WriteByte(c)
			}
		case stateLineComment:
			if c == '\n' {
				state = stateCode
				out.WriteByte(c)
			}
		case stateBlockComment:
			if c == '*' && next == '/' {
				state = stateCode
				i++
			}
		case stateString:
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				state = stateCode
			}
		case stateRawString:
			if c == '`' {
				state = stateCode
			}
		case stateChar:
			if c == '\\' {
				i++
				continue
			}
			if c == '\'' {
				state = stateCode
			}
		}
	}
	return out.String()
}
