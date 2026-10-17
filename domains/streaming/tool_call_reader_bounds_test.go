package streaming

import (
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/kaixuan/llm-gateway-go/internal/sse"
)

type offeredCountingBody struct {
	io.Reader
	read int
}

func (r *offeredCountingBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
func (*offeredCountingBody) Close() error { return nil }

func TestOfferedReaderBoundsPhysicalLines(t *testing.T) {
	previous := streamConfigStore.Load()
	streamConfigStore.Store(nil)
	t.Cleanup(func() { streamConfigStore.Store(previous) })
	t.Setenv("LLM_GATEWAY_SSE_MAX_LINE_BYTES", "1024")
	for _, suffix := range []string{"", "\n"} {
		t.Run("oversize"+strconv.Itoa(len(suffix)), func(t *testing.T) {
			up := &offeredCountingBody{Reader: strings.NewReader("data: {" + strings.Repeat("x", 9216) + suffix)}
			body := NewXMLToolCallCoercingBodyWithNames(up, offeredSearchNames())
			_, err := io.ReadAll(body)
			var lineErr *sse.LineTooLongError
			if !errors.Is(err, sse.ErrLineTooLong) || !errors.As(err, &lineErr) || lineErr.Limit != 1024 {
				t.Fatalf("oversized physical line must retain existing typed error: %v", err)
			}
			if up.read > 1024+4096 {
				t.Fatalf("must fail before draining an unbounded line: read=%d", up.read)
			}
		})
	}
	// A Reader may return its last data and EOF together; EOF must not mask the cap error.
	dataAndEOF := iotest.DataErrReader(strings.NewReader("data: {" + strings.Repeat("x", 1500)))
	_, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(dataAndEOF), offeredSearchNames()))
	if !errors.Is(err, sse.ErrLineTooLong) {
		t.Fatalf("EOF masked physical-line failure: %v", err)
	}
	partial := `<tool_call><function=search><parameter=q>visible pending`
	input := offeredFrame(t, partial) + "data: {" + strings.Repeat("x", 1500)
	beforeFailure, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(input)), offeredSearchNames()))
	if !errors.Is(err, sse.ErrLineTooLong) || offeredStreamText(t, string(beforeFailure)) != partial {
		t.Fatalf("previous valid text was lost before line failure: %q (%v)", beforeFailure, err)
	}
	// A single read can contain many legal lines whose combined size exceeds the cap.
	wire := strings.Repeat(offeredFrame(t, "ordinary text"), 80) + "data: [DONE]\n\n"
	got, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(wire)), offeredSearchNames()))
	if err != nil || string(got) != wire {
		t.Fatalf("cap applies per physical line, not per read: %v", err)
	}
	line := offeredFrame(t, strings.Repeat("z", 200))
	for _, delta := range []int{0, -1} {
		t.Setenv("LLM_GATEWAY_SSE_MAX_LINE_BYTES", strconv.Itoa(len(line)-1+delta))
		got, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(line)), offeredSearchNames()))
		if delta == 0 && (err != nil || string(got) != line) {
			t.Fatalf("line exactly at limit including LF rejected: %v", err)
		}
		if delta < 0 && !errors.Is(err, sse.ErrLineTooLong) {
			t.Fatalf("terminating LF must count toward line limit: %v", err)
		}
	}
}

func TestOfferedReaderDoesNotInventToolTerminal(t *testing.T) {
	call := `<tool_call>{"name":"search","arguments":{"q":"next"}}</tool_call>`
	for _, finish := range []string{"absent", "null", "empty"} {
		for _, terminal := range []bool{false, true} {
			t.Run(finish+"/terminal_"+strconv.FormatBool(terminal), func(t *testing.T) {
				frame := offeredFrame(t, call)
				if finish != "absent" {
					value := "null"
					if finish == "empty" {
						value = `""`
					}
					frame = strings.Replace(frame, `{"choices":[{`, `{"choices":[{"finish_reason":`+value+`,`, 1)
				}
				wire := frame + frame
				if terminal {
					wire += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				}
				out, err := io.ReadAll(NewXMLToolCallCoercingBodyWithNames(io.NopCloser(strings.NewReader(wire)), offeredSearchNames()))
				if err != nil {
					t.Fatal(err)
				}
				calls, endings := 0, 0
				for _, line := range strings.Split(string(out), "\n") {
					if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
						continue
					}
					var frame struct {
						Choices []struct {
							Delta struct {
								Calls []json.RawMessage `json:"tool_calls"`
							} `json:"delta"`
							Finish string `json:"finish_reason"`
						} `json:"choices"`
					}
					if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame); err != nil {
						t.Fatal(err)
					}
					for _, choice := range frame.Choices {
						calls += len(choice.Delta.Calls)
						if choice.Finish != "" {
							endings++
							if choice.Finish != "tool_calls" || len(choice.Delta.Calls) > 0 {
								t.Fatalf("recovery moved the terminal onto a nonterminal call frame: %s", line)
							}
						}
					}
				}
				wantEndings := 0
				if terminal {
					wantEndings = 1
				}
				if calls != 2 || endings != wantEndings {
					t.Fatalf("terminal=%v calls=%d endings=%d; want 2/%d", terminal, calls, endings, wantEndings)
				}
			})
		}
	}
}
