package telemetry

import (
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestUpdateRequestLog_SessionsV2DefaultsToFullBodies(t *testing.T) {
	withSessionsV2Settings(t, map[string]bool{"sessions_v2.enabled": true})
	requestBody := longBodyForSummary("DEFAULT-FULL-REQUEST")
	responseBody := longBodyForSummary("DEFAULT-FULL-RESPONSE")

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-default-full", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	status := RequestStatusSuccess
	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(requestLogUpdateArgs(RequestLogEntry{Success: true, RequestStatus: &status})...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(), // tenant_id
			fullBodyMatcher{want: requestBody},
			fullBodyMatcher{want: responseBody},
			pgxmock.AnyArg(), // outbound_body (Phase 1)
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-default-full",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		RequestBody:   &requestBody,
		ResponseBody:  &responseBody,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestUpdateRequestLog_LegacyFalseOverrideStillKeepsFullBodies(t *testing.T) {
	withSessionsV2Settings(t, map[string]bool{
		"sessions_v2.enabled":             true,
		"sessions_v2.request_bodies_full": false,
	})
	requestBody := longBodyForSummary("LEGACY-FALSE-REQUEST")
	responseBody := longBodyForSummary("LEGACY-FALSE-RESPONSE")

	mockDB, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mockDB.Close()

	mockDB.ExpectBegin()
	mockDB.ExpectExec(`UPDATE usage_ledger_hot`).
		WithArgs("req-legacy-false", pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	status := RequestStatusSuccess
	mockDB.ExpectExec(`UPDATE request_logs_hot`).
		WithArgs(requestLogUpdateArgs(RequestLogEntry{Success: true, RequestStatus: &status})...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_logs_bodies_hot`).
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(), // tenant_id
			fullBodyMatcher{want: requestBody},
			fullBodyMatcher{want: responseBody},
			pgxmock.AnyArg(), // outbound_body (Phase 1)
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mockDB.ExpectCommit()

	client := &Client{requestLogDB: mockDB}
	err = client.updateRequestLog(&RequestLogEntry{
		RequestID:     "req-legacy-false",
		Op:            RequestLogUpdate,
		Success:       true,
		RequestStatus: &status,
		RequestBody:   &requestBody,
		ResponseBody:  &responseBody,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}
