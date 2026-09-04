// Package memory 提供双模式存储架构中 lite 模式的进程内 KV 状态存储，
// 用于替代 full 模式下的 Redis StateStore。
//
// 语义对齐 Redis：过期即不存在（Get 返回 storage.ErrNotFound），
// ErrExpired 哨兵在本实现中保留不用。
package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/kaixuan/llm-gateway-go/storage"
)

// defaultSweepInterval 后台过期清理协程的默认扫描间隔（30s ~ 1min 区间内取 30s）。
const defaultSweepInterval = 30 * time.Second

// errClosed 私有哨兵错误：存储 Close 之后调用 Set 返回该错误。
// Get 在 Close 后返回 storage.ErrNotFound（存储已关闭，视同全部不可见）；
// Delete 保持幂等，Close 后仍返回 nil。
var errClosed = errors.New("memory: state store closed")

// item 存储条目：值 + 过期时间。
type item struct {
	value      interface{}
	expiration time.Time // 永不过期时保存零值 time.Time
	hasExpiry  bool      // 是否设置了 TTL；与零值 expiration 搭配区分"永不过期"与"过期时间为零"
}

// expiredAt 判断条目在时刻 now 是否已过期。
func (it *item) expiredAt(now time.Time) bool {
	return it.hasExpiry && now.After(it.expiration)
}

// MemoryStateStore storage.StateStore 的内存实现（lite 模式替代 Redis 的 KV 存储）。
// 并发安全；后台协程定期清理过期条目，读路径也会惰性删除已过期条目。
type MemoryStateStore struct {
	mu            sync.RWMutex
	items         map[string]*item
	sweepInterval time.Duration // 后台清理扫描间隔
	stopCh        chan struct{} // 关闭以通知清理协程退出
	doneCh        chan struct{} // 清理协程退出后关闭，供 Close 等待
	closeOnce     sync.Once     // 保证 Close 幂等
	closed        bool          // 是否已关闭（由 mu 保护）
}

// 编译期断言：MemoryStateStore 必须实现 storage.StateStore。
var _ storage.StateStore = (*MemoryStateStore)(nil)

// NewMemoryStateStore 创建内存状态存储，并启动后台过期清理协程（默认 30s 扫描一次）。
func NewMemoryStateStore() *MemoryStateStore {
	return NewMemoryStateStoreWithInterval(defaultSweepInterval)
}

// NewMemoryStateStoreWithInterval 创建内存状态存储，并指定后台清理扫描间隔。
// interval <= 0 时使用默认间隔（30s）。主要为可测试性而暴露，便于测试用短间隔验证过期回收。
func NewMemoryStateStoreWithInterval(interval time.Duration) *MemoryStateStore {
	if interval <= 0 {
		interval = defaultSweepInterval
	}
	s := &MemoryStateStore{
		items:         make(map[string]*item),
		sweepInterval: interval,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
	go s.janitor()
	return s
}

// janitor 后台清理协程：按 sweepInterval 周期扫描并删除过期条目，Close 后退出。
func (s *MemoryStateStore) janitor() {
	defer close(s.doneCh)
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.sweep(time.Now())
		case <-s.stopCh:
			return
		}
	}
}

// sweep 删除当前所有已过期条目（调用方保证 sweepInterval 合理，本方法自身加写锁）。
func (s *MemoryStateStore) sweep(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, it := range s.items {
		if it.expiredAt(now) {
			delete(s.items, key)
		}
	}
}

// Set 写入键值。ttl <= 0 表示永不过期。
// 存储已关闭时返回 errClosed。
func (s *MemoryStateStore) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	it := &item{value: value}
	if ttl > 0 {
		it.expiration = time.Now().Add(ttl)
		it.hasExpiry = true
	}
	s.items[key] = it
	return nil
}

// Get 读取键值。采用 RWMutex 双检：读锁快路径未过期直接返回；
// 已过期则升级写锁二次确认后惰性删除（避免误删并发 Set 写入的新值），
// 并按 Redis 语义返回 storage.ErrNotFound（过期即不存在，不返回 ErrExpired）。
func (s *MemoryStateStore) Get(ctx context.Context, key string) (interface{}, error) {
	s.mu.RLock()
	if s.closed {
		// 存储已关闭，视同全部条目不可见。
		s.mu.RUnlock()
		return nil, storage.ErrNotFound
	}
	it, ok := s.items[key]
	if !ok {
		s.mu.RUnlock()
		return nil, storage.ErrNotFound
	}
	if !it.expiredAt(time.Now()) {
		v := it.value
		s.mu.RUnlock()
		return v, nil
	}
	s.mu.RUnlock()

	// 慢路径：条目已过期，加写锁双检后删除。
	s.mu.Lock()
	cur, ok := s.items[key]
	// 指针相等说明仍是刚才读到的过期条目才删除；若已被并发 Set 覆盖为新条目则不动它。
	if ok && cur == it {
		delete(s.items, key)
	}
	s.mu.Unlock()
	return nil, storage.ErrNotFound
}

// Delete 删除键值。键不存在时同样返回 nil（幂等）。
func (s *MemoryStateStore) Delete(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, key)
	return nil
}

// Len 返回当前条目数量（含尚未被清理的已过期条目），用于测试与监控。
func (s *MemoryStateStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.items)
}

// Close 停止后台清理协程。幂等：重复 Close 不 panic、无副作用，永远返回 nil。
// Close 后 Set 返回 errClosed，Get 返回 storage.ErrNotFound，Delete 保持幂等返回 nil。
func (s *MemoryStateStore) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.stopCh)
		<-s.doneCh
	})
	return nil
}
