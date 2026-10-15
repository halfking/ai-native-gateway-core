package sanitizerules

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

var identifier = regexp.MustCompile(`^[\p{L}\p{N}_][\p{L}\p{N}_.:/@*\[\]-]{0,127}$`)

func validID(v string) bool { return len(v) <= 256 && identifier.MatchString(v) }
func invalid(index int, field, reason string) error {
	return fmt.Errorf("%w: rules[%d].%s %s", ErrInvalid, index, field, reason)
}
func Validate(cfg Config) error { _, err := Compile(cfg, 0); return err }

// Compile detaches all mutable configuration and compiles each expression once.
// Higher priority rules run first; equal priorities retain their declared order.
func Compile(cfg Config, version int64) (*Snapshot, error) {
	if len(cfg.Rules) == 0 || len(cfg.Rules) > 2048 {
		return nil, fmt.Errorf("%w: rules must contain 1..2048 entries", ErrInvalid)
	}
	cfg = cloneConfig(cfg)
	ids := make(map[string]bool, len(cfg.Rules))
	compiled := make([]CompiledRule, 0, len(cfg.Rules))
	for i, r := range cfg.Rules {
		if !validID(r.ID) || ids[r.ID] {
			return nil, invalid(i, "id", "must be unique and a bounded identifier")
		}
		ids[r.ID] = true
		if strings.TrimSpace(r.Name) == "" || len(r.Name) > 256 {
			return nil, invalid(i, "name", "must contain 1..256 bytes")
		}
		switch r.Type {
		case "phone", "id_card", "email", "credit_card", "secret", "internal_ip", "server_ip", "account", "name", "custom":
		default:
			return nil, invalid(i, "type", "must be a restorable sensitive type")
		}
		switch r.Kind {
		case "pattern", "review_combination", "network_destination", "vendor_key_shape", "credential_declaration", "credential_assignment", "credential_pair", "account_declaration", "account_assignment":
		default:
			return nil, invalid(i, "kind", "is unknown")
		}
		if len(r.Pattern) == 0 || len(r.Pattern) > 16384 {
			return nil, invalid(i, "pattern", "must contain 1..16384 bytes")
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, invalid(i, "pattern", "is not valid RE2")
		}
		if re.MatchString("") {
			return nil, invalid(i, "pattern", "must not match empty input")
		}
		if r.Group < 0 || r.Group > re.NumSubexp() {
			return nil, invalid(i, "group", "is outside the expression capture groups")
		}
		if r.Priority < -10000 || r.Priority > 10000 {
			return nil, invalid(i, "priority", "must be within -10000..10000")
		}
		if r.TenantID != "" && !validID(r.TenantID) {
			return nil, invalid(i, "tenant_id", "must be a bounded identifier")
		}
		if r.Action != "mask" && r.Action != "block" && r.Action != "log" {
			return nil, invalid(i, "action", "must be mask, block or log")
		}
		if r.Kind == "review_combination" && r.Action != "log" {
			return nil, invalid(i, "action", "review combinations only log candidates")
		}
		if len(r.Scopes) == 0 || len(r.Scopes) > 3 {
			return nil, invalid(i, "scopes", "must contain 1..3 stages")
		}
		seen := map[string]bool{}
		for _, phase := range r.Scopes {
			if (phase != "input" && phase != "output" && phase != "egress") || seen[phase] {
				return nil, invalid(i, "scopes", "contains an unknown or duplicate stage")
			}
			seen[phase] = true
			if r.Kind == "network_destination" && phase != "egress" {
				return nil, invalid(i, "scopes", "network destinations are egress-only")
			}
			if (r.Kind == "credential_declaration" || r.Kind == "credential_pair" || r.Kind == "account_declaration" || r.Kind == "review_combination") && phase != "input" {
				return nil, invalid(i, "scopes", "semantic declarations are input-only")
			}
			if phase == "egress" && r.Action == "mask" {
				return nil, invalid(i, "action", "egress supports block or log")
			}
		}
		if len(r.Exemptions) > 3 {
			return nil, invalid(i, "exemptions", "exceeds 3 entries")
		}
		seenExemptions := map[string]bool{}
		for _, e := range r.Exemptions {
			if (e != "inert_tool" && e != "nonroutable_destination" && e != "identity") || seenExemptions[e] || !seen["egress"] {
				return nil, invalid(i, "exemptions", "unknown, duplicate or outside egress")
			}
			seenExemptions[e] = true
		}
		for _, values := range [][]string{r.Tools, r.Fields} {
			if len(values) > 64 {
				return nil, invalid(i, "constraints", "exceeds 64 entries")
			}
			unique := map[string]bool{}
			for _, v := range values {
				if !validID(v) || unique[v] {
					return nil, invalid(i, "constraints", "requires unique bounded identifiers")
				}
				unique[v] = true
			}
		}
		if len(r.Samples) > 20 {
			return nil, invalid(i, "samples", "exceeds 20 entries")
		}
		for _, sample := range r.Samples {
			if len(sample.Text) > 4096 {
				return nil, invalid(i, "samples", "text exceeds 4096 bytes")
			}
			matches := false
			for _, indices := range re.FindAllStringSubmatchIndex(sample.Text, -1) {
				if indices[2*r.Group] >= 0 && indices[2*r.Group+1] > indices[2*r.Group] {
					matches = true
					break
				}
			}
			if matches != sample.Match {
				return nil, invalid(i, "samples", "expected match differs from the expression")
			}
		}
		compiled = append(compiled, CompiledRule{Rule: r, Regex: re})
	}
	sort.SliceStable(compiled, func(i, j int) bool { return compiled[i].Priority > compiled[j].Priority })
	for i, r := range compiled {
		cfg.Rules[i] = r.Rule
	}
	data, _ := json.Marshal(cfg)
	digest := sha256.Sum256(data)
	return &Snapshot{Version: version, ConfigDigest: hex.EncodeToString(digest[:]), Rules: compiled}, nil
}
func cloneConfig(c Config) Config {
	out := Config{Rules: make([]Rule, len(c.Rules))}
	for i, r := range c.Rules {
		r.Scopes = slices.Clone(r.Scopes)
		r.Tools = slices.Clone(r.Tools)
		r.Fields = slices.Clone(r.Fields)
		r.Samples = slices.Clone(r.Samples)
		r.Exemptions = slices.Clone(r.Exemptions)
		out.Rules[i] = r
	}
	return out
}
func (s *Snapshot) Config() Config {
	cfg := Config{Rules: make([]Rule, len(s.Rules))}
	for i, r := range s.Rules {
		cfg.Rules[i] = r.Rule
	}
	return cloneConfig(cfg)
}
func (r CompiledRule) MatchString(text string) bool { return r.Regex.MatchString(text) }
func (r CompiledRule) Applies(phase, tenant, tool, field string) bool {
	return r.Enabled && slices.Contains(r.Scopes, phase) && (r.TenantID == "" || r.TenantID == tenant) && (len(r.Tools) == 0 || slices.Contains(r.Tools, tool)) && (len(r.Fields) == 0 || slices.Contains(r.Fields, field))
}
