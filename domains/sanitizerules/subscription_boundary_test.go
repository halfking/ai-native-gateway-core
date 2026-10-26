package sanitizerules

import (
	"context"
	"testing"
	"time"
)

type stalledSubscription struct{ entered chan struct{} }

func (n stalledSubscription) Publish(context.Context, int64) error { return nil }
func (n stalledSubscription) Subscribe(ctx context.Context) (<-chan int64, error) {
	close(n.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestStalledSubscriptionCannotStopPollingOrShutdown(t *testing.T) {
	s := NewMemoryStore()
	n := stalledSubscription{entered: make(chan struct{})}
	m, _ := NewManager(s, testConfig(), Options{Notifications: n, PollInterval: func() time.Duration { return 10 * time.Millisecond }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	select {
	case <-n.entered:
	case <-time.After(time.Second):
		t.Fatal("subscription not attempted")
	}
	v, err := s.Publish(context.Background(), 1, testConfig(), "test", "DB publication", 0)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return m.Current().Version == v.ID })
	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("subscription blocked manager shutdown")
	}
}
