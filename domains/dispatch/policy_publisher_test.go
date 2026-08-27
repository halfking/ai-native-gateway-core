package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestParseCredentialsRevision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    uint64
		ok      bool
	}{
		{"valid", "42", 42, true},
		{"trimmed", " 7 ", 7, true},
		{"empty", "", 0, false},
		{"zero", "0", 0, false},
		{"invalid", "abc", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCredentialsRevision(tc.payload)
			if tc.ok && err != nil {
				t.Fatalf("parse error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected parse error")
			}
			if got != tc.want {
				t.Fatalf("revision = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPolicyPublisherStopBeforeStartIsSafe(t *testing.T) {
	p := NewPolicyPublisher(nil, nil)
	p.Stop()
	p.Stop()
}

func TestPolicyPublisherStartWithoutDependenciesIsNoop(t *testing.T) {
	p := NewPolicyPublisher(nil, nil)
	p.Start(context.Background())
	p.Stop()
}

func TestSleepContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleepContext(ctx, time.Hour) {
		t.Fatal("sleepContext returned true after cancellation")
	}
}

func newMockPolicyPublisher(t *testing.T) (*PolicyPublisher, pgxmock.PgxPoolIface, *Pipeline) {
	t.Helper()

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("pgxmock: %v", err)
	}
	t.Cleanup(func() { mock.Close() })
	pipeline := NewPipeline(Deps{})
	t.Cleanup(pipeline.Stop)
	return &PolicyPublisher{queryer: mock, pipeline: pipeline}, mock, pipeline
}

func policyPublisherRows(revision int64, limit int) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "provider_id", "concurrency_mode", "concurrency_limit", "rpm_limit", "tpm_limit",
		"max_queue_depth", "max_queue_wait_ms", "revision",
	}).AddRow(int64(101), int64(3), ModeConcurrency, limit, 0, 0, 0, 0, revision)
}

func TestPolicyPublisherPublishCatchUpAppliesPolicyThroughPipeline(t *testing.T) {
	publisher, mock, pipeline := newMockPolicyPublisher(t)
	backend := &fakeBackend{kind: BackendLocal, name: "local"}
	pipeline.SetGovernorBackend(backend)

	forwarder := pipeline.getOrCreateForwarder(cred(101, ModeConcurrency, 2))
	original, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("initial governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}
	mock.ExpectQuery(`SELECT id, provider_id`).
		WithArgs(uint64(0)).
		WillReturnRows(policyPublisherRows(9, 12))

	if err := publisher.publishCatchUp(context.Background()); err != nil {
		t.Fatalf("publishCatchUp: %v", err)
	}
	if got := publisher.lastRevision.Load(); got != 9 {
		t.Fatalf("last revision = %d, want 9", got)
	}
	if got := pipeline.ActiveRevision(); got != 9 {
		t.Fatalf("active revision = %d, want 9", got)
	}
	updated, ok := forwarder.govLocked().(*concurrencyGovernor)
	if !ok {
		t.Fatalf("updated governor type = %T, want *concurrencyGovernor", forwarder.govLocked())
	}
	if updated == original || updated.cap != 12 {
		t.Fatalf("policy was not applied to live governor: got %p cap=%d, want replacement cap=12", updated, updated.cap)
	}
	if got := len(backend.notifs); got != 0 {
		t.Fatalf("local backend NotifyRevisions calls = %d, want 0", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}

func TestPolicyPublisherPublishCatchUpApplyFailureLeavesLastRevisionUnchanged(t *testing.T) {
	publisher, mock, pipeline := newMockPolicyPublisher(t)
	if err := pipeline.ApplyPolicy(context.Background(), GovernorPolicy{Revision: 4}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	publisher.lastRevision.Store(4)

	want := errors.New("redis unavailable")
	pipeline.SetGovernorBackend(&fakeBackend{
		kind:      BackendRedisEnforce,
		name:      "redis",
		notifyErr: want,
	})
	mock.ExpectQuery(`SELECT id, provider_id`).
		WithArgs(uint64(4)).
		WillReturnRows(policyPublisherRows(5, 15))

	err := publisher.publishCatchUp(context.Background())
	if !errors.Is(err, want) {
		t.Fatalf("publishCatchUp error = %v, want %v", err, want)
	}
	if got := publisher.lastRevision.Load(); got != 4 {
		t.Fatalf("last revision after failure = %d, want 4", got)
	}
	if got := pipeline.ActiveRevision(); got != 4 {
		t.Fatalf("active revision after failure = %d, want 4", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("query expectations: %v", err)
	}
}
