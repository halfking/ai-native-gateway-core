package requestjourney

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/redis/go-redis/v9"
)

func recentJourneyEvent(t *testing.T, tenantID, requestID string, seq int64) JourneyEvent {
	t.Helper()
	event := testJourneyEvent(tenantID, requestID, seq)
	// Keep the journey inside the divergence grace window and the Redis TTL so
	// staleness rules are exercised rather than retention expiry.
	event.OccurredAt = time.Now().UTC().Add(-time.Minute).Add(time.Duration(seq) * time.Second)
	return event
}

// TestQueryServiceDetailMergesMemoryTerminalMissingInRedis guards the
// three-source merge contract: a Redis journey that lags behind the local
// projection must never be presented as the final state. The merged view keeps
// the freshest events, is marked degraded, and reports the divergence kind.
func TestQueryServiceDetailMergesMemoryTerminalMissingInRedis(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	first := recentJourneyEvent(t, "tenant-a", "request-1", 1)
	terminal := recentJourneyEvent(t, "tenant-a", "request-1", 2)
	terminal.Type = EventRequestSucceeded
	terminal.Stage = StageTerminal
	terminal.Outcome = OutcomeSuccess
	terminal.HTTPStatus = 200
	mustApply(t, memory, first)
	mustApply(t, memory, terminal)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	if err := store.Apply(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	service := NewQueryService(store, nil, memory, DefaultConfig())
	result, err := service.Detail(context.Background(), "tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Journey == nil {
		t.Fatal("Detail returned no journey")
	}
	if len(result.Journey.Events) != 2 || result.Journey.Events[1].Stage != StageTerminal {
		t.Fatalf("merged events = %#v", result.Journey.Events)
	}
	if result.ObservationStatus != ObservationDegraded {
		t.Fatalf("merged status = %q, want degraded", result.ObservationStatus)
	}
	if !containsKind(result.Divergence, "redis_divergence") {
		t.Fatalf("divergence kinds = %#v", result.Divergence)
	}
}

func containsKind(kinds []string, want string) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}

// TestQueryServiceDetailFlagsPostgresGap guards durable-tier verification:
// PostgreSQL missing a sequence the hot tiers hold must degrade the merged
// view, because the durable copy is the recovery source of record.
func TestQueryServiceDetailFlagsPostgresGap(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	first := recentJourneyEvent(t, "tenant-a", "request-1", 1)
	terminal := recentJourneyEvent(t, "tenant-a", "request-1", 2)
	terminal.Type = EventRequestFailed
	terminal.Stage = StageTerminal
	terminal.Outcome = OutcomeFailure
	terminal.ErrorKind = "upstream_error"
	mustApply(t, memory, first)
	mustApply(t, memory, terminal)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	for _, event := range []JourneyEvent{first, terminal} {
		if err := store.Apply(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`SELECT[\s\S]+FROM request_state_transitions[\s\S]+tenant_id = \$1 AND request_id = \$2`).
		WithArgs("tenant-a", "request-1").
		WillReturnRows(eventRows(first))

	service := NewQueryService(store, NewPostgresRepository(mock), memory, DefaultConfig())
	result, err := service.Detail(context.Background(), "tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Journey == nil || len(result.Journey.Events) != 2 {
		t.Fatalf("merged journey = %#v", result.Journey)
	}
	if result.ObservationStatus != ObservationDegraded || !containsKind(result.Divergence, "postgres_gap") {
		t.Fatalf("status/divergence = %q/%#v", result.ObservationStatus, result.Divergence)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestQueryServiceDetailToleratesFreshFanOutLag guards the grace window: an
// in-flight request whose Redis write is merely lagging must return the
// freshest merged view without crying divergence.
func TestQueryServiceDetailToleratesFreshFanOutLag(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	fresh := testJourneyEvent("tenant-a", "request-fresh", 1)
	fresh.OccurredAt = time.Now().UTC().Add(-2 * time.Second)
	mustApply(t, memory, fresh)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	if err := store.Apply(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}

	service := NewQueryService(store, nil, memory, DefaultConfig())
	result, err := service.Detail(context.Background(), "tenant-a", "request-fresh")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationStatus != ObservationComplete || len(result.Divergence) != 0 {
		t.Fatalf("fresh lag flagged: status=%q divergence=%#v", result.ObservationStatus, result.Divergence)
	}
}

// TestQueryServiceDetailTreatsRedisExpiryAsRetention guards against false
// divergence: a journey older than the Redis TTL may legitimately be absent
// from Redis while memory and PostgreSQL agree.
func TestQueryServiceDetailTreatsRedisExpiryAsRetention(t *testing.T) {
	config := DefaultConfig() // DetailTTL = 24h
	memory := NewProjection(config)
	aged := testJourneyEvent("tenant-a", "request-aged", 1)
	aged.OccurredAt = time.Now().UTC().Add(-config.DetailTTL - time.Hour)
	mustApply(t, memory, aged)

	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery(`SELECT[\s\S]+FROM request_state_transitions[\s\S]+tenant_id = \$1 AND request_id = \$2`).
		WithArgs("tenant-a", "request-aged").
		WillReturnRows(eventRows(aged))

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	service := NewQueryService(NewRedisStore(client, config), NewPostgresRepository(mock), memory, config)

	result, err := service.Detail(context.Background(), "tenant-a", "request-aged")
	if err != nil {
		t.Fatal(err)
	}
	if result.ObservationStatus != ObservationComplete || len(result.Divergence) != 0 {
		t.Fatalf("expired Redis flagged: status=%q divergence=%#v", result.ObservationStatus, result.Divergence)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestQueryServiceDetailFlagsContentConflict guards identity of sequence
// meaning: two sources disagreeing about the same sequence must degrade the
// journey, and the local projection's copy wins the merged view.
func TestQueryServiceDetailFlagsContentConflict(t *testing.T) {
	memory := NewProjection(DefaultConfig())
	local := recentJourneyEvent(t, "tenant-a", "request-1", 1)
	mustApply(t, memory, local)

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	// RedisStore.Apply refuses conflicting bodies for the same seq, so a
	// divergent copy can only exist through corruption or a legacy bug: inject
	// it directly to prove the merge detects it.
	conflicting := local
	conflicting.Stage = StageRouting
	conflicting.Type = EventRouteResolved
	body, err := json.Marshal(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HSet(context.Background(), redisDetailKey("tenant-a", "request-1"),
		strconv.FormatInt(conflicting.Seq, 10), string(body)).Err(); err != nil {
		t.Fatal(err)
	}

	service := NewQueryService(NewRedisStore(client, DefaultConfig()), nil, memory, DefaultConfig())
	result, err := service.Detail(context.Background(), "tenant-a", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Journey == nil || len(result.Journey.Events) != 1 {
		t.Fatalf("merged journey = %#v", result.Journey)
	}
	if result.Journey.Events[0].Stage != StageReceived {
		t.Fatalf("conflicting copy won merge: %#v", result.Journey.Events[0])
	}
	if result.ObservationStatus != ObservationDegraded || !containsKind(result.Divergence, "content_conflict") {
		t.Fatalf("status/divergence = %q/%#v", result.ObservationStatus, result.Divergence)
	}
}
