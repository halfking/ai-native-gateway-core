package sanitizerules

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestRedisNotificationAndPollingFallback(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	defer client.Close()
	n := NewRedisNotifications(client, "test:sanitize")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := n.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = n.Publish(ctx, 42); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ch:
		if id != 42 {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("notification lost")
	}
	s := NewMemoryStore()
	m, _ := NewManager(s, testConfig(), Options{InstanceID: "node", Notifications: n, PollInterval: func() time.Duration { return 10 * time.Millisecond }})
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitFor(t, func() bool { return m.Current().Version == 1 })
	server.Close()
	v, err := s.Publish(ctx, 1, testConfig(), "admin", "notification unavailable", 0)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return m.Current().Version == v.ID })
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notification subscription leaked")
	}
}
