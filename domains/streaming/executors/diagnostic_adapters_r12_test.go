package executors

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// R12（预研 §6 盲点 8 + adapter 三实现注入等价）：
//  1. RawDataLoggerAdapter 对 envelope 感知 logger 的 *WithEnvelope 字段
//     透传必须逐字段无损（此前只有回退路径有断言）；
//  2. 三实现（sync/async/buffered）经同一 adapter 注入后行为等价。

// envelopeRecordingRawLogger 记录全部 *WithEnvelope 调用的参数。
type envelopeRecordingRawLogger struct {
	mu        sync.Mutex
	calls     []string
	envelopes []logging.RawCorrelationEnvelope
}

func (l *envelopeRecordingRawLogger) LogClientRequest(requestID, protocol string, body []byte, _ map[string]string, step string) {
	l.record("LogClientRequest:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
}

func (l *envelopeRecordingRawLogger) LogUpstreamRequest(requestID, protocol string, body []byte, step string) {
	l.record("LogUpstreamRequest:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
}

func (l *envelopeRecordingRawLogger) LogUpstreamResponse(requestID, protocol string, body []byte, step string) {
	l.record("LogUpstreamResponse:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
}

func (l *envelopeRecordingRawLogger) LogClientResponse(requestID, protocol string, body []byte, step string) {
	l.record("LogClientResponse:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
}

func (l *envelopeRecordingRawLogger) LogClientRequestWithEnvelope(requestID, protocol string, body []byte, headers map[string]string, step string, env logging.RawCorrelationEnvelope) {
	l.record("LogClientRequestWithEnvelope:" + requestID + ":" + protocol + ":" + string(body) + ":" + step + ":" + headers["h"])
	l.envelopes = append(l.envelopes, env)
}

func (l *envelopeRecordingRawLogger) LogUpstreamRequestWithEnvelope(requestID, protocol string, body []byte, step string, env logging.RawCorrelationEnvelope) {
	l.record("LogUpstreamRequestWithEnvelope:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
	l.envelopes = append(l.envelopes, env)
}

func (l *envelopeRecordingRawLogger) LogUpstreamResponseWithEnvelope(requestID, protocol string, body []byte, step string, env logging.RawCorrelationEnvelope) {
	l.record("LogUpstreamResponseWithEnvelope:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
	l.envelopes = append(l.envelopes, env)
}

func (l *envelopeRecordingRawLogger) LogClientResponseWithEnvelope(requestID, protocol string, body []byte, step string, env logging.RawCorrelationEnvelope) {
	l.record("LogClientResponseWithEnvelope:" + requestID + ":" + protocol + ":" + string(body) + ":" + step)
	l.envelopes = append(l.envelopes, env)
}

func (l *envelopeRecordingRawLogger) record(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *envelopeRecordingRawLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// TestRawDataLoggerAdapter_ForwardsWithEnvelopeFields 覆盖盲点 8：四个
// *WithEnvelope 方法的全部参数（含 envelope）必须原样透传。
func TestRawDataLoggerAdapter_ForwardsWithEnvelopeFields(t *testing.T) {
	rec := &envelopeRecordingRawLogger{}
	adapter := NewRawDataLoggerAdapter(rec)

	env := logging.RawCorrelationEnvelope{
		ClientRequestID:  "cr-1",
		GWSessionID:      "sess-1",
		GWTaskID:         "task-1",
		ParentRequestID:  "parent-1",
		TenantID:         "tenant-1",
		ApplicationID:    "app-1",
		APIKeyID:         7,
		ProviderID:       9,
		CredentialID:     11,
		AttemptNo:        2,
		ChunkIndex:       5,
		UpstreamEndpoint: "https://up.example",
		TraceID:          "trace-1",
		SpanID:           "span-1",
	}

	adapter.LogClientRequestWithEnvelope("r1", "openai-chat", []byte("b1"), map[string]string{"h": "v1"}, "pre_parse", env)
	adapter.LogUpstreamRequestWithEnvelope("r2", "anthropic-messages", []byte("b2"), "post_conversion", env)
	adapter.LogUpstreamResponseWithEnvelope("r3", "anthropic-messages", []byte("b3"), "post_conversion", env)
	adapter.LogClientResponseWithEnvelope("r4", "openai-chat", []byte("b4"), "post_conversion", env)

	want := []string{
		"LogClientRequestWithEnvelope:r1:openai-chat:b1:pre_parse:v1",
		"LogUpstreamRequestWithEnvelope:r2:anthropic-messages:b2:post_conversion",
		"LogUpstreamResponseWithEnvelope:r3:anthropic-messages:b3:post_conversion",
		"LogClientResponseWithEnvelope:r4:openai-chat:b4:post_conversion",
	}
	got := rec.snapshot()
	if len(got) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(rec.envelopes) != 4 {
		t.Fatalf("got %d envelopes, want 4", len(rec.envelopes))
	}
	for i, e := range rec.envelopes {
		if e != env {
			t.Errorf("envelope[%d] drifted: %+v", i, e)
		}
	}
}

// TestRawDataLoggerAdapter_ThreeSinkEquivalence 三实现经同一 adapter 注入
// 后，四个方向的落盘行为必须等价（预研 §6）。
func TestRawDataLoggerAdapter_ThreeSinkEquivalence(t *testing.T) {
	type sinkCase struct {
		name string
		new  func(t *testing.T) (logging.RawSink, string)
	}
	cases := []sinkCase{
		{"sync", func(t *testing.T) (logging.RawSink, string) {
			dir := t.TempDir()
			s, err := logging.NewRawDataLogger(dir, 1024*1024, true)
			if err != nil {
				t.Fatal(err)
			}
			return s, dir
		}},
		{"async", func(t *testing.T) (logging.RawSink, string) {
			dir := t.TempDir()
			s, err := logging.NewAsyncRawDataLogger(dir, 1024*1024, true, 512)
			if err != nil {
				t.Fatal(err)
			}
			return s, dir
		}},
		{"buffered", func(t *testing.T) (logging.RawSink, string) {
			dir := t.TempDir()
			s, err := logging.NewBufferedRawSink(dir, 1024*1024, true, logging.BufferedRawSinkConfig{BatchSize: 2, FlushEvery: 20 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			return s, dir
		}},
	}

	wantDirections := []string{"client_request", "upstream_response", "upstream_request", "client_response"}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink, dir := c.new(t)
			adapter := NewRawDataLoggerAdapter(sink)

			if err := adapter.LogRequest("eq-r", "openai-chat", []byte(`{"d":1}`)); err != nil {
				t.Errorf("LogRequest err = %v", err)
			}
			if err := adapter.LogResponse("eq-r", "openai-chat", []byte(`{"d":2}`), false); err != nil {
				t.Errorf("LogResponse err = %v", err)
			}
			if err := adapter.LogUpstreamRequest("eq-r", "openai-chat", []byte(`{"d":3}`)); err != nil {
				t.Errorf("LogUpstreamRequest err = %v", err)
			}
			if err := adapter.LogClientResponse("eq-r", "openai-chat", []byte(`{"d":4}`)); err != nil {
				t.Errorf("LogClientResponse err = %v", err)
			}
			if err := sink.Close(); err != nil {
				t.Fatal(err)
			}

			files, err := filepath.Glob(filepath.Join(dir, "raw_data_*.jsonl"))
			if err != nil || len(files) == 0 {
				t.Fatalf("no audit files in %s", dir)
			}
			var sb strings.Builder
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				sb.Write(data)
			}
			data := sb.String()
			for _, direction := range wantDirections {
				if !strings.Contains(data, `"direction":"`+direction+`"`) {
					t.Errorf("missing direction %q in output:\n%s", direction, data)
				}
			}
			for _, step := range []string{"pre_conversion", "post_conversion"} {
				if !strings.Contains(data, `"conversion_step":"`+step+`"`) {
					t.Errorf("missing conversion_step %q", step)
				}
			}
		})
	}
}
