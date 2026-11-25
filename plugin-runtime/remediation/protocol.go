// Package remediation defines the gateway-to-plugin recommendation protocol.
package remediation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	PluginID       = "upstream-cooldown"
	SuggestPath    = "/internal/remediation/v1/suggest"
	SchemaVersion  = 1
	Window         = 5 * time.Minute
	MinRequests    = 3
	MaxPayload     = 1024
	MaxCooldown    = 900
	ReviewNextStep = "review_next_model_ladder_step"
)

// Signal carries only aggregate evidence. The gateway retains tenant and provider identity.
type Signal struct {
	SchemaVersion    int   `json:"schema_version"`
	WindowStartUnix  int64 `json:"window_start_unix"`
	Count429         int64 `json:"count_429"`
	Count502         int64 `json:"count_502"`
	DistinctRequests int64 `json:"distinct_requests"`
}

func (s Signal) Valid() bool {
	return s.SchemaVersion == SchemaVersion && s.WindowStartUnix > 0 &&
		s.WindowStartUnix%int64(Window/time.Second) == 0 &&
		s.Count429 >= 0 && s.Count502 >= 0 && s.DistinctRequests >= MinRequests &&
		s.Count429 >= s.DistinctRequests-s.Count502
}

// Suggestion is a closed vocabulary for operator review, without an executable plan.
type Suggestion struct {
	SchemaVersion   int    `json:"schema_version"`
	TemplateVersion string `json:"template_version"`
	Recommendation  string `json:"recommendation"`
	CooldownSeconds int    `json:"cooldown_seconds"`
}

func (s Suggestion) Valid(version string) bool {
	return s.SchemaVersion == SchemaVersion && version != "" && s.TemplateVersion == version &&
		s.Recommendation == ReviewNextStep && ValidCooldown(s.CooldownSeconds)
}

func ValidCooldown(seconds int) bool {
	return seconds > 0 && seconds <= MaxCooldown
}

// decode accepts one bounded JSON object with exactly the declared fields.
func decode(r io.Reader, dst any, fields ...string) error {
	buf, err := io.ReadAll(io.LimitReader(r, MaxPayload+1))
	if err != nil {
		return err
	}
	if len(buf) > MaxPayload {
		return errors.New("remediation payload exceeds limit")
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	open, err := dec.Token()
	if err != nil || open != json.Delim('{') {
		return errors.New("expected remediation JSON object")
	}
	seen := make(map[string]bool, len(fields))
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return errors.New("invalid or duplicate remediation field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(value, []byte("null")) {
			return errors.New("null remediation field")
		}
	}
	if len(seen) != len(fields) {
		return errors.New("invalid remediation fields")
	}
	for _, field := range fields {
		if !seen[field] {
			return errors.New("missing remediation field")
		}
	}
	// Unmarshal also checks the closing brace, types, and trailing data.
	return json.Unmarshal(buf, dst)
}

func DecodeSignal(r io.Reader) (Signal, error) {
	var s Signal
	err := decode(r, &s, "schema_version", "window_start_unix", "count_429", "count_502", "distinct_requests")
	return s, err
}

func DecodeSuggestion(r io.Reader) (Suggestion, error) {
	var s Suggestion
	err := decode(r, &s, "schema_version", "template_version", "recommendation", "cooldown_seconds")
	return s, err
}

const (
	SSEPluginID   = "sse-break-analysis"
	DriftPluginID = "config-drift"

	ReviewUpstream  = "review_upstream_stream"
	ReviewProxyIdle = "review_proxy_idle_and_upstream_flush"
	ReviewNetwork   = "review_transport"
	ReviewMixed     = "correlate_stream_evidence"

	ConfigInvalid = "invalid_config"
	ConfigChanged = "unapplied_config"
	ReviewConfig  = "review_approved_configuration"
	RestoreConfig = "compare_with_last_accepted_configuration"
)

func PluginIDs() []string { return []string{PluginID, SSEPluginID, DriftPluginID} }

func IsPluginID(id string) bool {
	return id == PluginID || id == SSEPluginID || id == DriftPluginID
}

// SSESignal combines distinct request counts per evidence class. A request may
// contribute to more than one class when failover encounters different reasons.
type SSESignal struct {
	SchemaVersion    int   `json:"schema_version"`
	WindowStartUnix  int64 `json:"window_start_unix"`
	DistinctRequests int64 `json:"distinct_requests"`
	Upstream         int64 `json:"upstream"`
	IdleTimeout      int64 `json:"idle_timeout"`
	Network          int64 `json:"network"`
}

func (s SSESignal) Valid() bool {
	return s.SchemaVersion == SchemaVersion && s.WindowStartUnix > 0 &&
		s.WindowStartUnix%int64(Window/time.Second) == 0 && s.DistinctRequests >= MinRequests &&
		s.Upstream >= 0 && s.Upstream <= s.DistinctRequests &&
		s.IdleTimeout >= 0 && s.IdleTimeout <= s.DistinctRequests &&
		s.Network >= max(0, s.DistinctRequests-s.Upstream-s.IdleTimeout) && s.Network <= s.DistinctRequests
}

func DecodeSSESignal(r io.Reader) (SSESignal, error) {
	var s SSESignal
	err := decode(r, &s, "schema_version", "window_start_unix", "distinct_requests", "upstream", "idle_timeout", "network")
	return s, err
}

type DriftSignal struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
}

func (s DriftSignal) Valid() bool {
	return s.SchemaVersion == SchemaVersion && (s.Kind == ConfigInvalid || s.Kind == ConfigChanged)
}

func DecodeDriftSignal(r io.Reader) (DriftSignal, error) {
	var s DriftSignal
	err := decode(r, &s, "schema_version", "kind")
	return s, err
}

// Review represents a read-only suggestion without executable arguments.
type Review struct {
	SchemaVersion   int    `json:"schema_version"`
	TemplateVersion string `json:"template_version"`
	Recommendation  string `json:"recommendation"`
}

func DecodeReview(r io.Reader) (Review, error) {
	var s Review
	err := decode(r, &s, "schema_version", "template_version", "recommendation")
	return s, err
}

func (s Review) Valid(version, pluginID string) bool {
	if s.SchemaVersion != SchemaVersion || version == "" || s.TemplateVersion != version {
		return false
	}
	switch pluginID {
	case SSEPluginID:
		switch s.Recommendation {
		case ReviewUpstream, ReviewProxyIdle, ReviewNetwork, ReviewMixed:
			return true
		}
	case DriftPluginID:
		return s.Recommendation == ReviewConfig || s.Recommendation == RestoreConfig
	}
	return false
}
