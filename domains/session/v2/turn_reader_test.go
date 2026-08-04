package v2

import (
	"context"
	"regexp"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestTurnReader_LoadLatestOutbound_PreservesCompressedSnapshot(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	raw := `[{"role":"assistant","content":"[smm_v1:abc] compressed context"},{"role":"user","content":"next"}]`
	mock.ExpectQuery(regexp.QuoteMeta("SELECT outbound_body")).
		WithArgs("tenant-1", "session-1").
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body"}).AddRow([]byte(raw)))

	reader := newTurnReader(mock)
	msgs, err := reader.LoadLatestOutbound(context.Background(), "tenant-1", "session-1")
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, "[smm_v1:abc] compressed context", msgs[0].Content)
	require.Equal(t, "next", msgs[1].Content)
}

func TestTurnReader_LoadChain_ReconstructsSessionState(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	pool := setupTestDB(t)
	if pool == nil {
		t.Skip("no test pool available")
	}
	defer pool.Close()

	reader := NewTurnReader(pool)
	msgs, err := reader.LoadChain(context.Background(), "default", "gw_test_chain", 10)
	if err != nil {
		t.Fatalf("LoadChain: %v", err)
	}
	_ = msgs
}
