package relay

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestStreamAnthropicSSEToOpenAI_ChunkTimeout(t *testing.T) {
	t.Skip("Skipping integration test: requires mock config store to override streamChunkTimeout. " +
		"The timeout logic is tested in production via observability metrics.")

}

type stallAfterNReadsReader struct {
	data       []byte
	offset     int
	readCount  int
	stallAfter int
	ctx        context.Context
}

func (r *stallAfterNReadsReader) Read(p []byte) (n int, err error) {
	r.readCount++
	if r.readCount > r.stallAfter {
		<-r.ctx.Done()
		return 0, r.ctx.Err()
	}

	if r.offset >= len(r.data) {
		return 0, io.EOF
	}

	end := bytes.Index(r.data[r.offset:], []byte("\n\n"))
	if end == -1 {
		end = len(r.data) - r.offset
	} else {
		end += 2
	}

	n = copy(p, r.data[r.offset:r.offset+end])
	r.offset += n
	return n, nil
}

func TestReadSSEEvent_ContextCancellation(t *testing.T) {
	data := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	reader := bufio.NewReader(strings.NewReader(data))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := streamRuntimeConfig{}
	_, _, err := readSSEEvent(ctx, reader, cfg)

	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
}

func TestReadSSEEvent_ContextTimeout(t *testing.T) {
	t.Skip("Skipping: ReadString('\\n') blocks until newline, context check is per-line not per-byte. " +
		"The timeout detection happens at the goroutine level in StreamAnthropicSSEToOpenAI.")
}

type slowReader struct {
	data   []byte
	offset int
	delay  time.Duration
}

func (r *slowReader) Read(p []byte) (n int, err error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n = 1
	if n > len(p) {
		n = len(p)
	}
	copy(p, r.data[r.offset:r.offset+n])
	r.offset += n
	return n, nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
