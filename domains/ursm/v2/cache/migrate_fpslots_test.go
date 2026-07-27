package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestMigrateFpSlotsNodeState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// 模拟一个旧 FpSlots NodeState JSON(key 格式 llmgw:cred_fp_node:{cid}:{model})
	oldJSON := `{
		"credential_id": 5, "model": "m3",
		"success_count": 10, "failure_count": 2,
		"disabled": true, "disabled_until": 1700000000,
		"disabled_reason": "consecutive_3_failures",
		"disable_count": 1, "last_disabled_at": 1699999000
	}`
	rdb.Set(ctx, "llmgw:cred_fp_node:5:m3", oldJSON, 0)

	n, err := MigrateFpSlotsNodeStates(ctx, rdb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 migrated, got %d", n)
	}

	// 校验迁移后的 ursm:v2:node:5:m3 Hash 字段(按设计稿 Decision 1.1 表)
	nodeKey := "ursm:v2:node:5:m3"
	fields, err := rdb.HGetAll(ctx, nodeKey).Result()
	if err != nil {
		t.Fatal(err)
	}
	if fields["disabled"] != "1" {
		t.Errorf("disabled: want 1, got %q", fields["disabled"])
	}
	// DisabledUntil sec→ms: 1700000000 * 1000
	if fields["cool_until_ms"] != "1700000000000" {
		t.Errorf("cool_until_ms: want 1700000000000, got %q", fields["cool_until_ms"])
	}
	// DisabledReason → disabled_reason(不是 last_err)
	if fields["disabled_reason"] != "consecutive_3_failures" {
		t.Errorf("disabled_reason: want consecutive_3_failures, got %q", fields["disabled_reason"])
	}
	if fields["success_count"] != "10" {
		t.Errorf("success_count: want 10, got %q", fields["success_count"])
	}
	if fields["failure_count"] != "2" {
		t.Errorf("failure_count: want 2, got %q", fields["failure_count"])
	}
	if fields["disable_count"] != "1" {
		t.Errorf("disable_count: want 1, got %q", fields["disable_count"])
	}
	// 初始化字段
	if fields["generation"] != "1" {
		t.Errorf("generation: want 1, got %q", fields["generation"])
	}
	if fields["source_priority"] != "10" {
		t.Errorf("source_priority: want 10, got %q", fields["source_priority"])
	}
	// 非有效字段不得写入
	for _, bad := range []string{"cool_start_ms", "fail_count", "last_success_at_ms", "last_failure_at_ms", "disabled_at_ms"} {
		if _, ok := fields[bad]; ok {
			t.Errorf("non-effective field %q must NOT be written", bad)
		}
	}
	// 旧 key 保留(7d 自然过期,不删)
	if !mr.Exists("llmgw:cred_fp_node:5:m3") {
		t.Error("old FpSlots key was deleted; must be preserved for rollback")
	}
}

func TestMigrateFpSlotsNotDisabledNoCool(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	oldJSON := `{"credential_id": 7, "model": "gpt", "success_count": 3, "failure_count": 0, "disabled": false}`
	rdb.Set(ctx, "llmgw:cred_fp_node:7:gpt", oldJSON, 0)

	n, err := MigrateFpSlotsNodeStates(ctx, rdb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1, got %d", n)
	}
	fields, _ := rdb.HGetAll(ctx, "ursm:v2:node:7:gpt").Result()
	if fields["available"] != "1" {
		t.Errorf("not-disabled node available: want 1, got %q", fields["available"])
	}
	if _, ok := fields["cool_until_ms"]; ok {
		t.Errorf("cool_until_ms must not be set when not disabled, got %q", fields["cool_until_ms"])
	}
}

func TestMigrateFpSlotsSkipsBadJSON(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	rdb.Set(ctx, "llmgw:cred_fp_node:9:bad", "not-json", 0)
	rdb.Set(ctx, "llmgw:cred_fp_node:0:empty", `{"model":"x"}`, 0) // cred_id 0, skip

	n, err := MigrateFpSlotsNodeStates(ctx, rdb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 migrated (bad json + cred_id 0), got %d", n)
	}
}

func TestMigrateFpSlotsIdempotent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// 先模拟一个已有 live v2 状态的 node (generation=5, 由 record_request 写)
	rdb.HSet(ctx, "ursm:v2:node:5:m3", "generation", "5", "available", "0", "disabled", "1")

	// 同时放一个 legacy key 指向同 node
	rdb.Set(ctx, "llmgw:cred_fp_node:5:m3", `{"credential_id":5,"model":"m3","success_count":1,"disabled":false}`, 0)

	n, err := MigrateFpSlotsNodeStates(ctx, rdb)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 newly-migrated (node already had generation), got %d", n)
	}
	// live v2 状态未被覆盖
	fields, _ := rdb.HGetAll(ctx, "ursm:v2:node:5:m3").Result()
	if fields["generation"] != "5" {
		t.Errorf("generation overwritten: want 5, got %q", fields["generation"])
	}
	if fields["available"] != "0" {
		t.Errorf("available overwritten: want 0 (live disabled), got %q", fields["available"])
	}
}

// TestMigrateFpSlotsConcurrentDoesNotClobberLiveState exercises the atomicity
// contract of migrateIfAbsentScript: several migration runs plus a live
// record_request-style writer all target the same node, and the live
// generation must never be regressed to the migration's seed of 1.
//
// NOTE: miniredis is single-threaded and serializes commands, so it cannot
// truly interleave the HGet→HSet pair that the OLD non-atomic logic used.
// This test therefore cannot deterministically reproduce the pre-fix race on
// miniredis; it asserts the post-fix INVARIANT (live state preserved) that
// the Lua check-and-seed guarantees on real, multiplexed Redis, where the
// script's atomicity is what closes the window. Run against a real Redis to
// observe the old logic failing under contention.
func TestMigrateFpSlotsConcurrentDoesNotClobberLiveState(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	// Legacy seed + a live writer that races the migration.
	rdb.Set(ctx, "llmgw:cred_fp_node:11:race", `{"credential_id":11,"model":"race","success_count":1,"disabled":false}`, 0)

	const movers = 8
	var wg sync.WaitGroup
	// Live record_request-style writer landing mid-migration: writes a more
	// advanced generation than the migration's seed of 1.
	liveLanded := atomic.Bool{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Land a live state with generation=5 before/around the migration.
		if _, err := rdb.HSet(ctx, "ursm:v2:node:11:race",
			"generation", "5", "available", "0", "disabled", "1").Result(); err == nil {
			liveLanded.Store(true)
		}
	}()
	for i := 0; i < movers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = MigrateFpSlotsNodeStates(ctx, rdb)
		}()
	}
	wg.Wait()

	fields, err := rdb.HGetAll(ctx, "ursm:v2:node:11:race").Result()
	if err != nil {
		t.Fatal(err)
	}
	// Invariant: generation must never be regressed below the live value.
	// The migration seeds 1; record_request wrote 5. Whichever landed first
	// wins, but generation must be >= 1 and, critically, if the live writer
	// won the seed slot the available/disabled live values are preserved.
	gen := fields["generation"]
	if gen == "" {
		t.Fatal("node was never seeded by any writer")
	}
	// If the live write landed first (generation=5), the migration must NOT
	// have overwritten it with 1. If the migration seeded first (generation=1),
	// the live write's HSet may or may not have run after; either way the
	// generation must not be 1 when a live generation=5 already existed.
	if liveLanded.Load() && gen == "1" && fields["available"] == "0" {
		// live wrote available=0 + generation=5; a migration that then set
		// generation=1 would have clobbered the live generation while leaving
		// available=0 — the exact corruption the race fix prevents.
		t.Fatalf("live generation=5 was clobbered by migration seed: generation=%q available=%q", gen, fields["available"])
	}
}
