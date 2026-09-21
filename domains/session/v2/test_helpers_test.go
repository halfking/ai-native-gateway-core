package v2

import "testing"

func TestGetTestDBURLPrefersDedicatedV2DSN(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", "postgres://fallback")
	t.Setenv("TEST_DB_URL", "postgres://legacy")
	t.Setenv("TEST_SESSION_V2_DATABASE_URL", "postgres://dedicated")

	if got := getTestDBURL(); got != "postgres://dedicated" {
		t.Fatalf("getTestDBURL() = %q, want dedicated V2 DSN", got)
	}
}

func TestGetTestDBURLFallsBackForLocalRuns(t *testing.T) {
	t.Setenv("TEST_SESSION_V2_DATABASE_URL", "")
	t.Setenv("TEST_DATABASE_URL", "postgres://fallback")
	t.Setenv("TEST_DB_URL", "postgres://legacy")

	if got := getTestDBURL(); got != "postgres://legacy" {
		t.Fatalf("getTestDBURL() = %q, want legacy local DSN", got)
	}
}
