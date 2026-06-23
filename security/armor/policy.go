package armor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// Mode controls whether armor can reject requests or only log them.
//
// v1 SHIPS ONLY ModeObserve. The ModeEnforce value exists in the enum so that
// policies stored in settings_kv can be forward-compatible, but resolveDecision
// (judge.go) ignores enforce and always downgrades to warn until a separate
// PR flips the gate. This is a hard legal/compliance rule — see
// design_intent_test.go for the invariant test.
type Mode string

const (
	// ModeObserve (default): log + telemetry, never reject. v1 only mode.
	ModeObserve Mode = "observe"
	// ModeEnforce: may reject (DecisionBlock). NOT active in v1; the gate in
	// resolveDecision ignores this and returns warn regardless.
	ModeEnforce Mode = "enforce"
)

// Check types. Keep these stable strings: they appear in Policy.EnabledChecks
// and in audit event error_kind suffixes (e.g. "armor_warn:prompt_inject").
const (
	CheckPromptInject = "prompt_inject" // B1: prompt-injection detection
	CheckPII          = "pii"           // B2: sensitive-data (PII) detection
	CheckHallucinate  = "hallucination" // future: hallucination guard (NOT v1)
)

// AllChecks is the universe of known check types. Used by Policy.Validate to
// reject typos like "promt_inject" early.
var AllChecks = []string{CheckPromptInject, CheckPII, CheckHallucinate}

// Policy is a per-tenant armor configuration. It is stored in settings_kv
// (key="armor.policy.<tenant_id>", value=JSON) and loaded via LoadPolicy.
//
// System-global design (intentional): the Policy struct itself is shared
// across tenants (one struct shape), but each tenant's VALUES are isolated
// by tenant_id key. There is no cross-tenant policy table.
type Policy struct {
	TenantID           string             `json:"tenant_id"`
	EnabledChecks      []string           `json:"enabled_checks"`         // subset of AllChecks
	PerCheckThresholds map[string]float64 `json:"per_check_thresholds"`   // check → [0,1]
	Mode               Mode               `json:"mode"`                   // v1 always "observe" after normalize
}

// DefaultPolicy returns a policy with EVERYTHING OFF. This is the safe
// fallback for tenants with no stored policy and for unknown tenants.
// v1 must never auto-enable armor — it's opt-in per tenant.
func DefaultPolicy(tenantID string) Policy {
	return Policy{
		TenantID:      tenantID,
		EnabledChecks: nil, // nothing enabled
		PerCheckThresholds: map[string]float64{
			CheckPromptInject: 0.8, // sensible defaults, used only if check is enabled
			CheckPII:          0.7,
			CheckHallucinate:  0.8,
		},
		Mode: ModeObserve,
	}
}

// settingsStore is the minimal interface LoadPolicy needs. The real
// implementation lives in the settings/ package; we define the interface here
// so armor/ has no import dependency on settings/ (keeps the domain boundary
// clean — see refactor plan §3 dependency rules).
type settingsStore interface {
	// Get returns the raw JSON value for a key, or ("", nil) if absent.
	Get(ctx context.Context, key string) (string, error)
}

// LoadPolicy reads a tenant's armor policy from settings. Behavior:
//   - Missing key → DefaultPolicy(tenantID) (armor off)
//   - Invalid JSON → DefaultPolicy(tenantID) + logged (never panic)
//   - Present + valid → parsed, then normalized via Normalize (enforce→observe)
//
// LoadPolicy NEVER returns an error that would crash the caller; armor is a
// defense-in-depth layer, not a hard dependency. On store error it falls back
// to default. Callers wanting the error can inspect the returned Policy's
// TenantID for a sentinel, but the recommended pattern is to use the returned
// Policy and move on.
func LoadPolicy(ctx context.Context, store settingsStore, tenantID string) Policy {
	if store == nil {
		return DefaultPolicy(tenantID)
	}
	if strings.TrimSpace(tenantID) == "" {
		return DefaultPolicy("default")
	}
	raw, err := store.Get(ctx, "armor.policy."+tenantID)
	if err != nil || strings.TrimSpace(raw) == "" {
		return DefaultPolicy(tenantID)
	}
	var p Policy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		// malformed policy → safest default; do not propagate
		return DefaultPolicy(tenantID)
	}
	p.TenantID = tenantID
	return Normalize(p)
}

// Normalize cleans up a parsed Policy so the rest of the code can trust it:
//   - clamps every threshold to [0,1]
//   - drops unknown check types (typo guard)
//   - FORCES Mode to ModeObserve in v1 (the hard rule). Even if the stored
//     policy says "enforce", we ignore it. This is a single chokepoint; flip
//     it in a separate PR with legal sign-off.
func Normalize(p Policy) Policy {
	if p.PerCheckThresholds == nil {
		p.PerCheckThresholds = map[string]float64{}
	}
	known := map[string]bool{}
	for _, c := range AllChecks {
		known[c] = true
	}
	// drop unknown checks + clamp thresholds
	cleaned := make(map[string]float64, len(known))
	for check, th := range p.PerCheckThresholds {
		if known[check] {
			cleaned[check] = clampScore(th)
		}
	}
	for _, c := range AllChecks {
		if _, ok := cleaned[c]; !ok {
			cleaned[c] = defaultThreshold(c)
		}
	}
	p.PerCheckThresholds = cleaned
	// drop unknown enabled checks
	kept := p.EnabledChecks[:0]
	for _, c := range p.EnabledChecks {
		if known[c] {
			kept = append(kept, c)
		}
	}
	p.EnabledChecks = kept
	// v1 hard rule: force observe
	p.Mode = ModeObserve
	return p
}

func defaultThreshold(check string) float64 {
	switch check {
	case CheckPromptInject:
		return 0.8
	case CheckPII:
		return 0.7
	case CheckHallucinate:
		return 0.8
	default:
		return 0.8
	}
}

// IsEnabled reports whether a given check is turned on for this policy.
// Callers use this to short-circuit before constructing a Judge.
func (p Policy) IsEnabled(check string) bool {
	for _, c := range p.EnabledChecks {
		if c == check {
			return true
		}
	}
	return false
}

// ThresholdFor returns the policy threshold for a check (default if unset).
// Always returns a value in [0,1].
func (p Policy) ThresholdFor(check string) float64 {
	if v, ok := p.PerCheckThresholds[check]; ok {
		return clampScore(v)
	}
	return defaultThreshold(check)
}

// Validate returns an error if a Policy (as authored by an admin) is
// structurally invalid. This is used at WRITE time (admin API), not read time
// (LoadPolicy already calls Normalize which is permissive).
func (p Policy) Validate() error {
	if strings.TrimSpace(p.TenantID) == "" {
		return errors.New("armor: Policy.TenantID is required")
	}
	known := map[string]bool{}
	for _, c := range AllChecks {
		known[c] = true
	}
	for _, c := range p.EnabledChecks {
		if !known[c] {
			return errors.New("armor: unknown check type " + c)
		}
	}
	for check, th := range p.PerCheckThresholds {
		if !known[check] {
			return errors.New("armor: threshold for unknown check " + check)
		}
		if th < 0 || th > 1 {
			return errors.New("armor: threshold out of [0,1] for " + check)
		}
	}
	switch p.Mode {
	case ModeObserve, ModeEnforce:
		// ok at authoring time; Normalize will force observe at read time
	default:
		return errors.New("armor: unknown mode " + string(p.Mode))
	}
	return nil
}
