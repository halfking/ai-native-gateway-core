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
