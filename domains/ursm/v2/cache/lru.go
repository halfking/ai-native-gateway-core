package cache

import (
	"container/list"
	"sync"
)

// LRU 是一个线程安全的泛型 LRU。零值不可用,必须用 NewLRU。
// 设计目标: 复刻 domains/session/v2/cache_v2.go 的同款实现,避免引入
// hashicorp/golang-lru(go.mod 无此依赖,详见设计稿 Decision 2)。
type LRU[K comparable, V any] struct {
	cap   int
	mu    sync.Mutex
	idx   map[K]*list.Element
	order *list.List // front = 最近使用;back = 待淘汰
}

type lruEntry[K comparable, V any] struct {
	key K
	val V
}

func NewLRU[K comparable, V any](cap int) *LRU[K, V] {
	if cap <= 0 {
		panic("cache: LRU capacity must be > 0")
	}
	return &LRU[K, V]{
		cap:   cap,
		idx:   make(map[K]*list.Element, cap),
		order: list.New(),
	}
}

// Get 返回 key 对应的值,并把该 key 提升到最近使用。ok=false 表示未命中。
func (l *LRU[K, V]) Get(key K) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero V
	el, ok := l.idx[key]
	if !ok {
		return zero, false
	}
	l.order.MoveToFront(el)
	return el.Value.(*lruEntry[K, V]).val, true
}

// Put 写入 key=val,超容量时淘汰最久未使用项。返回被淘汰的 key 与 ok=true。
func (l *LRU[K, V]) Put(key K, val V) (evictedKey K, evicted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.idx[key]; ok {
		el.Value.(*lruEntry[K, V]).val = val
		l.order.MoveToFront(el)
		var zero K
		return zero, false
	}
	el := l.order.PushFront(&lruEntry[K, V]{key: key, val: val})
	l.idx[key] = el
	if l.order.Len() > l.cap {
		back := l.order.Back()
		if back != nil {
			entry := back.Value.(*lruEntry[K, V])
			l.order.Remove(back)
			delete(l.idx, entry.key)
			return entry.key, true
		}
	}
	var zero K
	return zero, false
}

// Peek 返回值但不提升 LRU 顺序。用于内部比较(如 gen 单调检查)。
func (l *LRU[K, V]) Peek(key K) (V, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var zero V
	el, ok := l.idx[key]
	if !ok {
		return zero, false
	}
	return el.Value.(*lruEntry[K, V]).val, true
}

func (l *LRU[K, V]) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}
