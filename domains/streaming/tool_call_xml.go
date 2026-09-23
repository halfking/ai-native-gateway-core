package streaming

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"
)

var (
	xmlToolCallRE   = regexp.MustCompile(`(?s)<tool_call>\s*<function=([A-Za-z_][\w.-]*)>(.*?)</function>\s*</tool_call>`)
	xmlParamRE      = regexp.MustCompile(`(?s)<parameter=([A-Za-z_][\w.-]*)>(.*?)</parameter>`)
	looseToolCallRE = regexp.MustCompile(`(?s)<tool_call>\s*(.*?)\s*</tool_call>`)

	// minimaxStyleRE matches the MiniMax M2.7 tool-call XML shape:
	//   <minimax:tool_call>
	//   <invoke name="func">
	//   <parameter name="arg">value</parameter>
	//   </invoke>
	//   </minimax:tool_call>
	// We saw this in production request_logs id 30089 (2026-06-11) when
	// MiniMax M2.7 falls back to XML when its native tool_use wire format
	// is unavailable.
	minimaxToolCallRE = regexp.MustCompile(`(?s)<minimax:tool_call>\s*<invoke\s+name="([A-Za-z_][\w.-]*)">(.*?)</invoke>\s*</minimax:tool_call>`)
	minimaxParamRE    = regexp.MustCompile(`(?s)<parameter\s+name="([A-Za-z_][\w.-]*)">(.*?)</parameter>`)
)

// CoerceXMLToolCallsInChatResponse is the exported alias used by
// cmd/gateway/main.go to wire XML tool-call coercion into the routing
// executor's non-streaming response post-processor.  See
// coerceXMLToolCallsInChatResponse for the implementation.
func CoerceXMLToolCallsInChatResponse(body []byte, toolsRequested bool) []byte {
	return coerceXMLToolCallsInChatResponse(body, toolsRequested)
}

func coerceXMLToolCallsInChatResponse(body []byte, toolsRequested bool) []byte {
	// Match either the Xiaomi MiMo/generic <tool_call><function=...> shape
	// or the MiniMax M2.7 <minimax:tool_call> shape before doing any
	// JSON parsing — the per-shape regex itself only runs in parseXMLToolCalls.
	if !toolsRequested {
		return body
	}
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "<tool_call>") && !strings.Contains(bodyStr, "<minimax:tool_call>") && !strings.Contains(bodyStr, "minimax[>[") {
		return body
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}
	choices, ok := resp["choices"].([]any)
	if !ok {
		return body
	}
	modified := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok || message["tool_calls"] != nil {
			continue
		}
		content, ok := message["content"].(string)
		if !ok {
			continue
		}
		remaining, toolCalls := parseXMLToolCalls(content)
		if len(toolCalls) == 0 {
			continue
		}
		if remaining == "" {
			message["content"] = nil
		} else {
			message["content"] = remaining
		}
		message["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		modified = true
	}
	if !modified {
		return body
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

const maxStreamXMLToolCallBytes = 64 * 1024

// xmlToolCallTriggerMarkers are the substrings whose presence marks a delta
// as a suspected XML/minimax tool-call fragment. A delta that merely ENDS
// with a proper prefix of one of these markers (e.g. "…minimax[>" before the
// next delta delivers "[<tool_call>") is also buffered: minimax-m3 splits
// its wrapper tokens across SSE deltas at arbitrary byte offsets.
var xmlToolCallTriggerMarkers = []string{
	"minimax[>[",
	"<tool_call>",
	"<minimax:tool_call>",
}

// endsWithMarkerPrefix reports whether text ends with a proper prefix (at
// least 4 bytes, to avoid pathological single-char matches) of any trigger
// marker — i.e. a marker may be split across deltas at this boundary.
func endsWithMarkerPrefix(text string) bool {
	for _, marker := range xmlToolCallTriggerMarkers {
		for n := len(marker) - 1; n >= 4; n-- {
			if strings.HasSuffix(text, marker[:n]) {
				return true
			}
		}
	}
	return false
}

// containsAnyMarker reports whether text already contains a complete marker.
func containsAnyMarker(text string) bool {
	for _, marker := range xmlToolCallTriggerMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// streamXMLToolCallCoercer buffers only a suspected XML tool-call fragment
// between SSE deltas. The cap keeps malformed upstream output bounded.
type streamXMLToolCallCoercer struct {
	fragment string
}

func newStreamXMLToolCallCoercer() *streamXMLToolCallCoercer {
	return &streamXMLToolCallCoercer{}
}

func (c *streamXMLToolCallCoercer) pending() bool {
	return c != nil && c.fragment != ""
}

func (c *streamXMLToolCallCoercer) apply(line string, toolsRequested bool) string {
	if c == nil || !toolsRequested || !strings.HasPrefix(line, "data: ") {
		return coerceXMLToolCallsInStreamLine(line, toolsRequested)
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "[DONE]" {
		// Never leak an unfinished tool fragment at stream termination.
		c.fragment = ""
		return line
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return line
	}
	choices, ok := obj["choices"].([]any)
	if !ok {
		return line
	}
	modified := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok || delta["tool_calls"] != nil {
			continue
		}
		content, ok := delta["content"].(string)
		if !ok {
			continue
		}
		candidate := c.fragment + content
		if c.fragment == "" && !containsAnyMarker(content) && !endsWithMarkerPrefix(content) {
			continue
		}
		if len(candidate) > maxStreamXMLToolCallBytes {
			// The fragment may include customer tool arguments, so keep the event
			// observable without logging any upstream content or request metadata.
			slog.Warn("stream_xml_tool_call_fragment_overflow",
				"fragment_len", len(candidate),
				"max_bytes", maxStreamXMLToolCallBytes)
			c.fragment = ""
			delete(delta, "content")
			modified = true
			continue
		}
		remaining, toolCalls := parseXMLToolCalls(candidate)
		if len(toolCalls) == 0 {
			if containsAnyMarker(candidate) || endsWithMarkerPrefix(candidate) {
				// A marker is present (waiting for its closing tag) or may
				// still be split across the next delta: keep buffering.
				c.fragment = candidate
				delta["content"] = ""
				modified = true
				continue
			}
			// The buffered bytes can no longer become a tool call (e.g. a
			// marker prefix that turned out to be ordinary text): hand them
			// back as visible content instead of swallowing the response.
			c.fragment = ""
			delta["content"] = candidate
			modified = true
			continue
		}
		c.fragment = ""
		if remaining == "" {
			delete(delta, "content")
		} else {
			delta["content"] = remaining
		}
		for idx, toolCall := range toolCalls {
			toolCall["index"] = idx
		}
		delta["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		modified = true
	}
	if !modified {
		return coerceXMLToolCallsInStreamLine(line, toolsRequested)
	}
	out, err := marshalSSEPayloadNoEscape(obj)
	if err != nil {
		return line
	}
	return "data: " + out + "\n"
}

// marshalSSEPayloadNoEscape serializes a rebuilt SSE data payload without
// Go's default HTML escaping (<, >, & → \u003c…). The upstream chat lines we
// rewrite carry tool-call XML full of those bytes; re-escaping them changes
// the wire shape clients diff against and makes byte-level passthrough tests
// brittle. JSON semantics are identical either way.
func marshalSSEPayloadNoEscape(obj any) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func coerceXMLToolCallsInStreamLine(line string, toolsRequested bool) string {
	if !toolsRequested || !strings.HasPrefix(line, "data: ") {
		return line
	}
	if !strings.Contains(line, "<tool_call>") && !strings.Contains(line, "<minimax:tool_call>") && !strings.Contains(line, "minimax[>[") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "[DONE]" {
		return line
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return line
	}
	choices, ok := obj["choices"].([]any)
	if !ok {
		return line
	}
	modified := false
	for _, rawChoice := range choices {
		choice, ok := rawChoice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok || delta["tool_calls"] != nil {
			continue
		}
		content, ok := delta["content"].(string)
		if !ok {
			continue
		}
		remaining, toolCalls := parseXMLToolCalls(content)
		if len(toolCalls) == 0 {
			continue
		}
		if remaining == "" {
			delete(delta, "content")
		} else {
			delta["content"] = remaining
		}
		for idx, toolCall := range toolCalls {
			toolCall["index"] = idx
		}
		delta["tool_calls"] = toolCalls
		choice["finish_reason"] = "tool_calls"
		modified = true
	}
	if !modified {
		return line
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	return "data: " + string(out) + "\n"
}

func parseXMLToolCalls(text string) (string, []map[string]any) {
	text = vendorstrip.UnwrapMiniMaxTokenWrappers(text)
	// Try the Xiaomi MiMo / generic shape first.
	if strings.Contains(text, "<tool_call>") && strings.Contains(text, "<function=") {
		if remaining, calls := parseXMLWith(text, xmlToolCallRE, xmlParamRE); len(calls) > 0 {
			return remaining, calls
		}
	}
	// Fall back to the MiniMax M2.7 shape: <minimax:tool_call>...<invoke name="X">...</invoke>...</minimax:tool_call>.
	if strings.Contains(text, "<minimax:tool_call>") && strings.Contains(text, "<invoke ") {
		if remaining, calls := parseXMLWith(text, minimaxToolCallRE, minimaxParamRE); len(calls) > 0 {
			return remaining, calls
		}
	}
	if strings.Contains(text, "<tool_call>") && strings.Contains(text, "</tool_call>") {
		if remaining, calls := parseLooseToolCalls(text); len(calls) > 0 {
			slog.Info("request_flow", "event", "request_flow", "stage", "minimax_tool_text_coerced",
				"kind", "conversion", "action", "coerce_tool_call", "retryable", false)
			return remaining, calls
		}
	}
	return text, nil
}

func parseLooseToolCalls(text string) (string, []map[string]any) {
	matches := looseToolCallRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	var toolCalls []map[string]any
	var builder strings.Builder
	cursor := 0
	for i, match := range matches {
		builder.WriteString(text[cursor:match[0]])
		cursor = match[1]
		inner := strings.TrimSpace(text[match[2]:match[3]])
		if inner == "" {
			continue
		}
		// 2026-09-08 audit: many vendors (Qwen-style) emit the tool call as a
		// bare JSON object {"name":...,"arguments":...} inside <tool_call>.
		// Extract the real name/arguments when the inner payload parses as
		// JSON; otherwise keep the historical {"input": <raw>} wrap. A fixed
		// name="tool" makes downstream agents fail with "no such tool" and
		// aborts their loop — worse than the leak it replaces.
		name := "tool"
		args := "{}"
		var asJSON map[string]any
		if err := json.Unmarshal([]byte(inner), &asJSON); err == nil && asJSON != nil {
			if n, ok := asJSON["name"].(string); ok && strings.TrimSpace(n) != "" {
				name = strings.TrimSpace(n)
				switch rawArgs := asJSON["arguments"].(type) {
				case string:
					args = rawArgs
				default:
					if rawArgs != nil {
						if b, err := json.Marshal(rawArgs); err == nil {
							args = string(b)
						}
					}
				}
			}
		}
		if args == "{}" && name == "tool" {
			wrapped, _ := json.Marshal(map[string]any{"input": inner})
			args = string(wrapped)
		}
		// 2026-09-08 audit: rune('a'+i) produces non-letter bytes past i=25 —
		// use a decimal index so ids stay [A-Za-z0-9] like OpenAI ids.
		id := strings.ReplaceAll("call_"+time.Now().UTC().Format("20060102150405.000000000")+"_"+strconv.Itoa(i), ".", "")
		toolCalls = append(toolCalls, map[string]any{
			"id":   id,
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		})
	}
	builder.WriteString(text[cursor:])
	if len(toolCalls) == 0 {
		return text, nil
	}
	return strings.TrimSpace(builder.String()), toolCalls
}

// parseXMLWith runs the supplied tool-call + parameter regexes against
// text and returns the leading/trailing free text plus the parsed calls.
// It is the shared inner loop for parseXMLToolCalls.
func parseXMLWith(text string, callRE, paramRE *regexp.Regexp) (string, []map[string]any) {
	matches := callRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, nil
	}
	var toolCalls []map[string]any
	var builder strings.Builder
	cursor := 0
	for i, match := range matches {
		builder.WriteString(text[cursor:match[0]])
		cursor = match[1]
		name := strings.TrimSpace(text[match[2]:match[3]])
		body := text[match[4]:match[5]]
		params := map[string]any{}
		for _, pm := range paramRE.FindAllStringSubmatchIndex(body, -1) {
			key := strings.TrimSpace(body[pm[2]:pm[3]])
			value := strings.TrimSpace(html.UnescapeString(body[pm[4]:pm[5]]))
			params[key] = value
		}
		args, _ := json.Marshal(params)
		toolCalls = append(toolCalls, map[string]any{
			"id":   strings.ReplaceAll("call_"+time.Now().UTC().Format("20060102150405.000000000")+"_"+string(rune('a'+i)), ".", ""),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(args),
			},
		})
	}
	builder.WriteString(text[cursor:])
	return strings.TrimSpace(builder.String()), toolCalls
}

// xmlToolCallCoercingBody wraps an upstream SSE body and runs the streaming
// XML/minimax tool-call coercer over every complete `data: {...}` line before
// the bridge's protocol conversion sees it. It exists because the coercer was
// historically wired only into the OpenAI-chat passthrough loop
// (StreamChat/applyGateLineTransforms): requests whose CLIENT protocol is
// anthropic-messages or openai-responses (e.g. agent harnesses on
// /v1/messages) but whose UPSTREAM is an OpenAI-shaped chat relay (minimax-m3
// via api.minimaxi.com) took the OpenAIToAnthropicStream /
// OpenAIToResponsesStream bridges, which never coerced — so minimax-m3's
// `minimax[>[<tool_call>…]<]minimax` wrapper tokens leaked verbatim into
// client-visible text (2026-09-23 forensics round, user report #13).
//
// Wire: cmd/gateway/main.go wraps resp.Body with NewXMLToolCallCoercingBody
// before invoking the bridge implementations. Line framing is preserved
// one-for-one (each input line maps to exactly one output line), so the
// bridges' bufio/LineReader consumers are unaffected.
type xmlToolCallCoercingBody struct {
	body    io.ReadCloser
	coercer *streamXMLToolCallCoercer
	tools   bool
	out     []byte // transformed bytes ready to hand to the reader
	pending []byte // partial line carried between upstream reads
	eof     bool
}

// NewXMLToolCallCoercingBody returns body with the streaming XML tool-call
// coercer applied. When toolsRequested is false the original body is
// returned unchanged (the coercer must not touch tool-less requests).
func NewXMLToolCallCoercingBody(body io.ReadCloser, toolsRequested bool) io.ReadCloser {
	if body == nil || !toolsRequested {
		return body
	}
	return &xmlToolCallCoercingBody{
		body:    body,
		coercer: newStreamXMLToolCallCoercer(),
		tools:   true,
	}
}

func (c *xmlToolCallCoercingBody) Read(p []byte) (int, error) {
	for len(c.out) == 0 {
		if c.eof {
			if len(c.pending) > 0 {
				// Final unterminated line: transform and serve without
				// appending a terminator so the consumer still sees EOF next.
				c.out = []byte(c.coercer.apply(string(c.pending), c.tools))
				c.pending = nil
				continue
			}
			return 0, io.EOF
		}
		buf := make([]byte, 4096)
		n, err := c.body.Read(buf)
		if n > 0 {
			c.pending = append(c.pending, buf[:n]...)
			for {
				idx := bytes.IndexByte(c.pending, '\n')
				if idx < 0 {
					break
				}
				line := string(c.pending[:idx+1]) // keep the terminator
				c.pending = c.pending[idx+1:]
				c.out = append(c.out, c.coercer.apply(line, c.tools)...)
			}
		}
		if err != nil {
			c.eof = true
			// Surface non-EOF errors only after the buffered/transformed
			// bytes are drained; EOF is handled on the next loop pass.
			if err != io.EOF && len(c.out) == 0 && len(c.pending) == 0 {
				return 0, err
			}
		}
	}
	n := copy(p, c.out)
	c.out = c.out[n:]
	return n, nil
}

func (c *xmlToolCallCoercingBody) Close() error {
	return c.body.Close()
}
