package startup

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

func readStatsMigration(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestStatsMigrationsContract(t *testing.T) {
	checks := map[string][]string{
		"536_stats_analytics_foundation.sql": {
			"CREATE TABLE IF NOT EXISTS stats_event_dedup",
			"CREATE TABLE IF NOT EXISTS stats_event_inbox",
			"PARTITION BY RANGE (occurred_at)",
			"CREATE TABLE IF NOT EXISTS stats_event_inbox_default",
			"idx_stats_event_inbox_pending",
		},
		"537_usage_facts.sql": {
			"CREATE TABLE IF NOT EXISTS usage_facts",
			"UNIQUE (event_id, revision, occurred_at)",
			"CREATE TABLE IF NOT EXISTS usage_facts_default",
		},
		"539_stats_reconciliation_tenant.sql": {
			"ADD COLUMN IF NOT EXISTS tenant_id text NOT NULL DEFAULT 'default'",
			"idx_stats_reconciliation_diffs_tenant",
		},
		"540_stats_event_inbox_consumer.sql": {
			"ADD COLUMN IF NOT EXISTS processing_status text NOT NULL DEFAULT 'pending'",
			"ADD COLUMN IF NOT EXISTS fencing_token bigint NOT NULL DEFAULT 0",
			"stats_event_inbox_processing_status_check",
			"processing_status IN ('pending', 'processing', 'processed', 'retryable', 'dead_letter')",
			"idx_stats_event_inbox_claimable",
			"idx_stats_event_inbox_dead_letter",
		},
	}
	for name, needles := range checks {
		body := readStatsMigration(t, name)
		for _, needle := range needles {
			if !strings.Contains(body, needle) {
				t.Errorf("%s: missing contract %q", name, needle)
			}
		}
	}
}

func TestStatsMigrationChecksumsAreStable(t *testing.T) {
	checksums := map[string]string{
		"536_stats_analytics_foundation.sql":  "a468b89c8ad83871f52fa4bc7c6f8e0c610c1b49ae581924b6e3bc009bbfcdb1",
		"537_usage_facts.sql":                 "d0b712cf9920b0f7f5ecdb00496c3c36be45b58c33c3dfe9c91b2901a252dac9",
		"539_stats_reconciliation_tenant.sql": "37c16b82524e8a4d7f4d69bd60a40629ec6fb0b46cb51b8874d4608eabb56515",
		"540_stats_event_inbox_consumer.sql":  "7241ab1a5e458e883d04031d97890049115f2e42fa5232fff94d48af8d7cfe0a",
	}
	for name, want := range checksums {
		got := fmt.Sprintf("%x", sha256.Sum256([]byte(readStatsMigration(t, name))))
		if got != want {
			t.Errorf("stats migration %s checksum changed: got %s want %s", name, got, want)
		}
	}
}
