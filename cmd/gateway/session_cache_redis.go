package main

import "context"

// Eval supplies the lease and atomic pair commit capability required whenever
// SessionCache is configured with a shared Redis backend.
func (a *redisBackendAdapter) Eval(ctx context.Context, script string, keys []string, args ...any) (any, error) {
	return a.c.Client().Eval(ctx, script, keys, args...).Result()
}
