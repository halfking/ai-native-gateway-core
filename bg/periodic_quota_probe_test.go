package bg

import (
	"os"
	"strings"
	"testing"
)

func TestPeriodicQuotaProbeIncludesAutoDisabledCredentials(t *testing.T) {
	src, err := os.ReadFile("periodic_quota_probe.go")
	if err != nil {
		t.Fatalf("read periodic quota probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"c.lifecycle_status = 'disabled'",
		"c.auto_disabled_at IS NOT NULL",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"c.quota_recover_at IS NULL OR c.quota_recover_at <= now()",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("periodic quota probe is missing recovery guard %q", want)
		}
	}
}

func TestCredentialProbeAllowsOnlyPeriodicAutoDisabledRecovery(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatalf("read credential probe source failed: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"lifecycle_status = 'disabled'",
		"auto_disabled_at IS NOT NULL",
		"COALESCE(quota_state, 'ok') = 'periodic_exhausted'",
		"quota_recover_at IS NULL OR quota_recover_at <= now()",
		"lifecycle_status = CASE",
		"periodic_quota_probe_recovered",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("credential probe recovery contract is missing %q", want)
		}
	}
}
