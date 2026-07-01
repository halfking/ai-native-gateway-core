// Package transform implements the request transform matrix: loading YAML rules,
// matching against client profile + model + mode, and rendering outbound model
// names.  Port of app/core/transform_matrix.py from the Python control plane.
package transformation

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Valid modes and clients.
var (
	validModes   = map[string]bool{"chat": true, "agent": true, "coder": true}
	validClients = map[string]bool{
		"cursor": true, "roocode": true, "claude-code": true,
		"copilot": true, "codex": true, "openclaw": true, "default": true,
	}
)

// TransformContext holds the per-request context for rule matching.
type TransformContext struct {
	RequestMode   string
	ClientProfile string
	ClientModel   string
	CanonicalName string
	Family        string
}

// TransformResult holds the resolved transform for a request.
type TransformResult struct {
	EgressPreference   []string
	OutboundModel      string
	StripHeaders       []string
	InjectHeaders      map[string]string
	Passthrough        []string
	PassthroughFields  []string
	StripRequestFields []string
	DisguiseProfileID  string
	MatchedRule        string
}

// Matrix loads and evaluates request transform rules.
type Matrix struct {
	mu       sync.RWMutex
	doc      map[string]any
	rules    []map[string]any
	defaults map[string]any
	path     string
}

// New loads the transform matrix from the data directory.
// It searches up from the repository root conventions.
func New(dataPath string) *Matrix {
	m := &Matrix{}
	m.Load(dataPath)
	return m
}

// DefaultMatrixPath resolves the default path for request_transform_matrix.yaml.
func DefaultMatrixPath() string {
	// Try relative to the service dir, then environment override
	if p := os.Getenv("TRANSFORM_MATRIX_PATH"); p != "" {
		return p
	}
	// Walk up from cwd to find data/ directory
	dir, _ := os.Getwd()
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "data", "request_transform_matrix.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// Load reads the transform matrix from a YAML file.
func (m *Matrix) Load(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path
	if path == "" {
		m.doc = nil
		m.rules = nil
		m.defaults = nil
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("transform matrix not found, using defaults", "path", path, "error", err)
		m.doc = nil
		m.rules = nil
		m.defaults = nil
		return
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		slog.Error("transform matrix parse error", "path", path, "error", err)
		m.doc = nil
		m.rules = nil
		m.defaults = nil
		return
	}
	m.doc = raw
	if r, ok := raw["rules"].([]any); ok {
		m.rules = make([]map[string]any, len(r))
		for i, v := range r {
			if rule, ok := v.(map[string]any); ok {
				m.rules[i] = rule
			}
		}
	}
	if d, ok := raw["defaults"].(map[string]any); ok {
		m.defaults = d
	} else {
		m.defaults = map[string]any{}
	}
	slog.Info("transform matrix loaded", "path", path, "rules", len(m.rules))
}

// NormalizeMode normalizes a mode string.
func (m *Matrix) NormalizeMode(value string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if value == "" {
		v, _ := m.defaults["request_mode"].(string)
		value = v
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		value = "chat"
	}
	if !validModes[value] {
		return "chat"
	}
	return value
}

// NormalizeClient normalizes a client profile string.
func (m *Matrix) NormalizeClient(value string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if value == "" {
		v, _ := m.defaults["client"].(string)
		value = v
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		value = "default"
	}
	if !validClients[value] {
		return "default"
	}
	return value
}

// normalizeModeLocked is the internal version of NormalizeMode without lock acquisition.
// Must be called with mu held.
func (m *Matrix) normalizeModeLocked(value string) string {
	if value == "" {
		v, _ := m.defaults["request_mode"].(string)
		value = v
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		value = "chat"
	}
	if !validModes[value] {
		return "chat"
	}
	return value
}

// normalizeClientLocked is the internal version of NormalizeClient without lock acquisition.
// Must be called with mu held.
func (m *Matrix) normalizeClientLocked(value string) string {
	if value == "" {
		v, _ := m.defaults["client"].(string)
		value = v
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		value = "default"
	}
	if !validClients[value] {
		return "default"
	}
	return value
}

func (m *Matrix) ruleMatches(rule map[string]any, ctx *TransformContext) bool {
	match, _ := rule["match"].(map[string]any)
	if match == nil {
		return false
	}
	mode := m.normalizeModeLocked(ctx.RequestMode)
	client := m.normalizeClientLocked(ctx.ClientProfile)

	if v, ok := match["mode"]; ok {
		if fmt.Sprint(v) != mode {
			return false
		}
	}
	if v, ok := match["client"]; ok {
		if fmt.Sprint(v) != client {
			return false
		}
	}
	if v, ok := match["canonical"]; ok {
		if !strings.EqualFold(ctx.CanonicalName, fmt.Sprint(v)) {
			return false
		}
	}
	if v, ok := match["family"]; ok {
		if !strings.EqualFold(ctx.Family, fmt.Sprint(v)) {
			return false
		}
	}
	return true
}

// Resolve matches the context against rules and returns the result.
func (m *Matrix) Resolve(ctx *TransformContext) *TransformResult {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := &TransformResult{
		EgressPreference: []string{"openai-completions"},
	}
	if ds, ok := m.defaults["strip_headers"].([]any); ok {
		for _, h := range ds {
			result.StripHeaders = append(result.StripHeaders, fmt.Sprint(h))
		}
	}
	if dp, ok := m.defaults["passthrough"].([]any); ok {
		for _, p := range dp {
			result.Passthrough = append(result.Passthrough, fmt.Sprint(p))
		}
	}

	for idx, rule := range m.rules {
		if !m.ruleMatches(rule, ctx) {
			continue
		}
		result.MatchedRule = fmt.Sprintf("rule_%d", idx)

		if pref, ok := rule["egress_preference"].([]any); ok {
			result.EgressPreference = make([]string, len(pref))
			for i, v := range pref {
				result.EgressPreference[i] = fmt.Sprint(v)
			}
		} else if egress, ok := rule["egress"]; ok {
			result.EgressPreference = []string{fmt.Sprint(egress)}
		}

		if sh, ok := rule["strip_headers"].([]any); ok {
			result.StripHeaders = make([]string, len(sh))
			for i, h := range sh {
				result.StripHeaders[i] = fmt.Sprint(h)
			}
		}

		if ih, ok := rule["inject_headers"].(map[string]any); ok {
			result.InjectHeaders = make(map[string]string, len(ih))
			for k, v := range ih {
				result.InjectHeaders[k] = fmt.Sprint(v)
			}
		}

		if ps, ok := rule["passthrough"].([]any); ok {
			result.Passthrough = make([]string, len(ps))
			for i, p := range ps {
				result.Passthrough[i] = fmt.Sprint(p)
			}
		}

		if pf, ok := rule["passthrough_fields"].([]any); ok {
			result.PassthroughFields = make([]string, len(pf))
			for i, p := range pf {
				result.PassthroughFields[i] = fmt.Sprint(p)
			}
		}

		if sf, ok := rule["strip_request_fields"].([]any); ok {
			result.StripRequestFields = make([]string, len(sf))
			for i, p := range sf {
				result.StripRequestFields[i] = fmt.Sprint(p)
			}
		}

		if outboundMap, ok := rule["outbound_model_map"].(map[string]any); ok {
			key := strings.ToLower(ctx.CanonicalName)
			if key == "" {
				key = strings.ToLower(ctx.ClientModel)
			}
			for k, v := range outboundMap {
				if strings.ToLower(k) == key {
					result.OutboundModel = fmt.Sprint(v)
					break
				}
			}
		}
		if result.OutboundModel == "" {
			if om, ok := rule["outbound_model"]; ok {
				result.OutboundModel = fmt.Sprint(om)
			}
		}

		break // first match wins
	}
	return result
}

var _fallbackRE = regexp.MustCompile(`\{([^}]+)\}`)

// RenderOutboundModel renders an outbound model template.
// Mirrors Python's render_outbound_model().
func RenderOutboundModel(
	template string,
	offerOutbound string,
	offerRaw string,
	canonicalName string,
) string {
	if template == "" {
		if offerOutbound != "" {
			return offerOutbound
		}
		return offerRaw
	}
	out := template
	out = strings.ReplaceAll(out, "{offer.outbound_model_name}", offerOutbound)
	out = strings.ReplaceAll(out, "{offer.raw_model_name}", offerRaw)
	if canonicalName != "" {
		out = strings.ReplaceAll(out, "{canon}", canonicalName)
	}

	out = _fallbackRE.ReplaceAllStringFunc(out, func(m string) string {
		inner := m[1 : len(m)-1]
		parts := strings.SplitN(inner, "|", 2)
		if len(parts) == 2 {
			left := strings.TrimSpace(parts[0])
			def := strings.TrimSpace(parts[1])
			switch left {
			case "offer.outbound_model_name":
				if offerOutbound != "" {
					return offerOutbound
				}
				return def
			case "offer.raw_model_name":
				if offerRaw != "" {
					return offerRaw
				}
				return def
			}
			return def
		}
		return m
	})
	return out
}
