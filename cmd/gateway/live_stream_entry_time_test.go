package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
	"github.com/redis/go-redis/v9"
)

func TestLiveStreamEventTime_PrefersEntryEventAt(t *testing.T) {
	eventAt := time.Date(2026, 7, 20, 12, 34, 56, 0, time.UTC)
	entry := &telemetry.RequestLogEntry{EventAt: &eventAt}
	if got := liveStreamEventTime(entry); !got.Equal(eventAt) {
		t.Fatalf("liveStreamEventTime()=%s want %s", got.Format(time.RFC3339), eventAt.Format(time.RFC3339))
	}
}

func TestAdminLiveRequestFromEntry_UsesEventAtTimestamp(t *testing.T) {
	eventAt := time.Date(2026, 7, 20, 12, 34, 56, 0, time.UTC)
	status := telemetry.RequestStatusSuccess
	entry := &telemetry.RequestLogEntry{
		RequestID:        "req-1",
		TenantID:         "tenant-a",
		EventAt:          &eventAt,
		ClientModel:      strPtrMain("gpt-4o"),
		RequestStatus:    &status,
		Success:          true,
		PromptTokens:     intPtrMain(3),
		CompletionTokens: intPtrMain(5),
	}

	got := adminLiveRequestFromEntry(entry, nil)
	if got.Ts != eventAt.Format(time.RFC3339) {
		t.Fatalf("adminLiveRequestFromEntry().Ts=%q want %q", got.Ts, eventAt.Format(time.RFC3339))
	}
}

func TestAdminLiveRequestFromEntry_PreservesCredentialIDWithoutHub(t *testing.T) {
	credentialID := 42
	entry := &telemetry.RequestLogEntry{
		RequestID:    "req-credential-fallback",
		TenantID:     "tenant-a",
		ClientModel:  strPtrMain("gpt-4o"),
		CredentialID: &credentialID,
	}

	got := adminLiveRequestFromEntry(entry, nil)
	if got.CredentialID != credentialID {
		t.Fatalf("CredentialID=%d want %d", got.CredentialID, credentialID)
	}
	if got.CredentialLabel != "" {
		t.Fatalf("CredentialLabel=%q want empty without hub lookup", got.CredentialLabel)
	}
}

func TestAdminLiveRequestFromEntry_StaleEventAtDoesNotMoveLaneBackward(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	store := admin.NewLiveStreamRedisStore(rdb)

	startAt := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	staleDoneAt := startAt.Add(-12 * time.Minute)
	startStatus := telemetry.RequestStatusInProgress
	doneStatus := telemetry.RequestStatusSuccess

	start := adminLiveRequestFromEntry(&telemetry.RequestLogEntry{
		RequestID:        "req-cross-layer",
		TenantID:         "tenant-a",
		EventAt:          &startAt,
		ClientModel:      strPtrMain("gpt-4o"),
		RequestStatus:    &startStatus,
		Success:          false,
		PromptTokens:     intPtrMain(3),
		CompletionTokens: intPtrMain(0),
	}, nil)
	start.ModelCategory = "openai"
	start.ProviderCode = "openai"

	staleDone := adminLiveRequestFromEntry(&telemetry.RequestLogEntry{
		RequestID:        "req-cross-layer",
		TenantID:         "tenant-a",
		EventAt:          &staleDoneAt,
		ClientModel:      strPtrMain("gpt-4o"),
		RequestStatus:    &doneStatus,
		Success:          true,
		PromptTokens:     intPtrMain(3),
		CompletionTokens: intPtrMain(5),
	}, nil)
	staleDone.ModelCategory = "openai"
	staleDone.ProviderCode = "openai"
	ctx := context.Background()

	if err := store.Record(ctx, start, ""); err != nil {
		t.Fatalf("Record start: %v", err)
	}
	if err := store.Record(ctx, staleDone, ""); err != nil {
		t.Fatalf("Record staleDone: %v", err)
	}

	items, err := store.Replay(ctx, "tenant-a", false, 10)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %#v", items)
	}
	if items[0].Status != "success" {
		t.Fatalf("expected success state, got %#v", items[0])
	}
	if items[0].Ts != startAt.Format(time.RFC3339) {
		t.Fatalf("timestamp moved backward across gateway adapter: got %q want %q", items[0].Ts, startAt.Format(time.RFC3339))
	}
}

func strPtrMain(s string) *string { return &s }
func intPtrMain(v int) *int       { return &v }
