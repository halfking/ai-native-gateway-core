package persist

import (
	"context"
	"fmt"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// ⭐⭐ §10.110.56 的核心事实：Collect 必须**一次性**把所有候选 key 交给批量读，
// 而不是回到「扫一个读一个」的逐 key 循环。
//
// 逐 key 时每个 key 是 TYPE + HGETALL 两次串行往返；~1,800 个 key 就是
// ~3,600~3,800 次往返挤在 30s 预算里，临界延迟只有 7.9ms/往返，而实测往返是
// 8.8~26.1ms（§54）⇒ Collect 必然吃满预算、Flush 只剩几百毫秒、整轮回滚。
func TestCollectReadsAllKeysInOneBatchedCall(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	if err := rdb.HSet(ctx, "ursm:v2:meta:epoch", "counter", "7").Err(); err != nil {
		t.Fatal(err)
	}
	// IDs start at 1: the canonical grammar rejects a 0 credential id, and
	// TestCollectStillExcludesUnparseableKeys covers that rejection separately.
	const n = 40
	for i := 1; i <= n; i++ {
		k := fmt.Sprintf("ursm:v2:node:tenant-a:%d:model-x", i)
		if err := rdb.HSet(ctx, k, map[string]any{
			"available": "1", "credential_id": fmt.Sprint(i), "fail_streak": "1",
		}).Err(); err != nil {
			t.Fatal(err)
		}
	}

	var calls int
	var seen []string
	w := New(rdb, "ursm:v2:", nil).WithHashReader(
		func(_ context.Context, keys []string) (map[string]map[string]string, error) {
			calls++
			seen = append(seen, keys...)
			out := make(map[string]map[string]string, len(keys))
			for _, k := range keys {
				h, err := rdb.HGetAll(ctx, k).Result()
				if err != nil {
					return nil, err
				}
				out[k] = h
			}
			return out, nil
		})

	rows, err := w.Collect(ctx)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(rows) != n {
		t.Fatalf("rows=%d, want %d", len(rows), n)
	}
	if calls != 1 {
		t.Fatalf("the batched reader was called %d times; the per-key loop is back", calls)
	}
	if len(seen) != n {
		t.Fatalf("batched reader saw %d keys, want %d — some keys must be handed over individually", len(seen), n)
	}
}

// The seam must not change what Collect produces: rows, their order and their
// contents must match the default path exactly.
func TestCollectWithInjectedReaderMatchesDefaultPath(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()

	if err := rdb.HSet(ctx, "ursm:v2:meta:epoch", "counter", "3").Err(); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ key string; hash map[string]any }{
		{"ursm:v2:node:tenant-a:7:model-a", map[string]any{"available": "1", "credential_id": "7", "canonical": "m/a"}},
		{"ursm:v2:node:tenant-b:9:model-b", map[string]any{"available": "0", "credential_id": "9", "health": "down"}},
		// A dedup marker must still be skipped before parsing.
		{"ursm:v2:node:tenant-a:7:model-a:request_dedup:abcdef", map[string]any{"available": "1"}},
	} {
		if err := rdb.HSet(ctx, spec.key, spec.hash).Err(); err != nil {
			t.Fatal(err)
		}
	}

	want, err := New(rdb, "ursm:v2:", nil).Collect(ctx)
	if err != nil {
		t.Fatalf("default collect: %v", err)
	}
	got, err := New(rdb, "ursm:v2:", nil).WithHashReader(
		func(c context.Context, keys []string) (map[string]map[string]string, error) {
			return readAllWithClient(c, rdb, keys)
		}).Collect(ctx)
	if err != nil {
		t.Fatalf("injected collect: %v", err)
	}

	if len(want) != len(got) {
		t.Fatalf("row count differs: default=%d injected=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].CredentialID != got[i].CredentialID ||
			want[i].RawModel != got[i].RawModel ||
			want[i].TenantID != got[i].TenantID ||
			want[i].Available != got[i].Available ||
			want[i].HealthStatus != got[i].HealthStatus ||
			want[i].RecoveryEpoch != got[i].RecoveryEpoch {
			t.Fatalf("row %d differs:\n default=%+v\n injected=%+v", i, want[i], got[i])
		}
	}
	// Row order is load-bearing: Flush logs rows[0] as first_cid.
	if len(got) > 1 && got[0].CredentialID == got[1].CredentialID {
		t.Fatalf("rows appear duplicated: %d and %d", got[0].CredentialID, got[1].CredentialID)
	}
}

// A reader failure must keep the pre-§56 contract: the whole cycle errors out
// with zero rows, never a half-populated snapshot.
func TestCollectFailsTheWholeCycleWhenTheBatchReadFails(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	ctx := context.Background()
	if err := rdb.HSet(ctx, "ursm:v2:node:tenant-a:1:m", map[string]any{"available": "1"}).Err(); err != nil {
		t.Fatal(err)
	}

	rows, err := New(rdb, "ursm:v2:", nil).WithHashReader(
		func(context.Context, []string) (map[string]map[string]string, error) {
			return nil, fmt.Errorf("boom")
		}).Collect(ctx)

	if err == nil {
		t.Fatal("expected the cycle to fail")
	}
	if rows != nil {
		t.Fatalf("expected zero rows on a failed batch read, got %d", len(rows))
	}
}

func readAllWithClient(ctx context.Context, rdb *redis.Client, keys []string) (map[string]map[string]string, error) {
	out := make(map[string]map[string]string, len(keys))
	for _, k := range keys {
		h, err := rdb.HGetAll(ctx, k).Result()
		if err != nil {
			return nil, err
		}
		out[k] = h
	}
	return out, nil
}
