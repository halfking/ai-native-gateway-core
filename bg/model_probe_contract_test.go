package bg

import (
	"os"
	"strings"
	"testing"
)

func TestTriggerManualSQLGuardsAndFallbackAreScoped(t *testing.T) {
	src, err := os.ReadFile("model_probe.go")
	if err != nil {
		t.Fatalf("read model_probe.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (r *ModelProbeRunner) TriggerManual(")
	if start < 0 {
		t.Fatal("TriggerManual not found")
	}
	end := strings.Index(body[start:], "\n}\n\n")
	if end < 0 {
		t.Fatal("TriggerManual closing brace not found")
	}
	fn := body[start : start+end]

	for _, want := range []string{
		"COALESCE(c.status, 'active') = 'active'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.enabled, FALSE) = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"JOIN provider_models pm ON pm.id = cmb.provider_model_id",
		"COALESCE(c.status, 'active') <> 'active'",
		"COALESCE(p.enabled, FALSE) = FALSE",
		"COALESCE(cmb.unavailable_reason, '') LIKE 'manual%'",
		"return ErrCredentialManuallyDisabled",
		"return fmt.Errorf(\"binding not found\")",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("TriggerManual missing %q", want)
		}
	}
	if strings.Contains(fn, "SELECT id FROM provider_models WHERE raw_model_name = $2 LIMIT 1") {
		t.Error("TriggerManual fallback must not use an unscoped provider_models LIMIT 1 lookup")
	}
}

func TestNonfeaturedWatchdogSQLGuardsEligibility(t *testing.T) {
	src, err := os.ReadFile("model_probe.go")
	if err != nil {
		t.Fatalf("read model_probe.go: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func (r *ModelProbeRunner) nonfeaturedWatchdogTick(")
	if start < 0 {
		t.Fatal("nonfeaturedWatchdogTick not found")
	}
	end := strings.Index(body[start:], "\n}\n\n")
	if end < 0 {
		t.Fatal("nonfeaturedWatchdogTick closing brace not found")
	}
	fn := body[start : start+end]

	for _, want := range []string{
		"mps.state = 'healthy_confirmed'",
		"COALESCE(c.lifecycle_status, 'active') = 'active'",
		"COALESCE(c.manual_disabled, FALSE) = FALSE",
		"COALESCE(p.enabled, FALSE) = TRUE",
		"COALESCE(p.manual_disabled, FALSE) = FALSE",
		"COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'",
		"lower(mps.raw_model_name) NOT IN (SELECT model FROM static)",
		"lower(mps.raw_model_name) NOT IN (SELECT raw_model FROM usage)",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("nonfeaturedWatchdogTick missing %q", want)
		}
	}
}

func TestProbeWatchdogIndexMigrationMatchesQuery(t *testing.T) {
	src, err := os.ReadFile("../sql/migrations/startup/514_model_probe_watchdog_index.sql")
	if err != nil {
		t.Fatalf("read watchdog migration: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"CREATE INDEX IF NOT EXISTS idx_mps_healthy_confirmed_next_retry",
		"ON model_probe_state (next_retry_at)",
		"WHERE state = 'healthy_confirmed'",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("watchdog migration missing %q", want)
		}
	}
}
