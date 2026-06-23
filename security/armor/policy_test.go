package armor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeStore is a minimal settingsStore for tests. It never touches a DB.
type fakeStore struct {
	data map[string]string
	err  error // if set, Get returns this
}

func (f fakeStore) Get(ctx context.Context, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.data[key], nil
}

func TestDefaultPolicy_AllOff(t *testing.T) {
	p := DefaultPolicy("acme")
	if len(p.EnabledChecks) != 0 {
		t.Errorf("default policy should enable nothing, got %v", p.EnabledChecks)
	}
	if p.Mode != ModeObserve {
		t.Errorf("default Mode = %v, want observe", p.Mode)
	}
	for _, c := range AllChecks {
		if th := p.ThresholdFor(c); th <= 0 || th > 1 {
			t.Errorf("default threshold for %s = %v, want in (0,1]", c, th)
		}
	}
}

func TestLoadPolicy_MissingKey_DefaultsOff(t *testing.T) {
	store := fakeStore{data: map[string]string{}}
	p := LoadPolicy(context.Background(), store, "unknown")
	if len(p.EnabledChecks) != 0 {
		t.Errorf("missing policy must default to off, got %v", p.EnabledChecks)
	}
	if p.TenantID != "unknown" {
		t.Errorf("TenantID = %q, want unknown", p.TenantID)
	}
}

func TestLoadPolicy_InvalidJSON_DefaultsOff(t *testing.T) {
	store := fakeStore{data: map[string]string{
		"armor.policy.acme": "{not json",
	}}
	p := LoadPolicy(context.Background(), store, "acme")
	// must not panic; must return a safe default
	if p.Mode != ModeObserve {
		t.Errorf("Mode = %v, want observe after bad JSON", p.Mode)
	}
	if len(p.EnabledChecks) != 0 {
		t.Errorf("EnabledChecks should be empty after bad JSON, got %v", p.EnabledChecks)
	}
}

func TestLoadPolicy_StoreError_DefaultsOff(t *testing.T) {
	store := fakeStore{err: errSentinel}
	p := LoadPolicy(context.Background(), store, "acme")
	if len(p.EnabledChecks) != 0 {
		t.Errorf("store error must fall back to default, got %v", p.EnabledChecks)
	}
}

func TestLoadPolicy_EmptyTenantID(t *testing.T) {
	store := fakeStore{data: map[string]string{}}
	p := LoadPolicy(context.Background(), store, "")
	// empty tenant must never match a key; default to "default" tenant
	if p.TenantID != "default" {
		t.Errorf("TenantID = %q, want default", p.TenantID)
	}
}

func TestLoadPolicy_NilStore(t *testing.T) {
	p := LoadPolicy(context.Background(), nil, "acme")
	if p.TenantID != "acme" {
		t.Errorf("nil store must still return policy, got TenantID=%q", p.TenantID)
	}
}

func TestNormalize_ForcesObserve(t *testing.T) {
	// Even if an admin somehow stored "enforce", normalize must downgrade.
	raw := Policy{
		TenantID:      "acme",
		EnabledChecks: []string{CheckPromptInject},
		Mode:          ModeEnforce, // forbidden in v1
	}
	got := Normalize(raw)
	if got.Mode != ModeObserve {
		t.Errorf("Normalize must force observe, got %v", got.Mode)
	}
}

func TestNormalize_DropsUnknownChecks(t *testing.T) {
	raw := Policy{
		TenantID:      "acme",
		EnabledChecks: []string{CheckPromptInject, "typo_check", CheckPII},
		PerCheckThresholds: map[string]float64{
			CheckPromptInject: 0.8,
			"typo_check":      0.5, // should be dropped
		},
	}
	got := Normalize(raw)
	for _, c := range got.EnabledChecks {
		if c == "typo_check" {
			t.Error("unknown check typo_check must be dropped")
		}
	}
	if _, ok := got.PerCheckThresholds["typo_check"]; ok {
		t.Error("unknown threshold must be dropped")
	}
}

func TestNormalize_ClampsThresholds(t *testing.T) {
	raw := Policy{
		TenantID:           "acme",
		EnabledChecks:      []string{CheckPromptInject},
		PerCheckThresholds: map[string]float64{CheckPromptInject: 1.5}, // out of range
	}
	got := Normalize(raw)
	if th := got.PerCheckThresholds[CheckPromptInject]; th != 1 {
		t.Errorf("threshold clamped = %v, want 1", th)
	}
}

func TestNormalize_FillsMissingThresholds(t *testing.T) {
	raw := Policy{
		TenantID:           "acme",
		EnabledChecks:      []string{CheckPromptInject},
		PerCheckThresholds: nil,
	}
	got := Normalize(raw)
	for _, c := range AllChecks {
		if _, ok := got.PerCheckThresholds[c]; !ok {
			t.Errorf("Normalize must fill default for %s", c)
		}
	}
}

func TestLoadPolicy_ValidPolicy_Normalized(t *testing.T) {
	// round-trip: a valid policy survives normalize
	p := Policy{
		TenantID:           "acme",
		EnabledChecks:      []string{CheckPromptInject, CheckPII},
		PerCheckThresholds: map[string]float64{CheckPromptInject: 0.85, CheckPII: 0.6},
		Mode:               ModeObserve,
	}
	buf, _ := json.Marshal(p)
	store := fakeStore{data: map[string]string{"armor.policy.acme": string(buf)}}
	got := LoadPolicy(context.Background(), store, "acme")
	if !got.IsEnabled(CheckPromptInject) {
		t.Error("prompt_inject must be enabled")
	}
	if got.ThresholdFor(CheckPromptInject) != 0.85 {
		t.Errorf("threshold = %v, want 0.85", got.ThresholdFor(CheckPromptInject))
	}
}

func TestPolicy_Validate(t *testing.T) {
	cases := []struct {
		name    string
		p       Policy
		wantErr bool
	}{
		{"valid", Policy{TenantID: "x", EnabledChecks: []string{CheckPromptInject}, Mode: ModeObserve}, false},
		{"missing tenant", Policy{EnabledChecks: []string{CheckPromptInject}}, true},
		{"unknown check", Policy{TenantID: "x", EnabledChecks: []string{"bogus"}}, true},
		{"threshold out of range", Policy{TenantID: "x", PerCheckThresholds: map[string]float64{CheckPromptInject: 2.0}}, true},
		{"unknown mode", Policy{TenantID: "x", Mode: Mode("bogus")}, true},
		{"enforce allowed at authoring", Policy{TenantID: "x", Mode: ModeEnforce}, false}, // allowed at write, forced at read
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.p.Validate()
			if (err != nil) != c.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestPolicy_ThresholdFor_Default(t *testing.T) {
	p := Policy{} // empty
	if th := p.ThresholdFor(CheckPromptInject); th <= 0 {
		t.Errorf("default threshold = %v, want > 0", th)
	}
}

// errSentinel avoids importing errors just for one value.
var errSentinel = sentinelErr("store down")

type sentinelErr string

func (s sentinelErr) Error() string { return string(s) }

// guard: ensure sentinelErr satisfies error at compile time.
var _ error = sentinelErr("")

// TestAllChecks_Stable: the check-type strings are part of the audit schema.
func TestAllChecks_Stable(t *testing.T) {
	want := []string{CheckPromptInject, CheckPII, CheckHallucinate}
	if len(AllChecks) != len(want) {
		t.Fatalf("AllChecks len = %d, want %d", len(AllChecks), len(want))
	}
	for i, c := range AllChecks {
		if c != want[i] {
			t.Errorf("AllChecks[%d] = %q, want %q", i, c, want[i])
		}
	}
	// strings used in audit must be lowercase + underscore (no spaces)
	for _, c := range AllChecks {
		if c != strings.ToLower(c) || strings.ContainsAny(c, " -") {
			t.Errorf("check type %q must be lowercase, no spaces/dashes", c)
		}
	}
}
