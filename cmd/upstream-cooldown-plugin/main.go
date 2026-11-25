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
	SchemaVersion int `yaml:"schema_version"`
	Cooldown429   int `yaml:"cooldown_429_seconds"`
	Cooldown502   int `yaml:"cooldown_502_seconds"`
}

func main() {
	if err := child.Serve(remediation.PluginID, configure); err != nil {
		log.Fatal(err)
	}
}

func configure(manifest *pluginruntime.Manifest) (http.HandlerFunc, error) {
	var p policy
	if err := child.ReadPolicy(filepath.Join(filepath.Dir(manifest.ManifestPath), "template.yaml"), &p); err != nil {
		return nil, err
	}
	if p.SchemaVersion != remediation.SchemaVersion || !remediation.ValidCooldown(p.Cooldown429) || !remediation.ValidCooldown(p.Cooldown502) {
		return nil, errors.New("invalid cooldown template")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		signal, err := remediation.DecodeSignal(r.Body)
		if err != nil || !signal.Valid() {
			http.Error(w, "invalid aggregate signal", http.StatusBadRequest)
			return
		}
		var cooldown int
		if signal.Count429 > 0 {
			cooldown = p.Cooldown429
		}
		if signal.Count502 > 0 {
			cooldown = max(cooldown, p.Cooldown502)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remediation.Suggestion{
			SchemaVersion: remediation.SchemaVersion, TemplateVersion: manifest.PluginVersion,
			Recommendation: remediation.ReviewNextStep, CooldownSeconds: cooldown,
		})
	}, nil
}
