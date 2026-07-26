package telemetry

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type jsonTextArgument struct {
	want string
}

func (a jsonTextArgument) Match(value interface{}) bool {
	actual, ok := value.(string)
	return ok && actual == a.want
}

func TestPersistUpdateInTx_BindsCompressionMetaAsJSONText(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	requestID := "request-json-text"
	compressionMeta := `{"msg_count":5}`
	mockDB.ExpectExec(`UPDATE request_wal_hot SET`).
		WithArgs(
			requestID,
			"success",
			StageCompleted,
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"delta_append",
			jsonTextArgument{want: compressionMeta},
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	rl := &RequestLogger{}
	err = rl.persistUpdateInTx(context.Background(), mockDB, &LogUpdate{
		RequestID:           requestID,
		Stage:               StageCompleted,
		Status:              StatusSuccess,
		CompressionStrategy: "delta_append",
		CompressionMeta:     map[string]interface{}{"msg_count": 5},
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestPersistUpdateInTx_BindsNilCompressionMetaAsJSONText(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	requestID := "request-json-null"
	mockDB.ExpectExec(`UPDATE request_wal_hot SET`).
		WithArgs(
			requestID,
			"failure",
			StageExecuteFail,
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			"",
			jsonTextArgument{want: "null"},
		).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	rl := &RequestLogger{}
	err = rl.persistUpdateInTx(context.Background(), mockDB, &LogUpdate{
		RequestID: requestID,
		Stage:     StageExecuteFail,
		Status:    StatusFailure,
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}

func TestPersistUpdateInTx_BindsBodyCompressionMetaAsJSONText(t *testing.T) {
	mockDB, err := pgxmock.NewConn()
	require.NoError(t, err)
	defer mockDB.Close(context.Background())

	requestID := "request-json-body"
	compressionMeta := `{"strategy":"delta_append"}`
	anyUpdateArgs := []interface{}{
		requestID,
		"success",
		StageCompleted,
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		"delta_append",
		jsonTextArgument{want: compressionMeta},
	}
	mockDB.ExpectExec(`UPDATE request_wal_hot SET`).
		WithArgs(anyUpdateArgs...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mockDB.ExpectExec(`INSERT INTO request_wal_bodies`).
		WithArgs(requestID, []byte("body"), jsonTextArgument{want: compressionMeta}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	rl := &RequestLogger{}
	err = rl.persistUpdateInTx(context.Background(), mockDB, &LogUpdate{
		RequestID:           requestID,
		Stage:               StageCompleted,
		Status:              StatusSuccess,
		OutboundBody:        []byte("body"),
		CompressionStrategy: "delta_append",
		CompressionMeta:     map[string]interface{}{"strategy": "delta_append"},
	})
	require.NoError(t, err)
	require.NoError(t, mockDB.ExpectationsWereMet())
}
