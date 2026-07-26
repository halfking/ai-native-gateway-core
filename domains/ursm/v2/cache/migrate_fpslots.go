package cache

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/redis/go-redis/v9"
)

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
		// 幂等: 若目标 node 已有 generation 字段, 说明已迁移或有 live v2 状态,
		// 不覆盖(避免重启时把 record_request 写的 generation>1 回退到 1)。
		existingGen, err := rdb.HGet(ctx, nodeKey, "generation").Result()
		if err == nil && existingGen != "" {
			continue // 已迁移/live, 跳过, 不计入 n
		}
		fields := map[string]interface{}{
			"available":       "1",
			"disabled":        boolToStr(st.Disabled),
			"fail_streak":     "0",
			"success_count":   fmt.Sprintf("%d", st.SuccessCount),
			"failure_count":   fmt.Sprintf("%d", st.FailureCount),
			"disable_count":   fmt.Sprintf("%d", st.DisableCount),
			"generation":      "1",
			"source_priority": "10",
			"updated_at_ms":   "0",
		}
		if st.Disabled {
			fields["available"] = "0"
			// DisabledUntil sec → cool_until_ms ms
			fields["cool_until_ms"] = fmt.Sprintf("%d", st.DisabledUntil*1000)
			fields["disabled_reason"] = st.DisabledReason
		}
		if err := rdb.HSet(ctx, nodeKey, fields).Err(); err != nil {
			return n, fmt.Errorf("cache: migrate %s: %w", oldKey, err)
		}
		n++
	}
	return n, iter.Err()
}

func boolToStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
