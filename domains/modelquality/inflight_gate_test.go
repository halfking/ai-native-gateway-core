package modelquality

import "testing"

func TestNodeInFlightGateSerializesNodeAcrossTriggers(t *testing.T) {
	g := NewNodeInFlightGate()
	release, ok := g.TryAcquire("provider", "model", 7)
	if !ok {
		t.Fatal("first acquire failed")
	}
	if _, ok := g.TryAcquire("provider", "model", 7); ok {
		t.Fatal("duplicate node acquire succeeded")
	}
	if _, ok := g.TryAcquire("provider", "model", 8); !ok {
		t.Fatal("different credential should acquire")
	}
	release()
	if release2, ok := g.TryAcquire("provider", "model", 7); !ok {
		t.Fatal("node not released")
	} else {
		release2()
	}
}
