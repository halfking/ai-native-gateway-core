package streaming

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingBody struct {
	closed chan struct{}
	once   sync.Once
}

func (b *blockingBody) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.EOF
}

func (b *blockingBody) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestReadRequestBodyTimeoutClosesSlowBody(t *testing.T) {
	t.Setenv("LLM_GATEWAY_ATTACHMENT_REQUEST_BODY_TIMEOUT_SEC", "1")
	body := &blockingBody{closed: make(chan struct{})}
	start := time.Now()
	_, err := readRequestBody(context.Background(), body, 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readRequestBody error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("readRequestBody took %s after timeout", elapsed)
	}
}

// panickingReadCloser panics inside Read so we can exercise the goroutine
// panic-recover guard added in request_meta.go (audit-2026-08-29 §5.2).
// Without recover() the goroutine dies, the channel send is skipped, and
// the caller blocks on the outer select until ctx (default 120s) expires,
// AND then blocks on the unguarded second <-resultCh inside the timeout
// branch forever — leaking the request goroutine for the full request
// body timeout.
type panickingReadCloser struct{}

func (panickingReadCloser) Read([]byte) (int, error) {
	panic("synthetic panic inside request body Read")
}

func (panickingReadCloser) Close() error { return nil }

func TestReadRequestBodyPanicBecomesError(t *testing.T) {
	// Override the env-driven default timeout so the test would surface a
	// hang clearly: if recover() is missing, the caller blocks for the full
	// 120s default. We pass a parent ctx with an even tighter 2s deadline
	// as belt-and-suspenders.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	t.Setenv("LLM_GATEWAY_ATTACHMENT_REQUEST_BODY_TIMEOUT_SEC", "30")
	start := time.Now()
	_, err := readRequestBody(ctx, panickingReadCloser{}, 1024)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("readRequestBody after panic = nil error, want recovered panic error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Fatalf("error = %v, want recovered panic", err)
	}
	// Sanity check: recovery must surface promptly, not wait for ctx timeout.
	if elapsed >= 2*time.Second {
		t.Fatalf("blocked %v after panic; recover() likely missing", elapsed)
	}
}
