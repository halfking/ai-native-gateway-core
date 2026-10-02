package proxy

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// R28-P-1: the egress provider resolves providers.egress_profile through a
// TTL cache and hands proxy-profile providers the subscription node-pool
// transport; direct-profile providers keep the default path; a proxy-profile
// provider with no healthy node FAILS instead of dialing direct.

type fakeEgressDB struct {
	profile   string
	subID     *int
	err       error
	queryHits int
}

func (f *fakeEgressDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return &fakeEgressRow{db: f}
}

type fakeEgressRow struct{ db *fakeEgressDB }

func (r *fakeEgressRow) Scan(dest ...any) error {
	r.db.queryHits++
	if r.db.err != nil {
		return r.db.err
	}
	if p, ok := dest[0].(*string); ok {
		*p = r.db.profile
	}
	if idp, ok := dest[1].(**int); ok {
		*idp = r.db.subID
	}
	return nil
}

func newEgressManagerForTest(nodes []*Node) *Manager {
	return NewManager(&fakeStore{nodes: nodes}, nil, nil)
}

func TestEgressProviderDirectProfileUsesDefaultTransport(t *testing.T) {
	db := &fakeEgressDB{profile: "direct"}
	p := NewEgressProvider(newEgressManagerForTest(nil), db, 0)
	rt, err := p.TransportFor(context.Background(), 42)
	if err != nil || rt != nil {
		t.Fatalf("direct profile must keep default transport: rt=%v err=%v", rt, err)
	}
}

func TestEgressProviderProxyProfileRoutesThroughNodePool(t *testing.T) {
	subID := 7
	db := &fakeEgressDB{profile: "proxy", subID: &subID}
	mgr := newEgressManagerForTest([]*Node{
		{ID: 1, SubscriptionID: 7, Protocol: "http", Server: "127.0.0.1", Port: 8080, Status: "active"},
	})
	p := NewEgressProvider(mgr, db, 0)
	rt, err := p.TransportFor(context.Background(), 42)
	if err != nil {
		t.Fatalf("proxy profile with a node must return a transport: %v", err)
	}
	if rt == nil {
		t.Fatal("proxy profile must not fall back to the default transport")
	}
}

func TestEgressProviderProxyProfileFailsClosedWithoutNode(t *testing.T) {
	db := &fakeEgressDB{profile: "proxy"}
	p := NewEgressProvider(newEgressManagerForTest(nil), db, 0)
	if rt, err := p.TransportFor(context.Background(), 42); err == nil || rt != nil {
		t.Fatalf("proxy profile without a healthy node must fail closed: rt=%v err=%v", rt, err)
	}
}

func TestEgressProviderPolicyCacheHitsDBOnce(t *testing.T) {
	subID := 7
	db := &fakeEgressDB{profile: "proxy", subID: &subID}
	mgr := newEgressManagerForTest([]*Node{
		{ID: 1, SubscriptionID: 7, Protocol: "http", Server: "127.0.0.1", Port: 8080, Status: "active"},
	})
	p := NewEgressProvider(mgr, db, time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := p.TransportFor(context.Background(), 42); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if db.queryHits != 1 {
		t.Fatalf("policy cache must collapse repeat lookups: hits=%d", db.queryHits)
	}
	p.Invalidate(42)
	if _, err := p.TransportFor(context.Background(), 42); err != nil {
		t.Fatal(err)
	}
	if db.queryHits != 2 {
		t.Fatalf("invalidate must force a refresh: hits=%d", db.queryHits)
	}
}

func TestEgressProviderDBErrorFailsOpenToDirect(t *testing.T) {
	db := &fakeEgressDB{err: errors.New("db down")}
	p := NewEgressProvider(newEgressManagerForTest(nil), db, 0)
	rt, err := p.TransportFor(context.Background(), 42)
	if err != nil || rt != nil {
		t.Fatalf("db error must degrade to the legacy direct path: rt=%v err=%v", rt, err)
	}
}

var _ http.RoundTripper = (http.RoundTripper)(nil)
