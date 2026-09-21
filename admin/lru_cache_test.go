package admin

import (
	"fmt"
	"testing"
)

func TestLRUCache_BasicOperations(t *testing.T) {
	cache := newLRUCache(3)

	// Test Set and Get
	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")

	if v, ok := cache.Get("a"); !ok || v != "1" {
		t.Errorf("Expected a=1, got %v, %v", v, ok)
	}
	if v, ok := cache.Get("b"); !ok || v != "2" {
		t.Errorf("Expected b=2, got %v, %v", v, ok)
	}
	if v, ok := cache.Get("c"); !ok || v != "3" {
		t.Errorf("Expected c=3, got %v, %v", v, ok)
	}

	// Test Len
	if cache.Len() != 3 {
		t.Errorf("Expected len=3, got %d", cache.Len())
	}
}

func TestLRUCache_Eviction(t *testing.T) {
	cache := newLRUCache(2)

	cache.Set("a", "1")
	cache.Set("b", "2")
	
	// This should evict "a" (least recently used)
	cache.Set("c", "3")

	if _, ok := cache.Get("a"); ok {
		t.Error("Expected 'a' to be evicted")
	}
	if v, ok := cache.Get("b"); !ok || v != "2" {
		t.Errorf("Expected b=2, got %v, %v", v, ok)
	}
	if v, ok := cache.Get("c"); !ok || v != "3" {
		t.Errorf("Expected c=3, got %v, %v", v, ok)
	}

	if cache.Len() != 2 {
		t.Errorf("Expected len=2, got %d", cache.Len())
	}
}

func TestLRUCache_LRUOrder(t *testing.T) {
	cache := newLRUCache(3)

	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")

	// Access "a" to make it recently used
	cache.Get("a")

	// Add "d" - should evict "b" (oldest, since "a" was just accessed)
	cache.Set("d", "4")

	if v, ok := cache.Get("a"); !ok || v != "1" {
		t.Error("Expected 'a' to still be present")
	}
	if _, ok := cache.Get("b"); ok {
		t.Error("Expected 'b' to be evicted")
	}
	if v, ok := cache.Get("c"); !ok || v != "3" {
		t.Error("Expected 'c' to still be present")
	}
	if v, ok := cache.Get("d"); !ok || v != "4" {
		t.Error("Expected 'd' to be present")
	}
}

func TestLRUCache_Update(t *testing.T) {
	cache := newLRUCache(2)

	cache.Set("a", "1")
	cache.Set("b", "2")

	// Update "a" with new value
	cache.Set("a", "100")

	if v, ok := cache.Get("a"); !ok || v != "100" {
		t.Errorf("Expected a=100, got %v, %v", v, ok)
	}

	// Add "c" - should evict "b" since "a" was just updated (made recent)
	cache.Set("c", "3")

	if v, ok := cache.Get("a"); !ok || v != "100" {
		t.Error("Expected 'a' to still be present")
	}
	if _, ok := cache.Get("b"); ok {
		t.Error("Expected 'b' to be evicted")
	}
	if v, ok := cache.Get("c"); !ok || v != "3" {
		t.Error("Expected 'c' to be present")
	}
}

func TestLRUCache_Delete(t *testing.T) {
	cache := newLRUCache(3)

	cache.Set("a", "1")
	cache.Set("b", "2")
	cache.Set("c", "3")

	cache.Delete("b")

	if _, ok := cache.Get("b"); ok {
		t.Error("Expected 'b' to be deleted")
	}
	if cache.Len() != 2 {
		t.Errorf("Expected len=2 after delete, got %d", cache.Len())
	}

	// Should be able to add new item without eviction now
	cache.Set("d", "4")
	if cache.Len() != 3 {
		t.Errorf("Expected len=3, got %d", cache.Len())
	}
}

func TestLRUCache_Empty(t *testing.T) {
	cache := newLRUCache(3)

	if _, ok := cache.Get("nonexistent"); ok {
		t.Error("Expected Get on empty cache to return false")
	}

	cache.Delete("nonexistent") // Should not panic

	if cache.Len() != 0 {
		t.Errorf("Expected len=0, got %d", cache.Len())
	}
}

func TestLRUCache_Concurrent(t *testing.T) {
	cache := newLRUCache(100)
	done := make(chan bool)

	// Concurrent writes
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.Set(key, fmt.Sprintf("val-%d", j))
			}
			done <- true
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				cache.Get(key)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 20; i++ {
		<-done
	}

	// Should not panic and should have at most 100 items
	if cache.Len() > 100 {
		t.Errorf("Expected len <= 100, got %d", cache.Len())
	}
}
