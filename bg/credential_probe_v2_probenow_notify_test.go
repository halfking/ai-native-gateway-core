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
		"loadBoundRawModelsAll",
	} {
		if !strings.Contains(fn, marker) {
			t.Fatalf("ProbeNow missing %q", marker)
		}
	}
	if strings.Contains(fn, "default_probe_model, '') <> ''") {
		t.Fatal("ProbeNow must not skip credentials without default_probe_model")
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
