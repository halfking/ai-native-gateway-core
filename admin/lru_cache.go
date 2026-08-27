package admin

import (
	"container/list"
	"sync"
)

// lruCache is a simple LRU cache with a fixed capacity.
// It's designed for the actionTenantIndex use case where we need
// bounded memory with better eviction strategy than random deletion.
type lruCache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*list.Element
	evictList *list.List
}

type lruEntry struct {
	key   string
	value string
}

// newLRUCache creates a new LRU cache with the given capacity.
func newLRUCache(capacity int) *lruCache {
	return &lruCache{
		capacity:  capacity,
		items:     make(map[string]*list.Element),
		evictList: list.New(),
	}
}

// Set adds or updates a key-value pair. If the cache is at capacity,
// the least recently used item is evicted.
func (c *lruCache) Set(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If key exists, move to front and update value
	if elem, ok := c.items[key]; ok {
		c.evictList.MoveToFront(elem)
		elem.Value.(*lruEntry).value = value
		return
	}

	// Add new entry
	entry := &lruEntry{key: key, value: value}
	elem := c.evictList.PushFront(entry)
	c.items[key] = elem

	// Evict if over capacity
	if c.evictList.Len() > c.capacity {
		c.evictOldest()
	}
}

// Get retrieves a value by key. Returns the value and true if found,
// or empty string and false if not found. Marks the item as recently used.
func (c *lruCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return "", false
	}

	// Move to front (most recently used)
	c.evictList.MoveToFront(elem)
	return elem.Value.(*lruEntry).value, true
}

// Delete removes a key from the cache.
func (c *lruCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.removeElement(elem)
	}
}

// Len returns the current number of items in the cache.
func (c *lruCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.evictList.Len()
}

// evictOldest removes the least recently used item.
func (c *lruCache) evictOldest() {
	elem := c.evictList.Back()
	if elem != nil {
		c.removeElement(elem)
	}
}

// removeElement removes an element from the cache.
func (c *lruCache) removeElement(elem *list.Element) {
	c.evictList.Remove(elem)
	entry := elem.Value.(*lruEntry)
	delete(c.items, entry.key)
}
