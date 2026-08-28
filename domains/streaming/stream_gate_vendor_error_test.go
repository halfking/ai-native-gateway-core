package streaming

import (
	"bufio"
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/config"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

func TestRunEmptyStreamGateMiniMaxErrorIsClassifiedBeforeStrip(t *testing.T) {
	previous := streamConfigStore.Load()
	streamConfigStore.Store(config.NewStore(&config.Config{
		EnableEmptyStreamGate:       true,
		EmptyStreamEarlyEmptyChunks: 0,
	}))
	t.Cleanup(func() { streamConfigStore.Store(previous) })

	body := io.NopCloser(strings.NewReader("data: [DONE]\n\n"))
	recorder := httptest.NewRecorder()
	lastSend := time.Now()
	chunkCount := 0
	starting := `data: {"id":"error","choices":[],"base_resp":{"status_code":1008,"status_msg":"quota"}}` + "\n"

	lines, outcome := runEmptyStreamGateWithVendor(
		context.Background(), bufio.NewReader(body), body, recorder, recorder,
		nil, nil, nil, "gpt-test", new(string), starting, time.Second,
		&lastSend, &chunkCount, nil, "minimax", StripMinimaxFieldsBody,
	)
	if lines != nil {
		t.Fatalf("error frame must not be flushed: %v", lines)
	}
	if outcome == nil || !outcome.Interrupted || outcome.Kind != errorsx.KindQuota {
		t.Fatalf("outcome = %+v, want quota interruption", outcome)
	}
	if !outcome.Resumable || outcome.ChunkCount != 0 {
		t.Fatalf("outcome = %+v, want resumable zero-client-output failure", outcome)
	}
	if recorder.Body.Len() != 0 {
		t.Fatalf("error frame leaked to client: %q", recorder.Body.String())
	}
}
