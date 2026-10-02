package sanitize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

func auditStreamFrame(t *testing.T, size int, terminal bool) []byte {
	t.Helper()
	var payload any = map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": ""}}}}
	if terminal {
		payload = map[string]any{"type": "response.completed", "response": map[string]any{"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": ""}}}}}}
	}
	empty := marshalAuditBody(t, payload)
	padding := bytes.Repeat([]byte("a"), size-len(empty)-len("data: \n\n"))
	// Both shapes contain exactly one empty string value.
	raw := bytes.Replace(empty, []byte(`""`), append(append([]byte{'"'}, padding...), '"'), 1)
	if !json.Valid(raw) {
		t.Fatal("invalid fixture")
	}
	return append(append([]byte("data: "), raw...), '\n', '\n')
}

// B/op is cumulative Go allocation, not live heap/RSS. The wire cap is not
// a promise that the parsed frames and regex working memory occupy 1 MiB.
func BenchmarkStorageAuditStreamCapacity(b *testing.B) {
	for _, frameSize := range []int{128, 8192, 1 << 20} {
		b.Run(fmt.Sprint(frameSize), func(b *testing.B) {
			prefix, suffix := `data: {"choices":[{"index":0,"delta":{"content":"`, "\"}}]}\n\n"
			frame := []byte(prefix + strings.Repeat("a", frameSize-len(prefix)-len(suffix)) + suffix)
			frames := ((1 << 20) - len("data: [DONE]\n\n")) / len(frame)
			if frames == 0 {
				frames = 1
			}
			s, _ := NewSanitizer(NewPatternDetector())
			guard := NewOutputSensitiveInterceptor(s, OutputMask)
			b.ReportAllocs()
			b.SetBytes(int64(frames * len(frame)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				meta := &response.StreamMeta{State: response.NewStreamState()}
				for j := 0; j < frames; j++ {
					result, err := guard.InterceptStreamChunk(context.Background(), frame, meta)
					if err != nil || result == nil || result.ShouldBlock || !result.SuppressChunk {
						b.Fatalf("pending failed: %+v %v", result, err)
					}
				}
				out, err := guard.FlushStreamPending(context.Background(), meta)
				if err != nil || len(out) != frames*len(frame) {
					b.Fatalf("flush len=%d err=%v", len(out), err)
				}
			}
			b.ReportMetric(float64(frames), "frames/op")
		})
	}
}

func TestStorageAuditStreamTerminalCapacity(t *testing.T) {
	const limit = 1 << 20
	for _, tc := range []struct {
		name              string
		pending, terminal int
		block             bool
	}{
		{"terminal_at_limit", 0, limit, false},
		{"pending_plus_terminal_at_limit", 512, limit - 512, false},
		{"terminal_over_limit", 0, limit + 1, true},
		{"pending_plus_terminal_over_limit", 512, limit, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := NewSanitizer(NewPatternDetector())
			guard := NewOutputSensitiveInterceptor(s, OutputMask)
			meta := &response.StreamMeta{State: response.NewStreamState()}
			if tc.pending > 0 {
				result, err := guard.InterceptStreamChunk(context.Background(), auditStreamFrame(t, tc.pending, false), meta)
				if err != nil || result == nil || !result.SuppressChunk || result.ShouldBlock {
					t.Fatalf("pending: %+v %v", result, err)
				}
			}
			result, err := guard.InterceptStreamChunk(context.Background(), auditStreamFrame(t, tc.terminal, true), meta)
			if err != nil {
				t.Fatal(err)
			}
			blocked := result != nil && result.ShouldBlock
			if blocked != tc.block {
				t.Fatalf("blocked=%v want=%v; pending=%d terminal=%d", blocked, tc.block, tc.pending, tc.terminal)
			}
		})
	}
}
