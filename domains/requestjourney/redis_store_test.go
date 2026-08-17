package requestjourney

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisStoreIngressIsGlobalBoundedAndUpdatesWithoutReordering(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	writer := NewRedisStore(client, Config{TotalRequestCapacity: 2, DetailTTL: time.Hour})
	reader := NewRedisStore(client, Config{TotalRequestCapacity: 2, DetailTTL: time.Hour})
	ctx := context.Background()
	arrivedAt := time.Unix(1700000000, 0).UTC()
	for _, item := range []struct {
		requestID string
		second    int
	}{{"request-2", 1}, {"request-1", 0}, {"request-3", 2}} {
		event := IngressEvent{
			RequestID: item.requestID, GatewayInstanceID: "gateway-1",
			Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
			ArrivedAt: arrivedAt.Add(time.Duration(item.second) * time.Second),
			UpdatedAt: arrivedAt.Add(time.Duration(item.second) * time.Second), Status: IngressStatusArrived,
		}
		if err := writer.ApplyIngress(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	terminal := IngressEvent{
		RequestID: "request-2", GatewayInstanceID: "gateway-1",
		Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
		ArrivedAt: arrivedAt.Add(time.Second), UpdatedAt: arrivedAt.Add(time.Minute),
		Status: IngressStatusFailed, ErrorKind: "invalid_key", HTTPStatus: 401,
	}
	if err := writer.ApplyIngress(ctx, terminal); err != nil {
		t.Fatal(err)
	}

	recent, err := reader.RecentIngress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].RequestID != "request-2" || recent[1].RequestID != "request-3" {
		t.Fatalf("recent = %#v", recent)
	}
	if recent[0].Status != IngressStatusFailed || recent[0].ErrorKind != "invalid_key" {
		t.Fatalf("updated ingress = %#v", recent[0])
	}
}

func TestRedisStoreIsAtomicBoundedAndVisibleAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	config := Config{TotalRequestCapacity: 2, PerModelCapacity: 2, PerNodeCapacity: 2, DetailTTL: time.Hour}
	writer := NewRedisStore(client, config)
	reader := NewRedisStore(client, config)
	ctx := context.Background()

	for i, requestID := range []string{"request-1", "request-2", "request-3"} {
		event := testJourneyEvent("tenant-a", requestID, 1)
		event.ResolvedModel = "model-a"
		event.OccurredAt = event.OccurredAt.Add(time.Duration(i) * time.Second)
		if err := writer.Apply(ctx, event); err != nil {
			t.Fatalf("Apply(%s): %v", requestID, err)
		}
	}

	recent, err := reader.RecentTotal(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].RequestID != "request-2" || recent[1].RequestID != "request-3" {
		t.Fatalf("recent = %#v", recent)
	}
	if ttl := server.TTL(redisDetailKey("tenant-a", "request-3")); ttl != time.Hour {
		t.Fatalf("detail TTL = %s", ttl)
	}
}

func TestRedisStoreIngressRespectsArrivalOrder(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	writer := NewRedisStore(client, Config{TotalRequestCapacity: 3, DetailTTL: time.Hour})
	reader := NewRedisStore(client, Config{TotalRequestCapacity: 3, DetailTTL: time.Hour})
	ctx := context.Background()
	base := time.Unix(1700000000, 0).UTC()
	for i, requestID := range []string{"request-b", "request-a", "request-c"} {
		event := IngressEvent{
			RequestID: requestID, GatewayInstanceID: "gateway-1",
			Protocol: IngressProtocolChat, PathClass: IngressPathChatCompletions,
			ArrivedAt: base.Add(time.Duration(i) * time.Second),
			UpdatedAt: base.Add(time.Duration(i) * time.Second),
			Status:    IngressStatusArrived,
		}
		if err := writer.ApplyIngress(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	recent, err := reader.RecentIngress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 3 || recent[0].RequestID != "request-b" || recent[1].RequestID != "request-a" || recent[2].RequestID != "request-c" {
		t.Fatalf("recent = %#v", recent)
	}
}

func TestRedisStoreModelSwitchAddsTargetModelQueue(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	ctx := context.Background()

	request := testJourneyEvent("tenant-a", "request-1", 1)
	request.ResolvedModel = "model-a"
	request.Type = EventRouteResolved
	request.Stage = StageRouting
	if err := store.Apply(ctx, request); err != nil {
		t.Fatal(err)
	}

	switchEvent := request
	switchEvent.Seq = 2
	switchEvent.Type = EventModelSwitched
	switchEvent.Stage = StageModelQueue
	switchEvent.FromModel = "model-a"
	switchEvent.ToModel = "model-b"
	switchEvent.ResolvedModel = "model-b"
	switchEvent.OccurredAt = switchEvent.OccurredAt.Add(time.Second)
	if err := store.Apply(ctx, switchEvent); err != nil {
		t.Fatal(err)
	}

	models, err := store.RecentModels(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v", models)
	}
	if models[1].Model != "model-b" || len(models[1].Requests) != 1 || models[1].Requests[0].RequestID != "request-1" {
		t.Fatalf("model switch queue = %#v", models)
	}
}

func TestRedisStoreListsModelAndNodeFIFOsAcrossInstances(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	writer := NewRedisStore(client, DefaultConfig())
	reader := NewRedisStore(client, DefaultConfig())
	ctx := context.Background()

	if err := writer.Apply(ctx, testAttemptEvent("tenant-a", "request-1", 1, "attempt-1", 1, 11)); err != nil {
		t.Fatal(err)
	}
	models, err := reader.RecentModels(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Model != "model-a" || len(models[0].Requests) != 1 {
		t.Fatalf("models = %#v", models)
	}
	nodes, err := reader.RecentNodes(ctx, "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].CredentialID != 11 || nodes[0].Requests[0].Attempt.AttemptID != "attempt-1" {
		t.Fatalf("nodes = %#v", nodes)
	}
}

func TestRedisStoreDetailIsSequenceIdempotentAndTenantIsolated(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	ctx := context.Background()

	first := testJourneyEvent("tenant-a", "same-request", 1)
	second := testJourneyEvent("tenant-a", "same-request", 2)
	second.Type = EventRouteResolved
	second.Stage = StageRouting
	second.OccurredAt = second.OccurredAt.Add(time.Second)
	for _, event := range []JourneyEvent{first, first, second} {
		if err := store.Apply(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	otherTenant := testJourneyEvent("tenant-b", "same-request", 1)
	if err := store.Apply(ctx, otherTenant); err != nil {
		t.Fatal(err)
	}

	detail, err := store.Detail(ctx, "tenant-a", "same-request")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Events) != 2 || detail.Events[0].Seq != 1 || detail.Events[1].Seq != 2 {
		t.Fatalf("detail events = %#v", detail.Events)
	}
	other, err := store.Detail(ctx, "tenant-b", "same-request")
	if err != nil || len(other.Events) != 1 {
		t.Fatalf("other tenant detail = %#v, %v", other, err)
	}

	conflict := first
	conflict.Stage = StageRouting
	if err := store.Apply(ctx, conflict); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
}

func TestRedisStoreTracksIndependentNodeAttempts(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewRedisStore(client, DefaultConfig())
	ctx := context.Background()

	first := testAttemptEvent("tenant-a", "request-1", 1, "attempt-1", 1, 11)
	second := testAttemptEvent("tenant-a", "request-1", 2, "attempt-2", 2, 11)
	second.OccurredAt = second.OccurredAt.Add(time.Second)
	if err := store.Apply(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, second); err != nil {
		t.Fatal(err)
	}

	recent, err := store.RecentNode(ctx, "tenant-a", NodeKey{Model: "model-a", ProviderID: 7, CredentialID: 11})
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].Attempt.AttemptID != "attempt-1" || recent[1].Attempt.AttemptID != "attempt-2" {
		t.Fatalf("node attempts = %#v", recent)
	}
}
