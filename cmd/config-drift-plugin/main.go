package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"

	pluginruntime "github.com/kaixuan/llm-gateway-go/plugin-runtime"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation"
	"github.com/kaixuan/llm-gateway-go/plugin-runtime/remediation/child"
)

type policy struct {
	SchemaVersion int    `yaml:"schema_version"`
	InvalidConfig string `yaml:"invalid_config"`
	ChangedConfig string `yaml:"unapplied_config"`
}

func main() {
	if err := child.Serve(remediation.DriftPluginID, configure); err != nil {
		log.Fatal(err)
	}
}

func configure(m *pluginruntime.Manifest) (http.HandlerFunc, error) {
	var p policy
	if err := child.ReadPolicy(filepath.Join(filepath.Dir(m.ManifestPath), "template.yaml"), &p); err != nil {
		return nil, err
	}
	if p.SchemaVersion != remediation.SchemaVersion || p.InvalidConfig != remediation.RestoreConfig ||
		p.ChangedConfig != remediation.ReviewConfig {
		return nil, errors.New("invalid config drift template")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := remediation.DecodeDriftSignal(r.Body)
		if err != nil || !s.Valid() {
			http.Error(w, "invalid config drift signal", http.StatusBadRequest)
			return
		}
		action := p.ChangedConfig
		if s.Kind == remediation.ConfigInvalid {
			action = p.InvalidConfig
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remediation.Review{
			SchemaVersion: remediation.SchemaVersion, TemplateVersion: m.PluginVersion, Recommendation: action,
		})
	}, nil
}
