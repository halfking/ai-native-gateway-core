package streaming

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/security/sanitize"
)

func auditOutputChain(t *testing.T) *response.InterceptorChain {
	t.Helper()
	s, err := sanitize.NewSanitizer(sanitize.NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	return response.NewInterceptorChain(sanitize.NewOutputSensitiveInterceptor(s, sanitize.OutputMask))
}

func TestStorageAuditSSEVisibleTextLatency(t *testing.T) {
	for _, terminal := range []bool{true, false} {
		name := "terminal"
		if !terminal {
			name = "EOF"
		}
		t.Run(name, func(t *testing.T) {
			chain := auditOutputChain(t)
			start := time.Now()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writer := newInterceptingStreamWriter(w, chain, r.Context(), response.StreamMeta{})
				writer.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range []string{": keep-alive\n\n", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"first-visible-word\"}}]}\n\n"} {
					if _, err := writer.Write([]byte(frame)); err != nil {
						t.Error(err)
					}
					writer.Flush()
				}
				time.Sleep(150 * time.Millisecond) // synthetic upstream generation tail
				if terminal {
					if _, err := writer.Write([]byte("data: [DONE]\n\n")); err != nil {
						t.Error(err)
					}
				}
				writer.finish()
				writer.Flush()
			}))
			defer srv.Close()
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var heartbeat, text time.Duration
			scanner := bufio.NewScanner(resp.Body)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "keep-alive") {
					heartbeat = time.Since(start)
				}
				if strings.Contains(line, "first-visible-word") {
					text = time.Since(start)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if heartbeat == 0 || text < 150*time.Millisecond || text <= heartbeat {
				t.Fatalf("heartbeat=%s visible_text=%s", heartbeat, text)
			}
			t.Logf("production SSE writer + mandatory guard: heartbeat=%s visible_text=%s synthetic_tail=150ms", heartbeat, text)
		})
	}
}

func TestStorageAuditWriterBlocksCombinedTerminalCapacity(t *testing.T) {
	writer := newInterceptingStreamWriter(httptest.NewRecorder(), auditOutputChain(t), context.Background(), response.StreamMeta{})
	frame := append([]byte(`data: {"choices":[{"index":0,"delta":{"content":"`), bytes.Repeat([]byte("a"), 512<<10)...)
	frame = append(frame, []byte("\"}}]}\n\n")...)
	if _, err := writer.Write(frame); err != nil {
		t.Fatal(err)
	}
	terminal := append([]byte(`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"`), bytes.Repeat([]byte("a"), 512<<10)...)
	terminal = append(terminal, []byte("\"}]}]}}\n\n")...)
	if _, err := writer.Write(terminal); err == nil || !writer.OutputPolicyBlocked() {
		t.Fatalf("combined oversized stream not blocked: %v", err)
	}
}
