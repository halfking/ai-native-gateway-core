package sessioncontext

import (
	"context"
	"encoding/json"
	"io"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// Only tool linkage and role fields are materialized for a giant message.
func fileMetadata(ctx context.Context, r io.ReaderAt, span largebody.Span, depth int) ([]byte, error) {
	remaining := 256 << 10
	return fileMetadataBounded(ctx, r, span, depth, &remaining)
}

// maxMetadataCalls bounds the per-message tool_calls array in metadata space;
// beyond it the excess calls are explicitly omitted (never a hard failure) —
// describeMessage simply sees fewer edges.
const maxMetadataCalls = 1024

// toolCallIDStub materializes only the linkage identity of one tool call
// (r1053): {"id":…,"type":…} — the arguments/function payload is explicitly
// omitted because this stub never leaves the metadata plane.
func toolCallIDStub(ctx context.Context, r io.ReaderAt, span largebody.Span, depth int, remaining *int) (json.RawMessage, error) {
	if depth > 6 || *remaining < 128 {
		return nil, ErrProtectedContext
	}
	*remaining -= 128
	doc, err := largebody.IndexObject(ctx, io.NewSectionReader(r, span.Offset, span.Length))
	if err != nil {
		return nil, err
	}
	stub := map[string]json.RawMessage{}
	for _, key := range []string{"id", "type"} {
		if p, ok := doc.Fields[key]; ok && p.Length <= 1024 {
			b, e := func() (json.RawMessage, error) {
				if p.Length > int64(*remaining) {
					return nil, ErrProtectedContext
				}
				*remaining -= int(p.Length)
				b := make([]byte, int(p.Length))
				_, err := r.ReadAt(b, span.Offset+p.Offset)
				return b, err
			}()
			if e != nil {
				return nil, e
			}
			stub[key] = b
		}
	}
	if _, ok := stub["id"]; !ok {
		// 显式省略标记：id 缺失时 describeMessage 抽不到边——账目不无声消失。
		stub["gw_tool_call_omitted"] = json.RawMessage("true")
	}
	return json.Marshal(stub)
}
func fileMetadataBounded(ctx context.Context, r io.ReaderAt, span largebody.Span, depth int, remaining *int) ([]byte, error) {
	if depth > 4 || *remaining < 256 {
		return nil, ErrProtectedContext
	}
	// Charge every object for structure/keys even when it contains no scalar.
	// This bounds both output framing and transient maps before construction.
	*remaining -= 256
	doc, err := largebody.IndexObject(ctx, io.NewSectionReader(r, span.Offset, span.Length))
	if err != nil {
		return nil, err
	}
	read := func(p largebody.Span, limit int64) (json.RawMessage, error) {
		if p.Length > limit || p.Length > int64(*remaining) {
			return nil, ErrProtectedContext
		}
		*remaining -= int(p.Length)
		b := make([]byte, int(p.Length))
		_, e := r.ReadAt(b, span.Offset+p.Offset)
		return b, e
	}
	root := map[string]json.RawMessage{}
	for _, key := range []string{"role", "type", "id", "call_id", "tool_call_id", "tool_use_id", "name"} {
		if p, ok := doc.Fields[key]; ok {
			b, e := read(p, 1024)
			if e != nil {
				return nil, e
			}
			root[key] = b
		}
	}
	var kind string
	_ = json.Unmarshal(root["type"], &kind)
	switch kind {
	case "image", "image_url", "input_image", "input_audio", "audio", "video", "input_video", "document", "file", "input_file", "redacted_thinking", "item_reference":
		// r1052（2026-11-13，211 fable-5-2 入口 422 根修）：巨型消息（>64KB）
		// 内的媒体块此前直接 ErrProtectedContext ⇒ 整个 Reduce 失败 ⇒
		// LargeIngress 422「cannot produce a valid bounded context; split the
		// protected input」⇒ 带截图的 ZCode 会话（历史全量 >1MB）每笔请求
		// 必败。媒体块的 token 计量归 contextbudget 的 external 面管；本层
		// 只需要 role + 工具链元数据——媒体块没有工具链，打桩保留类型、
		// 丢弃数据，让缩减继续（该消息若非保护区成员，后续照样被丢弃）。
		stub, _ := json.Marshal(map[string]any{"type": kind, "gw_media_omitted": true})
		return stub, nil
	}
	for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data", "encrypted_content", "image_url", "input_audio"} {
		if _, ok := doc.Fields[key]; ok {
			// r1052：同上——消息级媒体字段打桩而非整请求拒绝。
			root[key] = json.RawMessage(`"[media omitted from bounded context]"`)
		}
	}
	for _, key := range []string{"content", "parts"} {
		if spans, ok := doc.Arrays[key]; ok {
			if len(spans) > 1024 {
				return nil, ErrProtectedContext
			}
			blocks := make([]json.RawMessage, 0, len(spans))
			for _, part := range spans {
				part.Offset += span.Offset
				b, e := fileMetadataBounded(ctx, r, part, depth+1, remaining)
				if e != nil {
					return nil, e
				}
				blocks = append(blocks, b)
			}
			root[key], err = json.Marshal(blocks)
			if err != nil {
				return nil, err
			}
		}
	}
	if arr, ok := doc.Arrays["tool_calls"]; ok {
		// r1053（2026-11-14，211 gpt-6-astra 入口 422 根修）：tool_calls 整体
		// 64KB 帽此前 fail-closed ⇒ ZCode 大文件写入（Write 工具 arguments
		// 全文进 tool_calls）的消息元数据提取必败 ⇒ 整个 Reduce 失败 ⇒
		// LargeIngress 422「cannot produce a valid bounded context」——与
		// r1052 的媒体三处是同一族（改写层制造入口自己都不会放的形状·第四
		// 案）。本函数的输出只喂 describeMessage 抽 role + 工具链 id，从不
		// 进入最终 body（受保护消息从 RAW 原样读回）⇒ 每个 call 只需保
		// id/type，arguments 载荷显式省略（工具链闭包完整性不受影响）。
		if len(arr) > maxMetadataCalls {
			arr = arr[:maxMetadataCalls]
		}
		calls := make([]json.RawMessage, 0, len(arr))
		for _, call := range arr {
			call.Offset += span.Offset
			stub, e := toolCallIDStub(ctx, r, call, depth+1, remaining)
			if e != nil {
				return nil, e
			}
			calls = append(calls, stub)
		}
		root["tool_calls"], err = json.Marshal(calls)
		if err != nil {
			return nil, err
		}
	} else if p, ok := doc.Fields["tool_calls"]; ok {
		// 非数组形态（畸形/异构 wire）：可整体物化则照旧；超帽打桩省略继续
		//（id 抽不到 ⇒ 工具链闭包少一条边，保护集少保一条消息，不破坏正确性）。
		if p.Length <= materializeItem {
			b, e := read(p, materializeItem)
			if e != nil {
				return nil, e
			}
			root["tool_calls"] = b
		} else {
			if *remaining < 64 {
				return nil, ErrProtectedContext
			}
			*remaining -= 64
			root["tool_calls"] = json.RawMessage(`"[tool calls omitted from metadata]"`)
		}
	}
	for _, key := range []string{"functionCall", "functionResponse"} {
		if p, ok := doc.Fields[key]; ok {
			p.Offset += span.Offset
			b, e := fileMetadataBounded(ctx, r, p, depth+1, remaining)
			if e != nil {
				return nil, e
			}
			root[key] = b
		}
	}
	return json.Marshal(root)
}
