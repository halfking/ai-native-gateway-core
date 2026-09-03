package middleware

import (
	"context"
	"sync"
)

type cleanupStackKey struct{}

// CleanupStack runs request-scoped cleanup functions in LIFO order. Cleanup
// failures are isolated so one bad cleanup cannot prevent the remaining ones.
type CleanupStack struct {
	mu      sync.Mutex
	fns     []func()
	runOnce sync.Once
}

func NewCleanupStack() *CleanupStack { return &CleanupStack{} }

func (c *CleanupStack) Add(fn func()) {
	if c == nil || fn == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fns = append(c.fns, fn)
}

func (c *CleanupStack) RunAll() {
	if c == nil {
		return
	}
	c.runOnce.Do(func() {
		c.mu.Lock()
		fns := append([]func(){}, c.fns...)
		c.fns = nil
		c.mu.Unlock()
		for i := len(fns) - 1; i >= 0; i-- {
			func() { defer func() { _ = recover() }(); fns[i]() }()
		}
	})
}

func WithCleanupStack(ctx context.Context, stack *CleanupStack) context.Context {
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
