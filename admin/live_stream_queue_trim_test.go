package admin

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestSelectiveTrimLiveStreamQueue_EvictsCompletedBeforeFreshInProgress(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	key := liveStreamDimPrefix + "model:test-model"
	now := time.Now().UTC()

	doneMember, err := marshalTileSlim(LiveStreamTile{
		RequestID: "done-1",
		Timestamp: now.Add(-30 * time.Minute).Format(time.RFC3339),
		Status:    "success",
	})
	require.NoError(t, err)
	freshMember, err := marshalTileSlim(LiveStreamTile{
		RequestID: "fresh-1",
		Timestamp: now.Add(-5 * time.Minute).Format(time.RFC3339),
		Status:    "in_progress",
	})
	require.NoError(t, err)
	staleMember, err := marshalTileSlim(LiveStreamTile{
		RequestID: "stale-1",
		Timestamp: now.Add(-3 * time.Hour).Format(time.RFC3339),
		Status:    "in_progress",
	})
	require.NoError(t, err)

	liveStreamInflightProtectDuration = 2 * time.Hour
	t.Cleanup(func() { liveStreamInflightProtectDuration = 2 * time.Hour })

	require.NoError(t, rdb.ZAdd(ctx, key,
		redis.Z{Score: float64(now.Add(-30 * time.Minute).UnixMilli()), Member: doneMember},
		redis.Z{Score: float64(now.Add(-3 * time.Hour).UnixMilli()), Member: staleMember},
		redis.Z{Score: float64(now.Add(-5 * time.Minute).UnixMilli()), Member: freshMember},
	).Err())

	require.NoError(t, selectiveTrimLiveStreamQueue(ctx, rdb, key, 1))

	remaining, err := rdb.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	require.Equal(t, freshMember, remaining[0])
}

func TestSelectiveTrimLiveStreamQueue_ProtectsFreshInProgressWhenAllProtected(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()

	key := liveStreamDimPrefix + "vendor:openai"
	now := time.Now().UTC()
	liveStreamInflightProtectDuration = 2 * time.Hour
	t.Cleanup(func() { liveStreamInflightProtectDuration = 2 * time.Hour })

	members := make([]redis.Z, 0, 3)
	for i, id := range []string{"a", "b", "c"} {
		slim, err := marshalTileSlim(LiveStreamTile{
			RequestID: id,
			Timestamp: now.Add(-time.Duration(i+1) * time.Minute).Format(time.RFC3339),
			Status:    "in_progress",
		})
		require.NoError(t, err)
		members = append(members, redis.Z{
			Score:  float64(now.Add(-time.Duration(i+1) * time.Minute).UnixMilli()),
			Member: slim,
		})
	}
	require.NoError(t, rdb.ZAdd(ctx, key, members...).Err())

	require.NoError(t, selectiveTrimLiveStreamQueue(ctx, rdb, key, 1))

	count, err := rdb.ZCard(ctx, key).Result()
	require.NoError(t, err)
	require.Equal(t, int64(3), count)
}

func TestSelectiveTrimLiveStreamQueue_MainQueueUsesDetailStatus(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx := context.Background()
	now := time.Now().UTC()
	liveStreamInflightProtectDuration = 2 * time.Hour
	t.Cleanup(func() { liveStreamInflightProtectDuration = 2 * time.Hour })

	writeDetail := func(id, status string, ts time.Time) {
		payload, err := marshalLiveRequestRedisPayload(LiveRequest{
			RequestID: id,
			Ts:        ts.UTC().Format(time.RFC3339),
			Status:    status,
		})
		require.NoError(t, err)
		require.NoError(t, rdb.Set(ctx, liveStreamGlobalRequestDetailKey(id), payload, liveStreamTTL).Err())
	}

	writeDetail("done-main", "success", now.Add(-40*time.Minute))
	writeDetail("fresh-main", "in_progress", now.Add(-3*time.Minute))

	key := liveStreamMainKey
	require.NoError(t, rdb.ZAdd(ctx, key,
		redis.Z{Score: float64(now.Add(-40 * time.Minute).UnixMilli()), Member: "done-main"},
		redis.Z{Score: float64(now.Add(-3 * time.Minute).UnixMilli()), Member: "fresh-main"},
	).Err())

	require.NoError(t, selectiveTrimLiveStreamQueue(ctx, rdb, key, 1))

	remaining, err := rdb.ZRange(ctx, key, 0, -1).Result()
	require.NoError(t, err)
	require.Equal(t, []string{"fresh-main"}, remaining)
}
