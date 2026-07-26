package cache

import (
	"context"
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
