package streaming

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
)

type frameCollectingInterceptor struct {
	frames [][]byte
}

func (i *frameCollectingInterceptor) InterceptNonStream(context.Context, *response.InterceptRequest) (*response.InterceptResult, error) {
	return nil, nil
}

func (i *frameCollectingInterceptor) InterceptStreamChunk(_ context.Context, frame []byte, _ *response.StreamMeta) (*response.ChunkResult, error) {
	i.frames = append(i.frames, append([]byte(nil), frame...))
	return nil, nil
}

func (i *frameCollectingInterceptor) InterceptStreamEnd(context.Context, *response.StreamMeta) (*response.EndResult, error) {
	return nil, nil
}

func TestInterceptingWriterFramesAllSSELineEndingVariants(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		split int
	}{
		{name: "LF", frame: "data: {\"ok\":true}\n\n"},
		{name: "CRLF", frame: "data: {\"ok\":true}\r\n\r\n"},
		{name: "CR", frame: "data: {\"ok\":true}\r\r"},
		{name: "mixed line endings", frame: "event: ping\r\ndata:{\"ok\":true}\n\r\n"},
		{name: "delimiter split after CR", frame: "data: {\"ok\":true}\r\r", split: len("data: {\"ok\":true}\r")},
		{name: "CRLF delimiter split", frame: "data: {\"ok\":true}\r\n\r\n", split: len("data: {\"ok\":true}\r\n\r")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			collector := &frameCollectingInterceptor{}
			writer := newInterceptingStreamWriter(httptest.NewRecorder(), response.NewInterceptorChain(collector), context.Background(), response.StreamMeta{})
			if tc.split > 0 {
				if _, err := writer.Write([]byte(tc.frame[:tc.split])); err != nil {
					t.Fatalf("first write: %v", err)
				}
				if _, err := writer.Write([]byte(tc.frame[tc.split:])); err != nil {
					t.Fatalf("second write: %v", err)
				}
			} else if _, err := writer.Write([]byte(tc.frame)); err != nil {
				t.Fatalf("write: %v", err)
			}
			writer.finish()
			if len(collector.frames) != 1 || !bytes.Equal(collector.frames[0], []byte(tc.frame)) {
				t.Fatalf("intercepted frames = %q; want one unchanged frame %q (pending=%q)", collector.frames, tc.frame, writer.pending)
			}
			if writer.writeErr != nil {
				t.Fatalf("writeErr = %v", writer.writeErr)
			}
		})
	}
}
