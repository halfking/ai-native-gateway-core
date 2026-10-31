package pluginruntime

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// plugins.yml is the desired-state SSOT for which plugins are on and each
// one's config (magpie semantics). Path is beside the plugins install dir
// unless overridden.
//
//	preset: all          # or essentials
//	plugins:
//	  - id: hello
//	    enabled: true
//	    config: {}

const (
	PresetEssentials = "essentials"
	PresetAll        = "all"
)

// PluginsFile is plugins.yml as written.
type PluginsFile struct {
	Preset  string       `yaml:"preset,omitempty"`
	Plugins []PluginsRow `yaml:"plugins,omitempty"`
}

// PluginsRow is the operator's say about one plugin.
type PluginsRow struct {
	ID      string         `yaml:"id"`
	Enabled *bool          `yaml:"enabled,omitempty"`
	Config  map[string]any `yaml:"config,omitempty"`
}

// DefaultPluginsYMLPath returns <pluginsDir>/../plugins.yml (sibling of install root).
func DefaultPluginsYMLPath(pluginsDir string) string {
	return filepath.Join(filepath.Dir(pluginsDir), "plugins.yml")
}

// LoadPluginsFile reads path. Missing file → (empty, false, nil).
func LoadPluginsFile(path string) (PluginsFile, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return PluginsFile{}, false, nil
	}
	if err != nil {
		return PluginsFile{}, true, err
	}
	var f PluginsFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return PluginsFile{}, true, fmt.Errorf("%s doesn't parse: %w", path, err)
	}
	switch f.Preset {
	case "", PresetAll, PresetEssentials:
	default:
		return PluginsFile{}, true, fmt.Errorf("%s: no preset %q (essentials or all)", path, f.Preset)
	}
	return f, true, nil
}

// ComposedPlugin is one plugin after Defaults ⊕ preset ⊕ user rows.
type ComposedPlugin struct {
	ID      string         `json:"id"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config,omitempty"`
}

// ComposeDesired layers file over manifests. Missing file / empty preset ⇒ all on.
func ComposeDesired(ms []*Manifest, f PluginsFile) []ComposedPlugin {
	rows := map[string]PluginsRow{}
	for _, r := range f.Plugins {
		rows[r.ID] = r
	}
	out := make([]ComposedPlugin, 0, len(ms))
	for _, m := range ms {
		if m == nil {
			continue
		}
		c := ComposedPlugin{ID: m.PluginID, Enabled: true, Config: map[string]any{}}
		for k, v := range m.Defaults {
			c.Config[k] = v
		}
		if f.Preset == PresetEssentials && !m.Essential {
			c.Enabled = false
		}
		if r, ok := rows[m.PluginID]; ok {
			if r.Enabled != nil {
				c.Enabled = *r.Enabled
			}
			for k, v := range r.Config {
				if s := m.EffectiveSettingsSchema(); s != nil {
					okMap, err := s.Validate(map[string]any{k: v})
					if err != nil {
						slog.Warn("plugins.yml setting refused", "plugin", m.PluginID, "key", k, "error", err)
						continue
					}
					v = okMap[k]
				}
				c.Config[k] = mergeAny(c.Config[k], v)
			}
		}
		if len(c.Config) == 0 {
			c.Config = nil
		}
		out = append(out, c)
	}
	return out
}

func mergeAny(under, over any) any {
	u, ok1 := under.(map[string]any)
	o, ok2 := over.(map[string]any)
	if !ok1 || !ok2 {
		return over
	}
	out := maps.Clone(u)
	for k, v := range o {
		out[k] = mergeAny(out[k], v)
	}
	return out
}

// DesiredMaps splits ComposeDesired into Reconcile's on/config maps.
func DesiredMaps(cs []ComposedPlugin) (map[string]bool, map[string]map[string]any) {
	on := map[string]bool{}
	cfg := map[string]map[string]any{}
	for _, c := range cs {
		on[c.ID] = c.Enabled
		if c.Config != nil {
			cfg[c.ID] = c.Config
		}
	}
	return on, cfg
}
