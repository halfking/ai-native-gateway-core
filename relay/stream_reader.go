package relay

import (
	"bufio"
	"context"
	"io"
	"time"
)

type streamReadResult struct {
	line  string
	state streamReadState
	err   error
	EOF   bool
}

// readNextStreamLine reads the next SSE line from the upstream response.
// closer is the underlying io.ReadCloser (resp.Body); it is passed through
// to readLineWithTimeoutAndCloser so that on chunk timeout the body is
// closed immediately, unblocking the ReadString goroutine (BUG-1 fix,
// 2026-06-19). Pass nil when no closer is available (e.g. tests,
// anthropic_stream.go which manages its own body lifecycle).
func readNextStreamLine(ctx context.Context, reader *bufio.Reader, closer io.ReadCloser, w streamKeepaliveWriter, lastSend *time.Time, runtimeCfg streamRuntimeConfig) streamReadResult {
	maybeSendKeepalive(w, lastSend, runtimeCfg.keepaliveInterval)
	line, err := readLineWithTimeoutAndCloser(ctx, reader, closer, runtimeCfg.streamChunkTimeout)
	if err != nil {
		state := classifyStreamReadError(ctx, err)
		return streamReadResult{line: line, state: state, err: err, EOF: state == streamReadEOF}
	}
	return streamReadResult{line: line, state: streamReadNext}
}

type streamKeepaliveWriter interface {
	Write([]byte) (int, error)
}

func maybeSendKeepalive(w streamKeepaliveWriter, lastSend *time.Time, keepaliveInterval time.Duration) {
	if w == nil || lastSend == nil || keepaliveInterval <= 0 {
		return
	}
	if time.Since(*lastSend) <= keepaliveInterval {
		return
	}
	_, _ = w.Write([]byte(sseKeepaliveComment))
	*lastSend = time.Now()
}
