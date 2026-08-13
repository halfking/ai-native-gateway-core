package main

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/admin"
	"github.com/kaixuan/llm-gateway-go/domains/dispatch"
)

func TestLiveQueueSnapshotProvider(t *testing.T) {
	p := dispatch.NewPipeline(dispatch.Deps{})
	got := liveQueueSnapshotProvider(p)
	if !got.Enabled {
		t.Fatalf("Enabled = false, want dispatch gate state true in test")
	}
	if !got.Wired {
		t.Fatal("Wired = false, want true")
	}
	if got.Models == nil || got.Credentials == nil {
		t.Fatalf("empty lanes must be non-nil: models=%v credentials=%v", got.Models, got.Credentials)
	}

	got = liveQueueSnapshotProvider(nil)
	if got.Wired {
		t.Fatal("nil pipeline must report Wired=false")
	}
	if got.Models == nil || got.Credentials == nil {
		t.Fatal("nil pipeline must still expose empty lane arrays")
	}
}

func TestLiveQueueSnapshotFromLanes(t *testing.T) {
	got := liveQueueSnapshotFromLanes(
		[]dispatch.QueueSnapshot{{Model: "m", Depth: 2}},
		[]dispatch.QueueSnapshot{{Credential: 7, Mode: "concurrency", Depth: 3}},
		true,
		true,
	)
	if len(got.Models) != 1 || got.Models[0].Model != "m" || got.Models[0].Depth != 2 {
		t.Fatalf("model lane = %+v", got.Models)
	}
	if len(got.Credentials) != 1 || got.Credentials[0].Credential != 7 || got.Credentials[0].Mode != "concurrency" || got.Credentials[0].Depth != 3 {
		t.Fatalf("credential lane = %+v", got.Credentials)
	}
}

func TestLiveNodeStatusCacheRetainsLastGoodSnapshot(t *testing.T) {
	cache := &liveNodeStatusCache{snapshot: []admin.LiveNodeStatus{}}
	want := []admin.LiveNodeStatus{{CredentialID: 1, ProviderID: 2, ProviderCode: "anthropic", HealthStatus: "healthy"}}
	if err := cache.refreshWith(func(context.Context) ([]admin.LiveNodeStatus, error) {
		return want, nil
	}, context.Background()); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	if got := cache.get(); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("snapshot after success = %+v", got)
	}

	errBoom := errors.New("database unavailable")
	if err := cache.refreshWith(func(context.Context) ([]admin.LiveNodeStatus, error) {
		return nil, errBoom
	}, context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("refresh error = %v, want %v", err, errBoom)
	}
	got := cache.get()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("last good snapshot was not retained: %+v", got)
	}

	got[0].ProviderCode = "mutated"
	if cache.get()[0].ProviderCode != "anthropic" {
		t.Fatal("cache returned internal slice instead of a copy")
	}
}
