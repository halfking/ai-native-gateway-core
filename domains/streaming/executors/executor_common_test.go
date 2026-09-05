package executors

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/credential" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// newCircuitManagerForTest returns a credential.Manager with no DB /
// external dependencies. credential.NewManager is in-memory and works in
// tests without further config.
func newCircuitManagerForTest() *credential.Manager {
	return credential.NewManager()
}

// newLimiterForTest returns a credential.Limiter with default 4-layer
// concurrency buckets. The recovery loop runs in a background goroutine;
// tests that don't want it can call lim.Stop().
func newLimiterForTest() *credential.Limiter {
	return credential.NewLimiter()
}

// wireDispatchPipelineForTest wires the real V2 dispatch pipeline (the only
// execute path since AUDIT_24H B2b retired the legacy sync candidate loop)
// so Execute-driven tests exercise the production path.
func wireDispatchPipelineForTest(t *testing.T, e *Executor) {
	t.Helper()
	p := e.NewDispatchPipeline()
	p.Start()
	t.Cleanup(p.Stop)
	e.SetDispatchPipeline(p)
}
