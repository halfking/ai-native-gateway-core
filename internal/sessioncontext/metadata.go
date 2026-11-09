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
		return nil, ErrProtectedContext
	}
	for _, key := range []string{"inlineData", "inline_data", "fileData", "file_data", "encrypted_content", "image_url", "input_audio"} {
		if _, ok := doc.Fields[key]; ok {
			return nil, ErrProtectedContext
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
	if p, ok := doc.Fields["tool_calls"]; ok {
		b, e := read(p, materializeItem)
		if e != nil {
			return nil, e
		}
		root["tool_calls"] = b
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
