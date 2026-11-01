package sanitizerules

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// 2026-11-01 12h 审计轮：批写路径的**失败**面。
//
// ★ 背景 ★：2026-10-31 第 12 轮把审计事件的落库从「逐条事务」改成
// 「每批一条事务」（auditWriteBatchSize = 128），目的是修「丢事件」
// （211 实测 3 小时丢 44250 条）。但**失败路径**没有同步收窄：
//
//	原实现：RecordEvents 失败 ⇒ 只打一条静态串 ⇒ **整批 ≤128 条**事件
//	        全部消失 ⇒ 且没有任何计数 ⇒ 且不退回逐条写。
//
// ⇒ 本组用例钉住三件事：
//	① 批写失败 ⇒ 退回逐条写（只有真正写不进去的那几条会丢）；
//	② 逐条写也失败 ⇒ 丢失**计入** DroppedEvents（管理面必须看得见）；
//	③ 失败上报必须带**错误内容**（原先是一条没有细节的静态串）。

// failingBatchStore 让批写永远失败，模拟「DB 抖动/事务失败」。
type failingBatchStore struct {
	Store
	mu         sync.Mutex
	batchCalls int
	eventCalls int
	eventErr   error
}

func (s *failingBatchStore) RecordEvents(ctx context.Context, events []Event, limit int) error {
	s.mu.Lock()
	s.batchCalls++
	s.mu.Unlock()
	return errors.New("batch write unavailable")
}

func (s *failingBatchStore) RecordEvent(ctx context.Context, e Event, limit int) error {
	s.mu.Lock()
	s.eventCalls++
	err := s.eventErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Store.RecordEvent(ctx, e, limit)
}

func (s *failingBatchStore) counts() (batch, event int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batchCalls, s.eventCalls
}

// TestAuditBatchFailureFallsBackToPerEventWrites 钉住 ①：
// 批写失败必须退回逐条写 ⇒ 事件**不丢**。
func TestAuditBatchFailureFallsBackToPerEventWrites(t *testing.T) {
	memory := NewMemoryStore()
	store := &failingBatchStore{Store: memory}
	var reports []string
	var mu sync.Mutex
	m, err := NewManager(store, testConfig(), Options{
		InstanceID: "batch-fallback",
		QueueSize:  64,
		OnError: func(err error) {
			mu.Lock()
			reports = append(reports, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	for i := 0; i < 20; i++ {
		if !m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"}) {
			t.Fatalf("event %d was not accepted into the queue", i)
		}
	}
	waitForBatch(t, func() (bool, bool) {
		got, err := memory.ListEvents(ctx, EventFilter{})
		if err != nil {
			return false, false
		}
		total := 0
		for _, e := range got {
			total += int(e.Count)
		}
		return true, total == 20
	}, "all 20 events to land through the per-event fallback")

	batchCalls, eventCalls := store.counts()
	if batchCalls == 0 {
		t.Fatal("批写路径没有被走到（用例前提不成立）")
	}
	if eventCalls == 0 {
		t.Fatal("批写失败后没有退回逐条写 ⇒ 整批事件会一起消失")
	}
	if m.DroppedEvents() != 0 {
		t.Fatalf("dropped = %d，期望 0（逐条写成功了 ⇒ 没有事件真的丢）", m.DroppedEvents())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) == 0 {
		t.Fatal("批写失败必须上报（否则操作者看不到这条路径出过事）")
	}
	if !strings.Contains(reports[0], "batch") {
		t.Fatalf("失败上报必须说清是批写失败，实际 %q", reports[0])
	}
}

// TestAuditWriteFailuresAreCounted 钉住 ②：
// 批写失败 + 逐条写也失败 ⇒ 丢失的每一条都必须计入 DroppedEvents。
//
// ★ 没有这一条 ⇒ 审计事件在写失败路径上彻底静默消失 ★。
func TestAuditWriteFailuresAreCounted(t *testing.T) {
	memory := NewMemoryStore()
	store := &failingBatchStore{Store: memory, eventErr: errors.New("db down")}
	var reports []string
	var mu sync.Mutex
	m, err := NewManager(store, testConfig(), Options{
		InstanceID: "batch-fail-count",
		QueueSize:  64,
		OnError: func(err error) {
			mu.Lock()
			reports = append(reports, err.Error())
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()

	const n = 20
	for i := 0; i < n; i++ {
		m.Record(ctx, Event{TenantID: "tenant-1", RuleID: "rule-1", Version: 1, Phase: "output", Action: "mask"})
	}
	waitForBatch(t, func() (bool, bool) {
		_, eventCalls := store.counts()
		return eventCalls > 0, eventCalls >= n
	}, "the per-event fallback to run")

	dropped := m.DroppedEvents()
	if dropped == 0 {
		t.Fatal("写失败丢掉的审计事件没有计数 ⇒ 管理面看不见这笔损失")
	}
	if got := m.Status().DroppedEvents; got != dropped {
		t.Fatalf("Status().DroppedEvents = %d，期望 %d（写失败的损失也必须在状态面上）", got, dropped)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, r := range reports {
		if strings.Contains(r, "db down") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("失败上报必须带**错误内容**（原先是一条没有细节的静态串），实际 %v", reports)
	}
}
