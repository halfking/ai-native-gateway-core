package bg

import (
	"os"
	"strings"
	"testing"
)

func TestProbeNowNotifiesQuotaRecovered(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	idx := strings.Index(body, "func (c *CredentialProbeV2) ProbeNow(")
	if idx < 0 {
		t.Fatal("ProbeNow missing")
	}
	fn := body[idx:]
	if end := strings.Index(fn[1:], "\nfunc ("); end > 0 {
		fn = fn[:end+1]
	}
	for _, marker := range []string{
		`c.onQuotaRecovered(credID, "probe_now")`,
		"c.fallbackProbeModel(",
	} {
		if !strings.Contains(fn, marker) {
			t.Fatalf("ProbeNow missing %q", marker)
		}
	}
	if strings.Contains(fn, "default_probe_model, '') <> ''") {
		t.Fatal("ProbeNow must not skip credentials without default_probe_model")
	}
}

// Without default_probe_model the fallback must prefer a binding that is
// currently routable (available=TRUE) over an arbitrary unavailable one;
// probing a binding that is already unavailable proves nothing about the
// credential's quota/health.
func TestProbeNowFallbackPrefersAvailableBinding(t *testing.T) {
	src, err := os.ReadFile("credential_probe_v2.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	idx := strings.Index(body, "func (c *CredentialProbeV2) fallbackProbeModel(")
	if idx < 0 {
		t.Fatal("fallbackProbeModel missing")
	}
	fn := body[idx:]
	if end := strings.Index(fn[1:], "\nfunc ("); end > 0 {
		fn = fn[:end+1]
	}
	avail := strings.Index(fn, "c.loadBoundRawModels(")
	all := strings.Index(fn, "c.loadBoundRawModelsAll(")
	if avail < 0 || all < 0 || avail > all {
		t.Fatal("fallbackProbeModel must try available bindings before all bindings")
	}
}

func TestBalanceQuotaTickPrefersImmediateProbe(t *testing.T) {
	src, err := os.ReadFile("balance_quota_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	idx := strings.Index(body, "func (p *BalanceQuotaProbe) probeBalanceExhausted(")
	if idx < 0 {
		t.Fatal("probeBalanceExhausted missing")
	}
	fn := body[idx:]
	if end := strings.Index(fn[1:], "\nfunc ("); end > 0 {
		fn = fn[:end+1]
	}
	if !strings.Contains(fn, "p.probeNowAsync != nil") || !strings.Contains(fn, "p.probeNowAsync(credID)") {
		t.Fatal("scheduled balance quota tick must prefer ProbeNowAsync")
	}
}
