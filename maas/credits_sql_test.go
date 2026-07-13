package maas

import (
	"strings"
	"testing"
)

func TestRequestLogCreditsSQL_prefersStoredCredits(t *testing.T) {
	tenantSQL := RequestLogCreditsSQL("r", false)
	for _, want := range []string{
		"COALESCE(r.credits_charged",
		"maas_settings",
		"tenant_id IN ('', 'default')",
	} {
		if !strings.Contains(tenantSQL, want) {
			t.Fatalf("tenant-scoped SQL missing %q", want)
		}
	}

	platformSQL := RequestLogCreditsSQL("r", true)
	if strings.Contains(platformSQL, "tenant_id IN ('', 'default')") {
		t.Fatal("platform SQL must not skip default tenant estimates")
	}
	if !strings.Contains(platformSQL, "tenant_id = ''") {
		t.Fatal("platform SQL must still skip empty tenant_id")
	}
}
