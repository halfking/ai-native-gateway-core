package v2

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/contextbudget"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStrictChainRejectsCumulativeBytesEvenIfBackendFlagIsWrong(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	part := []byte(`[{"role":"user","content":"` + strings.Repeat("x", 600000) + `"}]`)
	mock.ExpectQuery(`(?s)cumulative_bytes <= \$4`).WithArgs("t", "s", 3, 1<<20).
		WillReturnRows(pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta", "oversized"}).
			AddRow(1, part, nil, false).AddRow(2, part, nil, false))
	messages, err := newTurnReader(mock).LoadChain(boundedTurnContext(), "t", "s", 3)
	require.Error(t, err)
	require.Nil(t, messages)
	require.NoError(t, mock.ExpectationsWereMet())
}

func boundedTurnContext() context.Context {
	return contextbudget.WithSpec(context.Background(), contextbudget.Spec{ContextWindow: 1000000})
}

func TestStrictLatestSelectsNewestBeforeBounding(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`(?s)WITH latest AS \(.*ORDER BY ts DESC.*LIMIT 1.*CASE WHEN.*octet_length.*THEN outbound_body.*oversized`).
		WithArgs("t", "s", 1<<20).
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body", "oversized"}).AddRow(nil, true))
	msgs, err := newTurnReader(mock).LoadLatestOutbound(boundedTurnContext(), "t", "s")
	require.Error(t, err)
	require.Nil(t, msgs)
	require.NoError(t, mock.ExpectationsWereMet(), "oversized latest must not query an older snapshot")
}

func TestStrictLatestMissUsesBoundedLegacyQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`(?s)WITH latest AS.*kind = 'final_full'.*CASE WHEN`).WithArgs("t", "s", 1<<20).WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`(?s)WITH latest AS.*ORDER BY turn_no DESC, ts DESC.*LIMIT 1.*CASE WHEN`).WithArgs("t", "s", 1<<20).
		WillReturnRows(pgxmock.NewRows([]string{"outbound_body", "oversized"}).AddRow([]byte(`[{"role":"user","content":"newest"}]`), false))
	msgs, err := newTurnReader(mock).LoadLatestOutbound(boundedTurnContext(), "t", "s")
	require.NoError(t, err)
	require.Equal(t, "newest", msgs[0].Content)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStrictChainLimitsInSQLAndBoundsCombinedDeltas(t *testing.T) {
	for _, lastN := range []int{0, 3, 100000} {
		t.Run(itoa(lastN), func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			t.Cleanup(mock.Close)
			wantLimit := 10
			if lastN > 0 && lastN < wantLimit {
				wantLimit = lastN
			}
			mock.ExpectQuery(`(?s)WITH recent AS.*ORDER BY.*turn_no DESC.*LIMIT \$3.*CASE WHEN.*octet_length.*ORDER BY turn_no ASC`).
				WithArgs("t", "s", wantLimit, 1<<20).
				WillReturnRows(pgxmock.NewRows([]string{"turn_no", "request_delta", "response_delta", "oversized"}).AddRow(2, nil, nil, true))
			msgs, err := newTurnReader(mock).LoadChain(boundedTurnContext(), "t", "s", lastN)
			require.Error(t, err)
			require.Nil(t, msgs)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
