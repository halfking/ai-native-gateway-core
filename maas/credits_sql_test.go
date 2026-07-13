package maas

import (
	"strings"
	"testing"
)

func TestRequestLogCreditsSQL_prefersStoredCredits(t *testing.T) {
	sql := RequestLogCreditsSQL("r")
	for _, want := range []string{
		"COALESCE(r.credits_charged",
		"maas_settings",
		"tenant_id IN ('', 'default')",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("RequestLogCreditsSQL missing %q", want)
		}
	}
}
