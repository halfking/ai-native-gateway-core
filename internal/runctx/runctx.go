package runctx

import (
	"context"
	"time"
)

// BackgroundTimeout creates a bounded context that is completely detached from
// the caller lifecycle. Use it for cleanup/release/state-writeback paths that
// must still run after the request context is canceled.
func BackgroundTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// DetachedTimeout keeps parent values (for trace/request metadata) but detaches
// from parent cancellation before applying a timeout. Use it for async
// writeback/compensation paths that still want to preserve request-scoped
// values while not being aborted by client disconnect.
func DetachedTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), d)
}
