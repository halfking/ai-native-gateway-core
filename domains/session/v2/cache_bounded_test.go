package v2

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStrictGovernanceReadBoundsValuesBeforeHGetAll(t *testing.T) {
	server := miniredis.RunT(t)
	cache := NewRedisGovernanceCache(server.Addr(), time.Minute, 0)
	t.Cleanup(func() { _ = cache.Close() })
	require.NoError(t, cache.client.HSet(context.Background(), redisKeyV2("tenant", "session"), "injection_verdict", strings.Repeat("x", (256<<10)+1)).Err())
	meta, err := cache.Get(boundedTurnContext(), "tenant", "session")
	require.Error(t, err)
	require.Nil(t, meta)
}

func TestStrictFileCacheRejectsBeforeReadingOversizedSnapshot(t *testing.T) {
	fc, err := NewFileCache(filepath.Join(t.TempDir(), "cache"), time.Hour, 128<<20)
	require.NoError(t, err)
	require.NoError(t, fc.Set(newTestFileState("tenant", "session", 1)))
	// Leave a stale small index entry, then enlarge the actual file.
	file, err := os.OpenFile(fc.buildPath("tenant", "session"), os.O_WRONLY, 0600)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(64<<20))
	require.NoError(t, file.Close())
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = fc.GetContext(boundedTurnContext(), "tenant", "session")
	runtime.ReadMemStats(&after)
	require.Error(t, err)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20), "oversized snapshot was materialized before rejection")
}

func TestStrictLoadStateBoundsChosenMarkerAfterSelectingIt(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`(?s)WITH bounded_state.*WITH latest AS.*marker_turn AS.*CASE WHEN.*octet_length.*oversized`).
		WithArgs("tenant", "session", 256<<10).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no", "ts", "strategy", "meta", "prompt", "completion", "injection", "output", "oversized"}).
			AddRow(4, time.Now(), "", nil, 0, 0, "skip", "skip", true))
	state, err := newSessionTurnsReader(mock).LoadState(boundedTurnContext(), "tenant", "session")
	require.Error(t, err)
	require.Nil(t, state)
	require.NoError(t, mock.ExpectationsWereMet(), "oversized marker must not fall back to an older marker")
}
