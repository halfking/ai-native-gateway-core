package persist

// section_14344_seed_signature_skip_test.go —— §143.44 选项 A 的门：
// writer 必须跳过「**种子签名**」的行，且**不得**误伤任何真实观测。
//
// ★ 判据的由来（2026-10-21 两实例实测推翻了字面版本）：
//
//	**不是**「租户为空」，而是「generation == 1 ∧ updated_at_ms == 0」。
//	252（20261020 分区）：空租户组 897,036 行 **100%** 命中该签名；
//	                      而 `default` 组里 updated_at_ms=0 的行 = **0**。
//	211（6,070,457 行）：租户分布**只有 default**，空租户行 = **0**；
//	                      gen1 = 19.2% 但 updated_at_ms=0 的行 = **0**
//	                      ⇒ 211 的那些行**已被真实观测覆盖** ⇒ 有信息 ⇒ 不能跳。
//
// ⇒ ⇒ **零误伤**是本门的第一要务：真实观测的 updated_at_ms 恒 > 0。

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// seedRowCount 采集一轮，返回落行数。
func seedRowCount(t *testing.T, mr *miniredis.Miniredis, rdb *redis.Client, seeds map[string]map[string]string) int {
	t.Helper()
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:meta:epoch", "counter", "7").Err(); err != nil {
		t.Fatalf("seed epoch: %v", err)
	}
	for key, fields := range seeds {
		if err := rdb.HSet(ctx, key, fields).Err(); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}
	rows, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return len(rows)
}

func TestSection14344SeedSignatureRowsAreSkipped(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	// 退化种子：generation=1 ∧ updated_at_ms=0（= fpSlots 迁移铺出来的形态）。
	n := seedRowCount(t, mr, rdb, map[string]map[string]string{
		"ursm:v2:node:42:model-a": {
			"available": "1", "generation": "1", "updated_at_ms": "0",
			"source_priority": "10", "success_count": "3",
		},
	})
	if n != 0 {
		t.Fatalf("种子签名行落了 %d 行，期望 0 —— 退化种子不携带任何观测信息", n)
	}
}

func TestSection14344RealObservationsAreNeverSkipped(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		hash  map[string]string
		wantN int
	}{
		{
			name: "generation=1 但 updated_at_ms>0（种子已被真实观测覆盖 ⇒ 有信息）",
			key:  "ursm:v2:node:43:model-b",
			hash: map[string]string{
				"available": "1", "generation": "1", "updated_at_ms": "1750000000000",
			},
			wantN: 1,
		},
		{
			name: "updated_at_ms=0 但 generation≠1（不是种子签名）",
			key:  "ursm:v2:node:44:model-c",
			hash: map[string]string{
				"available": "1", "generation": "2", "updated_at_ms": "0",
			},
			wantN: 1,
		},
		{
			name: "无租户部署形态：key 无租户但 hash 有真实观测（必须保留）",
			key:  "ursm:v2:node:45:model-d",
			hash: map[string]string{
				"available": "1", "generation": "3", "updated_at_ms": "1750000000001",
			},
			wantN: 1,
		},
		{
			name: "hash 无 generation/updated_at_ms（字段缺失 ⇒ 不是种子签名）",
			key:  "ursm:v2:node:46:model-e",
			hash: map[string]string{
				"available": "1",
			},
			wantN: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mr := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer rdb.Close()
			n := seedRowCount(t, mr, rdb, map[string]map[string]string{tc.key: tc.hash})
			if n != tc.wantN {
				t.Fatalf("落行 %d 行，期望 %d —— 真实观测绝不能被当成退化种子丢掉", n, tc.wantN)
			}
		})
	}
}

// TestSection14344SeedSignatureNeedsBothConditions 钉住「两个条件缺一不可」。
//
// ★ 只判 generation==1 会误伤「种子已被真实观测覆盖」的行（211 上有 19.2% 是这种）；
//
//	只判 updated_at_ms==0 会误伤「字段缺失的早期节点」。
func TestSection14344SeedSignatureNeedsBothConditions(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	n := seedRowCount(t, mr, rdb, map[string]map[string]string{
		// 只满足 generation==1
		"ursm:v2:node:47:only-gen": {
			"available": "1", "generation": "1", "updated_at_ms": "1750000000002",
		},
		// 只满足 updated_at_ms==0
		"ursm:v2:node:48:only-upd": {
			"available": "1", "generation": "4", "updated_at_ms": "0",
		},
	})
	if n != 2 {
		t.Fatalf("落行 %d 行，期望 2 —— 种子签名必须同时满足两个条件", n)
	}
}
