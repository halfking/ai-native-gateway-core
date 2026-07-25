package logging

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestLockFreeQueueBasicOperations 测试基本操作
func TestLockFreeQueueBasicOperations(t *testing.T) {
	q := NewLockFreeQueue[int](16)

	// 入队
	for i := 0; i < 10; i++ {
		if !q.Enqueue(i) {
			t.Errorf("Failed to enqueue %d", i)
		}
	}

	// 检查大小
	if size := q.Size(); size != 10 {
		t.Errorf("Size = %d, want 10", size)
	}

	// 出队
	for i := 0; i < 10; i++ {
		val, ok := q.Dequeue()
		if !ok {
			t.Errorf("Failed to dequeue at %d", i)
		}
		if val != i {
			t.Errorf("Dequeued value = %d, want %d", val, i)
		}
	}

	// 队列应该为空
	if size := q.Size(); size != 0 {
		t.Errorf("Size after dequeue all = %d, want 0", size)
	}
}

// TestLockFreeQueueConcurrentEnqueue 测试并发入队
func TestLockFreeQueueConcurrentEnqueue(t *testing.T) {
	q := NewLockFreeQueue[int](16384) // 增大容量以容纳所有元素

	const goroutines = 100
	const itemsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(base int) {
			defer wg.Done()
			for j := 0; j < itemsPerGoroutine; j++ {
				q.Enqueue(base*itemsPerGoroutine + j)
			}
		}(i)
	}

	wg.Wait()

	// 验证所有元素都入队了
	expectedSize := int64(goroutines * itemsPerGoroutine)
	if size := q.Size(); size != expectedSize {
		t.Errorf("Size = %d, want %d", size, expectedSize)
	}

	stats := q.Stats()
	if stats.EnqueueCount != uint64(expectedSize) {
		t.Errorf("EnqueueCount = %d, want %d", stats.EnqueueCount, expectedSize)
	}
}

// TestLockFreeQueueConcurrentDequeue 测试并发出队
func TestLockFreeQueueConcurrentDequeue(t *testing.T) {
	q := NewLockFreeQueue[int](16384) // 增大容量

	// 先填充队列
	const totalItems = 10000
	for i := 0; i < totalItems; i++ {
		q.Enqueue(i)
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	dequeueCount := atomic.Int64{}

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for {
				_, ok := q.Dequeue()
				if !ok {
					break
				}
				dequeueCount.Add(1)
			}
		}()
	}

	wg.Wait()

	// 验证所有元素都出队了
	if count := dequeueCount.Load(); count != totalItems {
		t.Errorf("Dequeued %d items, want %d", count, totalItems)
	}

	if size := q.Size(); size != 0 {
		t.Errorf("Queue size = %d, want 0", size)
	}
}

// TestLockFreeQueueConcurrentMixed 测试并发混合操作
func TestLockFreeQueueConcurrentMixed(t *testing.T) {
	q := NewLockFreeQueue[int](65536) // 大容量以支持高并发

	const duration = 2 * time.Second
	done := make(chan struct{})

	var wg sync.WaitGroup
	enqueueCount := atomic.Int64{}
	dequeueCount := atomic.Int64{}

	// 启动生产者
	const producers = 20
	wg.Add(producers)
	for i := 0; i < producers; i++ {
		go func(id int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-done:
					return
				default:
					if q.Enqueue(id*1000000 + counter) {
						enqueueCount.Add(1)
						counter++
					}
					time.Sleep(time.Microsecond)
				}
			}
		}(i)
	}

	// 启动消费者
	const consumers = 20
	wg.Add(consumers)
	for i := 0; i < consumers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					// 清空剩余元素
					for {
						if _, ok := q.Dequeue(); !ok {
							break
						}
						dequeueCount.Add(1)
					}
					return
				default:
					if _, ok := q.Dequeue(); ok {
						dequeueCount.Add(1)
					}
					time.Sleep(time.Microsecond)
				}
			}
		}()
	}

	// 运行一段时间
	time.Sleep(duration)
	close(done)
	wg.Wait()

	// 验证入队和出队数量接近（允许小误差）
	enqueued := enqueueCount.Load()
	dequeued := dequeueCount.Load()

	t.Logf("Enqueued: %d, Dequeued: %d, Queue size: %d", enqueued, dequeued, q.Size())

	// 由于并发停止时可能有少量未处理的元素，允许小误差
	diff := enqueued - dequeued
	if diff < 0 {
		diff = -diff
	}

	// 允许最多 20 个元素的差异（20个生产者在停止时可能各有1个在途元素）
	if diff > 20 {
		t.Errorf("Too large difference: enqueued %d, dequeued %d, diff %d", enqueued, dequeued, diff)
	}
}

// TestLockFreeQueueOverflow 测试队列满的情况
func TestLockFreeQueueOverflow(t *testing.T) {
	q := NewLockFreeQueue[int](16)

	// 填满队列
	for i := 0; i < 16; i++ {
		if !q.Enqueue(i) {
			t.Errorf("Failed to enqueue %d when queue should not be full", i)
		}
	}

	// 尝试继续入队（应该失败）
	if q.Enqueue(99) {
		t.Error("Should not enqueue when queue is full")
	}

	stats := q.Stats()
	if stats.DropCount == 0 {
		t.Error("DropCount should be > 0 when queue overflows")
	}
}

// TestLockFreeQueueBatchDequeue 测试批量出队
func TestLockFreeQueueBatchDequeue(t *testing.T) {
	q := NewLockFreeQueue[int](128)

	// 入队50个元素
	for i := 0; i < 50; i++ {
		q.Enqueue(i)
	}

	// 批量出队20个
	batch := q.TryDequeueBatch(20)
	if len(batch) != 20 {
		t.Errorf("Batch size = %d, want 20", len(batch))
	}

	// 剩余30个
	if size := q.Size(); size != 30 {
		t.Errorf("Remaining size = %d, want 30", size)
	}

	// 批量出队50个（但只有30个）
	batch = q.TryDequeueBatch(50)
	if len(batch) != 30 {
		t.Errorf("Batch size = %d, want 30", len(batch))
	}

	// 队列应该为空
	if size := q.Size(); size != 0 {
		t.Errorf("Size = %d, want 0", size)
	}
}

// BenchmarkLockFreeQueueEnqueue 入队性能测试
func BenchmarkLockFreeQueueEnqueue(b *testing.B) {
	q := NewLockFreeQueue[int](65536)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			q.Enqueue(i)
			i++
		}
	})
}

// BenchmarkLockFreeQueueDequeue 出队性能测试
func BenchmarkLockFreeQueueDequeue(b *testing.B) {
	q := NewLockFreeQueue[int](65536)

	// 预填充队列
	for i := 0; i < 65536; i++ {
		q.Enqueue(i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			q.Dequeue()
		}
	})
}

// BenchmarkLockFreeQueueMixed 混合操作性能测试
func BenchmarkLockFreeQueueMixed(b *testing.B) {
	q := NewLockFreeQueue[int](65536)

	// 预填充一半
	for i := 0; i < 32768; i++ {
		q.Enqueue(i)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				q.Enqueue(i)
			} else {
				q.Dequeue()
			}
			i++
		}
	})
}

// TestAsyncRawDataLoggerConcurrency 测试异步日志记录器的并发安全
func TestAsyncRawDataLoggerConcurrency(t *testing.T) {
	// 使用临时目录
	tmpDir := t.TempDir()

	// 使用更大的队列容量以容纳所有日志
	logger, err := NewAsyncRawDataLogger(tmpDir, 10*1024*1024, true, 8192)
	if err != nil {
		t.Fatalf("Failed to create async logger: %v", err)
	}
	defer logger.Close()

	const goroutines = 50
	const logsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < logsPerGoroutine; j++ {
				requestID := fmt.Sprintf("req-%d-%d", id, j)
				logger.LogClientRequest(requestID, "openai-chat", []byte("test data"), nil, "pre_parse")
			}
		}(i)
	}

	wg.Wait()

	// 等待队列刷新
	time.Sleep(500 * time.Millisecond)

	stats := logger.Stats()
	t.Logf("Queue stats: Size=%d, EnqueueCount=%d, DropCount=%d",
		stats.Size, stats.EnqueueCount, stats.DropCount)

	// 验证大部分日志都入队了（允许少量丢弃）
	expectedTotal := uint64(goroutines * logsPerGoroutine)
	if stats.EnqueueCount < expectedTotal*95/100 {
		t.Errorf("Too many logs dropped: enqueued %d out of %d (%.1f%%)",
			stats.EnqueueCount, expectedTotal, float64(stats.EnqueueCount)*100/float64(expectedTotal))
	}
}
