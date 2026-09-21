package modelquality

import "sync"

// NodeInFlightGate serializes benchmark runs per credential/model node across
// all trigger sources (scheduled, manual, anomaly, startup, and ticker).
type NodeInFlightGate struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func NewNodeInFlightGate() *NodeInFlightGate {
	return &NodeInFlightGate{m: make(map[string]struct{})}
}

func (g *NodeInFlightGate) TryAcquire(provider, model string, credentialID int) (release func(), ok bool) {
	if g == nil {
		return func() {}, true
	}
	key := nodeKey(provider, model, credentialID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, exists := g.m[key]; exists {
		return nil, false
	}
	g.m[key] = struct{}{}
	return func() { g.mu.Lock(); delete(g.m, key); g.mu.Unlock() }, true
}
