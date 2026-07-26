package cache

import "testing"

func TestLRUGetPutEvict(t *testing.T) {
	l := NewLRU[string, int](2)
	l.Put("a", 1)
	l.Put("b", 2)
	if v, ok := l.Get("a"); !ok || v != 1 {
		t.Fatalf("expected a=1, got %v,%v", v, ok)
	}
	// 访问 a 后再插 c,淘汰的应是 b(LRU),不是 a
	l.Put("c", 3)
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
