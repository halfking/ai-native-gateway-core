package streaming

import (
	"context"
	"io"
	"log/slog"
	"net/http"

	"github.com/kaixuan/llm-gateway-go/internal/largebody"
)

// The pipe applies disk backpressure instead of queuing response bytes. The
// response reference is deterministic and independently recoverable after EOF.
func (g *LargeIngress) serveArchivedResponse(next http.Handler, w http.ResponseWriter, r *http.Request, ref largebody.Ref) {
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	id := ref.Identity
	id.RequestID += "-response"
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	go func() { _, err := g.store.Capture(ctx, id, reader); _ = reader.CloseWithError(err); done <- err }()
	defer func() {
		_ = writer.Close()
		if err := <-done; err != nil {
			slog.Error("client response archive failed", "request_id", ref.RequestID, "error", err)
		}
	}()
	next.ServeHTTP(&archiveResponseWriter{ResponseWriter: w, archive: writer}, r)
}

type archiveResponseWriter struct {
	http.ResponseWriter
	archive io.Writer
}

func (w *archiveResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *archiveResponseWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		if _, e := w.archive.Write(p[:n]); e != nil {
			return n, e
		}
	}
	return n, err
}
func (w *archiveResponseWriter) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *archiveResponseWriter) Flush() { _ = w.FlushError() }
