package sanitizerules

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type failingStore struct {
	Store
	mu   sync.Mutex
	fail bool
}

func (s *failingStore) Current(ctx context.Context) (Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return Version{}, errors.New("database: raw-secret")
	}
	return s.Store.Current(ctx)
}
func (s *failingStore) Heartbeat(ctx context.Context, i Instance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("heartbeat: raw-secret")
	}
	return s.Store.Heartbeat(ctx, i)
}
func (s *failingStore) setFail(v bool) { s.mu.Lock(); s.fail = v; s.mu.Unlock() }
func waitFor(t *testing.T, p func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}
func TestManagerPublishRollbackPinAndFailureRetention(t *testing.T) {
	ctx := context.Background()
	s := &failingStore{Store: NewMemoryStore()}
	m, err := NewManager(s, testConfig(), Options{InstanceID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	pinned := m.Pin(ctx)
	if m.SnapshotFor(pinned).Version != 0 {
		t.Fatal("fallback not pinned")
	}
	if err := m.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := s.Current(ctx)
	if err != nil || v.ID != 1 {
		t.Fatal(v, err)
	}
	if m.SnapshotFor(pinned).Version != 0 {
		t.Fatal("in-flight snapshot changed")
	}
	changed := testConfig()
	changed.Rules[0].Action = "block"
	v, err = m.Publish(ctx, 1, changed, "admin", "second")
	if err != nil || v.ID != 2 {
		t.Fatal(v, err)
	}
	v, err = m.Rollback(ctx, 2, 1, "admin", "restore")
	if err != nil || v.ID != 3 || v.RollbackOf != 1 || v.ParentID != 2 || v.Config.Rules[0].Action != "mask" {
		t.Fatal(v, err)
	}
	s.setFail(true)
	if err = m.Refresh(ctx); err == nil {
		t.Fatal("refresh failure hidden")
	}
	if m.Current().Version != 3 {
		t.Fatal("refresh cleared snapshot")
	}
	if strings.Contains(m.Status().Error, "raw-secret") {
		t.Fatal("raw store error exposed")
	}
	s.setFail(false)
	if err = m.Refresh(ctx); err != nil || m.Status().Error != "" {
		t.Fatal("recovery failed", err)
	}
}
func TestManagersPollCompensatesDroppedNotificationsAndHeartbeatRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &failingStore{Store: NewMemoryStore()}
	poll := func() time.Duration { return 10 * time.Millisecond }
	a, _ := NewManager(s, testConfig(), Options{InstanceID: "a", PollInterval: poll})
	b, _ := NewManager(s, testConfig(), Options{InstanceID: "b", PollInterval: poll})
	if err := a.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	if _, err := a.Publish(ctx, 1, testConfig(), "admin", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return b.Current().Version == 2 })
	s.setFail(true)
	waitFor(t, func() bool { return b.Status().Error != "" })
	if b.Current().Version != 2 {
		t.Fatal("failure cleared active version")
	}
	s.setFail(false)
	waitFor(t, func() bool { return b.Status().Error == "" })
	rows, _ := s.ListInstances(ctx)
	found := false
	for _, i := range rows {
		if i.InstanceID == "b" && i.Version == 2 && i.Error == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("actual version not ACKed", rows)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
func TestManagerRecordIsBoundedAsyncAndDrainsOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := NewMemoryStore()
	var errs atomic.Int32
	m, _ := NewManager(s, testConfig(), Options{InstanceID: "a", QueueSize: 2, OnError: func(error) { errs.Add(1) }})
	e := Event{TenantID: "t", RuleID: "secret", Phase: "input", Action: "exempted", Type: "secret"}
	if !m.Record(ctx, e) || !m.Record(ctx, e) {
		t.Fatal("queue rejected")
	}
	if m.Record(ctx, e) {
		t.Fatal("unbounded queue")
	}
	if m.DroppedEvents() != 1 {
		t.Fatal("dropped event not counted")
	}
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitFor(t, func() bool {
		rows, _ := s.ListEvents(ctx, EventFilter{TenantID: "t"})
		return len(rows) == 1 && rows[0].Count == 2 && errs.Load() == 1
	})
	cancel()
	<-done
}

type readGateStore struct {
	Store
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
}

func (s *readGateStore) Current(ctx context.Context) (Version, error) {
	v, err := s.Store.Current(ctx)
	if !s.first.Swap(true) {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
		}
	}
	return v, err
}
