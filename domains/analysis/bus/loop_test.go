package bus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain/analysis"
)

// stubWorker 是 analysis.Worker 的最小实现。
type stubWorker struct {
	name      string
	types     []analysis.EventType
	handled   atomic.Int32
	failOnIDs map[string]bool
}

func (s *stubWorker) Name() string                          { return s.name }
func (s *stubWorker) SubscribedTypes() []analysis.EventType { return s.types }
func (s *stubWorker) Handle(_ context.Context, evt analysis.AnalysisEvent) error {
	if s.failOnIDs[evt.EventID] {
		return errors.New("forced failure for " + evt.EventID)
	}
	s.handled.Add(1)
	return nil
}

func TestRunLoop_NilArgs(t *testing.T) {
	// 全部 nil 应立即 return，不 panic。
	RunLoop(context.Background(), nil, nil, nil, LoopConfig{})
}

func TestRunLoop_PollsAndDispatches(t *testing.T) {
	w := &stubWorker{name: "w", types: []analysis.EventType{analysis.EventRequestCompleted}}
	events := []analysis.AnalysisEvent{
		{EventID: "e1", Type: analysis.EventRequestCompleted},
		{EventID: "e2", Type: analysis.EventRequestCompleted},
		{EventID: "e3", Type: analysis.EventSessionClosed}, // 类型不匹配也不会被 worker Handle；当前 stub 不过滤
	}
	polls := atomic.Int32{}
	poll := func(_ context.Context, _ int) ([]analysis.AnalysisEvent, error) {
		polls.Add(1)
		// 只返回 1 次后停止循环
		out := events
		events = nil
		return out, nil
	}
	marked := sync.Map{}
	mark := func(_ context.Context, eventID, workerName string, err error) error {
		marked.Store(eventID, markRecord{worker: workerName, err: err})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, w, poll, mark, LoopConfig{Interval: 5 * time.Millisecond})
		close(done)
	}()

	// 等到至少 1 次 poll 完成
	deadline := time.After(2 * time.Second)
	for polls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("never polled")
		case <-time.After(2 * time.Millisecond):
		}
	}

	// 等待 mark 落地（worker 处理 3 条 + poll 一次）
	deadline = time.After(2 * time.Second)
	for {
		count := 0
		marked.Range(func(_, _ any) bool { count++; return true })
		if count >= 3 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d marks recorded", count)
		case <-time.After(2 * time.Millisecond):
		}
	}

	cancel()
	<-done

	if got := w.handled.Load(); got < 3 {
		t.Fatalf("worker handled %d, want >= 3", got)
	}
}

func TestRunLoop_HandleFailureMarksError(t *testing.T) {
	w := &stubWorker{
		name:      "w",
		types:     []analysis.EventType{analysis.EventRequestCompleted},
		failOnIDs: map[string]bool{"bad": true},
	}
	events := []analysis.AnalysisEvent{
		{EventID: "bad", Type: analysis.EventRequestCompleted},
	}
	poll := func(_ context.Context, _ int) ([]analysis.AnalysisEvent, error) {
		out := events
		events = nil
		return out, nil
	}
	marked := sync.Map{}
	mark := func(_ context.Context, eventID, workerName string, err error) error {
		marked.Store(eventID, markRecord{worker: workerName, err: err})
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, w, poll, mark, LoopConfig{Interval: 5 * time.Millisecond})
		close(done)
	}()

	deadline := time.After(2 * time.Second)
	for {
		_, ok := marked.Load("bad")
		if ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("'bad' never marked")
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done

	v, _ := marked.Load("bad")
	rec := v.(markRecord)
	if rec.err == nil {
		t.Fatal("mark should record err for failed handle")
	}
}

func TestRunLoop_PollErrorContinues(t *testing.T) {
	w := &stubWorker{name: "w", types: []analysis.EventType{analysis.EventRequestCompleted}}
	polls := atomic.Int32{}
	poll := func(_ context.Context, _ int) ([]analysis.AnalysisEvent, error) {
		polls.Add(1)
		if polls.Load() == 1 {
			return nil, errors.New("transient db error")
		}
		return nil, nil
	}
	mark := func(_ context.Context, _, _ string, _ error) error { return nil }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunLoop(ctx, w, poll, mark, LoopConfig{Interval: 5 * time.Millisecond})
		close(done)
	}()
	deadline := time.After(2 * time.Second)
	for polls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("loop did not survive poll error")
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

type markRecord struct {
	worker string
	err    error
}
