package sessioncontext

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// r1053（2026-11-14，211 gpt-6-astra 入口 422 根修）钉测。
//
// 事故形状（归档件 7029f49d…，1,059,055B，gw_staging_ee2683c3 会话）：
// ZCode 会话里一条 assistant 消息的 `tool_calls` 字段长 69,901B > 64KB
// （materializeItem）帽 ⇒ fileMetadataBounded 的 `read(p, materializeItem)`
// fail-closed ⇒ 整个 Reduce 失败 ⇒ LargeIngress 422「cannot produce a valid
// bounded context; split the protected input」⇒ 会话砖化（客户端无法 split
// 历史里的工具调用）。与 r1052 的媒体三处同族（改写层制造入口自己都不会
// 放的形状·第四案），但与媒体无关——纯文本 1MiB body 照样命中。
//
// 生成机制＝ZCode 的 Write 工具：arguments 携带整个文件内容 ⇒ 一条
// assistant 消息可以合法携带 ~70KB 的 tool_calls。元数据面只需要每个
// call 的 id（工具链闭包），arguments 载荷从不进入最终 body（受保护
// 消息从 RAW 原样读回，那是设计内的精确保留）⇒ 修复＝按元素保 id、
// 省略 arguments（显式省略，非无声消失）。
func TestR1053ReduceSurvivesOversizedToolCallsInHistory(t *testing.T) {
	const oneMiB = 1 << 20
	// 单个 tool call 的 arguments ≈ 68KB（Write 大文件的 ZCode 形状），
	// message 级 tool_calls 字段合计 >64KB 帽。放在历史中部（非保护）。
	bigArgs := `{"file_path":"/tmp/generated-bundle.ts","content":"` + strings.Repeat("x", 68000) + `"}`
	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
		{"role": "user", "content": "write the generated bundle to disk"},
		{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{
				"id": "call_r1053_oversized", "type": "function",
				"function": map[string]any{"name": "Write", "arguments": bigArgs},
			},
		}},
		{"role": "tool", "tool_call_id": "call_r1053_oversized", "content": "ok"},
	}
	// pad 到 >1MiB（无关轮次）。
	pad := strings.Repeat("history filler line for the megabyte padding test. ", 190)
	for i := 0; i < 155; i++ {
		msgs = append(msgs, map[string]any{"role": "user", "content": pad})
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "ack " + jsonItoa(i)})
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "请继续收尾"})
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes must exceed 1MiB", len(body))
	}

	store, ref := captureForReduce(t, body)
	out, report, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must survive oversized tool_calls in history (r1053): %v", err)
	}
	if !report.Reduced || len(out) > oneMiB {
		t.Fatalf("reduce report=%+v outBytes=%d", report, len(out))
	}
	if !bytes.Contains(out, []byte("请继续收尾")) {
		t.Fatalf("latest user message must survive")
	}
	// 摘要面永不读 tool_calls ⇒ 巨型 arguments 不得泄漏进缩减体
	//（旧 fail-closed 之下这句跑不到；变异还原旧帽时上面的 Reduce 必红）。
	if bytes.Contains(out, []byte(strings.Repeat("x", 1024))) {
		t.Fatalf("arguments payload leaked into bounded context")
	}
}

// 尾部活动工具链形态：巨型 tool_calls 属于保护集（最后一轮工具循环）⇒
// 消息从 RAW 原样保留（设计内精确保留），id 与 result 闭合。旧 64KB 帽
// 在元数据提取阶段就已失败——本钉测证明新路径不再失败且闭包完整。
func TestR1053ReduceKeepsOversizedToolChainAtTail(t *testing.T) {
	const oneMiB = 1 << 20
	bigArgs := `{"file_path":"/tmp/generated-bundle.ts","content":"` + strings.Repeat("x", 68000) + `"}`
	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
	}
	msgs = append(msgs, padMsgs(150)...)
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "write the generated bundle to disk"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
			map[string]any{
				"id": "call_r1053_tail", "type": "function",
				"function": map[string]any{"name": "Write", "arguments": bigArgs},
			},
		}},
		map[string]any{"role": "tool", "tool_call_id": "call_r1053_tail", "content": "ok"},
	)
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes must exceed 1MiB", len(body))
	}
	store, ref := captureForReduce(t, body)
	out, report, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must keep oversized tail tool chain (r1053): %v", err)
	}
	if !report.Reduced || len(out) > oneMiB {
		t.Fatalf("reduce report=%+v outBytes=%d", report, len(out))
	}
	if !bytes.Contains(out, []byte("call_r1053_tail")) {
		t.Fatalf("tail tool call id must survive in protected chain")
	}
}

// 多 call 形态：一条 assistant 消息并行发起多个工具调用（每个 arguments
// 中型），tool_calls 合计超 64KB 帽且位于保护尾部 ⇒ 全部 id 保留。
func TestR1053ReduceSurvivesManyToolCallsAtTail(t *testing.T) {
	const oneMiB = 1 << 20
	calls := make([]any, 0, 40)
	for i := 0; i < 40; i++ {
		calls = append(calls, map[string]any{
			"id": "call_r1053_many_" + jsonItoa(i), "type": "function",
			"function": map[string]any{
				"name":      "Read",
				"arguments": `{"path":"/data/file` + jsonItoa(i) + `.bin","range":"` + strings.Repeat("y", 1800) + `"}`,
			},
		})
	}
	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
	}
	msgs = append(msgs, padMsgs(150)...)
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "inspect the data files"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": calls},
	)
	for i := 0; i < 40; i++ {
		msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": "call_r1053_many_" + jsonItoa(i), "content": "ok"})
	}
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes", len(body))
	}
	store, ref := captureForReduce(t, body)
	out, report, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must survive many tool_calls at tail (r1053): %v", err)
	}
	if !report.Reduced || len(out) > oneMiB {
		t.Fatalf("reduce report=%+v outBytes=%d", report, len(out))
	}
	if !bytes.Contains(out, []byte("call_r1053_many_0")) || !bytes.Contains(out, []byte("call_r1053_many_39")) {
		t.Fatalf("tool call ids lost from protected chain")
	}
}

// 超额省略形态：tool_calls 元素数超过 maxMetadataCalls（该消息 >64KB，
// 走 fileMetadata）⇒ 元数据面显式省略超额部分（不 fail），Reduce 照常
// 收敛；位于保护尾部时消息原文照旧精确保留（元数据省略不外溢）。
func TestR1053ReduceOmitsExcessToolCallElements(t *testing.T) {
	const oneMiB = 1 << 20
	calls := make([]any, 0, maxMetadataCalls+50)
	for i := 0; i < maxMetadataCalls+50; i++ {
		calls = append(calls, map[string]any{
			"id": "call_r1053_excess_" + jsonItoa(i), "type": "function",
			"function": map[string]any{"name": "Bash", "arguments": `{"cmd":"echo hi"}`},
		})
	}
	msgs := []map[string]any{
		{"role": "system", "content": "You are ZCode."},
	}
	msgs = append(msgs, padMsgs(150)...)
	msgs = append(msgs,
		map[string]any{"role": "user", "content": "run them all"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": calls},
	)
	raw, _ := json.Marshal(msgs[len(msgs)-1])
	if len(raw) <= materializeItem {
		t.Fatalf("fixture premise broken: message %d bytes must exceed %d", len(raw), materializeItem)
	}
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= oneMiB {
		t.Fatalf("fixture premise broken: body %d bytes", len(body))
	}
	store, ref := captureForReduce(t, body)
	out, report, err := Reduce(context.Background(), store, ref, oneMiB)
	if err != nil {
		t.Fatalf("Reduce must omit excess tool_calls without failing (r1053): %v", err)
	}
	if !report.Reduced || len(out) > oneMiB {
		t.Fatalf("reduce report=%+v outBytes=%d", report, len(out))
	}
	if !bytes.Contains(out, []byte("call_r1053_excess_0")) || !bytes.Contains(out, []byte("call_r1053_excess_"+jsonItoa(maxMetadataCalls+49))) {
		t.Fatalf("protected raw must keep all tool call ids (metadata omission must not leak)")
	}
}

// padMsgs 生成中部无关历史轮次（把 fixture 顶到 >1MiB）。
func padMsgs(n int) []map[string]any {
	pad := strings.Repeat("history filler line for the megabyte padding test. ", 190)
	out := make([]map[string]any, 0, n*2)
	for i := 0; i < n; i++ {
		out = append(out, map[string]any{"role": "user", "content": pad})
		out = append(out, map[string]any{"role": "assistant", "content": "ack " + jsonItoa(i)})
	}
	return out
}
