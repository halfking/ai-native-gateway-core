package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/routingopt"
)

// TestBuildRoutingOptimizer_DisabledByDefault proves the P2.2 plugin stays
// off unless ROUTING_OPT_ENABLED=true — routing must be baseline-identical.
func TestBuildRoutingOptimizer_DisabledByDefault(t *testing.T) {
	t.Setenv("ROUTING_OPT_ENABLED", "")
	if got := buildRoutingOptimizer(nil); got != nil {
		t.Fatalf("optimizer must be nil when flag is unset, got %T", got)
	}
	t.Setenv("ROUTING_OPT_ENABLED", "false")
	if got := buildRoutingOptimizer(nil); got != nil {
		t.Fatalf("optimizer must be nil when flag is false, got %T", got)
	}
}

// TestBuildRoutingOptimizer_EnabledWithoutPoolDegrades proves the enabled
// flag without a DB pool degrades to baseline instead of panicking.
func TestBuildRoutingOptimizer_EnabledWithoutPoolDegrades(t *testing.T) {
	t.Setenv("ROUTING_OPT_ENABLED", "true")
	if got := buildRoutingOptimizer(nil); got != nil {
		t.Fatalf("enabled flag with nil pool must degrade to nil, got %T", got)
	}
}

// TestBuildRoutingOptimizer_EnabledWithPool wires a real (lazy) pool: no
// connection is attempted until first acquire, so this stays hermetic.
func TestBuildRoutingOptimizer_EnabledWithPool(t *testing.T) {
	t.Setenv("ROUTING_OPT_ENABLED", "true")
	pool, err := pgxpool.New(context.Background(), "postgres://gateway:gw@127.0.0.1:1/gw?sslmode=disable")
	if err != nil {
		t.Fatalf("lazy pool construction failed: %v", err)
	}
	defer pool.Close()

	got := buildRoutingOptimizer(pool)
	if got == nil {
		t.Fatal("enabled flag with pool must return a non-nil optimizer")
	}
	// The wired optimizer must satisfy the autoroute plugin contract through
	// the decider-facing interface (compile-time guarantee asserted here).
	var _ interface {
		PreClassify(ctx context.Context, signals interface{}) (interface{}, error)
		PostClassify(ctx context.Context, taskType string, confidence float64) (float64, error)
		RecommendModel(ctx context.Context, candidates interface{}, context interface{}) (interface{}, error)
		RecordFeedback(ctx context.Context, feedback interface{}) error
		GetStats(ctx context.Context) (interface{}, error)
	} = got

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	statsAny, err := got.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats on fresh optimizer must not fail: %v", err)
	}
	_ = statsAny
}

// TestStartAdaptiveMaintenance_DisabledIsNoop proves the flag gate: with
// ROUTING_OPT_ADAPTIVE_LEARNING unset/false no loop decision is made. The
// function must return immediately without spawning work (no panic with a
// nil-pool optimizer either — cold-start safety).
func TestStartAdaptiveMaintenance_DisabledIsNoop(t *testing.T) {
	t.Setenv("ROUTING_OPT_ADAPTIVE_LEARNING", "")
	optimizer := routingopt.NewRealOptimizer(nil)
	startAdaptiveMaintenance(optimizer, false) // must return, not block
}

// TestRunAdaptiveMaintenance_NilPoolIsQuiet proves one maintenance pass on an
// unwired optimizer degrades to logs (ErrNoRows path) instead of panicking —
// the worker must survive a cold DB.
func TestRunAdaptiveMaintenance_NilPoolIsQuiet(t *testing.T) {
	optimizer := routingopt.NewRealOptimizer(nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	optimizer.RunAdaptiveMaintenance(ctx) // must not panic
}
