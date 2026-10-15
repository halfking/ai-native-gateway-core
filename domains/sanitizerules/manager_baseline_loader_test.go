package sanitizerules

import (
	"context"
	"testing"
	"time"
)

func TestManagerBaselineLoaderCASPreservesConcurrentPublishedHead(t *testing.T) {
	store := NewMemoryStore()
	entered, release := make(chan struct{}), make(chan struct{})
	m, err := NewManager(store, testConfig(), Options{
		InstanceID: "baseline-race",
		BaselineLoader: func(ctx context.Context) (Config, error) {
			close(entered)
			select {
			case <-release:
				return testConfig(), nil
			case <-ctx.Done():
				return Config{}, ctx.Err()
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- m.Refresh(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("baseline import did not start")
	}
	winner := testConfig()
	winner.Rules[0].Name = "Published by the other instance"
	if _, err := store.Publish(ctx, 0, winner, "admin", "concurrent first publication", 0); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	versions, _ := store.ListVersions(ctx, 10)
	if len(versions) != 1 || m.Current().Version != 1 || m.Current().Rules[0].Name != winner.Rules[0].Name {
		t.Fatal("legacy import overwrote a concurrent published head", versions)
	}
}

func TestManagerBaselineLoaderReceivesRefreshDeadline(t *testing.T) {
	store := NewMemoryStore()
	m, err := NewManager(store, testConfig(), Options{InstanceID: "baseline-timeout", BaselineLoader: func(ctx context.Context) (Config, error) {
		<-ctx.Done()
		return Config{}, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.Refresh(ctx); err == nil {
		t.Fatal("baseline load deadline was ignored")
	}
	if head, _ := store.Current(context.Background()); head.ID != 0 || m.Current().Version != 0 {
		t.Fatal("cancelled import seeded a partial baseline")
	}
}
