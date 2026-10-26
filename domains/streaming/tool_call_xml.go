package streaming

import (
	"bytes"
	"encoding/json"
	"errors"
	"html"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/kaixuan/llm-gateway-go/internal/sse"
	"github.com/kaixuan/llm-gateway-go/internal/tooloffer"
	vendorstrip "github.com/kaixuan/llm-gateway-go/internal/vendorstrip"
)

var (
	toolBlockRE           = regexp.MustCompile(`(?s)<tool_call>(.*?)</tool_call>|<minimax:tool_call>(.*?)</minimax:tool_call>`)
	xmlFunctionRE         = regexp.MustCompile(`(?s)^\s*<function=([A-Za-z_][\w.-]*)>(.*?)</function>\s*$`)
	xmlParamRE            = regexp.MustCompile(`(?s)<parameter=([A-Za-z_][\w.-]*)>(.*?)</parameter>`)
	minimaxFunctionRE     = regexp.MustCompile(`(?s)^\s*<invoke\s+name="([A-Za-z_][\w.-]*)">(.*?)</invoke>\s*$`)
	minimaxParamRE        = regexp.MustCompile(`(?s)<parameter\s+name="([A-Za-z_][\w.-]*)">(.*?)</parameter>`)
	recoveredCallSequence atomic.Uint64
)

// CoerceXMLToolCallsInChatResponse is retained for compatibility. A boolean
// cannot authorize a function name; callers must supply the actual offer set.
func CoerceXMLToolCallsInChatResponse(body []byte, _ bool) []byte { return body }
func coerceXMLToolCallsInChatResponse(body []byte, _ bool) []byte { return body }

// CoerceXMLToolCallsInChatResponseWithNames repairs complete calls of tools the
// client actually offered. Failed candidates and existing native calls are kept.
func CoerceXMLToolCallsInChatResponseWithNames(body []byte, names tooloffer.Names) []byte {
	if names.Len() == 0 {
		return body
	}
	var response map[string]any
	if decodeToolJSON(body, &response) != nil {
		return body
	}
	choices, _ := response["choices"].([]any)
	modified := false
	for _, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		message, ok := choice["message"].(map[string]any)
		if !ok || message["tool_calls"] != nil {
			continue
		}
		text, ok := message["content"].(string)
		if !ok {
			continue
		}
		rest, calls := parseXMLToolCallsWithNames(text, names)
		if len(calls) == 0 {
			continue
		}
		if rest == "" {
			message["content"] = nil
		} else {
			message["content"] = rest
		}
		message["tool_calls"] = calls
		correctRecoveredFinish(choice)
		modified = true
	}
	if !modified {
		return body
	}
	result, err := marshalSSEPayloadNoEscape(response)
	if err != nil {
		return body
	}
	return []byte(result)
}

func decodeToolJSON(body []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("extra JSON value")
	}
	return nil
}

func correctRecoveredFinish(choice map[string]any) {
	reason, _ := choice["finish_reason"].(string)
	if reason == "" || reason == "stop" {
		choice["finish_reason"] = "tool_calls"
	}
}

// parseXMLToolCalls is a closed legacy helper: without names nothing executes.
func parseXMLToolCalls(text string) (string, []map[string]any)   { return text, nil }
func parseLooseToolCalls(text string) (string, []map[string]any) { return text, nil }

func parseXMLToolCallsWithNames(original string, names tooloffer.Names) (string, []map[string]any) {
	if names.Len() == 0 {
		return original, nil
	}
	text := vendorstrip.UnwrapMiniMaxTokenWrappers(original)
	matches := toolBlockRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return original, nil
	}
	var rest strings.Builder
	var calls []map[string]any
	cursor := 0
	for _, m := range matches {
		rest.WriteString(text[cursor:m[0]])
		inner := ""
		functionRE, paramRE := xmlFunctionRE, xmlParamRE
		if m[2] < 0 {
			inner = text[m[4]:m[5]]
			functionRE, paramRE = minimaxFunctionRE, minimaxParamRE
		} else {
			inner = text[m[2]:m[3]]
		}
		name, args, ok := readWrittenCall(inner, functionRE, paramRE)
		if !ok || !names.Has(name) {
			return original, nil
		}
		calls = append(calls, map[string]any{"id": "call_recovered_" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + strconv.FormatUint(recoveredCallSequence.Add(1), 10), "type": "function", "function": map[string]any{"name": name, "arguments": args}})
		cursor = m[1]
	}
	rest.WriteString(text[cursor:])
	// A complete call next to an unfinished/unsupported block is still ambiguous.
	if containsAnyMarker(rest.String()) || endsWithMarkerPrefix(rest.String()) {
		return original, nil
	}
	return rest.String(), calls
}

func readWrittenCall(inner string, functionRE, paramRE *regexp.Regexp) (string, string, bool) {
	if m := functionRE.FindStringSubmatch(inner); m != nil {
		body := m[2]
		params := map[string]any{}
		cursor := 0
		for _, p := range paramRE.FindAllStringSubmatchIndex(body, -1) {
			if strings.TrimSpace(body[cursor:p[0]]) != "" {
				return "", "", false
			}
			key := body[p[2]:p[3]]
			if _, duplicate := params[key]; duplicate {
				return "", "", false
			}
			params[key] = strings.TrimSpace(html.UnescapeString(body[p[4]:p[5]]))
			cursor = p[1]
		}
		if strings.TrimSpace(body[cursor:]) != "" {
			return "", "", false
		}
		args, _ := marshalSSEPayloadNoEscape(params)
		return m[1], args, true
	}
	value, err := readUniqueToolJSON([]byte(inner))
	if err != nil {
		return "", "", false
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", "", false
	}
	name, ok := object["name"].(string)
	if !ok || name == "" {
		return "", "", false
	}
	var arguments any
	found := false
	for _, key := range []string{"arguments", "parameters", "input"} {
		if arg, exists := object[key]; exists {
			if found {
				return "", "", false
			}
			arguments = arg
			found = true
		}
	}
	if encoded, ok := arguments.(string); ok {
		arguments, err = readUniqueToolJSON([]byte(encoded))
		if err != nil {
			return "", "", false
		}
	}
	if arguments == nil {
		if found {
			return "", "", false
		}
		arguments = map[string]any{}
	}
	if _, ok := arguments.(map[string]any); !ok {
		return "", "", false
	}
	args, err := marshalSSEPayloadNoEscape(arguments)
	return name, args, err == nil
}

// Token decoding rejects duplicate keys rather than silently choosing one name
// or argument value. Numbers remain decimal literals rather than float64 values.
func readUniqueToolJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readUniqueToolValue(decoder)
	if err != nil {
		return nil, err
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("extra JSON value")
	}
	return value, nil
}
func readUniqueToolValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("invalid object key")
			}
			if _, exists := object[key]; exists {
				return nil, errors.New("duplicate object key")
			}
			value, err := readUniqueToolValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		_, err := decoder.Token()
		return object, err
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := readUniqueToolValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		_, err := decoder.Token()
		return array, err
	default:
		return nil, errors.New("invalid delimiter")
	}
}

const maxStreamXMLToolCallBytes = 64 * 1024
const maxStreamToolChoices = 64

var xmlToolCallTriggerMarkers = []string{"minimax[>[", "<tool_call>", "<minimax:tool_call>"}

func endsWithMarkerPrefix(text string) bool {
	for _, marker := range xmlToolCallTriggerMarkers {
		for n := len(marker) - 1; n >= 1; n-- {
			if strings.HasSuffix(text, marker[:n]) {
				return true
			}
		}
	}
	return false
}
func containsAnyMarker(text string) bool {
	for _, marker := range xmlToolCallTriggerMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
func incompleteToolText(text string) bool {
	unwrapped := vendorstrip.UnwrapMiniMaxTokenWrappers(text)
	return endsWithMarkerPrefix(text) || endsWithMarkerPrefix(unwrapped) || strings.Count(unwrapped, "<tool_call>") > strings.Count(unwrapped, "</tool_call>") || strings.Count(unwrapped, "<minimax:tool_call>") > strings.Count(unwrapped, "</minimax:tool_call>")
}

type toolTextFragment struct{ text string }
type streamXMLToolCallCoercer struct {
	names          tooloffer.Names
	fragments      map[int]toolTextFragment
	converted      map[int]bool
	disabled       map[int]bool
	nextCallIndex  map[int]int
	heldBytes      int
	trackedChoices map[int]struct{}
	choiceOverflow bool
}

func newStreamXMLToolCallCoercer(names ...tooloffer.Names) *streamXMLToolCallCoercer {
	c := &streamXMLToolCallCoercer{}
	if len(names) > 0 {
		c.names = names[0]
	}
	if c.names.Len() > 0 {
		c.fragments = map[int]toolTextFragment{}
		c.converted = map[int]bool{}
		c.disabled = map[int]bool{}
		c.nextCallIndex = map[int]int{}
		c.trackedChoices = map[int]struct{}{}
	}
	return c
}

func (c *streamXMLToolCallCoercer) trackChoice(index int) bool {
	if _, tracked := c.trackedChoices[index]; tracked {
		return true
	}
	if c.choiceOverflow || len(c.trackedChoices) >= maxStreamToolChoices {
		c.choiceOverflow = true
		return false
	}
	c.trackedChoices[index] = struct{}{}
	return true
}
func (c *streamXMLToolCallCoercer) finishChoice(index int) {
	c.discardFragment(index)
	delete(c.converted, index)
	delete(c.disabled, index)
	delete(c.nextCallIndex, index)
	delete(c.trackedChoices, index)
}
func (c *streamXMLToolCallCoercer) reset() {
	clear(c.fragments)
	clear(c.converted)
	clear(c.disabled)
	clear(c.nextCallIndex)
	clear(c.trackedChoices)
	c.heldBytes = 0
	c.choiceOverflow = false
}

func (c *streamXMLToolCallCoercer) pending() bool { return c != nil && len(c.fragments) > 0 }

// apply is used with names only by the body reader. A terminal may release more
// than one frame; placing this seam before protocol readers preserves framing.
func (c *streamXMLToolCallCoercer) apply(line string, _ bool) string {
	if c == nil || c.names.Len() == 0 || !strings.HasPrefix(line, "data: ") {
		return line
	}
	payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
	if payload == "[DONE]" {
		return c.flush() + line
	}
	var root map[string]any
	if decodeToolJSON([]byte(payload), &root) != nil {
		return line
	}
	choices, _ := root["choices"].([]any)
	modified := false
	var released strings.Builder
	var finishedChoices []int
	for position, raw := range choices {
		choice, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		index := position
		if n, ok := choice["index"].(json.Number); ok {
			if parsed, err := strconv.Atoi(string(n)); err == nil {
				index = parsed
			}
		}
		fragment := c.fragments[index]
		reason, hasFinish := choice["finish_reason"].(string)
		terminal := hasFinish && reason != ""
		if terminal {
			finishedChoices = append(finishedChoices, index)
		}
		if c.converted[index] && terminal {
			correctRecoveredFinish(choice)
			modified = true
		}
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			if terminal && fragment.text != "" {
				released.WriteString(toolTextFrames(index, fragment.text))
				c.discardFragment(index)
				modified = true
			}
			continue
		}
		text, hasText := delta["content"].(string)
		if delta["tool_calls"] != nil {
			if !c.trackChoice(index) {
				continue
			}
			c.disabled[index] = true
			if fragment.text != "" {
				released.WriteString(toolTextFrames(index, fragment.text))
				c.discardFragment(index)
				modified = true
			}
			continue
		}

		if c.disabled[index] {
			continue
		}
		if !hasText {
			if terminal && fragment.text != "" {
				released.WriteString(toolTextFrames(index, fragment.text))
				c.discardFragment(index)
				modified = true
			}
			continue
		}
		candidate := fragment.text + text
		if fragment.text == "" && !containsAnyMarker(text) && !endsWithMarkerPrefix(text) {
			continue
		}
		if !c.trackChoice(index) {
			continue
		}
		if c.heldBytes-len(fragment.text)+len(candidate) > maxStreamXMLToolCallBytes {
			released.WriteString(toolTextFrames(index, candidate))
			delete(delta, "content")
			c.discardFragment(index)
			c.disabled[index] = true
			modified = true
			continue
		}
		rest, calls := parseXMLToolCallsWithNames(candidate, c.names)
		if len(calls) == 0 {
			if !terminal && incompleteToolText(candidate) {
				fragment.text = candidate
				c.discardFragment(index)
				c.fragments[index] = fragment
				c.heldBytes += len(fragment.text)
				delta["content"] = ""
				modified = true
			} else if fragment.text != "" || len(candidate) > maxReleasedToolTextBytes {
				if len(candidate) > maxReleasedToolTextBytes {
					released.WriteString(toolTextFrames(index, candidate))
					delete(delta, "content")
				} else {
					delta["content"] = candidate
				}
				c.discardFragment(index)
				modified = true
			}
			continue
		}
		c.discardFragment(index)
		if rest == "" {
			delete(delta, "content")
		} else {
			delta["content"] = rest
		}
		for _, call := range calls {
			call["index"] = c.nextCallIndex[index]
			c.nextCallIndex[index]++
		}
		delta["tool_calls"] = calls
		if terminal {
			correctRecoveredFinish(choice)
		}
		c.converted[index] = true
		modified = true
	}
	for _, index := range finishedChoices {
		c.finishChoice(index)
	}
	if !modified {
		return line
	}
	result, err := marshalSSEPayloadNoEscape(root)
	if err != nil {
		return line
	}
	return released.String() + "data: " + result + "\n"
}
func (c *streamXMLToolCallCoercer) discardFragment(index int) {
	c.heldBytes -= len(c.fragments[index].text)
	delete(c.fragments, index)
}

const maxReleasedToolTextBytes = 8 * 1024

// Only content and choice identity are synthesized. Usage, roles and terminal
// fields remain in their original frames and cannot be duplicated by a flush.
func toolTextFrames(index int, text string) string {
	var output strings.Builder
	for len(text) > 0 {
		end := min(len(text), maxReleasedToolTextBytes)
		for end < len(text) && !utf8.RuneStart(text[end]) {
			end--
		}
		root := map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{"content": text[:end]}}}}
		payload, _ := marshalSSEPayloadNoEscape(root)
		output.WriteString("data: " + payload + "\n\n")
		text = text[end:]
	}
	return output.String()
}

func (c *streamXMLToolCallCoercer) flush() string {
	defer c.reset()
	if !c.pending() {
		return ""
	}
	keys := make([]int, 0, len(c.fragments))
	for index := range c.fragments {
		keys = append(keys, index)
	}
	sort.Ints(keys)
	var output strings.Builder
	for _, index := range keys {
		output.WriteString(toolTextFrames(index, c.fragments[index].text))
		c.discardFragment(index)
	}
	return output.String()
}
func marshalSSEPayloadNoEscape(value any) (string, error) {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}
func coerceXMLToolCallsInStreamLine(line string, _ bool) string { return line }

type xmlToolCallCoercingBody struct {
	body         io.ReadCloser
	coercer      *streamXMLToolCallCoercer
	out, pending []byte
	readErr      error
	lineLimit    int
}

// NewXMLToolCallCoercingBody is the closed compatibility shim: a boolean alone
// conveys no executable names and must preserve the original stream.
func NewXMLToolCallCoercingBody(body io.ReadCloser, _ bool) io.ReadCloser { return body }

func NewXMLToolCallCoercingBodyWithNames(body io.ReadCloser, names tooloffer.Names) io.ReadCloser {
	if body == nil || names.Len() == 0 {
		return body
	}
	return &xmlToolCallCoercingBody{body: body, coercer: newStreamXMLToolCallCoercer(names), lineLimit: currentStreamRuntimeConfig().sseMaxLineBytes}
}
func (c *xmlToolCallCoercingBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(c.out) == 0 {
		if c.readErr != nil {
			if len(c.pending) > 0 {
				c.out = []byte(c.coercer.apply(string(c.pending), true))
				c.pending = nil
			}
			c.out = append(c.out, c.coercer.flush()...)
			if len(c.out) == 0 {
				return 0, c.readErr
			}
			break
		}
		buffer := make([]byte, 4096)
		n, err := c.body.Read(buffer)
		if n > 0 {
			c.pending = append(c.pending, buffer[:n]...)
			for {
				end := bytes.IndexByte(c.pending, '\n')
				observed := len(c.pending)
				if end >= 0 {
					observed = end + 1
				}
				if observed > c.lineLimit {
					// This wrapper precedes the downstream bounded SSE reader.
					// Fail without draining an indefinitely unterminated line.
					c.readErr = &sse.LineTooLongError{Limit: c.lineLimit, Observed: observed}
					c.pending = nil
					break
				}
				if end < 0 {
					break
				}
				c.out = append(c.out, c.coercer.apply(string(c.pending[:end+1]), true)...)
				c.pending = c.pending[end+1:]
			}
		}
		if err != nil && c.readErr == nil {
			c.readErr = err
		}
	}
	n := copy(p, c.out)
	c.out = c.out[n:]
	return n, nil
}
func (c *xmlToolCallCoercingBody) Close() error { return c.body.Close() }
