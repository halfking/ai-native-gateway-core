package streaming

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/durabletask"
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/secret"
)

func TestDurableClientProtocolName(t *testing.T) {
	require.Equal(t, "openai_chat", durableClientProtocolName(ProtocolOpenAIChat))
	require.Equal(t, "openai_responses", durableClientProtocolName(ProtocolOpenAIResponses))
	require.Equal(t, "anthropic", durableClientProtocolName(ProtocolAnthropic))
}

func TestDurableResultBodyPrefersCapturedBody(t *testing.T) {
	fg := &durabletask.Foreground{}
	fg.Lease.TaskID = "018f-task"
	attempt := &AttemptResult{ExecResult: &executors.ExecuteResult{ResponseBody: []byte(`{"id":"resp_1"}`)}}
	body, contentType := durableResultBody(attempt, fg, "request-1")
	require.Equal(t, `{"id":"resp_1"}`, string(body))
	require.Equal(t, "application/json", contentType)
}

func TestDurableResultBodyStreamsFallBackToStub(t *testing.T) {
	fg := &durabletask.Foreground{}
	fg.Lease.TaskID = "018f-task"
	body, contentType := durableResultBody(&AttemptResult{}, fg, "request-1")
	require.Contains(t, string(body), `"completed"`)
	require.Contains(t, string(body), "018f-task")
	require.Contains(t, string(body), "request-1")
	require.Equal(t, "application/json", contentType)
}

func TestDurableCheckpointHookMapsStates(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	fg := durabletask.NewForeground(durabletask.NewStore(mock, streamingTestKeyring(t)),
		durabletask.Lease{TaskID: "018f-task", Owner: "gateway/request-1", FencingToken: 1},
		durabletask.ForegroundConfig{})
	hook := durableCheckpointHook(context.Background(), fg)
	require.NotNil(t, hook)

	// Known state maps onto the durable checkpoint write.
	mock.ExpectExec("UPDATE durable_llm_tasks SET commit_state").
		WithArgs("018f-task", "gateway/request-1", int64(1), durabletask.CommitContent, true).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, hook(CommitStateContent))

	// Unknown states fail closed as content.
	mock.ExpectExec("UPDATE durable_llm_tasks SET commit_state").
		WithArgs("018f-task", "gateway/request-1", int64(1), durabletask.CommitContent, true).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	require.NoError(t, hook(CommitState(99)))

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableCheckpointHookFencedOffPropagates(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	fg := durabletask.NewForeground(durabletask.NewStore(mock, streamingTestKeyring(t)),
		durabletask.Lease{TaskID: "018f-task", Owner: "gateway/request-1", FencingToken: 1},
		durabletask.ForegroundConfig{})
	hook := durableCheckpointHook(context.Background(), fg)

	mock.ExpectExec("UPDATE durable_llm_tasks SET commit_state").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	require.ErrorIs(t, hook(CommitStateContent), durabletask.ErrLeaseLost)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDurableCheckpointHookNilWithoutForeground(t *testing.T) {
	require.Nil(t, durableCheckpointHook(context.Background(), nil))
}

// streamingTestKeyring builds a throwaway AES-GCM keyring (the streaming
// package cannot reach durabletask's test helpers).
func streamingTestKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	var key [32]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	kr, err := secret.NewKeyring(map[string][32]byte{"current": key}, "current")
	require.NoError(t, err)
	return kr
}
