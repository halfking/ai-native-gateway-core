package pluginruntime

import (
	"errors"
	"sync"
	"testing"
)

type memDriver struct {
	mu       sync.Mutex
	applied  []string
	disposed []string
	failOn   map[string]error
	panicOn  string
}

func (d *memDriver) Apply(m *Manifest, _ map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.panicOn != "" && m.PluginID == d.panicOn {
		panic("boom")
	}
	if err, ok := d.failOn[m.PluginID]; ok {
		return err
	}
	d.applied = append(d.applied, m.PluginID)
	return nil
}

func (d *memDriver) Dispose(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.disposed = append(d.disposed, id)
	return nil
}

func manifests(ids ...string) []*Manifest {
	out := make([]*Manifest, 0, len(ids))
	for _, id := range ids {
		out = append(out, &Manifest{PluginID: id, API: "^1"})
	}
	return out
}

func TestReconcile_ReverseDisposeAndCascade(t *testing.T) {
	a := &Manifest{PluginID: "a", API: "^1", Provides: []string{"svc.a"}}
	b := &Manifest{PluginID: "b", API: "^1", Requires: []string{"svc.a"}}
	d := &memDriver{}
	rt := NewReconciler([]*Manifest{a, b}, d)
	rt.Reconcile(map[string]bool{"a": true, "b": true}, nil)
	if !rt.Active("a") || !rt.Active("b") {
		t.Fatalf("want both active, got %+v", rt.Statuses())
	}
	// turn a off → b must dispose first (depends on a), then a
	rt.Reconcile(map[string]bool{"a": false, "b": true}, nil)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.disposed) < 2 {
		t.Fatalf("disposed=%v want b then a", d.disposed)
	}
	// b disposed before a
	bi, ai := -1, -1
	for i, id := range d.disposed {
		if id == "b" && bi < 0 {
			bi = i
		}
		if id == "a" && ai < 0 {
			ai = i
		}
	}
	if !(bi >= 0 && ai >= 0 && bi < ai) {
		t.Fatalf("dispose order %v: want b before a", d.disposed)
	}
	if rt.Active("b") {
		t.Fatal("b should not stay active without svc.a")
	}
}

func TestReconcile_PendingUntilProvides(t *testing.T) {
	a := &Manifest{PluginID: "prov", API: "^1", Provides: []string{"router.groups"}}
	b := &Manifest{PluginID: "cons", API: "^1", Requires: []string{"router.groups"}}
	d := &memDriver{}
	rt := NewReconciler([]*Manifest{a, b}, d)
	rt.Reconcile(map[string]bool{"prov": false, "cons": true}, nil)
	st := map[string]LifecycleStatus{}
	for _, s := range rt.Statuses() {
		st[s.ID] = s
	}
	if st["cons"].State != LifePending {
		t.Fatalf("cons=%+v want pending", st["cons"])
	}
	rt.Reconcile(map[string]bool{"prov": true, "cons": true}, nil)
	if !rt.Active("cons") || !rt.Active("prov") {
		t.Fatalf("want both active after provide: %+v", rt.Statuses())
	}
}

func TestReconcile_FailedIsolated(t *testing.T) {
	good := &Manifest{PluginID: "good", API: "^1"}
	bad := &Manifest{PluginID: "bad", API: "^1"}
	d := &memDriver{failOn: map[string]error{"bad": errors.New("nope")}}
	rt := NewReconciler([]*Manifest{good, bad}, d)
	rt.Reconcile(map[string]bool{"good": true, "bad": true}, nil)
	if !rt.Active("good") {
		t.Fatal("good should stay active")
	}
	for _, s := range rt.Statuses() {
		if s.ID == "bad" && s.State != LifeFailed {
			t.Fatalf("bad=%+v want failed", s)
		}
	}
}

func TestReconcile_APIMismatchIsolated(t *testing.T) {
	ok := &Manifest{PluginID: "ok", API: "^1"}
	no := &Manifest{PluginID: "no", API: "^9"}
	d := &memDriver{}
	rt := NewReconciler([]*Manifest{ok, no}, d)
	rt.Reconcile(map[string]bool{"ok": true, "no": true}, nil)
	if !rt.Active("ok") {
		t.Fatal("ok should be active")
	}
	for _, s := range rt.Statuses() {
		if s.ID == "no" && s.State != LifeFailed {
			t.Fatalf("no=%+v want failed", s)
		}
	}
}

func TestReconcile_PanicIsolated(t *testing.T) {
	ok := &Manifest{PluginID: "ok", API: "^1"}
	boom := &Manifest{PluginID: "boom", API: "^1"}
	d := &memDriver{panicOn: "boom"}
	rt := NewReconciler([]*Manifest{ok, boom}, d)
	rt.Reconcile(map[string]bool{"ok": true, "boom": true}, nil)
	if !rt.Active("ok") {
		t.Fatal("ok should survive neighbour panic")
	}
}

func TestCompatible(t *testing.T) {
	if !Compatible("") || !Compatible("^1") || !Compatible("^1.0") {
		t.Fatal("host 1.0 should accept ^1")
	}
	if Compatible("^2") || Compatible("1") || Compatible("^9") {
		t.Fatal("should reject mismatched ranges")
	}
}
