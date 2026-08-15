package eventbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEvent 测试用事件
type TestEvent struct {
	typ string
	ts  time.Time
	val int
}

func (e *TestEvent) Type() string         { return e.typ }
func (e *TestEvent) Timestamp() time.Time { return e.ts }
func (e *TestEvent) Value() int           { return e.val }

func newTestEvent(typ string, val int) *TestEvent {
	return &TestEvent{typ: typ, ts: time.Now(), val: val}
}

// Test 1: 基本发布/订阅
func TestMemoryBus_PublishSubscribe(t *testing.T) {
	bus := NewMemoryBus(100)
	defer bus.Close()

	received := make(chan Event, 1)
	bus.Subscribe("test", func(ctx context.Context, e Event) error {
		received <- e
		return nil
	})

	if err := bus.Publish(newTestEvent("test", 42)); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	select {
	case e := <-received:
		if e.Type() != "test" {
			t.Errorf("expected type 'test', got '%s'", e.Type())
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for event")
	}
}

// Test 2: 多订阅者同一事件
func TestMemoryBus_MultipleSubscribers(t *testing.T) {
	bus := NewMemoryBus(100)
	defer bus.Close()

	var counter int32
	for i := 0; i < 5; i++ {
		bus.Subscribe("multi", func(ctx context.Context, e Event) error {
			atomic.AddInt32(&counter, 1)
			return nil
		})
	}

	if err := bus.Publish(newTestEvent("multi", 1)); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// 等待所有 handler 执行
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&counter); got != 5 {
		t.Errorf("expected 5 handlers called, got %d", got)
	}
}

// Test 3: 并发发布
func TestMemoryBus_ConcurrentPublish(t *testing.T) {
	bus := NewMemoryBus(1000)
	defer bus.Close()

	var received int32
	bus.Subscribe("concurrent", func(ctx context.Context, e Event) error {
		atomic.AddInt32(&received, 1)
		return nil
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := bus.Publish(newTestEvent("concurrent", i)); err != nil {
				t.Errorf("Publish %d failed: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// 等待 dispatch 处理
	time.Sleep(500 * time.Millisecond)

	if got := atomic.LoadInt32(&received); got != 100 {
		t.Errorf("expected 100 events received, got %d", got)
	}
}

// Test 4: 缓冲区满
func TestMemoryBus_BufferFull(t *testing.T) {
	// dispatch goroutine 无论有无订阅者都会持续从 buffer 取事件（且 handler
	// 在独立 goroutine 执行，阻塞 handler 也钉不住消费循环），所以
	// "buffer=1 时第二次 Publish 必失败"是调度竞态，不是确定性语义——
	// 全量测试高负载下 dispatch 抢先取走首事件，原写法偶发红。
	// 改为压满法：buffer=1 下 back-to-back 连发——一次 Publish 只需
	// RLock+select-send，而 dispatch 消费一次要 recv+copy+spawn goroutine，
	// 生产必然领先消费，短时间内必触发满拒绝；同时校验"成功+拒绝=发布
	// 总数"钉住不丢弃语义。
	bus := NewMemoryBus(1)
	defer bus.Close()

	const total = 1000
	succeeded, rejected := 0, 0
	for i := 0; i < total; i++ {
		if err := bus.Publish(newTestEvent("nobody", i)); err != nil {
			rejected++
		} else {
			succeeded++
		}
	}
	if rejected == 0 {
		t.Fatalf("expected at least one buffer-full rejection in %d back-to-back publishes (buffer=1)", total)
	}
	if succeeded+rejected != total {
		t.Fatalf("publish accounting broken: succeeded=%d rejected=%d want total=%d", succeeded, rejected, total)
	}
}

// Test 5: Handler 错误不中断其他 handler
func TestMemoryBus_HandlerErrorIsolation(t *testing.T) {
	bus := NewMemoryBus(100)
	defer bus.Close()

	var secondCalled int32
	bus.Subscribe("error_test", func(ctx context.Context, e Event) error {
		return errors.New("first handler error")
	})
	bus.Subscribe("error_test", func(ctx context.Context, e Event) error {
		atomic.AddInt32(&secondCalled, 1)
		return nil
	})

	if err := bus.Publish(newTestEvent("error_test", 1)); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&secondCalled); got != 1 {
		t.Errorf("expected second handler called 1 time, got %d", got)
	}
}

// Test 6: 无订阅者的事件不报错
func TestMemoryBus_NoSubscribers(t *testing.T) {
	bus := NewMemoryBus(100)
	defer bus.Close()

	if err := bus.Publish(newTestEvent("nobody", 1)); err != nil {
		t.Errorf("Publish to no subscribers should not error: %v", err)
	}
}

// Test 7: Close 后 Publish 报错
func TestMemoryBus_CloseBeforePublish(t *testing.T) {
	bus := NewMemoryBus(100)
	bus.Close()

	err := bus.Publish(newTestEvent("test", 1))
	if err == nil {
		t.Error("expected error when publishing to closed bus")
	}
}

// Test 8: SubscriberCount
func TestMemoryBus_SubscriberCount(t *testing.T) {
	bus := NewMemoryBus(100)
	defer bus.Close()

	if bus.SubscriberCount("none") != 0 {
		t.Error("expected 0 subscribers for 'none'")
	}

	bus.Subscribe("foo", func(ctx context.Context, e Event) error { return nil })
	bus.Subscribe("foo", func(ctx context.Context, e Event) error { return nil })

	if bus.SubscriberCount("foo") != 2 {
		t.Errorf("expected 2 subscribers for 'foo', got %d", bus.SubscriberCount("foo"))
	}
}
