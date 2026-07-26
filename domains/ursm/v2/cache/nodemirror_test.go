package cache

import (
	"testing"
	"time"
)

func TestNodeMirrorNewerGenerationWins(t *testing.T) {
	m := NewNodeMirror(100, 30*time.Second)
	old := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 10, Available: true}
	m.applyToLRU(old)
	// 迟到的旧 gen 不应覆盖
	stale := NodeView{CredentialID: 1, RawModel: "m", Generation: 4, SourcePriority: 20, Available: false}
	m.applyToLRU(stale)
	got, ok := m.Peek(1, "m")
	if !ok || !got.Available {
		t.Fatalf("stale gen overwrote newer: %+v ok=%v", got, ok)
	}
	if got.Generation != 5 {
		t.Fatalf("gen regressed to %d", got.Generation)
	}
	// 同 gen 但更高 priority 应覆盖
	newer := NodeView{CredentialID: 1, RawModel: "m", Generation: 5, SourcePriority: 30, Available: false}
	m.applyToLRU(newer)
	got, _ = m.Peek(1, "m")
	if got.Available {
		t.Fatalf("same-gen higher-pri did not overwrite: %+v", got)
	}
}

func TestNodeMirrorSoftExpiry(t *testing.T) {
	m := NewNodeMirror(100, 10*time.Millisecond)
	m.applyToLRU(NodeView{CredentialID: 1, RawModel: "m", Generation: 1, Available: true})
	if _, ok := m.Get(1, "m"); !ok {
		t.Fatal("fresh entry should hit")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := m.Get(1, "m"); ok {
		t.Fatal("soft-expired entry should miss")
	}
}

func TestNodeMirrorConcurrentApplyNoRegress(t *testing.T) {
	// 并发写入不同 generation, 最终应为最大 gen, Available 跟随最大 gen 的值
	m := NewNodeMirror(100, time.Minute)
	done := make(chan struct{})
	for g := 0; g < 20; g++ {
		go func(gen int64) {
			m.applyToLRU(NodeView{
				CredentialID: 1, RawModel: "m",
				Generation:     gen,
				SourcePriority: 10,
				Available:      gen%2 == 0,
			})
			done <- struct{}{}
		}(int64(g))
	}
	for g := 0; g < 20; g++ {
		<-done
	}
	got, ok := m.Peek(1, "m")
	if !ok {
		t.Fatal("no entry after concurrent writes")
	}
	if got.Generation != 19 {
		t.Fatalf("expected max gen 19, got %d", got.Generation)
	}
	// gen 19 is odd → Available false
	if got.Available {
		t.Fatalf("gen 19 should be Available=false, got true")
	}
}
