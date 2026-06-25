// Package eventbus 提供进程内事件发布/订阅基础设施。
// 这是领域驱动重构的核心组件之一，所有领域通过事件解耦。
package eventbus

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Event 事件接口。
// 所有发布到事件总线的事件必须实现这两个方法。
type Event interface {
	// Type 返回事件类型字符串，作为订阅路由 key。
	Type() string
	// Timestamp 返回事件创建时间，用于排序和延迟分析。
	Timestamp() time.Time
}

// Handler 事件处理器签名。
// 接收 context 用于传递取消/超时信号。
type Handler func(ctx context.Context, event Event) error

// MemoryBus 进程内事件总线。
// 使用 buffered channel 异步分发，支持多订阅者，goroutine 并发安全。
type MemoryBus struct {
	mu          sync.RWMutex
	subscribers map[string][]Handler
	buffer      chan Event
	closed      bool
	wg          sync.WaitGroup
	stopCh      chan struct{}
}

// NewMemoryBus 创建一个新的内存事件总线。
// bufferSize 是事件缓冲区大小；缓冲区满时 Publish 返回 error（不阻塞）。
// 同时启动后台 dispatch goroutine。
func NewMemoryBus(bufferSize int) *MemoryBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	bus := &MemoryBus{
		subscribers: make(map[string][]Handler),
		buffer:      make(chan Event, bufferSize),
		stopCh:      make(chan struct{}),
	}
	bus.wg.Add(1)
	go bus.dispatch()
	return bus
}

// Subscribe 订阅指定类型的事件。
// 同一事件类型可注册多个 handler，按注册顺序串行调用（每次 Publish 时）。
func (b *MemoryBus) Subscribe(eventType string, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

// Publish 发布事件。
// 非阻塞：缓冲区满时立即返回 error，不丢弃。
func (b *MemoryBus) Publish(event Event) error {
	b.mu.RLock()
	closed := b.closed
	b.mu.RUnlock()
	if closed {
		return fmt.Errorf("eventbus: bus is closed")
	}

	select {
	case b.buffer <- event:
		return nil
	default:
		return fmt.Errorf("eventbus: buffer full (event type=%s)", event.Type())
	}
}

// Close 关闭事件总线，停止 dispatch goroutine。
// 等待所有已发布事件的 handler 执行完毕。
func (b *MemoryBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	close(b.stopCh)
	b.mu.Unlock()

	b.wg.Wait()
}

// dispatch 后台分发循环。
// 从 buffer 读取事件，查找订阅者，并发调用所有 handler。
// 任一 handler 返回 error 仅记录，不影响其他 handler。
func (b *MemoryBus) dispatch() {
	defer b.wg.Done()
	for {
		select {
		case event, ok := <-b.buffer:
			if !ok {
				return
			}
			b.dispatchEvent(event)
		case <-b.stopCh:
			// 处理剩余 buffer 中事件后退出
			for {
				select {
				case event, ok := <-b.buffer:
					if !ok {
						return
					}
					b.dispatchEvent(event)
				default:
					return
				}
			}
		}
	}
}

// dispatchEvent 分发单个事件到所有订阅者。
func (b *MemoryBus) dispatchEvent(event Event) {
	b.mu.RLock()
	handlers := make([]Handler, len(b.subscribers[event.Type()]))
	copy(handlers, b.subscribers[event.Type()])
	b.mu.RUnlock()

	for _, h := range handlers {
		go func(handler Handler, e Event) {
			ctx := context.Background()
			if err := handler(ctx, e); err != nil {
				log.Printf("eventbus: handler error for event type=%s: %v", e.Type(), err)
			}
		}(h, event)
	}
}

// SubscriberCount 返回指定事件类型的订阅者数量（用于测试）。
func (b *MemoryBus) SubscriberCount(eventType string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers[eventType])
}
