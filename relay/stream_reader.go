package relay

import (
	"bufio"
	"context"
	"time"
)

type streamReadResult struct {
	line  string
	state streamReadState
	err   error
	EOF   bool
}

func readNextStreamLine(ctx context.Context, reader *bufio.Reader, w streamKeepaliveWriter, lastSend *time.Time, runtimeCfg streamRuntimeConfig) streamReadResult {
	maybeSendKeepalive(w, lastSend, runtimeCfg.keepaliveInterval)
	line, err := readLineWithTimeout(ctx, reader, runtimeCfg.streamChunkTimeout)
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
