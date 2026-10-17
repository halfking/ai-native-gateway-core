package pluginruntime

import "testing"

func TestRegistry_RegisterAndList(t *testing.T) {
	r := NewRegistry()
	r.SetPlugin(&PluginState{PluginID: "p1", PluginVersion: "0.1", Status: "ready"})
	r.SetNav("p1", "0.1", []Page{
		{Path: "sessions", Type: "data", Nav: &Nav{Group: "requests-sessions", LabelKey: "nav.item.sessions", Order: 100}},
		{Path: "settings", Type: "settings", Nav: &Nav{Group: "data-ops", LabelKey: "nav.item.x", Super: true, Order: 110}},
		{Path: "sessions/:id", Type: "data", Nav: nil},
	})

	entries := r.NavEntries(ViewerOpts{IsSuper: false, IsPlatformOps: false, IsTenantPortal: true})
	if len(entries) != 1 || entries[0].PagePath != "sessions" {
		t.Fatalf("entries = %+v", entries)
	}

	entries = r.NavEntries(ViewerOpts{IsSuper: true, IsPlatformOps: true})
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestRegistry_DeactivateHidesNav(t *testing.T) {
	r := NewRegistry()
	r.SetPlugin(&PluginState{PluginID: "p1", Status: "ready"})
	r.SetNav("p1", "0.1", []Page{{Path: "s", Type: "data", Nav: &Nav{Group: "g", LabelKey: "k"}}})
	r.SetPluginStatus("p1", "degraded")
	if got := r.NavEntries(ViewerOpts{IsSuper: true}); len(got) != 0 {
		t.Fatalf("degraded plugin should hide nav, got %+v", got)
	}
}

// Snapshot 与 NavEntries 的关键差异：运维视图必须看到非 ready 插件
// （failed/degraded 恰恰是最需要被看到的状态），且返回拷贝 ——
// 改 Snapshot 的返回值不得影响 registry 内部状态。
func TestRegistry_SnapshotKeepsNonReadyAndCopies(t *testing.T) {
	r := NewRegistry()
	r.SetPlugin(&PluginState{PluginID: "p1", Status: "ready"})
	r.SetPlugin(&PluginState{PluginID: "p2", Status: "failed"})

	snap := r.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot should keep failed plugins, got %+v", snap)
	}
	statuses := map[string]string{}
	for _, st := range snap {
		statuses[st.PluginID] = st.Status
	}
	if statuses["p2"] != "failed" {
		t.Fatalf("failed plugin must survive snapshot, got %+v", statuses)
	}

	// 拷贝语义：改快照不回写。
	snap[0].Status = "hacked"
	after := r.Snapshot()
	if len(after) != 2 {
		t.Fatalf("snapshot must be a copy, internal=%+v", after)
	}
	for _, st := range after {
		if st.Status == "hacked" {
			t.Fatalf("snapshot leaked internal state: %+v", after)
		}
	}

	var nilReg *Registry
	if nilReg.Snapshot() != nil {
		t.Fatalf("nil registry snapshot must be nil")
	}
}
