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

// Regression: lastN == 0 must return ALL turns (per the contract documented in
// outbound_builder.go), not silently coerce to the default of 10. With 5 turns
// each carrying 2 messages, LoadChain(_, _, 0) must return 10 messages.
func TestTurnReader_LoadChain_LastNZeroReturnsAllTurns(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		mock.Close()
	})

	rows := pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta"})
	for i := 1; i <= 5; i++ {
		req := []byte(`[{"role":"user","content":"q` + itoa(i) + `"}]`)
		resp := []byte(`[{"role":"assistant","content":"a` + itoa(i) + `"}]`)
		rows.AddRow(i, req, resp)
	}
	mock.ExpectQuery(`(?s)SELECT b\.turn_no.*FROM public\.session_bodies_unified b`).
		WithArgs("tenant-1", "session-1").
		WillReturnRows(rows)

	reader := newTurnReader(mock)
	msgs, err := reader.LoadChain(context.Background(), "tenant-1", "session-1", 0)
	require.NoError(t, err)
	require.Len(t, msgs, 10, "expected 5 turns × 2 messages = 10, got %d", len(msgs))
	require.Equal(t, "q1", msgs[0].Content)
	require.Equal(t, "a5", msgs[9].Content)
}

// Regression: lastN < 0 must surface as an explicit error rather than be
// silently coerced to "all turns" or to the legacy default of 10.
func TestTurnReader_LoadChain_LastNNegativeIsError(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(mock.Close)

	reader := newTurnReader(mock)
	_, err = reader.LoadChain(context.Background(), "tenant-1", "session-1", -3)
	require.Error(t, err, "lastN = -3 must error, not silently coerce")
	require.Contains(t, err.Error(), "lastN must be >= 0")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
