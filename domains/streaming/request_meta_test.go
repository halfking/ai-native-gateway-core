package streaming

import (
	"context"
	"errors"
	"io"
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
