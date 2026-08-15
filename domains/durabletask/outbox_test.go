package durabletask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/pending"
	"github.com/kaixuan/llm-gateway-go/secret"
)

// fakeProjector records ProjectCAS calls.
type fakeProjector struct {
	applied bool
	err     error
	calls   []*pending.Response
}

func (f *fakeProjector) ProjectCAS(_ context.Context, r *pending.Response) (bool, error) {
	f.calls = append(f.calls, r)
	return f.applied, f.err
}

// outboxClaimColumns mirrors the ClaimOutbox SELECT in store.go.
func outboxClaimColumns() []string {
	return []string{"id", "task_id", "tenant_id", "request_id", "session_id", "projection_status",
		"fencing_token", "result_version", "result_hash", "attempt_count", "created_at",
		"reason_code", "request_hash", "result_ciphertext", "content_type"}
}

func expectOutboxClaim(mock pgxmock.PgxPoolIface, item OutboxItem) {
	expectBypassBegin(mock)
	mock.ExpectQuery("WITH picked AS").
		WithArgs(100, "outbox-1", pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(outboxClaimColumns()).AddRow(
			item.ID, item.TaskID, item.TenantID, item.RequestID, item.SessionID, string(item.Status),
			item.FencingToken, item.ResultVersion, item.ResultHash, item.AttemptCount, item.CreatedAt,
			item.ReasonCode, item.RequestHash, item.ResultCiphertext, item.ContentType))
	mock.ExpectCommit()
}

func expectDelivered(mock pgxmock.PgxPoolIface, id int64) {
	expectBypassBegin(mock)
	mock.ExpectExec("UPDATE durable_pending_outbox SET status='delivered'").
		WithArgs(id, "outbox-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
}

func expectOutboxFailed(mock pgxmock.PgxPoolIface, id int64) {
	expectBypassBegin(mock)
	mock.ExpectExec("UPDATE durable_pending_outbox SET status='failed'").
		WithArgs(id, "outbox-1", pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
}

func TestOutboxDeliverCompletedResult(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	body := []byte(`{"id":"resp_1","output":[]}`)
	sum := sha256Hex(body)
	ciphertext, _, err := secret.EncryptWithAAD(body, kr, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: "tenant-text-id", TaskID: "018f-task", RequestHash: "sha256:request"})
	require.NoError(t, err)

	item := OutboxItem{ID: 7, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusCompleted, FencingToken: 3, ResultVersion: 2,
		ResultHash: sum, RequestHash: "sha256:request", ResultCiphertext: ciphertext,
		ContentType: "application/json", AttemptCount: 1, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	expectDelivered(mock, 7)

	projector := &fakeProjector{applied: true}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())

	require.Len(t, projector.calls, 1)
	got := projector.calls[0]
	require.Equal(t, pending.StatusCompleted, got.Status)
	require.Equal(t, string(body), got.Body)
	require.Equal(t, "018f-task", got.TaskID)
	require.Equal(t, int64(2), got.ResultVersion)
	require.True(t, got.Durable)
	require.Equal(t, int64(3), got.FencingToken)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxDeliverFailureStatusWithoutBody(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	item := OutboxItem{ID: 9, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusExpired, ReasonCode: ReasonSurvivalExpired,
		FencingToken: 5, ResultVersion: 1, ResultHash: "", RequestHash: "sha256:request",
		ResultCiphertext: "", ContentType: "", AttemptCount: 2, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	expectDelivered(mock, 9)

	projector := &fakeProjector{applied: true}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())

	require.Len(t, projector.calls, 1)
	got := projector.calls[0]
	require.Equal(t, pending.StatusFailed, got.Status)
	require.Contains(t, got.ErrorMessage, string(StatusExpired))
	require.Contains(t, got.ErrorMessage, ReasonSurvivalExpired)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxDeliverUndecryptableResultFailsClosed(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	item := OutboxItem{ID: 11, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusCompleted, FencingToken: 3, ResultVersion: 2,
		ResultHash: "deadbeef", RequestHash: "sha256:request",
		ResultCiphertext: "v2|llm-gateway:durable-result:v1|current|AAAA",
		ContentType:      "application/json", AttemptCount: 1, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	// Not delivered: parked as failed with backoff instead.
	expectOutboxFailed(mock, 11)

	projector := &fakeProjector{applied: true}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())

	require.Empty(t, projector.calls, "undecryptable result must never reach the PendingStore")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxDeliverResultHashMismatchFailsClosed(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	body := []byte(`{"tampered":true}`)
	ciphertext, _, err := secret.EncryptWithAAD(body, kr, secret.AADDomainDurableResult,
		secret.AADBinding{TenantID: "tenant-text-id", TaskID: "018f-task", RequestHash: "sha256:request"})
	require.NoError(t, err)

	item := OutboxItem{ID: 12, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusCompleted, FencingToken: 3, ResultVersion: 2,
		ResultHash:  "0000000000000000000000000000000000000000000000000000000000000000",
		RequestHash: "sha256:request", ResultCiphertext: ciphertext,
		ContentType: "application/json", AttemptCount: 1, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	expectOutboxFailed(mock, 12)

	projector := &fakeProjector{applied: true}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())

	require.Empty(t, projector.calls, "hash-mismatched result must never reach the PendingStore")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxDeliverProjectCASErrorBacksOff(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	item := OutboxItem{ID: 13, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusExpired, ReasonCode: ReasonSurvivalExpired,
		FencingToken: 5, ResultVersion: 1, RequestHash: "sha256:request", AttemptCount: 3, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	expectOutboxFailed(mock, 13)

	projector := &fakeProjector{err: context.DeadlineExceeded}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())

	require.Len(t, projector.calls, 1)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxDeliverSupersededProjectionStillMarksDelivered(t *testing.T) {
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	require.NoError(t, err)
	defer mock.Close()
	kr := testKeyring(t)

	item := OutboxItem{ID: 14, TaskID: "018f-task", TenantID: "tenant-text-id", RequestID: "request-1",
		SessionID: "session-1", Status: StatusExpired, ReasonCode: ReasonSurvivalExpired,
		FencingToken: 5, ResultVersion: 1, RequestHash: "sha256:request", AttemptCount: 1, CreatedAt: time.Now()}
	expectOutboxClaim(mock, item)
	expectDelivered(mock, 14)

	// applied=false: a same-or-newer projection already exists — idempotent.
	projector := &fakeProjector{applied: false}
	deliverer := NewOutboxDeliverer(NewStore(mock, kr), kr, projector, OutboxDelivererConfig{Owner: "outbox-1"})
	deliverer.DeliverOnce(context.Background())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestOutboxBackoffIsBounded(t *testing.T) {
	cfg := OutboxDelivererConfig{Owner: "x", BaseBackoff: time.Second, MaxBackoff: 5 * time.Minute}.withDefaults()
	require.Equal(t, time.Second, cfg.BaseBackoff)
	require.Equal(t, 5*time.Minute, cfg.MaxBackoff)
	// The shift cap keeps delay bounded for any attempt count.
	deliverer := &OutboxDeliverer{cfg: cfg}
	for _, attempt := range []int{0, 1, 5, 20, 100} {
		delay := deliverer.backoff(attempt)
		require.LessOrEqual(t, delay, cfg.MaxBackoff)
		require.GreaterOrEqual(t, delay, cfg.BaseBackoff)
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
