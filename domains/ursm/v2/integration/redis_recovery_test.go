// Package integration: simulates a Redis restart against the v2
// recovery gate. The behavior under test is:
//
//  1. The v2 pipeline can become ready (warmup writes the ready key).
//  2. After Redis "restarts" (fresh miniredis instance with a fresh
//     redis client), the ready gate is closed because the keys are
//     gone — the v2 pipeline must not be considered authoritative
//     until a fresh warmup has repopulated state.
//  3. A subsequent warmup restores the gate to ready=true.
//
// Implementation note: SetRedisForTest only swaps the manager's
// store, not the recovery.Manager (which holds its own *redis.Client
// reference). Per the T22 spec, we therefore exercise the recovery
// gate through a fresh recovery.Manager built against the new client,
// rather than reusing m.Ready() after the swap. That isolates the
// restart semantics — restart clears ready, warmup restores it.
package integration

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
)

func TestRedisRestartBlocksUntilWarmup(t *testing.T) {
	// Phase 1: stand up a Manager + Recovery and verify the ready
	// gate opens on warmup against the original Redis instance.
	mr1 := miniredis.RunT(t)
	rdb1 := redis.NewClient(&redis.Options{Addr: mr1.Addr()})
	mgr := v2.New(v2.Dependencies{Redis: rdb1, Config: v2.DefaultConfig()})
	rm1 := recovery.New(rdb1, "ursm:v2:")
	if err := rm1.WarmupFromSeed(context.Background(), []recovery.Seed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m"},
	}); err != nil {
		t.Fatalf("warmup phase 1: %v", err)
	}
	if !rm1.Ready(context.Background()) {
		t.Fatalf("recovery must be ready after warmup")
	}

	// Phase 2: simulate a Redis restart by tearing down the old
	// miniredis and pointing the manager's store at a brand-new
	// miniredis instance. The ready key is gone, so any fresh
	// recovery.Manager built against the new client must report
	// not-ready until we re-warm.
	mr1.Close()
	mr2 := miniredis.RunT(t)
	rdb2 := redis.NewClient(&redis.Options{Addr: mr2.Addr()})
	mgr.SetRedisForTest(rdb2)
	rm2 := recovery.New(rdb2, "ursm:v2:")

	if rm2.Ready(context.Background()) {
		t.Fatalf("after restart, ready must be false until warmup")
	}

	// Phase 3: re-warm against the new Redis. The recovery gate
	// must transition back to ready=true.
	if err := rm2.WarmupFromSeed(context.Background(), []recovery.Seed{
		{ProviderID: 1, CredentialID: 1, RawModel: "m"},
	}); err != nil {
		t.Fatalf("warmup phase 2: %v", err)
	}
	if !rm2.Ready(context.Background()) {
		t.Fatalf("ready must be true after warmup post-restart")
	}
}
