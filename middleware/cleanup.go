package middleware

import (
	"context"
	"log/slog"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type cleanupStackKey struct{}

var cleanupPanicsTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "request_cleanup_panics_total",
	Help: "Total panics recovered while running request cleanup functions.",
})

// CleanupStack runs request-scoped cleanup functions in LIFO order. Cleanup
// failures are isolated so one bad cleanup cannot prevent the remaining ones.
type CleanupStack struct {
	mu      sync.Mutex
	fns     []func()
	running bool
	done    bool
	runOnce sync.Once
}

func NewCleanupStack() *CleanupStack { return &CleanupStack{} }

// Add registers a cleanup. If cleanup has already started, run the function
// synchronously so it cannot be silently stranded in an already-drained stack.
func (c *CleanupStack) Add(fn func()) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	if !c.running && !c.done {
		c.fns = append(c.fns, fn)
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	invokeCleanup(fn)
}

func (c *CleanupStack) RunAll() {
	if c == nil {
		return
	}
	c.runOnce.Do(func() {
		c.mu.Lock()
		c.running = true
		fns := append([]func(){}, c.fns...)
		c.fns = nil
		c.mu.Unlock()
		for i := len(fns) - 1; i >= 0; i-- {
			invokeCleanup(fns[i])
		}
		c.mu.Lock()
		c.running = false
		c.done = true
		c.mu.Unlock()
	})
}

func invokeCleanup(fn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			cleanupPanicsTotal.Inc()
			slog.Error("request_cleanup_panicked", "panic", rec)
		}
	}()
	fn()
}

func WithCleanupStack(ctx context.Context, stack *CleanupStack) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if stack == nil {
		stack = NewCleanupStack()
	}
	return context.WithValue(ctx, cleanupStackKey{}, stack)
}

func CleanupStackFromContext(ctx context.Context) *CleanupStack {
	if ctx == nil {
		return nil
	}
	stack, _ := ctx.Value(cleanupStackKey{}).(*CleanupStack)
	return stack
}

func RegisterCleanup(ctx context.Context, fn func()) {
	if stack := CleanupStackFromContext(ctx); stack != nil {
		stack.Add(fn)
	}
}
