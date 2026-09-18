package taskprofile

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync/atomic"
)

// registry.go — the versioned task-type → model-tier profile registry.
//
// Defaults mirror autoroute's V3 taxonomy (task_types_v3.go:
// TaskTypeTierMapping + MinConfidenceThresholds + tier fallback chains from
// tier_selector.go) so the module is self-contained without importing
// autoroute. The registry version pins the data lineage.
//
// Independent upgrade path: an overlay JSON file (TASKPROFILE_OVERLAY env,
// wired in cmd/gateway) can replace/extend profiles without a rebuild.
// Reload swaps the snapshot atomically; invalid overlays never take effect.

// SchemaVersion is the overlay/registry schema understood by this binary.
// Bump when TaskProfile gains/changes fields; loaders reject overlays with a
// different major version instead of guessing.
const SchemaVersion = 1

// RegistryVersion identifies the shipped default profile dataset.
const RegistryVersion = "2026.09.v3-defaults"

// defaultProfiles is the embedded baseline. Values MUST stay in sync with
// autoroute.TaskTypeTierMapping / MinConfidenceThresholds (V3 defaults);
// registry_defaults_test.go pins the tier tiers and thresholds.
var defaultProfiles = []TaskProfile{
	{TaskType: "architecture", Description: "System/API design, technical proposals", PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.70},
	{TaskType: "audit", Description: "Code review, security audit, PR review", PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.70},
	{TaskType: "debugging", Description: "Bug investigation, root cause analysis", PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.65},
	{TaskType: "coding", Description: "Feature implementation, API integration", PreferredTier: TierB, FallbackTiers: []string{TierA, TierC}, MinConfidence: 0.75},
	{TaskType: "refactoring", Description: "Code restructuring, optimization", PreferredTier: TierB, FallbackTiers: []string{TierA, TierC}, MinConfidence: 0.70},
	{TaskType: "testing", Description: "Test generation, coverage work", PreferredTier: TierB, FallbackTiers: []string{TierC}, MinConfidence: 0.75},
	{TaskType: "devops", Description: "CI/CD, deployment, infrastructure scripting", PreferredTier: TierC, FallbackTiers: []string{TierB}, MinConfidence: 0.80},
	{TaskType: "documentation", Description: "Comments, README, API docs", PreferredTier: TierC, FallbackTiers: []string{}, MinConfidence: 0.85},
	{TaskType: "summary", Description: "Summarization, session recap", PreferredTier: TierC, FallbackTiers: []string{}, MinConfidence: 0.85},
	{TaskType: "dependency", Description: "Dependency management, upgrades", PreferredTier: TierC, FallbackTiers: []string{TierB}, MinConfidence: 0.75},
	// V2 legacy task types still produced by the deployed classifier and
	// dominant in auto_route_selections (verified on 252: chat/code/creative
	// dominate). The profile must cover what production actually assigns so
	// corrections validate against reality; 2026-09-18 252 smoke.
	{TaskType: "chat", Description: "Open-ended conversation, Q&A (V2 legacy type)", PreferredTier: TierB, FallbackTiers: []string{TierA}, MinConfidence: 0.80},
	{TaskType: "code", Description: "Code generation/editing (V2 legacy type)", PreferredTier: TierB, FallbackTiers: []string{TierA, TierC}, MinConfidence: 0.75},
	{TaskType: "creative", Description: "Creative writing, brainstorming (V2 legacy type)", PreferredTier: TierB, FallbackTiers: []string{TierC}, MinConfidence: 0.75},
	{TaskType: "reasoning", Description: "Multi-step reasoning, math (V2 legacy type)", PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.70},
	{TaskType: "planning", Description: "Task decomposition, planning (V2 legacy type)", PreferredTier: TierA, FallbackTiers: []string{TierB}, MinConfidence: 0.70},
}

// snapshot is the immutable registry view swapped via atomic.Pointer.
type snapshot struct {
	version  string
	profiles map[string]TaskProfile
}

var registry atomic.Pointer[snapshot]

func init() { storeSnapshot(buildSnapshot(RegistryVersion, defaultProfiles, nil)) }

func buildSnapshot(version string, base []TaskProfile, overlay map[string]TaskProfile) *snapshot {
	profiles := make(map[string]TaskProfile, len(base)+len(overlay))
	for _, p := range base {
		profiles[p.TaskType] = p
	}
	for k, p := range overlay {
		profiles[k] = p
	}
	return &snapshot{version: version, profiles: profiles}
}

func storeSnapshot(s *snapshot) { registry.Store(s) }

// Snapshot returns the current registry version and a sorted, stable view of
// all profiles (sorted by task type so admin output is deterministic).
func Snapshot() (string, []TaskProfile) {
	s := registry.Load()
	out := make([]TaskProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskType < out[j].TaskType })
	return s.version, out
}

// SnapshotVersion returns just the current registry version string.
func SnapshotVersion() string {
	v, _ := Snapshot()
	return v
}

// Profile returns the profile for taskType; ok=false for unknown types.
func Profile(taskType string) (TaskProfile, bool) {
	s := registry.Load()
	p, ok := s.profiles[taskType]
	return p, ok
}

// IsValidTaskType reports whether taskType is in the active registry.
func IsValidTaskType(taskType string) bool {
	_, ok := Profile(taskType)
	return ok
}

// TaskTypes returns the known task type ids, sorted.
func TaskTypes() []string {
	s := registry.Load()
	out := make([]string, 0, len(s.profiles))
	for k := range s.profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// overlayFile is the JSON shape of an overlay file.
type overlayFile struct {
	SchemaVersion int                    `json:"schema_version"`
	Version       string                 `json:"version"`
	Profiles      map[string]TaskProfile `json:"profiles"`
}

// LoadOverlay validates the overlay file at path and atomically installs it
// on top of the embedded defaults. An error leaves the current registry
// untouched (failed upgrades never half-apply).
func LoadOverlay(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("taskprofile: read overlay %s: %w", path, err)
	}
	return LoadOverlayBytes(raw, path)
}

// LoadOverlayBytes is LoadOverlay over in-memory bytes (tests).
func LoadOverlayBytes(raw []byte, source string) (string, error) {
	var of overlayFile
	if err := json.Unmarshal(raw, &of); err != nil {
		return "", fmt.Errorf("taskprofile: parse overlay %s: %w", source, err)
	}
	if of.SchemaVersion != SchemaVersion {
		return "", fmt.Errorf("taskprofile: overlay %s schema_version=%d, binary understands %d",
			source, of.SchemaVersion, SchemaVersion)
	}
	if of.Version == "" {
		return "", fmt.Errorf("taskprofile: overlay %s missing version", source)
	}
	if len(of.Profiles) == 0 {
		return "", fmt.Errorf("taskprofile: overlay %s has no profiles", source)
	}
	for k, p := range of.Profiles {
		if k != p.TaskType {
			return "", fmt.Errorf("taskprofile: overlay %s profile key %q != task_type %q", source, k, p.TaskType)
		}
		if err := validateProfile(p); err != nil {
			return "", fmt.Errorf("taskprofile: overlay %s profile %q: %w", source, k, err)
		}
	}
	version := of.Version
	storeSnapshot(buildSnapshot(version, defaultProfiles, of.Profiles))
	return version, nil
}

// ReloadOverlay re-applies the overlay file at path. With an empty path it
// resets to the embedded defaults (used by the admin reload endpoint).
func ReloadOverlay(path string) (string, error) {
	if path == "" {
		storeSnapshot(buildSnapshot(RegistryVersion, defaultProfiles, nil))
		return RegistryVersion, nil
	}
	return LoadOverlay(path)
}

func validateProfile(p TaskProfile) error {
	if p.TaskType == "" {
		return fmt.Errorf("empty task_type")
	}
	switch p.PreferredTier {
	case TierA, TierB, TierC:
	default:
		return fmt.Errorf("preferred_tier %q not in {%s,%s,%s}", p.PreferredTier, TierA, TierB, TierC)
	}
	for _, f := range p.FallbackTiers {
		switch f {
		case TierA, TierB, TierC:
		default:
			return fmt.Errorf("fallback tier %q not in {%s,%s,%s}", f, TierA, TierB, TierC)
		}
	}
	if p.MinConfidence < 0 || p.MinConfidence > 1 {
		return fmt.Errorf("min_confidence %v outside [0,1]", p.MinConfidence)
	}
	return nil
}
