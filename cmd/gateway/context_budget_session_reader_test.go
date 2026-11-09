package main

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStrictBoundedOutboundReaderSuppressesOversizedLatest(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	count, tokens := 4, 1000
	mock.ExpectQuery(`(?s)WITH bounded_latest.*LIMIT 1.*CASE WHEN.*octet_length.*oversized`).
		WithArgs("session", "tenant", 1<<20, 256<<10).
		WillReturnRows(pgxmock.NewRows([]string{"body", "count", "tokens", "hashes", "meta", "oversized"}).AddRow(nil, &count, &tokens, nil, nil, true))
	row, err := readBoundedLastOutbound(context.Background(), mock, "tenant", "session", 1<<20, 256<<10)
	require.Error(t, err)
	require.Contains(t, err.Error(), "bounded recovery capacity")
	require.Nil(t, row)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStrictBoundedOutboundReaderKeepsSafeExactBody(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"safe"}]}`)
	count, tokens := 1, 10
	mock.ExpectQuery(`(?s)WITH bounded_latest.*CASE WHEN`).
		WithArgs("session", "tenant", 1<<20, 256<<10).
		WillReturnRows(pgxmock.NewRows([]string{"body", "count", "tokens", "hashes", "meta", "oversized"}).AddRow(body, &count, &tokens, []byte(`[]`), []byte(`{}`), false))
	row, err := readBoundedLastOutbound(context.Background(), mock, "tenant", "session", 1<<20, 256<<10)
	require.NoError(t, err)
	require.Equal(t, string(body), string(row.OutboundBody))
	require.NoError(t, mock.ExpectationsWereMet())
}
