package cache

import (
	"sync"
	"testing"
)

func TestLRUGetPutEvict(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	l.Put("b", 2)
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatalf("expected a=1, got %v,%v", v, ok)
	}
	// 访问 a 后再插 c,淘汰的应是 b(LRU),不是 a
	evictedKey, evicted := l.Put("c", 3)
	if !evicted || evictedKey != "b" {
		t.Fatalf("expected b to be evicted, got key=%q evicted=%v", evictedKey, evicted)
	}
	if _, ok := l.Get("b"); ok {
		t.Fatal("b should have been evicted")
	}
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatalf("a evicted incorrectly: %v,%v", v, ok)
	}
	if v, ok := l.Get("c"); !ok || v != 3 {
		t.Fatalf("c missing: %v,%v", v, ok)
	}
}

func TestLRUConcurrent(t *testing.T) {
	l := NewLRU[int, int](100)
	done := make(chan struct{})
	for g := 0; g < 10; g++ {
		go func(off int) {
			for i := 0; i < 1000; i++ {
				l.Put(off*1000+i, i)
				_, _ = l.Get(off*1000 + i)
			}
			done <- struct{}{}
		}(g)
	}
	for g := 0; g < 10; g++ {
		<-done
	}
	if l.Len() > 100 {
		t.Fatalf("len %d exceeded capacity 100", l.Len())
	}
}

func TestLRUCap1(t *testing.T) {
	l := NewLRU[string, int](1)
	l.Put("a", 1)
	evictedKey, evicted := l.Put("b", 2)
	if !evicted || evictedKey != "a" {
		t.Fatalf("expected a evicted, got key=%q evicted=%v", evictedKey, evicted)
	}
	if l.Len() != 1 {
		t.Fatalf("expected len=1, got %d", l.Len())
	}
	if _, ok := l.Get("a"); ok {
		t.Fatal("a should have been evicted")
	}
	if v, ok := l.Get("b"); !ok || v != 2 {
		t.Fatalf("expected b=2, got %v,%v", v, ok)
	}
}

func TestLRUUpdate(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	l.Update("a", func(old int, exists bool) (int, bool) {
		if !exists || old != 1 {
			t.Fatalf("fn expected old=1 exists=true, got old=%d exists=%v", old, exists)
		}
		return old + 1, true
	})
	if v, ok := l.Get("a"); !ok || v != 2 {
		t.Fatalf("after Update(+1), expected a=2, got %v,%v", v, ok)
	}
	// write=false: 不改变值
	l.Update("a", func(old int, exists bool) (int, bool) {
		return 0, false
	})
	if v, ok := l.Get("a"); !ok || v != 2 {
		t.Fatalf("after no-op Update, expected a still ==2, got %v,%v", v, ok)
	}
	// Update 对不存在的 key 写入
	l.Update("x", func(old int, exists bool) (int, bool) {
		if exists {
			t.Fatalf("expected exists=false for missing key, got old=%d", old)
		}
		return 42, true
	})
	if v, ok := l.Get("x"); !ok || v != 42 {
		t.Fatalf("expected x=42, got %v,%v", v, ok)
	}
}

func TestLRUUpdateAtomic(t *testing.T) {
	// 10 goroutine × 1000 次自增, 证明 Update 的 RMW 在锁内原子执行、无丢失更新。
	// 若非原子, 最终值会 < 10000。
	l := NewLRU[int, int](64)
	l.Put(0, 0)
	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				l.Update(0, func(old int, exists bool) (int, bool) {
					return old + 1, true
				})
			}
		}()
	}
	wg.Wait()
	v, ok := l.Get(0)
	if !ok {
		t.Fatal("key 0 missing")
	}
	if v != 10000 {
		t.Fatalf("expected 10000 (no lost updates), got %d", v)
	}
}

func TestLRUPeekNoPromote(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	l.Put("b", 2)
	if v, ok := l.Peek("a"); !ok || v != 1 {
		t.Fatalf("Peek a expected 1, got %v,%v", v, ok)
	}
	// Peek 不应提升 a; 插入 c 应淘汰 a (最久未使用) 而非 b
	l.Put("c", 3)
	if _, ok := l.Get("a"); ok {
		t.Fatal("a should have been evicted (Peek must not promote)")
	}
	if v, ok := l.Get("b"); !ok || v != 2 {
		t.Fatalf("expected b=2, got %v,%v", v, ok)
	}
	if v, ok := l.Get("c"); !ok || v != 3 {
		t.Fatalf("expected c=3, got %v,%v", v, ok)
	}
}

func TestLRUMissing(t *testing.T) {
	l := NewLRU[string, int](2)
	if v, ok := l.Get("x"); ok || v != 0 {
		t.Fatalf("Get missing expected (0,false), got (%v,%v)", v, ok)
	}
	if v, ok := l.Peek("x"); ok || v != 0 {
		t.Fatalf("Peek missing expected (0,false), got (%v,%v)", v, ok)
	}
	if del := l.Delete("x"); del {
		t.Fatal("Delete missing expected false")
	}
}

func TestLRUDelete(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	if !l.Delete("a") {
		t.Fatal("Delete existing key expected true")
	}
	if _, ok := l.Get("a"); ok {
		t.Fatal("a should be gone after Delete")
	}
	if l.Delete("a") {
		t.Fatal("second Delete expected false")
	}
}
