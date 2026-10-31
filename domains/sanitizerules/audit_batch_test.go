package sanitizerules

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// batchSpyStore 实现 BatchEventStore 的探针：记录每批内容，首写可阻塞，
// 用来验证 auditWorker 的凑批与 queue-full 告警节流。
type batchSpyStore struct {
	Store
	mu      sync.Mutex
	batches [][]Event
	gate    chan struct{}
	release chan struct{}
}

func (s *batchSpyStore) RecordEvents(ctx context.Context, events []Event, limit int) error {
	select {
	case s.gate <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	s.mu.Lock()
	s.batches = append(s.batches, append([]Event(nil), events...))
	s.mu.Unlock()
	return nil
}

func (s *batchSpyStore) snapshot() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total, maxN := 0, 0
	for _, b := range s.batches {
		total += len(b)
		if len(b) > maxN {
			maxN = len(b)
		}
	}
	return total, maxN
}

// TestRecordQueueFullThrottleReportsTotal：队列灌满时 (a) 丢事件必须计数、
// Record 返回 false；(b) queue-full WARN 必须节流 —— 旧实现每丢一条打一条
// WARN（211 实测 44250 条/3h，把其它告警挤出视野），新实现同一窗口至多一条
// 且带累计 dropped_total。
func TestRecordQueueFullThrottleReportsTotal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var queueFullMsgs []string
	s := &batchSpyStore{Store: NewMemoryStore(), gate: make(chan struct{}, 1), release: make(chan struct{})}
	m, err := NewManager(s, testConfig(), Options{InstanceID: "node", QueueSize: 4, OnError: func(err error) {
		if !strings.Contains(err.Error(), "queue full") {
			return
		}
		mu.Lock()
		queueFullMsgs = append(queueFullMsgs, err.Error())
		mu.Unlock()
	}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { close(s.release); cancel(); <-done }()

	e := Event{RuleID: "secret", Phase: "input", Action: "mask"}
	// worker 首写被 release 阻塞 ⇒ 队列（4）灌满后全部丢弃，确定性成立。
	dropped := 0
	for i := 0; i < 200; i++ {
		if !m.Record(ctx, e) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected drops with QueueSize=4 and a blocked writer")
	}
	if got := m.DroppedEvents(); got != int64(dropped) {
		t.Fatalf("DroppedEvents()=%d, dropped=%d — 计数器与真实丢事件数脱钩", got, dropped)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(queueFullMsgs)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	msgs := append([]string(nil), queueFullMsgs...)
	mu.Unlock()
	if len(msgs) == 0 {
		t.Fatal("expected at least one queue-full report on the alert surface")
	}
	if len(msgs) > 2 {
		t.Fatalf("queue-full reports not throttled: %d messages for %d drops\n%v", len(msgs), dropped, msgs)
	}
	if !strings.Contains(msgs[0], "dropped_total=") {
		t.Fatalf("queue-full report missing cumulative dropped_total: %q", msgs[0])
	}
}
