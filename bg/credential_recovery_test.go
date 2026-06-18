package bg

import (
	"strings"
	"testing"
)

func TestMnfCoolingRecoverySQLGuards(t *testing.T) {
	sql := mnfCoolingRecoverySQL()
	mustContain := []string{
		"cmb.unavailable_reason = 'mnf_cooling'",
		"cmb.unavailable_at <= NOW() - make_interval(mins => $1)",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"COALESCE(cmb.admin_protected, FALSE) = FALSE",
		"unavailable_reason = NULL",
		"unavailable_at = NULL",
	}
	for _, want := range mustContain {
		if !strings.Contains(sql, want) {
			t.Fatalf("mnfCoolingRecoverySQL missing %q in:\n%s", want, sql)
		}
	}
}

func TestMnfCoolingRecoveryMinutes(t *testing.T) {
	t.Setenv("LLM_GATEWAY_MNF_COOL_MINUTES", "7")
	if got := mnfCoolingRecoveryMinutes(); got != 7 {
		t.Fatalf("mnfCoolingRecoveryMinutes = %d, want 7", got)
	}
	t.Setenv("LLM_GATEWAY_MNF_COOL_MINUTES", "bad")
	if got := mnfCoolingRecoveryMinutes(); got != 2 {
		t.Fatalf("mnfCoolingRecoveryMinutes invalid = %d, want 2", got)
	}
}
