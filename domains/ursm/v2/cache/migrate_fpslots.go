package cache

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// migrateIfAbsentScript atomically seeds a v2 node Hash ONLY when its
// `generation` field does not yet exist. It takes the node key plus an
// even-length list of field/value pairs (the seed), and returns 1 if it
// wrote (this caller won the race) or 0 if the key already had a
// generation (another migration run, or a live record_request, got there
// first).
//
// Why a Lua script instead of HGet→HSet: commit e6945c53 made the migration
// "idempotent" against SEQUENTIAL re-runs, but the HGet/HSet pair is a
// read-then-write across two round-trips. Two gateway pods booting at once
// (or a migration racing apply_decision.lua) can both observe
// generation=="" and both HSet generation=1, clobbering a freshly-written
// generation=5/available=0 from record_request. The script makes the
// check-and-set a single atomic Redis operation, so exactly one writer
// seeds the node and every concurrent contender observes the seed and
// bows out. Keys/argv layout matches KEYS[1]=nodeKey, ARGV[1..]=field
// then value pairs (flat, even count).
var migrateIfAbsentScript = redis.NewScript(`
local exists = redis.call('HEXISTS', KEYS[1], 'generation')
if exists == 1 then
  return 0
end
for i = 1, #ARGV, 2 do
  redis.call('HSET', KEYS[1], ARGV[i], ARGV[i+1])
end
return 1
`)

// fpSlotsNodeState 是 credentialfpslot.NodeState 的迁移端反序列化结构。
// 字段取自 credentialfpslot/node_state.go:33-48(只取要迁移的子集)。
type fpSlotsNodeState struct {
	CredentialID   int    `json:"credential_id"`
	Model          string `json:"model"`
	SuccessCount   int64  `json:"success_count"`
	FailureCount   int64  `json:"failure_count"`
	Disabled       bool   `json:"disabled"`
	DisabledUntil  int64  `json:"disabled_until"`  // unix sec
	DisabledReason string `json:"disabled_reason"` // → ursm disabled_reason
	DisableCount   int    `json:"disable_count"`
	// SlideWindow / LastSuccessAt / LastFailureAt / LastDisabledAt 按设计稿 Decision 1.1 不迁移
}

// MigrateFpSlotsNodeStates 扫描旧 llmgw:cred_fp_node:* key,按设计稿
// Decision 1.1 字段映射表写入 ursm:v2:node:{cid}:{model} Hash。
// 旧 key 不删(7d TTL 自然过期),保证回滚期数据可恢复。
// 返回迁移条数。坏 JSON / credential_id==0 / 空 model 的 key 被跳过(不报错)。
//
// 并发安全:每个 node 的"检查 generation 不存在则写入全部字段"由
// migrateIfAbsentScript 在 Redis 侧原子完成,因此多个 gateway pod 同时启动
// 迁移、或迁移与 live record_request 并发时,只有一个写入者会播种该 node,
// 不会把已有的 generation>1 回退到 1。
func MigrateFpSlotsNodeStates(ctx context.Context, rdb *redis.Client) (int, error) {
	iter := rdb.Scan(ctx, 0, "llmgw:cred_fp_node:*", 100).Iterator()
	n := 0
	for iter.Next(ctx) {
		oldKey := iter.Val()
		data, err := rdb.Get(ctx, oldKey).Bytes()
		if err != nil {
			continue // 跳过坏 key,不中断
		}
		var st fpSlotsNodeState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		if st.CredentialID == 0 || st.Model == "" {
			continue
		}
		nodeKey := fmt.Sprintf("ursm:v2:node:%d:%s", st.CredentialID, st.Model)
		// Build the seed field/value list. available defaults to "1"; a
		// disabled node flips it to "0" and adds cool_until_ms + reason.
		available := "1"
		if st.Disabled {
			available = "0"
		}
		// Flat ARGV pairs (field, value, field, value, ...).
		args := []interface{}{
			"available", available,
			"disabled", boolToStr(st.Disabled),
			"fail_streak", "0",
			"success_count", fmt.Sprintf("%d", st.SuccessCount),
			"failure_count", fmt.Sprintf("%d", st.FailureCount),
			"disable_count", fmt.Sprintf("%d", st.DisableCount),
			"generation", "1",
			"source_priority", "10",
			"updated_at_ms", "0",
		}
		if st.Disabled {
			// DisabledUntil sec → cool_until_ms ms
			args = append(args,
				"cool_until_ms", fmt.Sprintf("%d", st.DisabledUntil*1000),
				"disabled_reason", st.DisabledReason,
			)
		}
		// Atomic check-and-seed. Returns 1 if this caller seeded the node,
		// 0 if it already had a generation (already migrated / live v2 state).
		wrote, err := migrateIfAbsentScript.Run(ctx, rdb, []string{nodeKey}, args...).Int()
		if err != nil {
			return n, fmt.Errorf("cache: migrate %s: %w", oldKey, err)
		}
		if wrote == 1 {
			n++
		}
	}
	return n, iter.Err()
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
