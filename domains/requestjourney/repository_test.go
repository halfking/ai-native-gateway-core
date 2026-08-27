package requestjourney

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

func TestPostgresRepositoryDetailIsTenantScoped(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	mock.ExpectQuery(`SELECT[\s\S]+FROM request_state_transitions[\s\S]+tenant_id = \$1 AND request_id = \$2`).
		WithArgs("tenant-a", "request-1").
		WillReturnRows(eventRows(event))

	repository := NewPostgresRepository(mock)
	journey, err := repository.Detail(context.Background(), "tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if journey.TenantID != "tenant-a" || len(journey.Events) != 1 {
		t.Fatalf("journey = %#v", journey)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRepositoryAttemptFactsUseTenantScopedContentFreeEvents(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	startedAt := time.Unix(1_700_000_000, 0).UTC()
	started := testJourneyEvent("tenant-a", "request-1", 1)
	started.Type = EventAttemptStarted
	started.Stage = StageUpstream
	started.OccurredAt = startedAt
	started.Model = "model-a"
	started.ProviderID = 101
	started.CredentialID = 11
	started.Attempt = &AttemptRef{AttemptID: "attempt-a", AttemptNo: 1, Model: "model-a", ProviderID: 101, CredentialID: 11}
	failed := started
	failed.Seq = 2
	failed.Type = EventAttemptFailed
	failed.Outcome = OutcomeFailure
	failed.ErrorKind = ErrorKindUpstreamError
	failed.HTTPStatus = 503
	failed.OccurredAt = startedAt.Add(time.Second)

	mock.ExpectQuery(`SELECT[\s\S]+FROM request_state_transitions[\s\S]+tenant_id = \$1[\s\S]+event_type = 'attempt_started'`).
		WithArgs("tenant-a", startedAt, startedAt.Add(time.Hour)).
		WillReturnRows(eventRows(started, failed))

	facts, err := NewPostgresRepository(mock).AttemptFacts(context.Background(), "tenant-a", startedAt, startedAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].AttemptID != "attempt-a" || facts[0].Outcome != OutcomeFailure {
		t.Fatalf("attempt facts = %+v", facts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAttemptFactsFromEventsKeepsModelSwitchOnThePreviousAttempt(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	first := testJourneyEvent("tenant-a", "request-1", 1)
	first.Type = EventAttemptStarted
	first.Stage = StageUpstream
	first.OccurredAt = start
	first.Model = "model-a"
	first.ProviderID = 101
	first.CredentialID = 11
	first.Attempt = &AttemptRef{AttemptID: "attempt-a", AttemptNo: 1, Model: "model-a", ProviderID: 101, CredentialID: 11}
	failed := first
	failed.Seq = 2
	failed.Type = EventAttemptFailed
	failed.Outcome = OutcomeFailure
	failed.OccurredAt = start.Add(time.Second)
	switched := first
	switched.Seq = 3
	switched.Type = EventModelSwitched
	switched.Stage = StageRetrying
	switched.FromModel = "model-a"
	switched.ToModel = "model-b"
	switched.Model = ""
	switched.Attempt = nil
	switched.OccurredAt = start.Add(2 * time.Second)
	second := first
	second.Seq = 4
	second.Type = EventAttemptStarted
	second.Model = "model-b"
	second.Attempt = &AttemptRef{AttemptID: "attempt-b", AttemptNo: 2, Model: "model-b", ProviderID: 101, CredentialID: 11}
	second.OccurredAt = start.Add(3 * time.Second)

	facts := attemptFactsFromEvents([]JourneyEvent{second, switched, failed, first}, start, start.Add(time.Minute))
	if len(facts) != 2 || !facts[0].ModelSwitched || facts[1].ModelSwitched {
		t.Fatalf("model switch attribution = %+v", facts)
	}
}

func TestAttemptFactsFromEventsUsesStartTimeForWindowAndKeepsLaterTerminalEvent(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	first := testJourneyEvent("tenant-a", "request-1", 1)
	first.Type = EventAttemptStarted
	first.Stage = StageUpstream
	first.OccurredAt = start
	first.Model = "model-a"
	first.ProviderID = 101
	first.CredentialID = 11
	first.Attempt = &AttemptRef{AttemptID: "attempt-a", AttemptNo: 1, Model: "model-a", ProviderID: 101, CredentialID: 11}
	terminal := first
	terminal.Seq = 2
	terminal.Type = EventAttemptSucceeded
	terminal.Outcome = OutcomeSuccess
	terminal.OccurredAt = start.Add(2 * time.Minute)
	later := first
	later.Seq = 3
	later.Attempt = &AttemptRef{AttemptID: "attempt-b", AttemptNo: 2, Model: "model-a", ProviderID: 101, CredentialID: 11}
	later.OccurredAt = start.Add(3 * time.Minute)

	facts := attemptFactsFromEvents([]JourneyEvent{later, terminal, first}, start, start.Add(time.Minute))
	if len(facts) != 1 || facts[0].AttemptID != "attempt-a" || facts[0].Outcome != OutcomeSuccess || facts[0].EndedAt == nil {
		t.Fatalf("windowed facts = %+v", facts)
	}
}

func TestQueryServiceFallsBackToPostgresOnRedisMiss(t *testing.T) {
	server := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	event := testJourneyEvent("tenant-a", "request-1", 1)
	expectRecentPGJourney(mock, event)

	service := NewQueryService(
		NewRedisStore(redisClient, DefaultConfig()),
		NewPostgresRepository(mock), nil, DefaultConfig(),
	)
	result, err := service.RecentTotal(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationStatus != ObservationComplete || len(result.Requests) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryServiceMarksPostgresFallbackDegradedOnRedisFailure(t *testing.T) {
	redisClient := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: time.Millisecond, ReadTimeout: time.Millisecond,
		WriteTimeout: time.Millisecond, MaxRetries: -1,
	})
	t.Cleanup(func() { _ = redisClient.Close() })

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	event := testJourneyEvent("tenant-a", "request-1", 1)
	expectRecentPGJourney(mock, event)

	service := NewQueryService(
		NewRedisStore(redisClient, DefaultConfig()),
		NewPostgresRepository(mock), nil, DefaultConfig(),
	)
	result, readErr := service.RecentTotal(context.Background(), "tenant-a")
	if readErr == nil {
		t.Fatal("expected infrastructure error for caller logging")
	}
	if result.ObservationStatus != ObservationDegraded || len(result.Requests) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if result.Requests[0].ObservationStatus != ObservationDegraded {
		t.Fatalf("request status = %q", result.Requests[0].ObservationStatus)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryServiceIngressReportsSharedOrInstanceLocalScope(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	event := IngressEvent{
		RequestID: "request-1", GatewayInstanceID: "gateway-1",
		Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
		ArrivedAt: time.Unix(1700000000, 0).UTC(), UpdatedAt: time.Unix(1700000000, 0).UTC(),
		Status: IngressStatusArrived,
	}
	if err := memory.ApplyIngress(event); err != nil {
		t.Fatal(err)
	}
	local, err := NewQueryService(nil, nil, memory, DefaultConfig()).RecentIngress(context.Background())
	if err != nil || local.ObservationStatus != ObservationDegraded || local.ObservationScope != "instance_local" || len(local.Requests) != 1 {
		t.Fatalf("local = %#v, %v", local, err)
	}

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	if err := store.ApplyIngress(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	shared, err := NewQueryService(store, nil, memory, DefaultConfig()).RecentIngress(context.Background())
	if err != nil || shared.ObservationStatus != ObservationComplete || shared.ObservationScope != "shared_redis" || len(shared.Requests) != 1 {
		t.Fatalf("shared = %#v, %v", shared, err)
	}
}

func TestQueryServiceIngressMergesLocalEventsPendingRedisFlush(t *testing.T) {
	config := Config{TotalRequestCapacity: 2, DetailTTL: time.Hour}
	memory := NewProjection(config)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, config)
	base := time.Unix(1700000000, 0).UTC()

	shared := IngressEvent{RequestID: "shared", GatewayInstanceID: "gateway-1", Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions, ArrivedAt: base, UpdatedAt: base, Status: IngressStatusArrived}
	local := IngressEvent{RequestID: "local", GatewayInstanceID: "gateway-1", Protocol: IngressProtocolMessages, PathClass: IngressPathMessages, ArrivedAt: base.Add(time.Second), UpdatedAt: base.Add(time.Second), Status: IngressStatusArrived}
	if err := store.ApplyIngress(context.Background(), shared); err != nil {
		t.Fatal(err)
	}
	if err := memory.ApplyIngress(shared); err != nil {
		t.Fatal(err)
	}
	if err := memory.ApplyIngress(local); err != nil {
		t.Fatal(err)
	}

	result, err := NewQueryService(store, nil, memory, config).RecentIngress(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Requests) != 2 || result.Requests[0].RequestID != "shared" || result.Requests[1].RequestID != "local" {
		t.Fatalf("merged ingress = %#v", result.Requests)
	}
}

func TestQueryServiceFallsBackToMemoryWhenRedisAndPostgresAreNil(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	mustApply(t, memory, testJourneyEvent("tenant-a", "request-1", 1))
	service := NewQueryService(nil, nil, memory, DefaultConfig())

	result, err := service.RecentTotal(context.Background(), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationStatus != ObservationDegraded || len(result.Requests) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestPostgresApplyUsesOnlyExplicitContentFreeColumns(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()

	event := testJourneyEvent("tenant-a", "request-1", 1)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO request_state_transitions")).
		WithArgs(
			event.TenantID, event.GatewayInstanceID, event.RequestID, event.Seq,
			event.Type, event.Stage, event.RequestedModel, nil, nil, nil, nil,
			nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
			event.ObservationStatus, nil, event.OccurredAt,
		).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	if err := NewPostgresRepository(mock).Apply(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectRecentPGJourney(mock pgxmock.PgxPoolIface, event JourneyEvent) {
	mock.ExpectQuery(`SELECT request_id[\s\S]+tenant_id = \$1`).
		WithArgs(event.TenantID, DefaultTotalRequestCapacity).
		WillReturnRows(pgxmock.NewRows([]string{"request_id"}).AddRow(event.RequestID))
	mock.ExpectQuery(`SELECT[\s\S]+FROM request_state_transitions[\s\S]+tenant_id = \$1 AND request_id = \$2`).
		WithArgs(event.TenantID, event.RequestID).
		WillReturnRows(eventRows(event))
}

func eventRows(events ...JourneyEvent) *pgxmock.Rows {
	rows := pgxmock.NewRows([]string{
		"tenant_id", "gateway_instance_id", "request_id", "seq", "event_type", "stage",
		"requested_model", "resolved_model", "model", "provider_id", "provider",
		"credential_id", "from_model", "to_model", "from_credential_id",
		"to_credential_id", "attempt_id", "attempt_no", "outcome", "error_kind",
		"http_status", "retry_reason", "switch_reason", "node_health_status",
		"observation_status", "retry_at", "occurred_at",
	})
	for _, event := range events {
		var attemptID, attemptNo any
		if event.Attempt != nil {
			attemptID = event.Attempt.AttemptID
			attemptNo = event.Attempt.AttemptNo
		}
		rows.AddRow(
			event.TenantID, event.GatewayInstanceID, event.RequestID, event.Seq,
			string(event.Type), string(event.Stage), nullableTestString(event.RequestedModel),
			nullableTestString(event.ResolvedModel), nullableTestString(event.Model),
			nullableTestInt64(event.ProviderID), nullableTestString(event.Provider),
			nullableTestInt64(event.CredentialID), nullableTestString(event.FromModel),
			nullableTestString(event.ToModel), nullableTestInt64(event.FromCredentialID),
			nullableTestInt64(event.ToCredentialID), attemptID, attemptNo,
			nullableTestString(string(event.Outcome)), nullableTestString(event.ErrorKind),
			nullableTestInt(event.HTTPStatus), nullableTestString(event.RetryReason),
			nullableTestString(event.SwitchReason), nullableTestString(string(event.NodeHealthStatus)),
			string(event.ObservationStatus), nullableTestTime(event.RetryAt), event.OccurredAt,
		)
	}
	return rows
}

func nullableTestString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTestInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableTestInt(value int) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableTestTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
