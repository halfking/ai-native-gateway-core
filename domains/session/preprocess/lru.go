package preprocess

import (
	"container/list"
	"sync"
)

// payloadLRU is the in-process L1 payload cache (R11.7): byte-bounded, LRU
// eviction re-run after every update, and every []byte crossing the boundary
// is deep-copied so external mutation can never reach the cached bytes
// (UT-SA-13).
type payloadLRU struct {
	mu      sync.Mutex
	budget  int64
	total   int64
	entries map[string]*list.Element
	order   *list.List // front = most recent
}

type lruEntry struct {
	key  string
	data []byte
}

func newPayloadLRU(budget int64) *payloadLRU {
	if budget <= 0 {
		return nil // L1 disabled
	}
	return &payloadLRU{
		budget:  budget,
		entries: make(map[string]*list.Element),
		order:   list.New(),
	}
}

// Get returns a deep copy of the cached payload, if present.
func (l *payloadLRU) Get(key string) ([]byte, bool) {
	if l == nil {
		return nil, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	el, ok := l.entries[key]
	if !ok {
		return nil, false
	}
	l.order.MoveToFront(el)
	return cloneBytes(el.Value.(*lruEntry).data), true
}

// Put stores a deep copy of val and re-runs byte-budget eviction (evict from
// the LRU tail while total exceeds the budget; the freshly inserted entry may
// itself be evicted when it alone exceeds the budget).
func (l *payloadLRU) Put(key string, val []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.total -= int64(len(el.Value.(*lruEntry).data))
		l.order.Remove(el)
		delete(l.entries, key)
	}
	cp := cloneBytes(val)
	l.entries[key] = l.order.PushFront(&lruEntry{key: key, data: cp})
	l.total += int64(len(cp))
	l.evictLocked()
}

func (l *payloadLRU) evictLocked() {
	for l.total > l.budget && l.order.Len() > 0 {
		tail := l.order.Back()
		if tail == nil {
			break
		}
		entry := tail.Value.(*lruEntry)
		l.total -= int64(len(entry.data))
		l.order.Remove(tail)
		delete(l.entries, entry.key)
	}
}

// Len returns the number of cached entries.
func (l *payloadLRU) Len() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.order.Len()
}

// TotalBytes returns the current byte usage.
func (l *payloadLRU) TotalBytes() int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

// Invalidate drops a single key.
func (l *payloadLRU) Invalidate(key string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if el, ok := l.entries[key]; ok {
		l.total -= int64(len(el.Value.(*lruEntry).data))
		l.order.Remove(el)
		delete(l.entries, key)
	}
}
