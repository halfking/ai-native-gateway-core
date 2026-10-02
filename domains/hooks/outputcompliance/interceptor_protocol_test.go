package outputcompliance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	compliance "github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
)

type protocolChecker struct {
	seen []string
	err  error
}

var testPhone = regexp.MustCompile(`1[3-9][0-9]{9}`)

func (c *protocolChecker) Check(_ context.Context, _, output string) (*compliance.ComplianceResult, error) {
	c.seen = append(c.seen, output)
	if c.err != nil {
		return nil, c.err
	}
	loc := testPhone.FindStringIndex(output)
	if loc == nil {
		return &compliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
	}
	return &compliance.ComplianceResult{
		Issues:         []compliance.ComplianceIssue{{Type: "pii", Subtype: "phone", Location: "char:" + strconv.Itoa(loc[0]) + "-" + strconv.Itoa(loc[1])}},
		RedactedOutput: output[:loc[0]] + "[PHONE]" + output[loc[1]:],
	}, nil
}

func TestProtocolTextRedactsVisibleFieldsOnly(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantSeen int
	}{
		{"chat", `{"id":"13800138000","usage":{"total_tokens":123},"choices":[{"message":{"role":"assistant","content":"call 13800138000","tool_calls":[{"function":{"arguments":"{\"phone\":\"13800138000\"}"}}]}}]}`, 2},
		{"messages", `{"type":"message","id":"13800138000","content":[{"type":"text","text":"call 13800138000"},{"type":"image","source":{"data":"13800138000"}},{"type":"tool_use","input":{"phone":"13800138000","attachment":{"type":"image","data":"13800138000"}}},{"type":"tool_use","input":"13800138000"},{"type":"output_audio","transcript":"13800138000","data":"13800138000"}]}`, 4},
		{"responses", `{"object":"response","id":"13800138000","output_text":"summary 13800138000","output":[{"type":"message","content":[{"type":"output_text","text":"call 13800138000"},{"type":"input_image","image_url":"13800138000"}]},{"type":"function_call","arguments":"{\"phone\":\"13800138000\"}"}]}`, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := &protocolChecker{}
			it := NewOutputComplianceInterceptor(checker, nil)
			result, err := it.processBody(context.Background(), &response.InterceptRequest{TenantID: "t", SessionID: "s", ResponseBody: []byte(tc.body)})
			if err != nil || result == nil || result.ShouldBlock || len(result.ModifiedBody) == 0 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if got := len(checker.seen); got != tc.wantSeen {
				t.Fatalf("checked fields=%d want=%d: %#v", got, tc.wantSeen, checker.seen)
			}
			if bytes.Contains(result.ModifiedBody, []byte(`"text":"call 13800138000"`)) || bytes.Contains(result.ModifiedBody, []byte(`"content":"call 13800138000"`)) {
				t.Fatalf("visible text leaked: %s", result.ModifiedBody)
			}
			var got map[string]any
			if err := json.Unmarshal(result.ModifiedBody, &got); err != nil {
				t.Fatal(err)
			}
			if got["id"] != "13800138000" {
				t.Fatalf("protocol metadata changed: %v", got["id"])
			}
			if tc.name == "messages" {
				parts := got["content"].([]any)
				image := parts[1].(map[string]any)
				if image["source"].(map[string]any)["data"] != "13800138000" {
					t.Fatal("media data changed")
				}
			}
		})
	}
}

func TestProtocolTextMalformedOrCheckerFailureBlocksBeforeWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    []byte
		checker *protocolChecker
	}{
		{"invalid", []byte(`{"choices":`), &protocolChecker{}},
		{"checker_down", []byte(`{"choices":[{"message":{"content":"hello"}}]}`), &protocolChecker{err: errors.New("db down")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := NewOutputComplianceInterceptor(tc.checker, nil)
			result, err := it.processBody(context.Background(), &response.InterceptRequest{TenantID: "t", ResponseBody: tc.body})
			if err != nil || result == nil || !result.ShouldBlock {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestStreamComplianceRedactsSplitPhoneBeforeAnyTextBytes(t *testing.T) {
	checker := &protocolChecker{}
	it := NewOutputComplianceInterceptor(checker, nil)
	meta := &response.StreamMeta{TenantID: "t", SessionID: "s", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, fragment := range []string{"138", "001", "38000 "} {
		frame := []byte("event: content_block_delta\ndata:{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"" + fragment + "\"}}\n\n")
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil || !result.SuppressChunk || len(result.ModifiedChunk) != 0 {
			t.Fatalf("fragment=%q result=%+v err=%v", fragment, result, err)
		}
	}
	released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
	if err != nil || released == nil || released.ShouldBlock || !released.SuppressChunk ||
		!bytes.Contains(released.ModifiedChunk, []byte("[PHONE]")) ||
		bytes.Contains(released.ModifiedChunk, []byte("13800138000")) {
		t.Fatalf("release=%+v err=%v", released, err)
	}
}

func TestStreamComplianceRedactsAndPreservesEventFraming(t *testing.T) {
	checker := &protocolChecker{}
	it := NewOutputComplianceInterceptor(checker, nil)
	meta := &response.StreamMeta{TenantID: "t", SessionID: "s", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	frame := []byte("event: content_block_delta\r\ndata:{\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"phone 13800138000 \"}}\r\n\r\n")
	result, err := it.processStreamChunk(context.Background(), frame, meta)
	if err != nil || result == nil || !result.SuppressChunk || len(result.ModifiedChunk) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\r\n\r\n"), meta)
	if err != nil || released == nil || !released.SuppressChunk || !bytes.Contains(released.ModifiedChunk, []byte("[PHONE]")) {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	if !bytes.HasPrefix(released.ModifiedChunk, []byte("event: content_block_delta\r\ndata:")) || !bytes.HasSuffix(released.ModifiedChunk, []byte("data: [DONE]\r\n\r\n")) {
		t.Fatalf("SSE framing changed: %q", released.ModifiedChunk)
	}
}

type replaceChecker struct {
	pattern     *regexp.Regexp
	replacement string
	seen        []string
}

func (c *replaceChecker) Check(_ context.Context, _, output string) (*compliance.ComplianceResult, error) {
	c.seen = append(c.seen, output)
	loc := c.pattern.FindStringIndex(output)
	if loc == nil {
		return &compliance.ComplianceResult{Compliant: true, RedactedOutput: output}, nil
	}
	return &compliance.ComplianceResult{
		Issues:         []compliance.ComplianceIssue{{Type: "secret", Subtype: "password"}},
		RedactedOutput: output[:loc[0]] + c.replacement + output[loc[1]:],
	}, nil
}

func TestStreamCompliancePasswordAcrossWhitespaceAndColon(t *testing.T) {
	checker := &replaceChecker{pattern: regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*\S+`), replacement: "[PASSWORD]"}
	it := NewOutputComplianceInterceptor(checker, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, fragment := range []string{"pass", "word", "   ", ":", "   ", "secret-value"} {
		frame := []byte(`data: {"choices":[{"index":0,"delta":{"content":` + strconv.Quote(fragment) + `}}]}` + "\n\n")
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil || !result.SuppressChunk || len(result.ModifiedChunk) != 0 {
			t.Fatalf("fragment=%q result=%+v err=%v", fragment, result, err)
		}
	}
	released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
	if err != nil || released == nil || released.ShouldBlock || !released.SuppressChunk ||
		!bytes.Contains(released.ModifiedChunk, []byte("[PASSWORD]")) ||
		bytes.Contains(released.ModifiedChunk, []byte("secret-value")) {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	if !containsString(checker.seen, "password   :   secret-value") {
		t.Fatalf("joined output never checked: %#v", checker.seen)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestStreamComplianceKeepsOutputLanesSeparate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []string
	}{
		{"chat_choices", []string{
			`data: {"choices":[{"index":0,"delta":{"content":"138"}}]}` + "\n\n",
			`data: {"choices":[{"index":1,"delta":{"content":"00138000"}}]}` + "\n\n",
		}},
		{"responses_output_index", []string{
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"138"}` + "\n\n",
			`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"00138000"}` + "\n\n",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &protocolChecker{}
			it := NewOutputComplianceInterceptor(checker, nil)
			meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
			meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
			for _, frame := range tc.frames {
				result, err := it.processStreamChunk(context.Background(), []byte(frame), meta)
				if err != nil || result == nil || !result.SuppressChunk {
					t.Fatalf("frame=%q result=%+v err=%v", frame, result, err)
				}
			}
			released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
			if err != nil || released == nil || released.ShouldBlock ||
				bytes.Contains(released.ModifiedChunk, []byte("[PHONE]")) ||
				!bytes.Contains(released.ModifiedChunk, []byte("00138000")) {
				t.Fatalf("separate lanes were merged: result=%+v err=%v", released, err)
			}
		})
	}
}

func TestStreamComplianceAudioTranscriptDeltaAndDone(t *testing.T) {
	checker := &protocolChecker{}
	it := NewOutputComplianceInterceptor(checker, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, fragment := range []string{"138", "00138000"} {
		frame := []byte(`data: {"type":"response.audio_transcript.delta","item_id":"audio-1","delta":` + strconv.Quote(fragment) + `}` + "\n\n")
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil || !result.SuppressChunk {
			t.Fatalf("audio delta=%+v err=%v", result, err)
		}
	}
	done := []byte(`data: {"type":"response.audio_transcript.done","item_id":"audio-1","transcript":"13800138000"}` + "\n\n")
	if result, err := it.processStreamChunk(context.Background(), done, meta); err != nil || result == nil || !result.SuppressChunk {
		t.Fatalf("audio done=%+v err=%v", result, err)
	}
	released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
	if err != nil || released == nil || released.ShouldBlock ||
		bytes.Contains(released.ModifiedChunk, []byte("13800138000")) ||
		bytes.Count(released.ModifiedChunk, []byte("[PHONE]")) != 2 {
		t.Fatalf("audio transcript leaked: result=%+v err=%v", released, err)
	}
}

func TestResponsesInitialPartAndDeltaShareOneOutputLane(t *testing.T) {
	for _, tc := range []struct {
		name      string
		added     string
		delta     string
		done      string
		addedText func(map[string]any) string
		doneText  func(map[string]any) string
	}{
		{
			name:      "content_part_text",
			added:     `{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"138"}}`,
			delta:     `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"00138000"}`,
			done:      `{"type":"response.output_text.done","item_id":"msg_1","output_index":0,"content_index":0,"text":"13800138000"}`,
			addedText: func(event map[string]any) string { return event["part"].(map[string]any)["text"].(string) },
			doneText:  func(event map[string]any) string { return event["text"].(string) },
		},
		{
			name:  "output_item_initial_text",
			added: `{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"138"}]}}`,
			delta: `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"00138000"}`,
			done:  `{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"13800138000"}]}}`,
			addedText: func(event map[string]any) string {
				return event["item"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
			},
			doneText: func(event map[string]any) string {
				return event["item"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
			},
		},
		{
			name:      "refusal_part",
			added:     `{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":"138"}}`,
			delta:     `{"type":"response.refusal.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"00138000"}`,
			done:      `{"type":"response.content_part.done","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"refusal","refusal":"13800138000"}}`,
			addedText: func(event map[string]any) string { return event["part"].(map[string]any)["refusal"].(string) },
			doneText:  func(event map[string]any) string { return event["part"].(map[string]any)["refusal"].(string) },
		},
		{
			name:      "audio_transcript_part",
			added:     `{"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":1,"part":{"type":"output_audio","transcript":"138"}}`,
			delta:     `{"type":"response.audio_transcript.delta","item_id":"msg_1","output_index":0,"content_index":1,"delta":"00138000"}`,
			done:      `{"type":"response.audio_transcript.done","item_id":"msg_1","output_index":0,"content_index":1,"transcript":"13800138000"}`,
			addedText: func(event map[string]any) string { return event["part"].(map[string]any)["transcript"].(string) },
			doneText:  func(event map[string]any) string { return event["transcript"].(string) },
		},
		{
			name:      "reasoning_summary_part",
			added:     `{"type":"response.reasoning_summary_part.added","item_id":"reason_1","output_index":1,"summary_index":0,"part":{"type":"summary_text","text":"138"}}`,
			delta:     `{"type":"response.reasoning_summary_text.delta","item_id":"reason_1","output_index":1,"summary_index":0,"delta":"00138000"}`,
			done:      `{"type":"response.reasoning_summary_text.done","item_id":"reason_1","output_index":1,"summary_index":0,"text":"13800138000"}`,
			addedText: func(event map[string]any) string { return event["part"].(map[string]any)["text"].(string) },
			doneText:  func(event map[string]any) string { return event["text"].(string) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &protocolChecker{}
			interceptor := NewOutputComplianceInterceptor(checker, nil)
			chain := response.NewInterceptorChain(interceptor)
			meta := &response.StreamMeta{TenantID: "t", ClientProtocol: "openai-responses", State: response.NewStreamState()}
			meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
			var wire []byte
			for _, payload := range []string{tc.added, tc.delta, tc.done, `{"type":"response.completed","response":{"id":"resp_1"}}`} {
				var envelope map[string]any
				if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
					t.Fatal(err)
				}
				typ := envelope["type"].(string)
				frame := []byte("event: " + typ + "\ndata: " + payload + "\n\n")
				result, err := chain.InterceptStreamChunk(context.Background(), frame, meta)
				if err != nil || result == nil || result.ShouldBlock || !result.SuppressChunk {
					t.Fatalf("event=%s result=%+v err=%v", typ, result, err)
				}
				wire = append(wire, result.ModifiedChunk...)
			}
			if bytes.Contains(wire, []byte("13800138000")) {
				t.Fatalf("full phone reached wire: %q", wire)
			}
			var got []map[string]any
			for _, rawFrame := range bytes.Split(wire, []byte("\n\n")) {
				if len(rawFrame) == 0 {
					continue
				}
				data, _, err := sseFrameData(append(append([]byte(nil), rawFrame...), '\n', '\n'))
				if err != nil {
					t.Fatal(err)
				}
				var event map[string]any
				if err := json.Unmarshal(data, &event); err != nil {
					t.Fatalf("invalid client event %q: %v", data, err)
				}
				got = append(got, event)
			}
			if len(got) != 4 {
				t.Fatalf("event count=%d want=4; wire=%q", len(got), wire)
			}
			assembled := tc.addedText(got[0]) + got[1]["delta"].(string)
			if assembled != "[PHONE]" || tc.doneText(got[2]) != "[PHONE]" {
				t.Fatalf("client assembled=%q done=%q wire=%q", assembled, tc.doneText(got[2]), wire)
			}
		})
	}
}

func TestResponsesInitialFunctionArgumentsAndDeltaRemainValidJSON(t *testing.T) {
	checker := &protocolChecker{}
	chain := response.NewInterceptorChain(NewOutputComplianceInterceptor(checker, nil))
	meta := &response.StreamMeta{TenantID: "t", ClientProtocol: "openai-responses", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	var wire []byte
	for _, payload := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"id":"call_1","type":"function_call","arguments":"{\"phone\":\"138"}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"call_1","output_index":0,"delta":"00138000\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"id":"call_1","type":"function_call","arguments":"{\"phone\":\"13800138000\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1"}}`,
	} {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			t.Fatal(err)
		}
		frame := []byte("event: " + envelope["type"].(string) + "\ndata: " + payload + "\n\n")
		result, err := chain.InterceptStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil || result.ShouldBlock || !result.SuppressChunk {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		wire = append(wire, result.ModifiedChunk...)
	}
	var assembled strings.Builder
	for _, rawFrame := range bytes.Split(wire, []byte("\n\n")) {
		if len(rawFrame) == 0 {
			continue
		}
		data, _, err := sseFrameData(append(append([]byte(nil), rawFrame...), '\n', '\n'))
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		switch event["type"] {
		case "response.output_item.added":
			assembled.WriteString(event["item"].(map[string]any)["arguments"].(string))
		case "response.function_call_arguments.delta":
			assembled.WriteString(event["delta"].(string))
		case "response.output_item.done":
			arguments := event["item"].(map[string]any)["arguments"].(string)
			if !json.Valid([]byte(arguments)) || strings.Contains(arguments, "13800138000") {
				t.Fatalf("done snapshot invalid or leaked: %q", arguments)
			}
		}
	}
	if !json.Valid([]byte(assembled.String())) || !strings.Contains(assembled.String(), "[PHONE]") || strings.Contains(assembled.String(), "13800138000") {
		t.Fatalf("assembled tool arguments invalid or leaked: %q; wire=%q", assembled.String(), wire)
	}
}

func TestSignedThinkingCannotBeRewritten(t *testing.T) {
	it := NewOutputComplianceInterceptor(&protocolChecker{}, nil)
	message := []byte(`{"type":"message","content":[{"type":"thinking","thinking":"13800138000","signature":"signed"}]}`)
	result, err := it.processBody(context.Background(), &response.InterceptRequest{TenantID: "t", ResponseBody: message})
	if err != nil || result == nil || !result.ShouldBlock {
		t.Fatalf("signed nonstream thinking result=%+v err=%v", result, err)
	}
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, fragment := range []string{"138", "00138000"} {
		frame := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + strconv.Quote(fragment) + `}}` + "\n\n")
		streamResult, streamErr := it.processStreamChunk(context.Background(), frame, meta)
		if streamErr != nil || streamResult == nil || !streamResult.SuppressChunk {
			t.Fatalf("thinking fragment=%q result=%+v err=%v", fragment, streamResult, streamErr)
		}
	}
	blocked, err := it.processStreamChunk(context.Background(), []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), meta)
	if err != nil || blocked == nil || !blocked.ShouldBlock || len(blocked.ModifiedChunk) != 0 {
		t.Fatalf("signed stream thinking result=%+v err=%v", blocked, err)
	}
}

func TestToolArgumentRedactionPreservesJSONOrBlocks(t *testing.T) {
	for _, tc := range []struct {
		name        string
		replacement string
		blocked     bool
	}{
		{"valid_json_rewrite", "[PHONE]", false},
		{"invalid_json_rewrite", `"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checker := &replaceChecker{pattern: testPhone, replacement: tc.replacement}
			it := NewOutputComplianceInterceptor(checker, nil)
			body := []byte(`{"choices":[{"message":{"tool_calls":[{"index":0,"function":{"arguments":"{\"phone\":\"13800138000\"}"}}]}}]}`)
			result, err := it.processBody(context.Background(), &response.InterceptRequest{TenantID: "t", ResponseBody: body})
			if err != nil || result == nil || result.ShouldBlock != tc.blocked {
				t.Fatalf("nonstream result=%+v err=%v", result, err)
			}
			if !tc.blocked {
				var parsed map[string]any
				if err := json.Unmarshal(result.ModifiedBody, &parsed); err != nil {
					t.Fatal(err)
				}
				choices := parsed["choices"].([]any)
				args := choices[0].(map[string]any)["message"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
				if !json.Valid([]byte(args)) || strings.Contains(args, "13800138000") {
					t.Fatalf("invalid or leaked nonstream arguments: %q", args)
				}
			}
			meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
			meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
			for _, fragment := range []string{`{"phone":"138`, `00138000"}`} {
				frame := []byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + strconv.Quote(fragment) + `}}]}}]}` + "\n\n")
				streamResult, streamErr := it.processStreamChunk(context.Background(), frame, meta)
				if streamErr != nil || streamResult == nil || !streamResult.SuppressChunk {
					t.Fatalf("tool fragment=%q result=%+v err=%v", fragment, streamResult, streamErr)
				}
			}
			released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
			if err != nil || released == nil || released.ShouldBlock != tc.blocked {
				t.Fatalf("stream result=%+v err=%v", released, err)
			}
			if tc.blocked {
				if len(released.ModifiedChunk) != 0 {
					t.Fatalf("invalid tool JSON leaked: %q", released.ModifiedChunk)
				}
				return
			}
			if bytes.Contains(released.ModifiedChunk, []byte("13800138000")) {
				t.Fatalf("stream arguments leaked: %q", released.ModifiedChunk)
			}
			var rebuilt strings.Builder
			for _, frame := range bytes.Split(released.ModifiedChunk, []byte("\n\n")) {
				if !bytes.HasPrefix(frame, []byte("data: ")) || bytes.Equal(frame, []byte("data: [DONE]")) {
					continue
				}
				var event map[string]any
				if err := json.Unmarshal(frame[len("data: "):], &event); err != nil {
					t.Fatal(err)
				}
				choices := event["choices"].([]any)
				arguments := choices[0].(map[string]any)["delta"].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
				rebuilt.WriteString(arguments)
			}
			if !json.Valid([]byte(rebuilt.String())) || !strings.Contains(rebuilt.String(), "[PHONE]") {
				t.Fatalf("stream arguments lost JSON validity: %q", rebuilt.String())
			}
		})
	}
}

func TestStreamComplianceFlushWithoutTerminalChecksWholeLane(t *testing.T) {
	it := NewOutputComplianceInterceptor(&protocolChecker{}, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, fragment := range []string{"138", "00138000"} {
		frame := []byte(`data: {"choices":[{"index":0,"delta":{"content":` + strconv.Quote(fragment) + `}}]}` + "\n\n")
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil || !result.SuppressChunk {
			t.Fatalf("fragment=%q result=%+v err=%v", fragment, result, err)
		}
	}
	released, err := it.FlushStreamPending(context.Background(), meta)
	if err != nil || !bytes.Contains(released, []byte("[PHONE]")) || bytes.Contains(released, []byte("13800138000")) {
		t.Fatalf("flush=%q err=%v", released, err)
	}
}

func TestStreamComplianceKeepsHeartbeatsAndControlOrder(t *testing.T) {
	it := NewOutputComplianceInterceptor(&protocolChecker{}, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	textFrame := []byte("event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"text":"hello"}}` + "\n\n")
	if result, err := it.processStreamChunk(context.Background(), textFrame, meta); err != nil || result == nil || !result.SuppressChunk {
		t.Fatalf("text result=%+v err=%v", result, err)
	}
	for _, heartbeat := range []string{": keep-alive\n\n", ": keep-alive\r\n\r\n", ": gw-survival-keepalive\n\n", ": gw-survival-keepalive\r\n\r\n", `event: ping` + "\n" + `data: {"type":"ping"}` + "\n\n"} {
		result, err := it.processStreamChunk(context.Background(), []byte(heartbeat), meta)
		if err != nil || result != nil {
			t.Fatalf("heartbeat=%q result=%+v err=%v", heartbeat, result, err)
		}
	}
	comment := []byte(": thinking: call 13800138000\n\n")
	if result, err := it.processStreamChunk(context.Background(), comment, meta); err != nil || result == nil || !result.SuppressChunk {
		t.Fatalf("thinking comment result=%+v err=%v", result, err)
	}
	control := []byte("event: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n")
	if result, err := it.processStreamChunk(context.Background(), control, meta); err != nil || result == nil || !result.SuppressChunk {
		t.Fatalf("control result=%+v err=%v", result, err)
	}
	released, err := it.processStreamChunk(context.Background(), []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"), meta)
	if err != nil || released == nil || released.ShouldBlock || bytes.Contains(released.ModifiedChunk, []byte("13800138000")) {
		t.Fatalf("release=%+v err=%v", released, err)
	}
	text := string(released.ModifiedChunk)
	if first, second, third, fourth := strings.Index(text, "hello"), strings.Index(text, "[PHONE]"), strings.Index(text, "message_delta"), strings.Index(text, "message_stop"); first < 0 || second <= first || third <= second || fourth <= third {
		t.Fatalf("semantic frame order changed: %q", text)
	}
}

func TestStreamComplianceChecksCommentTextAcrossFrames(t *testing.T) {
	checker := &replaceChecker{pattern: regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*\S+`), replacement: "[PASSWORD]"}
	it := NewOutputComplianceInterceptor(checker, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	for _, frame := range []string{": thinking: pass\n\n", ": word   : secret-value\n\n"} {
		result, err := it.processStreamChunk(context.Background(), []byte(frame), meta)
		if err != nil || result == nil || !result.SuppressChunk || len(result.ModifiedChunk) != 0 {
			t.Fatalf("comment=%q result=%+v err=%v", frame, result, err)
		}
	}
	released, err := it.processStreamChunk(context.Background(), []byte("data: [DONE]\n\n"), meta)
	if err != nil || released == nil || released.ShouldBlock ||
		!bytes.Contains(released.ModifiedChunk, []byte("[PASSWORD]")) ||
		bytes.Contains(released.ModifiedChunk, []byte("secret-value")) ||
		bytes.Contains(released.ModifiedChunk, []byte("\ndata: secret-value")) {
		t.Fatalf("comment leak or SSE injection: result=%+v err=%v", released, err)
	}
}

func TestStreamCompliancePendingLimitFailsClosed(t *testing.T) {
	it := NewOutputComplianceInterceptor(&protocolChecker{}, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	fragment := strings.Repeat("a", 8192)
	frame := []byte(`data: {"choices":[{"index":0,"delta":{"content":` + strconv.Quote(fragment) + `}}]}` + "\n\n")
	blocked := false
	for i := 0; i < maxCompliancePendingBytes/len(frame)+2; i++ {
		result, err := it.processStreamChunk(context.Background(), frame, meta)
		if err != nil || result == nil {
			t.Fatalf("frame %d result=%+v err=%v", i, result, err)
		}
		if result.ShouldBlock {
			if len(result.ModifiedChunk) != 0 {
				t.Fatalf("oversize leaked bytes: %+v", result)
			}
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("pending byte limit did not fail closed")
	}
}

func TestStreamComplianceMalformedJSONBlocks(t *testing.T) {
	it := NewOutputComplianceInterceptor(&protocolChecker{}, nil)
	meta := &response.StreamMeta{TenantID: "t", State: response.NewStreamState()}
	meta.State.GetOrCreate("output_compliance.pending", func() any { return &complianceStreamState{enabled: true, redact: true} })
	result, err := it.processStreamChunk(context.Background(), []byte("data: {bad}\n\n"), meta)
	if err != nil || result == nil || !result.ShouldBlock {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
