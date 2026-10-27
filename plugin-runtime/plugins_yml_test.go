package pluginruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestComposeDesired_EssentialsAndUserOverlay(t *testing.T) {
	ms := []*Manifest{
		{PluginID: "core", Essential: true, Defaults: map[string]any{"n": 1}},
		{PluginID: "extra", Essential: false, Defaults: map[string]any{"n": 2}},
	}
	on := true
	f := PluginsFile{Preset: PresetEssentials, Plugins: []PluginsRow{
		{ID: "extra", Enabled: &on, Config: map[string]any{"n": 9}},
	}}
	cs := ComposeDesired(ms, f)
	by := map[string]ComposedPlugin{}
	for _, c := range cs {
		by[c.ID] = c
	}
	if !by["core"].Enabled || !by["extra"].Enabled {
		t.Fatalf("%+v", by)
	}
	if by["extra"].Config["n"] != 9 {
		t.Fatalf("overlay=%v", by["extra"].Config)
	}
}

func TestLoadPluginsFile_MissingOK(t *testing.T) {
	_, there, err := LoadPluginsFile(filepath.Join(t.TempDir(), "nope.yml"))
	if err != nil || there {
		t.Fatalf("there=%v err=%v", there, err)
	}
}

func TestLoadPluginsFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plugins.yml")
	if err := os.WriteFile(p, []byte("preset: all\nplugins:\n  - id: hello\n    enabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, there, err := LoadPluginsFile(p)
	if err != nil || !there || f.Preset != PresetAll || len(f.Plugins) != 1 || f.Plugins[0].Enabled == nil || *f.Plugins[0].Enabled {
		t.Fatalf("%+v there=%v err=%v", f, there, err)
	}
}
