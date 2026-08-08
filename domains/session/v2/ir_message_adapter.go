package v2

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// ─────────────────────────────────────────────────────────────────────────
// IR ↔ v2.Message adapter (omni-ref3 A5/E6, Phase 1)
//
// Historically `v2.Message` is a flat-text representation:
//
//	{ "role": "...", "content": "string", "tool_calls": [...], "tool_call_id": "...", "name": "..." }
//
// while `ir.Message` is the strongly-typed superset:
//
//	{ "role": "...", "content": [ContentBlock...], "tool_calls": [ToolCall...], "tool_call_id": "...", "name": "...", "raw_content": any }
//
// The two representations overlap on `(role, tool_call_id, name)` and tool_calls
// but differ on content shape. To migrate without breaking the existing
// `session_bodies.*_body` JSON rows that already contain `content` as a plain
// string, this adapter uses a *dual-shape* contract:
//
//   - V2.Message.ToIR() converts the v2 Message back into an IR Message.
//     When content is a non-empty string, the string is wrapped in a single
//     `{"type":"text","text":<string>}` ContentBlock. When content is empty
//     and the IR Message had RawContent (multimodal array), IR is preferred
//     (round-tripping preserves multimodal blocks).
//   - MessageFromIR(ir.Message) produces a v2.Message. Text-only IR collapses
//     back into the legacy string content (byte-identical to legacy wire).
//     Multimodal IR stashes the IR envelope under `RawContent` (json:"-")
//     so the wire JSON for the legacy fields is unchanged; the reader
//     recognises the envelope per-message via looksEnvelope.
//
// The adapter is *pure*: it does not read or write session_bodies itself.
// The wire-format reader (sessionv2mirror/hook.go) and writer
// (BodiesRecord marshalling) call it. Future work (A5/E6 Phase 2) will
// switch BodiesRecord.*Delta to []ir.Message directly and these helpers
// become the boundary translation.
// ─────────────────────────────────────────────────────────────────────────

// ContentBlocksFromText is a small helper used by both the v2→IR and the
// IR→v2 paths so the two stay symmetric. Empty input yields a nil slice
// (preserving the "no content" semantic on both sides).
func ContentBlocksFromText(s string) []ir.ContentBlock {
	if s == "" {
		return nil
	}
	return []ir.ContentBlock{{Type: "text", Text: s}}
}

// TextFromContentBlocks returns the concatenation of every text block in the
// slice. Non-text blocks are ignored (their payload is preserved on the IR
// side via RawContent). Returns "" when the slice is empty or contains no
// text block, so callers can compare directly against the v2 Message.Content.
func TextFromContentBlocks(blocks []ir.ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	var out string
	for _, b := range blocks {
		if b.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// ToIR converts a v2.Message to an ir.Message. The conversion is symmetric
// with MessageFromIR: a non-empty v2.Content becomes a single text block;
// multimodal content previously serialised through IRMessageToV2Raw (see
// below) is recovered from RawContent when present.
//
// Safe to call on a zero-value v2.Message — the result is an empty IR
// message with role="" and nil content blocks, which downstream IR
// sanitisation treats as "no payload" (validate_and_fix.go removes it).
func (m Message) ToIR() ir.Message {
	out := ir.Message{
		Role:       m.Role,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}
	if raw, ok := m.RawContent.(json.RawMessage); ok && len(raw) > 0 {
		if recovered := IRMessageFromJSON(raw); recovered != nil {
			return *recovered
		}
	}
	if m.Content != "" {
		out.Content = ContentBlocksFromText(m.Content)
	}
	for _, raw := range m.ToolCalls {
		tc := ir.ToolCall{}
		if id, ok := raw["id"].(string); ok {
			tc.ID = id
		}
		if typ, ok := raw["type"].(string); ok {
			tc.Type = typ
		}
		if fn, ok := raw["function"].(map[string]interface{}); ok {
			tc.Function.Name, _ = fn["name"].(string)
			tc.Function.Arguments, _ = fn["arguments"].(string)
		}
		out.ToolCalls = append(out.ToolCalls, tc)
	}
	return out
}

// MessageFromIR is the inverse of (Message).ToIR. It accepts an IR Message
// and produces a v2.Message that round-trips back through ToIR without
// information loss for text-only payloads:
//
//   - When the IR Message has no RawContent and its content collapses to a
//     single text block, the v2 Message stores that text in the `content`
//     string (so on-disk JSON stays byte-identical to the legacy wire shape).
//   - When the IR Message carries multimodal blocks or a RawContent (the
//     typical case for image/audio/tool_use blocks produced by IR
//     serialisers), the v2 Message keeps `content: ""` and stashes the IR
//     JSON under RawContent via the envelope so the wire-format reader in
//     sessionv2mirror/hook.go can rebuild the IR message losslessly.
func MessageFromIR(in ir.Message) Message {
	out := Message{
		Role:       in.Role,
		ToolCallID: in.ToolCallID,
		Name:       in.Name,
	}
	text := TextFromContentBlocks(in.Content)
	hasMultimodal := false
	if len(in.Content) > 0 {
		for _, b := range in.Content {
			if b.Type != "text" {
				hasMultimodal = true
				break
			}
		}
	}
	if len(in.ToolCalls) > 0 {
		out.ToolCalls = make([]map[string]interface{}, 0, len(in.ToolCalls))
		for _, tc := range in.ToolCalls {
			out.ToolCalls = append(out.ToolCalls, map[string]interface{}{
				"id":   tc.ID,
				"type": tc.Type,
				"function": map[string]interface{}{
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				},
			})
		}
	}
	if !hasMultimodal && in.RawContent == nil && len(in.ToolCalls) == 0 {
		if text == "" {
			return out
		}
		out.Content = text
		return out
	}
	raw := irMessageToRaw(in)
	out.RawContent = raw
	return out
}

// IRMessagesFromV2 is a slice-level helper.
func IRMessagesFromV2(in []Message) []ir.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]ir.Message, 0, len(in))
	for i := range in {
		m := in[i]
		var recovered ir.Message
		if raw, ok := recoverIRRaw(m); ok {
			recovered = raw
		} else {
			recovered = m.ToIR()
		}
		out = append(out, recovered)
	}
	return out
}

// pickBodies selects the IR shadow when available and converts it to the V2
// compatibility shape. Legacy callers remain unchanged when no IR shadow is
// supplied.
func pickBodies(legacy []Message, shadow []ir.Message) []Message {
	if len(shadow) > 0 {
		return IRMessagesToV2(shadow)
	}
	return legacy
}

// pickBodiesForWrite is the persistence variant of pickBodies. It is kept as a
// named helper because request/response/outbound writers all use the same
// dual-shape selection rule.
func pickBodiesForWrite(legacy []Message, shadow []ir.Message) []Message {
	return pickBodies(legacy, shadow)
}

// IRMessagesToV2 is a slice-level helper.
func IRMessagesToV2(in []ir.Message) []Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]Message, 0, len(in))
	for _, m := range in {
		out = append(out, MessageFromIR(m))
	}
	return out
}

// ── Internal: IR raw payload round-trip via RawContent ──────────────────

// irRawEnvelope is the on-disk shape stored under RawContent. We keep the
// JSON small and stable; every field is exported so json.Marshal emits them
// by default; new fields are append-only.
type irRawEnvelope struct {
	Role    string          `json:"role"`
	Content []irRawBlock    `json:"content,omitempty"`
	Tools   []irRawToolCall `json:"tool_calls,omitempty"`
	TCID    string          `json:"tool_call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
}

type irRawBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	Image    json.RawMessage `json:"image,omitempty"`
	Audio    json.RawMessage `json:"audio,omitempty"`
	Document json.RawMessage `json:"document,omitempty"`
	ToolUse  json.RawMessage `json:"tool_use,omitempty"`
}

type irRawToolCall struct {
	ID       string          `json:"id"`
	Type     string          `json:"type,omitempty"`
	Name     string          `json:"name"`
	Args     string          `json:"arguments,omitempty"`
	ExtraRaw json.RawMessage `json:"raw,omitempty"`
}

// irMessageToRaw encodes an IR message into the irRawEnvelope JSON for
// dual-shape persistence under RawContent.
func irMessageToRaw(in ir.Message) json.RawMessage {
	env := irRawEnvelope{
		Role: in.Role,
		TCID: in.ToolCallID,
		Name: in.Name,
	}
	if len(in.Content) > 0 {
		env.Content = make([]irRawBlock, 0, len(in.Content))
		for _, b := range in.Content {
			rb := irRawBlock{Type: b.Type, Text: b.Text}
			if b.Image != nil {
				rb.Image, _ = json.Marshal(b.Image)
			}
			if b.Audio != nil {
				rb.Audio, _ = json.Marshal(b.Audio)
			}
			if b.Document != nil {
				rb.Document, _ = json.Marshal(b.Document)
			}
			if b.ToolUse != nil {
				rb.ToolUse, _ = json.Marshal(b.ToolUse)
			}
			env.Content = append(env.Content, rb)
		}
	}
	if len(in.ToolCalls) > 0 {
		env.Tools = make([]irRawToolCall, 0, len(in.ToolCalls))
		for _, tc := range in.ToolCalls {
			env.Tools = append(env.Tools, irRawToolCall{
				ID:   tc.ID,
				Type: tc.Type,
				Name: tc.Function.Name,
				Args: tc.Function.Arguments,
			})
		}
	}
	out, _ := json.Marshal(env)
	return out
}

// recoverIRRaw pulls an IR message back out of a v2.Message that was
// previously produced by MessageFromIR. Returns false when the v2.Message
// carries no RawContent payload, signalling that callers should fall back to
// the plain ToIR path.
func recoverIRRaw(m Message) (ir.Message, bool) {
	raw, ok := m.RawContent.(json.RawMessage)
	if !ok {
		return ir.Message{}, false
	}
	if len(raw) == 0 {
		return ir.Message{}, false
	}
	var env irRawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ir.Message{}, false
	}
	out := ir.Message{
		Role:       env.Role,
		ToolCallID: env.TCID,
		Name:       env.Name,
	}
	if len(env.Content) > 0 {
		out.Content = make([]ir.ContentBlock, 0, len(env.Content))
		for _, rb := range env.Content {
			b := ir.ContentBlock{Type: rb.Type, Text: rb.Text}
			if len(rb.Image) > 0 {
				var img ir.ImageSource
				if err := json.Unmarshal(rb.Image, &img); err == nil {
					b.Image = &img
				}
			}
			if len(rb.Audio) > 0 {
				var a ir.MediaSource
				if err := json.Unmarshal(rb.Audio, &a); err == nil {
					b.Audio = &a
				}
			}
			if len(rb.Document) > 0 {
				var d ir.DocumentBlock
				if err := json.Unmarshal(rb.Document, &d); err == nil {
					b.Document = &d
				}
			}
			if len(rb.ToolUse) > 0 {
				var t ir.ToolUse
				if err := json.Unmarshal(rb.ToolUse, &t); err == nil {
					b.ToolUse = &t
				}
			}
			out.Content = append(out.Content, b)
		}
	}
	if len(env.Tools) > 0 {
		out.ToolCalls = make([]ir.ToolCall, 0, len(env.Tools))
		for _, rt := range env.Tools {
			tc := ir.ToolCall{ID: rt.ID, Type: rt.Type}
			tc.Function.Name = rt.Name
			tc.Function.Arguments = rt.Args
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}
	return out, true
}

// IRMessagesFromJSON parses an arbitrary JSON payload into a slice of IR
// messages. It tries four shapes in order:
//
//  1. Object form {"messages":[...]}: each entry may be either the legacy
//     string-content shape or the IR-shaped envelope (the sentinel field is
//     detected per-message via looksEnvelope).
//  2. Bare array form [{role,content,...}, ...].
//  3. Single IR envelope ({"role":..,"content":[..],...}) — produced by
//     MessageFromIR when a single multimodal message is the only payload
//     (e.g. test fixtures that re-emit the RawContent of one v2.Message).
//  4. Single legacy message ({"role":..,"content":...}).
//
// Empty / unparseable payloads return nil. Callers treat nil as "no body".
func IRMessagesFromJSON(raw json.RawMessage) []ir.Message {
	if len(raw) == 0 {
		return nil
	}
	trimmed := json.RawMessage(stripBOM(raw))
	// Object form
	var obj struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(trimmed, &obj); err == nil && len(obj.Messages) > 0 {
		return parseIRMessageArray(obj.Messages)
	}
	// Bare array form
	var arr []json.RawMessage
	if err := json.Unmarshal(trimmed, &arr); err == nil && len(arr) > 0 {
		return parseIRMessageArray(arr)
	}
	// Single envelope or single legacy message.
	if m := IRMessageFromJSON(trimmed); m != nil {
		return []ir.Message{*m}
	}
	return nil
}

// stripBOM strips a UTF-8 byte-order mark if present, so json.Unmarshal does
// not choke on bodies persisted by clients that prepend a BOM.
func stripBOM(raw []byte) []byte {
	const bom = "\xEF\xBB\xBF"
	if len(raw) >= 3 && string(raw[:3]) == bom {
		return raw[3:]
	}
	return raw
}

// parseIRMessageArray walks a slice of per-message JSON objects, dispatching
// each to IRMessageFromJSON.
func parseIRMessageArray(items []json.RawMessage) []ir.Message {
	if len(items) == 0 {
		return nil
	}
	out := make([]ir.Message, 0, len(items))
	for _, raw := range items {
		m := IRMessageFromJSON(raw)
		if m != nil {
			out = append(out, *m)
		}
	}
	return out
}

// IRMessageFromJSON parses a single message payload. Returns nil when the
// payload is empty, unparseable, or carries no payload at all (an empty
// `{}` / `{"role":""}` shape).
//
// Shape resolution order (documented for A5/E6 audit trail):
//
//  1. Legacy OpenAI-style probe ({role,content,tool_calls,...}) — this is
//     the wire shape produced by request_logs.RequestBody / ResponseBody /
//     OutboundBody today. When content is an array of typed blocks
//     (text/image_url/input_audio/...) the probe path decodes each block
//     into the matching IR ContentBlock. This is the common case.
//  2. IR-shaped envelope ({role, content:[...], tool_calls:[...]}) — the
//     dual-shape persistence format produced by MessageFromIR (via
//     RawContent). Distinguished from the legacy probe by the presence of
//     an explicit "tool_calls" array element with a "raw" sub-field OR a
//     content block whose "image"/"audio"/"document" keys are objects (the
//     envelope never uses OpenAI's "image_url" alias).
//
// Returns nil when neither shape parses, or when the parsed message is
// empty in every meaningful field (so callers can treat nil as "no body").
func IRMessageFromJSON(raw json.RawMessage) *ir.Message {
	if len(raw) == 0 {
		return nil
	}
	trimmed := json.RawMessage(stripBOM(raw))

	// 1. Legacy probe (OpenAI wire). This is the dominant shape on disk and
	//    is the only path that knows how to translate image_url / input_audio
	//    blocks into IR ContentBlocks.
	var probe msgProbeCompat
	if err := json.Unmarshal(trimmed, &probe); err == nil {
		legacy := probe.toIR()
		if !looksEnvelope(trimmed) {
			return dropEmptyIRMessage(legacy)
		}
		// Envelope shape: re-parse via the envelope decoder so we get
		// lossless RawContent (image/audio/document blocks).
		var env irRawEnvelope
		if err := json.Unmarshal(trimmed, &env); err == nil && env.looksIR() {
			recovered, _ := recoverIRFromEnvelope(env)
			return dropEmptyIRMessage(recovered)
		}
		return dropEmptyIRMessage(legacy)
	}
	return nil
}

// dropEmptyIRMessage returns nil when the message carries no payload (no
// role, no content blocks, no tool calls, no raw content). Callers treat
// nil as "no message".
func dropEmptyIRMessage(m ir.Message) *ir.Message {
	if m.Role == "" && len(m.Content) == 0 && len(m.ToolCalls) == 0 && m.RawContent == nil && m.ToolCallID == "" && m.Name == "" {
		return nil
	}
	return &m
}

// looksEnvelope discriminates the IR-shaped envelope from the legacy OpenAI
// probe at the raw JSON level. The envelope produced by irMessageToRaw
// always populates one of these markers:
//
//   - any tool_calls entry with a non-empty "raw" field, or
//   - any content block carrying an "image", "audio", "document", or
//     "tool_use" object field (envelope-only names; OpenAI uses the alias
//     "image_url" which the envelope never emits).
//
// The function is intentionally strict: false positives force the decoder
// onto the legacy path, which already handles image_url / input_audio. The
// opposite (false negatives) would drop multimodal payload, which is the
// failure mode we are guarding against.
func looksEnvelope(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		return false
	}
	// Tool calls with a "raw" sub-field.
	if raw, ok := generic["tool_calls"]; ok {
		var arr []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, tc := range arr {
				if _, has := tc["raw"]; has {
					return true
				}
			}
		}
	}
	// Content blocks carrying envelope-only object fields.
	if raw, ok := generic["content"]; ok {
		var arr []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, b := range arr {
				for _, k := range []string{"image", "audio", "document", "tool_use"} {
					if _, has := b[k]; has {
						return true
					}
				}
			}
		}
	}
	return false
}

// msgProbeCompat is the on-wire shape used by request_logs entries: a flat
// content string plus optional tool_calls / tool_call_id / name. Defined here
// rather than reusing sessionv2mirror.msgProbe so this adapter stays
// self-contained and importable from any package (Phase 2 will move
// sessionv2mirror to depend on this adapter).
type msgProbeCompat struct {
	Role       string                   `json:"role"`
	Content    json.RawMessage          `json:"content"`
	ToolCallID string                   `json:"tool_call_id"`
	Name       string                   `json:"name"`
	ToolCalls  []map[string]interface{} `json:"tool_calls"`
}

// toIR converts a legacy wire probe to an ir.Message. The content field is
// decoded in three flavours:
//
//   - empty: nil
//   - "string": wrapped in a single text block
//   - array: each block decoded into the corresponding IR ContentBlock
func (p msgProbeCompat) toIR() ir.Message {
	out := ir.Message{
		Role:       p.Role,
		ToolCallID: p.ToolCallID,
		Name:       p.Name,
	}
	if len(p.ToolCalls) > 0 {
		out.RawContent = p.ToolCalls
		out.ToolCalls = make([]ir.ToolCall, 0, len(p.ToolCalls))
		for _, raw := range p.ToolCalls {
			tc := ir.ToolCall{}
			tc.ID, _ = raw["id"].(string)
			tc.Type, _ = raw["type"].(string)
			if fn, ok := raw["function"].(map[string]interface{}); ok {
				tc.Function.Name, _ = fn["name"].(string)
				tc.Function.Arguments, _ = fn["arguments"].(string)
			}
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}
	switch raw := p.Content; {
	case len(raw) == 0:
		// nothing
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			out.Content = ContentBlocksFromText(s)
		}
	case raw[0] == '[':
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			for _, bRaw := range arr {
				b := decodeContentBlock(bRaw)
				if b != nil {
					out.Content = append(out.Content, *b)
				}
			}
		}
	}
	return out
}

// decodeContentBlock maps an OpenAI-style content block JSON into an IR
// ContentBlock. Unknown block types round-trip through RawContent so callers
// never silently drop data.
func decodeContentBlock(raw json.RawMessage) *ir.ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	var probe struct {
		Type       string          `json:"type"`
		Text       string          `json:"text"`
		ImageURL   json.RawMessage `json:"image_url"`
		InputAudio json.RawMessage `json:"input_audio"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	b := &ir.ContentBlock{Type: probe.Type, Text: probe.Text}
	switch probe.Type {
	case "text":
		// already populated
	case "image", "image_url":
		var img ir.ImageSource
		if len(probe.ImageURL) > 0 {
			if err := json.Unmarshal(probe.ImageURL, &img); err == nil {
				img.MediaType = "image/png"
				b.Image = &img
			}
		}
	case "input_audio":
		var in ir.InputAudioBlock
		if len(probe.InputAudio) > 0 {
			if err := json.Unmarshal(probe.InputAudio, &in); err == nil {
				b.InputAudio = &in
			}
		}
	default:
		b.RawContent = json.RawMessage(raw)
	}
	return b
}

// looksIR returns true when the envelope actually carries an IR-shaped
// payload (i.e. a content array, tool calls, or non-empty role). The check
// prevents an empty `{}` envelope from being treated as a valid IR message
// and short-circuits to the legacy probe path.
func (e irRawEnvelope) looksIR() bool {
	if e.Role != "" || e.TCID != "" || e.Name != "" {
		return true
	}
	if len(e.Content) > 0 {
		return true
	}
	if len(e.Tools) > 0 {
		return true
	}
	return false
}

// recoverIRFromEnvelope rebuilds an ir.Message from a parsed envelope,
// mirroring the inverse of irMessageToRaw.
func recoverIRFromEnvelope(env irRawEnvelope) (ir.Message, bool) {
	out := ir.Message{
		Role:       env.Role,
		ToolCallID: env.TCID,
		Name:       env.Name,
	}
	if len(env.Content) > 0 {
		out.Content = make([]ir.ContentBlock, 0, len(env.Content))
		for _, rb := range env.Content {
			b := ir.ContentBlock{Type: rb.Type, Text: rb.Text}
			if len(rb.Image) > 0 {
				var img ir.ImageSource
				if err := json.Unmarshal(rb.Image, &img); err == nil {
					b.Image = &img
				}
			}
			if len(rb.Audio) > 0 {
				var a ir.MediaSource
				if err := json.Unmarshal(rb.Audio, &a); err == nil {
					b.Audio = &a
				}
			}
			if len(rb.Document) > 0 {
				var d ir.DocumentBlock
				if err := json.Unmarshal(rb.Document, &d); err == nil {
					b.Document = &d
				}
			}
			if len(rb.ToolUse) > 0 {
				var t ir.ToolUse
				if err := json.Unmarshal(rb.ToolUse, &t); err == nil {
					b.ToolUse = &t
				}
			}
			out.Content = append(out.Content, b)
		}
	}
	if len(env.Tools) > 0 {
		out.ToolCalls = make([]ir.ToolCall, 0, len(env.Tools))
		for _, rt := range env.Tools {
			tc := ir.ToolCall{ID: rt.ID, Type: rt.Type}
			tc.Function.Name = rt.Name
			tc.Function.Arguments = rt.Args
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}
	return out, true
}
