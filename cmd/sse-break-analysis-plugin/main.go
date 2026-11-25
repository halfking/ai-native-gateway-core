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
	SchemaVersion int   `yaml:"schema_version"`
	DominantShare int64 `yaml:"minimum_dominant_share_percent"`
}

func main() {
	if err := child.Serve(remediation.SSEPluginID, configure); err != nil {
		log.Fatal(err)
	}
}

func configure(m *pluginruntime.Manifest) (http.HandlerFunc, error) {
	var p policy
	if err := child.ReadPolicy(filepath.Join(filepath.Dir(m.ManifestPath), "template.yaml"), &p); err != nil {
		return nil, err
	}
	if p.SchemaVersion != remediation.SchemaVersion || p.DominantShare < 51 || p.DominantShare > 100 {
		return nil, errors.New("invalid SSE analysis template")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := remediation.DecodeSSESignal(r.Body)
		if err != nil || !s.Valid() {
			http.Error(w, "invalid SSE aggregate", http.StatusBadRequest)
			return
		}
		// Round up without overflowing the request count.
		required := (s.DistinctRequests/100)*p.DominantShare + ((s.DistinctRequests%100)*p.DominantShare+99)/100
		action := remediation.ReviewMixed
		switch {
		case s.Upstream >= required && s.Upstream > max(s.IdleTimeout, s.Network):
			action = remediation.ReviewUpstream
		case s.IdleTimeout >= required && s.IdleTimeout > max(s.Upstream, s.Network):
			action = remediation.ReviewProxyIdle
		case s.Network >= required && s.Network > max(s.Upstream, s.IdleTimeout):
			action = remediation.ReviewNetwork
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remediation.Review{
			SchemaVersion: remediation.SchemaVersion, TemplateVersion: m.PluginVersion, Recommendation: action,
		})
	}, nil
}
