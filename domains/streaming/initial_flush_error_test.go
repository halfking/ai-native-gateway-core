package streaming

import (
	"errors"
	"io"
	"net/http"
	"testing"
)

type initialFlushErrorWriter struct {
	header   http.Header
	flushErr error
}

func (w *initialFlushErrorWriter) Header() http.Header { return w.header }
func (w *initialFlushErrorWriter) WriteHeader(int)     {}
func (w *initialFlushErrorWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
func (w *initialFlushErrorWriter) Flush()            {}
func (w *initialFlushErrorWriter) FlushError() error { return w.flushErr }

func TestProtocolStreamsReportInitialFlushError(t *testing.T) {
	tests := []struct {
		name string
		run  func(http.ResponseWriter, *http.Response) StreamOutcome
	}{
		{name: "openai chat", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamChat(w, resp, "client", "upstream", nil)
		}},
		{name: "openai responses", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamResponsesSSE(w, resp, "client", "upstream", "req", nil)
		}},
		{name: "openai to anthropic", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamOpenAIToAnthropicSSE(w, resp, "client", "upstream", "req", nil, nil)
		}},
		{name: "anthropic passthrough", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamAnthropicPassthrough(w, resp, "client", "upstream", "req", nil, nil)
		}},
		{name: "anthropic to openai", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamAnthropicSSEToOpenAI(w, resp, "client", "upstream", "req", nil, nil)
		}},
		{name: "anthropic to responses", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamAnthropicSSEToResponses(w, resp, "client", "upstream", "req", nil, nil)
		}},
		{name: "openai to responses", run: func(w http.ResponseWriter, resp *http.Response) StreamOutcome {
			return StreamOpenAIToResponsesSSE(w, resp, "client", "upstream", "req", nil, nil)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writer := &initialFlushErrorWriter{header: make(http.Header), flushErr: errors.New("connection closed")}
			response := &http.Response{Body: io.NopCloser(&errorOnRead{})}
			outcome := test.run(writer, response)
			if !outcome.Interrupted || outcome.Reason != "client_write_failed" || outcome.Resumable {
				t.Fatalf("outcome = %+v, want NON-resumable client_write_failed (client disconnect is permanent; transparent retry cannot write headers to a dead connection)", outcome)
			}
		})
	}
}

type errorOnRead struct{}

func (*errorOnRead) Read([]byte) (int, error) { return 0, errors.New("upstream body must not be read") }
