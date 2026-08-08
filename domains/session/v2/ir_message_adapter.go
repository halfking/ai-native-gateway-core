package v2

import (
	"encoding/json"
	"reflect"

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
//     back into the legacy string content, byte-identical to the legacy wire.
//     Anything richer keeps the IR envelope under `RawContent` (json:"-") and
//     persists its content-block array into the `content` column, tagged with
//     irBlockMarker so the reader classifies each block exactly
//     (looksEnvelopeBlock).
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
// Sources are *merged*, never short-circuited. An envelope may legitimately
// carry only the message-level fields (that is what UnmarshalJSON rebuilds for
// a row whose `content` is provider-native but which also has a `raw` key), so
// letting the envelope win outright would discard the real content.
func (m Message) ToIR() ir.Message {
	out := ir.Message{
		Role:       m.Role,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}

	// 1. Envelope: message-level fields, and usually the content blocks too.
	if env, ok := recoverIRRaw(m); ok {
		out = env
		// A partial envelope must not blank out fields the Message still has.
		if out.Role == "" {
			out.Role = m.Role
		}
		if out.ToolCallID == "" {
			out.ToolCallID = m.ToolCallID
		}
		if out.Name == "" {
			out.Name = m.Name
		}
	}

	// 2. Content: the envelope's blocks win; otherwise decode the structured
	//    `content` value (provider-native or envelope-encoded — the per-block
	//    dispatch in decodeContentBlocks handles either), then the text
	//    projection.
	if len(out.Content) == 0 {
		if raw := m.structuredContent(); len(raw) > 0 && raw[0] == '[' {
			out.Content = decodeContentBlocks(raw)
		} else if m.Content != "" {
			out.Content = ContentBlocksFromText(m.Content)
		}
	}

	// 3. Tool calls: only when the envelope did not supply them.
	if len(out.ToolCalls) == 0 {
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
	hasMultimodal := !blocksCollapseToText(in.Content)
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

// irEnvelopeContent splits a Message's RawContent into the two things the wire
// shape needs: the `content` block array and the message-level `raw` payload
// (ir.Message.RawContent).
//
// It returns:
//   - (block array, message raw) when RawContent is an IR envelope object,
//   - (RawContent verbatim, nil) when it is already a bare array — a
//     provider-native content value that UnmarshalJSON mirrored into both
//     fields,
//   - (nil, nil) otherwise, so MarshalJSON omits both keys.
//
// Keeping the array (rather than the whole envelope) in the `content` column is
// what makes multimodal payload survive a DB round-trip: the row stays a
// well-formed message and decodeContentBlocks reconstructs every block.
func irEnvelopeContent(rawContent any) (content, messageRaw json.RawMessage) {
	raw, ok := rawContent.(json.RawMessage)
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	if raw[0] == '[' {
		return raw, nil
	}
	var env irRawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, nil
	}
	if len(env.Content) > 0 {
		if out, err := json.Marshal(env.Content); err == nil {
			content = out
		}
	}
	return content, env.Raw
}

// structuredContent returns the structured `content` JSON for this message,
// preferring ContentRaw (the value read back from the DB row) and falling back
// to a RawContent that holds a bare block array rather than a full envelope.
func (m Message) structuredContent() json.RawMessage {
	if len(m.ContentRaw) > 0 {
		return m.ContentRaw
	}
	if raw, ok := m.RawContent.(json.RawMessage); ok {
		return raw
	}
	return nil
}

// blocksCollapseToText reports whether the blocks can be represented by the
// legacy `content: "string"` shape with no loss.
//
// Checking `Type == "text"` alone is not enough: a text block can also carry
// CacheControl (Anthropic prompt caching), Index (interleaved tool results) or
// RawContent, and collapsing such a block to a bare string discards them. An
// empty slice collapses trivially.
func blocksCollapseToText(blocks []ir.ContentBlock) bool {
	for _, b := range blocks {
		if b.Type != "text" {
			return false
		}
		if b.CacheControl != nil || b.Index != nil || b.RawContent != nil {
			return false
		}
		// Defensive: a block typed "text" should not carry these, but if a
		// parser ever sets one we must not silently drop it.
		if b.Image != nil || b.Audio != nil || b.Video != nil || b.Document != nil ||
			b.InputAudio != nil || b.ToolUse != nil || b.ToolResult != nil ||
			b.Thinking != nil || b.RedactedThinking != "" {
			return false
		}
	}
	return true
}

// IRMessagesFromV2 is a slice-level helper. ToIR already prefers the richest
// available source (envelope → structured content → text), so this is a plain
// map over it.
func IRMessagesFromV2(in []Message) []ir.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]ir.Message, 0, len(in))
	for i := range in {
		out = append(out, in[i].ToIR())
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

// irRawEnvelope is the on-disk shape stored under RawContent. The JSON is
// deliberately message-shaped (`role` / `content` / `tool_calls` / ...) so a
// reader that knows nothing about IR still sees a well-formed message row.
// New fields are append-only.
type irRawEnvelope struct {
	Role    string          `json:"role"`
	Content []irRawBlock    `json:"content,omitempty"`
	Tools   []irRawToolCall `json:"tool_calls,omitempty"`
	TCID    string          `json:"tool_call_id,omitempty"`
	Name    string          `json:"name,omitempty"`
	// Raw carries ir.Message.RawContent so a provider-native payload the IR
	// parser could not model survives the in-memory round-trip.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// irBlockMarker is the sentinel key that identifies an envelope-encoded block.
//
// It replaces the previous heuristic (sniff for envelope-only field names),
// which was unsound in both directions. Anthropic attaches an object-valued
// `cache_control` to genuine wire blocks, so an Anthropic image carrying a
// cache hint was misread as an envelope and its `source` payload was lost;
// conversely an envelope block whose only extra field was `index` looked like
// wire and lost that field. A marker the wire formats never emit makes the
// decision exact.
const irBlockMarker = "$ir"

// irRawBlock mirrors *every* field of ir.ContentBlock. Adding a field to
// ir.ContentBlock without adding it here silently drops that payload on the
// persistence path, so the two must stay in sync.
type irRawBlock struct {
	// Marker is always 1 on write. On read it is ignored; looksEnvelopeBlock
	// inspects the raw JSON for the key instead. `omitempty` keeps a
	// re-encoding path that leaves it zero from emitting a misleading
	// `"$ir":0`, which would defeat any future version check on the value.
	Marker           int             `json:"$ir,omitempty"`
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Image            json.RawMessage `json:"image,omitempty"`
	Audio            json.RawMessage `json:"audio,omitempty"`
	Video            json.RawMessage `json:"video,omitempty"`
	Document         json.RawMessage `json:"document,omitempty"`
	InputAudio       json.RawMessage `json:"input_audio,omitempty"`
	ToolUse          json.RawMessage `json:"tool_use,omitempty"`
	ToolResult       json.RawMessage `json:"tool_result,omitempty"`
	Thinking         json.RawMessage `json:"thinking,omitempty"`
	RedactedThinking string          `json:"redacted_thinking,omitempty"`
	CacheControl     json.RawMessage `json:"cache_control,omitempty"`
	Index            *int            `json:"index,omitempty"`
	// Raw carries ir.ContentBlock.RawContent, which is how the IR parsers
	// preserve provider block types they do not model. Without it an unknown
	// block would persist as a bare {"type":"..."} and lose its payload.
	Raw json.RawMessage `json:"raw,omitempty"`
}

// irRawToolCall keeps OpenAI's nested `function` object rather than a flat
// {name, arguments} pair. The legacy probe decoder reads `tool_calls` from the
// same rows, and a flat shape would parse there as a tool call with an empty
// function name.
type irRawToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

// irMessageToRaw encodes an IR message into the irRawEnvelope JSON for
// dual-shape persistence under RawContent.
func irMessageToRaw(in ir.Message) json.RawMessage {
	env := irRawEnvelope{
		Role:    in.Role,
		TCID:    in.ToolCallID,
		Name:    in.Name,
		Content: irBlocksToRaw(in.Content),
		Raw:     rawJSONFromAny(in.RawContent),
	}
	if len(in.ToolCalls) > 0 {
		env.Tools = make([]irRawToolCall, 0, len(in.ToolCalls))
		for _, tc := range in.ToolCalls {
			rt := irRawToolCall{ID: tc.ID, Type: tc.Type}
			rt.Function.Name = tc.Function.Name
			rt.Function.Arguments = tc.Function.Arguments
			env.Tools = append(env.Tools, rt)
		}
	}
	out, _ := json.Marshal(env)
	return out
}

// irBlocksToRaw encodes the content blocks. It is split out of irMessageToRaw
// because Message.MarshalJSON persists the block array on its own (the
// `content` column keeps a message-shaped array, not a nested envelope).
func irBlocksToRaw(blocks []ir.ContentBlock) []irRawBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]irRawBlock, 0, len(blocks))
	for _, b := range blocks {
		rb := irRawBlock{
			Marker:           1,
			Type:             b.Type,
			Text:             b.Text,
			RedactedThinking: b.RedactedThinking,
			Index:            b.Index,
			Image:            marshalOrNil(b.Image),
			Audio:            marshalOrNil(b.Audio),
			Video:            marshalOrNil(b.Video),
			Document:         marshalOrNil(b.Document),
			InputAudio:       marshalOrNil(b.InputAudio),
			ToolUse:          marshalOrNil(b.ToolUse),
			ToolResult:       marshalOrNil(b.ToolResult),
			Thinking:         marshalOrNil(b.Thinking),
			CacheControl:     marshalOrNil(b.CacheControl),
			Raw:              rawJSONFromAny(b.RawContent),
		}
		out = append(out, rb)
	}
	return out
}

// marshalOrNil serialises a non-nil pointer field, returning nil (so the
// `omitempty` tag drops the key) when the pointer is nil or unmarshalable.
//
// The kind check guards reflect.Value.IsNil, which panics on non-nillable
// kinds. Every current caller passes a pointer, but the guard keeps a future
// value-typed field from turning a persistence write into a panic.
func marshalOrNil(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		if rv.IsNil() {
			return nil
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return out
}

// rawJSONFromAny normalises the `any`-typed RawContent fields used by
// internal/ir into JSON bytes. The IR parsers store either a JSON string
// (parse_openai / parse_anthropic / parse_responses all use `string(raw)`) or
// an already-decoded value, so both are handled.
func rawJSONFromAny(v any) json.RawMessage {
	switch t := v.(type) {
	case nil:
		return nil
	case json.RawMessage:
		if len(t) == 0 {
			return nil
		}
		return t
	case []byte:
		if len(t) == 0 || !json.Valid(t) {
			return nil
		}
		return t
	case string:
		if t == "" {
			return nil
		}
		if json.Valid([]byte(t)) {
			return json.RawMessage(t)
		}
		out, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		return out
	default:
		out, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return out
	}
}

// recoverIRRaw pulls an IR message back out of a v2.Message that was
// previously produced by MessageFromIR. Returns false when the v2.Message
// carries no RawContent payload, signalling that callers should fall back to
// the plain ToIR path.
func recoverIRRaw(m Message) (ir.Message, bool) {
	raw, ok := m.RawContent.(json.RawMessage)
	if !ok || len(raw) == 0 {
		return ir.Message{}, false
	}
	// A content-block array (what MarshalJSON persists) is not an envelope;
	// json.Unmarshal rejects it into the struct and the caller falls back to
	// the ToIR path, which knows how to decode block arrays.
	var env irRawEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return ir.Message{}, false
	}
	// A JSON `null` content — the standard OpenAI assistant-with-tool_calls
	// shape, which UnmarshalJSON mirrors into RawContent — unmarshals into the
	// struct *without error* and yields a zero envelope. Reporting success
	// there would make ToIR return that empty message and discard the role and
	// tool calls it was called with.
	if env.isEmpty() {
		return ir.Message{}, false
	}
	return irMessageFromEnvelope(env), true
}

// isEmpty reports whether the decoded envelope carries no payload at all. It
// separates "this JSON was not an envelope" from "this was an envelope that
// happens to be empty", both of which must fall through to the caller's other
// content sources.
func (e irRawEnvelope) isEmpty() bool {
	return e.Role == "" && e.TCID == "" && e.Name == "" &&
		len(e.Content) == 0 && len(e.Tools) == 0 && len(e.Raw) == 0
}

// irMessageFromEnvelope is the single inverse of irMessageToRaw. Both the
// in-memory recovery path (recoverIRRaw) and the on-disk parse path
// (IRMessageFromJSON) go through it so the two can never drift apart.
func irMessageFromEnvelope(env irRawEnvelope) ir.Message {
	out := ir.Message{
		Role:       env.Role,
		ToolCallID: env.TCID,
		Name:       env.Name,
		Content:    irBlocksFromRaw(env.Content),
	}
	if len(env.Raw) > 0 {
		out.RawContent = string(env.Raw)
	}
	if len(env.Tools) > 0 {
		out.ToolCalls = make([]ir.ToolCall, 0, len(env.Tools))
		for _, rt := range env.Tools {
			tc := ir.ToolCall{ID: rt.ID, Type: rt.Type}
			tc.Function.Name = rt.Function.Name
			tc.Function.Arguments = rt.Function.Arguments
			out.ToolCalls = append(out.ToolCalls, tc)
		}
	}
	return out
}

// irBlocksFromRaw is the inverse of irBlocksToRaw. A sub-payload that fails to
// decode is skipped rather than failing the whole block, so one malformed
// nested object cannot cost us the surrounding conversation.
func irBlocksFromRaw(raw []irRawBlock) []ir.ContentBlock {
	if len(raw) == 0 {
		return nil
	}
	out := make([]ir.ContentBlock, 0, len(raw))
	for _, rb := range raw {
		b := ir.ContentBlock{
			Type:             rb.Type,
			Text:             rb.Text,
			RedactedThinking: rb.RedactedThinking,
			Index:            rb.Index,
		}
		if len(rb.Image) > 0 {
			var v ir.ImageSource
			if json.Unmarshal(rb.Image, &v) == nil {
				b.Image = &v
			}
		}
		if len(rb.Audio) > 0 {
			var v ir.MediaSource
			if json.Unmarshal(rb.Audio, &v) == nil {
				b.Audio = &v
			}
		}
		if len(rb.Video) > 0 {
			var v ir.MediaSource
			if json.Unmarshal(rb.Video, &v) == nil {
				b.Video = &v
			}
		}
		if len(rb.Document) > 0 {
			var v ir.DocumentBlock
			if json.Unmarshal(rb.Document, &v) == nil {
				b.Document = &v
			}
		}
		if len(rb.InputAudio) > 0 {
			var v ir.InputAudioBlock
			if json.Unmarshal(rb.InputAudio, &v) == nil {
				b.InputAudio = &v
			}
		}
		if len(rb.ToolUse) > 0 {
			var v ir.ToolUse
			if json.Unmarshal(rb.ToolUse, &v) == nil {
				b.ToolUse = &v
			}
		}
		if len(rb.ToolResult) > 0 {
			var v ir.ToolResult
			if json.Unmarshal(rb.ToolResult, &v) == nil {
				b.ToolResult = &v
			}
		}
		if len(rb.Thinking) > 0 {
			var v ir.ThinkingBlock
			if json.Unmarshal(rb.Thinking, &v) == nil {
				b.Thinking = &v
			}
		}
		if len(rb.CacheControl) > 0 {
			var v ir.CacheControl
			if json.Unmarshal(rb.CacheControl, &v) == nil {
				b.CacheControl = &v
			}
		}
		if len(rb.Raw) > 0 {
			// internal/ir stores RawContent as a JSON *string*: every
			// serializer reads it back via `block.RawContent.(string)`
			// (serialize_openai.go:431, serialize_anthropic.go:643,
			// serialize_responses.go:318). Restoring a json.RawMessage here
			// would fail that assertion and drop the block on the way out.
			b.RawContent = string(rb.Raw)
		}
		out = append(out, b)
	}
	return out
}

// IRMessagesFromJSON parses an arbitrary JSON payload into a slice of IR
// messages. It tries four shapes in order:
//
//  1. Object form {"messages":[...]}.
//  2. Bare array form [{role,content,...}, ...].
//  3. Single IR envelope ({"role":..,"content":[..],...}) — produced by
//     MessageFromIR when a single multimodal message is the only payload
//     (e.g. test fixtures that re-emit the RawContent of one v2.Message).
//  4. Single legacy message ({"role":..,"content":...}).
//
// Within a message, each content block is classified independently
// (looksEnvelopeBlock), so mixed-shape content decodes correctly.
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

	var probe msgProbeCompat
	if err := json.Unmarshal(trimmed, &probe); err != nil {
		return nil
	}
	return dropEmptyIRMessage(probe.toIR())
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

// legacyEnvelopeBlockKeys is the fallback heuristic for rows written before
// irBlockMarker existed. Each name is a nested object that only the envelope
// encoder emits: Anthropic carries image and document payloads under `source`,
// inlines tool_use/tool_result fields at the block's top level, and encodes
// `thinking` as a string, so a same-named *object* here means envelope.
//
// `cache_control` is deliberately absent: Anthropic attaches it to genuine wire
// blocks, and including it made an Anthropic image with a cache hint decode as
// an envelope, losing its `source`.
var legacyEnvelopeBlockKeys = []string{
	"image", "audio", "video", "document", "tool_use", "tool_result", "thinking",
}

// looksEnvelopeBlock reports whether a single content block was produced by
// irBlocksToRaw and must therefore be decoded by irBlocksFromRaw rather than by
// the OpenAI-wire decoder.
//
// The decision is per-block, not per-message: a message may legitimately mix
// shapes, and a whole-message sniff means one legacy-looking block forces every
// sibling block down the lossy path.
func looksEnvelopeBlock(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(raw, &generic); err != nil {
		return false
	}
	// Current rows: exact, unambiguous.
	if _, has := generic[irBlockMarker]; has {
		return true
	}
	// Pre-marker rows.
	if _, has := generic["redacted_thinking"]; has {
		return true
	}
	for _, k := range legacyEnvelopeBlockKeys {
		if v, has := generic[k]; has && len(v) > 0 && v[0] == '{' {
			return true
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
	// Raw is the message-level ir.Message.RawContent written by
	// irMessageToRaw. Legacy rows never carry it.
	Raw json.RawMessage `json:"raw"`
}

// buildIREnvelope assembles a *content-free* envelope from already-decoded wire
// fields, so Message.UnmarshalJSON can restore a message-level `raw` payload
// without duplicating the envelope's field layout.
//
// Content is deliberately excluded. The row's `content` value is already held
// verbatim in Message.ContentRaw, and forcing it through irRawBlock here would
// strip every provider-native field the envelope does not model (`image_url`,
// Anthropic's `source`, unknown block keys). ToIR merges the two sources.
func buildIREnvelope(role, toolCallID, name string, raw json.RawMessage, toolCalls []map[string]interface{}) json.RawMessage {
	env := irRawEnvelope{Role: role, TCID: toolCallID, Name: name, Raw: raw}
	for _, tc := range toolCalls {
		rt := irRawToolCall{}
		rt.ID, _ = tc["id"].(string)
		rt.Type, _ = tc["type"].(string)
		if fn, ok := tc["function"].(map[string]interface{}); ok {
			rt.Function.Name, _ = fn["name"].(string)
			rt.Function.Arguments, _ = fn["arguments"].(string)
		}
		env.Tools = append(env.Tools, rt)
	}
	out, err := json.Marshal(env)
	if err != nil {
		return nil
	}
	return out
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
	if len(p.Raw) > 0 {
		out.RawContent = string(p.Raw)
	}
	if len(p.ToolCalls) > 0 {
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
		out.Content = decodeContentBlocks(raw)
	}
	return out
}

// decodeContentBlocks decodes a JSON content-block array, dispatching each
// element to the envelope decoder or the OpenAI-wire decoder independently.
func decodeContentBlocks(raw json.RawMessage) []ir.ContentBlock {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]ir.ContentBlock, 0, len(arr))
	for _, bRaw := range arr {
		if looksEnvelopeBlock(bRaw) {
			var rb irRawBlock
			if json.Unmarshal(bRaw, &rb) == nil {
				out = append(out, irBlocksFromRaw([]irRawBlock{rb})...)
				continue
			}
		}
		if b := decodeContentBlock(bRaw); b != nil {
			out = append(out, *b)
		}
	}
	if len(out) == 0 {
		return nil
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
		// Source is Anthropic's carrier for image and document payloads.
		Source json.RawMessage `json:"source"`
		// cache_control and index are cross-cutting Anthropic wire fields: they
		// ride on a block of *any* type, including plain text, so they are
		// decoded outside the type switch (mirroring
		// internal/ir/parse_anthropic.go:389).
		CacheControl json.RawMessage `json:"cache_control"`
		Index        *int            `json:"index"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}
	b := &ir.ContentBlock{Type: probe.Type, Text: probe.Text, Index: probe.Index}
	if len(probe.CacheControl) > 0 {
		var cc ir.CacheControl
		if json.Unmarshal(probe.CacheControl, &cc) == nil {
			b.CacheControl = &cc
		}
	}
	switch probe.Type {
	case "text":
		// already populated
	case "image", "image_url":
		// OpenAI carries the payload under image_url, as either
		// {"url":..,"detail":..} or a bare string; Anthropic uses `source`.
		//
		// MediaType is deliberately not invented when the body omits it:
		// hardcoding "image/png" corrupts every non-PNG image.
		img := decodeImageSource(probe.ImageURL)
		if img == nil {
			img = decodeImageSource(probe.Source)
		}
		if img == nil {
			b.RawContent = string(raw)
			break
		}
		b.Image = img
		// Normalize the discriminant the way internal/ir's own parser does
		// (parse_openai.go:310). Both serializers switch on Type == "image", so
		// leaving "image_url" here means the block matches no case and the
		// image is dropped on the way back to the provider.
		b.Type = "image"
	case "document":
		// Without a decoded Document, serialize_anthropic's validator rejects
		// the whole request ("source is missing"), so a block we cannot model
		// must not keep the "document" discriminant — preserving it verbatim
		// lets the serializers' default branch re-emit it untouched.
		if doc := decodeDocumentBlock(probe.Source, raw); doc != nil {
			b.Document = doc
		} else {
			b.Type = "raw"
			b.RawContent = string(raw)
		}
	case "input_audio":
		var in ir.InputAudioBlock
		if len(probe.InputAudio) > 0 {
			if err := json.Unmarshal(probe.InputAudio, &in); err == nil {
				b.InputAudio = &in
			}
		}
	default:
		// Same string contract as internal/ir's own parsers
		// (parse_openai.go:331 etc.): RawContent holds the block JSON as a
		// string so the serializers' `.(string)` assertion succeeds.
		b.RawContent = string(raw)
	}
	return b
}

// decodeDocumentBlock rebuilds an Anthropic document block from its `source`
// carrier. Returns nil when there is no usable source, so the caller can fall
// back to verbatim preservation.
func decodeDocumentBlock(source, whole json.RawMessage) *ir.DocumentBlock {
	if len(source) == 0 || source[0] != '{' {
		return nil
	}
	var src ir.DocumentSource
	if err := json.Unmarshal(source, &src); err != nil {
		return nil
	}
	if src.Data == "" && src.URL == "" {
		return nil
	}
	var meta struct {
		Title   string `json:"title"`
		Context string `json:"context"`
	}
	_ = json.Unmarshal(whole, &meta)
	return &ir.DocumentBlock{
		MIMEType: src.MediaType,
		Source:   &src,
		Title:    meta.Title,
		Context:  meta.Context,
	}
}

// decodeImageSource decodes an image payload carrier. It accepts OpenAI's
// image_url in both its object form ({"url":..,"detail":..}) and its
// bare-string form, and Anthropic's `source`
// ({"type":"base64","media_type":..,"data":..}) — ir.ImageSource's JSON tags
// already match the latter field-for-field.
//
// Returns nil when the carrier yields no actual image reference, so the caller
// preserves the block verbatim instead of emitting a `type:"image"` block with
// an empty source (which serialize_anthropic's validator rejects outright,
// failing the whole request).
func decodeImageSource(raw json.RawMessage) *ir.ImageSource {
	if len(raw) == 0 {
		return nil
	}
	if raw[0] == '"' {
		var url string
		if err := json.Unmarshal(raw, &url); err != nil || url == "" {
			return nil
		}
		return &ir.ImageSource{Type: "url", URL: url}
	}
	var img ir.ImageSource
	if err := json.Unmarshal(raw, &img); err != nil {
		return nil
	}
	if img.URL == "" && img.Data == "" && img.FileID == "" && img.FileURI == "" {
		return nil
	}
	if img.Type == "" && img.URL != "" {
		img.Type = "url"
	}
	return &img
}
