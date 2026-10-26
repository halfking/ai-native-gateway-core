package streaming

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/kaixuan/llm-gateway-go/internal/tooloffer"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func offeredJSON(t *testing.T, value any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func offeredResponse(t *testing.T, text string) []byte {
	return []byte(offeredJSON(t, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}}))
}

func offeredFrame(t *testing.T, text string) string {
	return "data: " + offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}}}}) + "\n\n"
}

func TestOfferedLegacyBooleanCannotAuthorizeCalls(t *testing.T) {
	for _, text := range []string{
		`<tool_call><function=not_offered><parameter=q>visible</parameter></function></tool_call>`,
		`<tool_call>run arbitrary prose</tool_call>`,
		`<tool_call>{"name":"search","arguments":"not JSON"}</tool_call>`,
	} {
		body := offeredResponse(t, text)
		if got := CoerceXMLToolCallsInChatResponse(body, true); !bytes.Equal(got, body) {
			t.Errorf("boolean alone authorized a call: %s", got)
		}
	}
}

func TestOfferedLegacyBooleanReaderPreservesText(t *testing.T) {
	input := offeredFrame(t, `<tool_call><function=search>unfinished`) + "data: [DONE]\n\n"
	got, err := io.ReadAll(NewXMLToolCallCoercingBody(io.NopCloser(strings.NewReader(input)), true))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != input {
		t.Fatalf("boolean-only reader changed content: %s", got)
	}
}

func TestOfferedMiniMaxFailedCandidateSurvivesVendorStrip(t *testing.T) {
	for _, text := range []string{
		`minimax[>[<tool_call>{"name":"unknown","arguments":{}}]<]minimax[>[</tool_call>`,
		`minimax[>[<tool_call><function=search>unfinished`,
		`<function_calls><tool_call>unrecognized prose</tool_call>`,
	} {
		body := offeredResponse(t, text)
		if got := StripMinimaxFieldsBody(body); !bytes.Equal(got, body) {
			t.Errorf("failed candidate changed: %s", got)
		}
	}
}

func offeredSearchNames() tooloffer.Names {
	return tooloffer.FromRequest([]byte(`{"tools":[{"type":"function","function":{"name":"search"}}]}`))
}

func TestOfferedCompleteCallsRequireExactNameAndReadableArguments(t *testing.T) {
	for _, text := range []string{
		`<tool_call><function=search><parameter=q>gateway &amp; tools</parameter></function></tool_call>`,
		`<minimax:tool_call><invoke name="search"><parameter name="q">gateway &amp; tools</parameter></invoke></minimax:tool_call>`,
		`<tool_call>{"name":"search","arguments":{"q":"gateway & tools","big":9007199254740993}}</tool_call>`,
		`<tool_call>{"name":"search","arguments":"{\"q\":\"gateway & tools\"}"}</tool_call>`,
	} {
		t.Run(text, func(t *testing.T) {
			body := offeredResponse(t, "before "+text+" after")
			got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames())
			var response struct {
				Choices []struct {
					Message struct {
						Content   string
						ToolCalls []struct {
							Function struct {
								Name      string
								Arguments string
							}
						} `json:"tool_calls"`
					}
					FinishReason string `json:"finish_reason"`
				}
			}
			if err := json.Unmarshal(got, &response); err != nil {
				t.Fatal(err)
			}
			m := response.Choices[0].Message
			if len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Name != "search" || m.Content != "before  after" || response.Choices[0].FinishReason != "tool_calls" {
				t.Fatalf("bad recovered call: %s", got)
			}
			if !strings.Contains(m.ToolCalls[0].Function.Arguments, `"q":"gateway & tools"`) {
				t.Fatalf("wrong arguments: %s", got)
			}
			if strings.Contains(text, "9007199254740993") && !strings.Contains(m.ToolCalls[0].Function.Arguments, "9007199254740993") {
				t.Fatalf("integer rounded: %s", got)
			}
		})
	}
}

func TestOfferedUnknownIncompleteAndAmbiguousCallsPreserveOriginal(t *testing.T) {
	for _, text := range []string{
		`<tool_call><function=unknown><parameter=q>x</parameter></function></tool_call>`,
		`<tool_call><function=Search></function></tool_call>`,
		`<tool_call><function=search><parameter=q>unclosed</function></tool_call>`,
		`<tool_call><function=search>not parameters</function></tool_call>`,
		`<tool_call><function=search><parameter=q>x</parameter><parameter=q>y</parameter></function></tool_call>`,
		`<tool_call>{"name":"search","arguments":"not JSON"}</tool_call>`,
		`<tool_call>{"name":"search","arguments":null}</tool_call>`,
		`<tool_call>{"name":"search","arguments":"null"}</tool_call>`,
		`<tool_call>{"name":"unknown","name":"search","arguments":{}}</tool_call>`,
		`<tool_call>{"name":"search","arguments":{"q":1,"q":2}}</tool_call>`,
		`<tool_call>ordinary prose</tool_call>`,
		`<tool_call>{"name":"search","arguments":{}}`,
		`minimax[>[<tool_call>{"name":"unknown","arguments":{}}]<]minimax[>[</tool_call>`,
	} {
		body := offeredResponse(t, text)
		if got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames()); !bytes.Equal(got, body) {
			t.Errorf("unreadable candidate was changed: %s", got)
		}
	}
	body := offeredResponse(t, `<tool_call>{"name":"search","arguments":{}}</tool_call>`)
	if got := CoerceXMLToolCallsInChatResponseWithNames(body, tooloffer.Names{}); !bytes.Equal(got, body) {
		t.Fatalf("absent offer changed reply: %s", got)
	}
}

func TestOfferedEscapedJSONAndExistingStructuredCalls(t *testing.T) {
	text := `<tool_call>{"name":"search","arguments":{"q":"x"}}</tool_call>`
	body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": text}}}})
	if got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames()); !bytes.Contains(got, []byte(`"tool_calls"`)) {
		t.Fatalf("escaped JSON bypassed recovery: %s", got)
	}
	body = []byte(`{"choices":[{"message":{"content":"<tool_call>not another call</tool_call>","tool_calls":[{"id":"native","function":{"name":"search","arguments":"{}"}}]},"finish_reason":"length"}]}`)
	if got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames()); !bytes.Equal(got, body) {
		t.Fatalf("structured call changed: %s", got)
	}
}

func offeredRead(t *testing.T, input string) string {
	t.Helper()
	got, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(input)), offeredSearchNames()))
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func offeredStreamText(t *testing.T, wire string) string {
	t.Helper()
	var text strings.Builder
	for _, line := range strings.Split(wire, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var frame struct {
			Choices []struct{ Delta struct{ Content string } }
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		for _, choice := range frame.Choices {
			text.WriteString(choice.Delta.Content)
		}
	}
	return text.String()
}

func TestOfferedReaderPreservesUnknownIncompleteAndOverflow(t *testing.T) {
	for _, text := range []string{
		`<tool_call>{"name":"unknown","arguments":{}}</tool_call>`,
		`<tool_call><function=search>unfinished`,
		`<tool_call><function=search>` + strings.Repeat("x", maxStreamXMLToolCallBytes),
	} {
		for _, terminal := range []string{"data: [DONE]\n\n", "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n", ""} {
			input := offeredFrame(t, text[:6]) + offeredFrame(t, text[6:]) + terminal
			got := offeredRead(t, input)
			if actual := offeredStreamText(t, got); actual != text {
				t.Errorf("lost content bytes got=%d want=%d", len(actual), len(text))
			}
			if strings.Contains(got, `"tool_calls"`) {
				t.Errorf("unreadable candidate became a call: %.200s", got)
			}
		}
	}
}

func TestOfferedReaderRecoversEverySplitAndPreservesTerminal(t *testing.T) {
	text := `<tool_call>{"name":"search","arguments":{"q":"x"}}</tool_call>`
	for cut := 1; cut < len(text); cut++ {
		input := offeredFrame(t, text[:cut]) + offeredFrame(t, text[cut:]) + "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		got := offeredRead(t, input)
		if !strings.Contains(got, `"name":"search"`) || !strings.Contains(got, `"finish_reason":"tool_calls"`) || strings.Contains(got, `"finish_reason":"stop"`) || offeredStreamText(t, got) != "" {
			t.Fatalf("split %d: %s", cut, got)
		}
	}
}

type offeredErrorReader struct{ sent bool }

func (r *offeredErrorReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, errors.New("upstream read failed")
	}
	r.sent = true
	return copy(p, `data: {"choices":[{"index":0,"delta":{"content":"<tool_call>unfinished"}}]}`+"\n"), nil
}
func (*offeredErrorReader) Close() error { return nil }
func TestOfferedReaderFlushesBeforeReadError(t *testing.T) {
	got, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(&offeredErrorReader{}, offeredSearchNames()))
	if err == nil || err.Error() != "upstream read failed" || offeredStreamText(t, string(got)) != "<tool_call>unfinished" {
		t.Fatalf("lost text or read error: %s %v", got, err)
	}
}

func TestOfferedReaderKeepsChoicesNativeCallsAndDistinctIndices(t *testing.T) {
	known := `<tool_call>{"name":"search","arguments":{"q":"x"}}</tool_call>`
	frame := func(index int, text string) string {
		return "data: " + offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{"content": text}}}}) + "\n\n"
	}
	input := frame(0, known[:20]) + frame(1, "choice one stays text") + frame(0, known[20:]) + frame(0, known) + "data: [DONE]\n\n"
	got := offeredRead(t, input)
	var indices []int
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var root struct {
			Choices []struct {
				Index int
				Delta struct {
					Content   string
					ToolCalls []struct{ Index int } `json:"tool_calls"`
				}
			}
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &root); err != nil {
			t.Fatal(err)
		}
		for _, choice := range root.Choices {
			if choice.Index == 1 && choice.Delta.Content != "choice one stays text" {
				t.Fatalf("cross-choice mixing: %s", got)
			}
			for _, call := range choice.Delta.ToolCalls {
				if choice.Index != 0 {
					t.Fatalf("call moved choices: %s", got)
				}
				indices = append(indices, call.Index)
			}
		}
	}
	if len(indices) != 2 || indices[0] != 0 || indices[1] != 1 {
		t.Fatalf("distinct calls reused an index: %v", indices)
	}
	native := `data: {"choices":[{"index":0,"delta":{"content":"native text","tool_calls":[{"index":0,"id":"existing","type":"function","function":{"name":"unoffered_native","arguments":"{}"}}]}}]}` + "\n\ndata: [DONE]\n\n"
	if actual := offeredRead(t, native); actual != native {
		t.Fatalf("native call modified: %s", actual)
	}
}

func TestOfferedReaderBoundsTotalChoiceHoldAndPreservesLength(t *testing.T) {
	c := newStreamXMLToolCallCoercer(offeredSearchNames())
	text := "<tool_call>" + strings.Repeat("x", maxStreamXMLToolCallBytes/2)
	for index := 0; index < 3; index++ {
		line := "data: " + offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{"content": text}}}}) + "\n"
		c.apply(line, true)
		if c.heldBytes > maxStreamXMLToolCallBytes {
			t.Fatalf("total hold exceeded cap: %d", c.heldBytes)
		}
	}
	flushed := c.flush()
	if c.pending() || c.heldBytes != 0 || offeredStreamText(t, flushed) != text {
		t.Fatal("pending text not released at EOF")
	}
	body := offeredResponse(t, `<tool_call>{"name":"search","arguments":{}}</tool_call>`)
	body = bytes.ReplaceAll(body, []byte(`"finish_reason":"stop"`), []byte(`"finish_reason":"length"`))
	if got := CoerceXMLToolCallsInChatResponseWithNames(body, offeredSearchNames()); !bytes.Contains(got, []byte(`"finish_reason":"length"`)) || !bytes.Contains(got, []byte(`"tool_calls"`)) {
		t.Fatalf("length was hidden: %s", got)
	}
}

func TestOfferedReaderReleasesTextBeforeFinishWithoutDelta(t *testing.T) {
	text := `<tool_call>unfinished`
	terminal := `data: {"choices":[{"index":0,"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"
	got := offeredRead(t, offeredFrame(t, text)+terminal)
	if offeredStreamText(t, got) != text || strings.Index(got, "unfinished") > strings.Index(got, `"finish_reason":"stop"`) {
		t.Fatalf("content arrived after terminal: %s", got)
	}
	known := `<tool_call>{"name":"search","arguments":{}}</tool_call>`
	first := strings.Replace(offeredFrame(t, known[:20]), `"index":0`, `"finish_reason":"","index":0`, 1)
	got = offeredRead(t, first+offeredFrame(t, known[20:])+"data: [DONE]\n\n")
	if !strings.Contains(got, `"name":"search"`) || offeredStreamText(t, got) != "" {
		t.Fatalf("empty finish prematurely released candidate: %s", got)
	}
}

func TestOfferedCustomToolCannotBecomeFunction(t *testing.T) {
	body := offeredResponse(t, `<tool_call>{"name":"search","arguments":{"q":"json shaped but custom/freeform offer"}}</tool_call>`)
	for _, request := range []string{
		`{"tools":[{"type":"custom","name":"search","format":{"type":"text"}}]}`,
		`{"tools":[{"type":"custom","custom":{"name":"search","format":{"type":"text"}}}]}`,
		`{"tools":[{"type":"custom_tool","name":"search"}]}`,
	} {
		if got := CoerceXMLToolCallsInChatResponseWithNames(body, tooloffer.FromRequest([]byte(request))); !bytes.Equal(got, body) {
			t.Errorf("custom/freeform offer became a function: %s", got)
		}
	}
}

func TestOfferedReaderStateClearsAfterFinishedChoices(t *testing.T) {
	c := newStreamXMLToolCallCoercer(offeredSearchNames())
	known := `<tool_call>{"name":"search","arguments":{}}</tool_call>`
	for index := 0; index < 1024; index++ {
		c.apply("data: "+offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{"content": known}}}})+"\n", true)
		c.apply("data: "+offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{}, "finish_reason": "stop"}}})+"\n", true)
	}
	if len(c.fragments)+len(c.converted)+len(c.disabled)+len(c.nextCallIndex) != 0 {
		t.Fatalf("finished choices retained state: fragment=%d converted=%d disabled=%d next=%d", len(c.fragments), len(c.converted), len(c.disabled), len(c.nextCallIndex))
	}
}

func TestOfferedReaderActiveChoiceStateIsBounded(t *testing.T) {
	c := newStreamXMLToolCallCoercer(offeredSearchNames())
	known := `<tool_call>{"name":"search","arguments":{}}</tool_call>`
	frame := func(index int) string {
		return "data: " + offeredJSON(t, map[string]any{"choices": []any{map[string]any{"index": index, "delta": map[string]any{"content": known}}}}) + "\n"
	}
	for index := 0; index < 64; index++ {
		c.apply(frame(index), true)
	}
	if got := c.apply(frame(64), true); offeredStreamText(t, got) != known || strings.Contains(got, `"tool_calls"`) {
		t.Fatalf("overflow choice was authorized: %s", got)
	}
	if len(c.converted) > 64 || len(c.disabled) > 64 || len(c.nextCallIndex) > 64 {
		t.Fatal("unbounded active choices")
	}
	c.apply("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n", true)
	if got := c.apply(frame(65), true); offeredStreamText(t, got) != known {
		t.Fatal("cap fallback forgot ignored-choice policy")
	}
	c.apply("data: [DONE]\n", true)
	if len(c.fragments)+len(c.converted)+len(c.disabled)+len(c.nextCallIndex) != 0 {
		t.Fatal("DONE did not clear all reader state")
	}
	body := NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(frame(0))), offeredSearchNames()).(*xmlToolCallCoercingBody)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if len(body.coercer.converted)+len(body.coercer.nextCallIndex) != 0 {
		t.Fatal("EOF retained conversion state")
	}
}

func TestOfferedReleasedFramesBoundUTF8AndKeepEnvelopeOnce(t *testing.T) {
	text := "<tool_call>" + strings.Repeat("漢字\x01", 11000)
	c := newStreamXMLToolCallCoercer(offeredSearchNames())
	root := map[string]any{"id": "original", "usage": map[string]any{"completion_tokens": 7}, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": text}, "finish_reason": "length"}}}
	wire := c.apply("data: "+offeredJSON(t, root)+"\n", true)
	if offeredStreamText(t, wire) != text {
		t.Fatal("released text changed")
	}
	usage, finish := 0, 0
	for _, line := range strings.Split(wire, "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if len(line) >= 64*1024 {
			t.Fatalf("released frame exceeds downstream scanner bound: %d", len(line))
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
			t.Fatal(err)
		}
		if frame["usage"] != nil {
			usage++
		}
		choices := frame["choices"].([]any)
		for _, raw := range choices {
			choice := raw.(map[string]any)
			delta := choice["delta"].(map[string]any)
			if content, ok := delta["content"].(string); ok {
				if len(content) > maxReleasedToolTextBytes || !utf8.ValidString(content) {
					t.Fatal("content frame split an UTF-8 rune or exceeded 8KiB")
				}
			}
			if choice["finish_reason"] != nil {
				finish++
			}
		}
	}
	if usage != 1 || finish != 1 {
		t.Fatalf("envelope duplicated usage=%d finish=%d", usage, finish)
	}
}
