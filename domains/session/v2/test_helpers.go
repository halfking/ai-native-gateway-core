package v2

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// setupTestDB creates a test database connection
//
// This is shared across all V2 tests to avoid duplication.
func setupTestDB(t *testing.T) *pgxpool.Pool {
	dbURL := getTestDBURL()
	if dbURL == "" {
		t.Skip("Test database not configured (set TEST_DB_URL)")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)

	// Verify connection
	err = db.Ping(ctx)
	require.NoError(t, err)

	return db
}

// cleanupTestDB cleans up test data from all V2 tables
//
// This is shared across all V2 tests to avoid duplication.
func cleanupTestDB(t *testing.T, db *pgxpool.Pool) {
	ctx := context.Background()

	// Delete test data from all tables
	tables := []string{
		"public.sessions",
		"public.session_bodies",
		"public.session_turns",
		"public.session_turn_logs",
	}

	for _, table := range tables {
		_, err := db.Exec(ctx, `DELETE FROM `+table+` WHERE tenant_id = 'test_tenant'`)
		if err != nil {
			t.Logf("Warning: cleanup %s failed: %v", table, err)
		}
	}

	db.Close()
}

// getTestDBURL returns the test database URL from environment.
//
// Preference: TEST_DB_URL (legacy v2-suite convention) first, then
// TEST_DATABASE_URL (the variable the CI 'gateway-rls' job injects via
// secrets). Accepting both lets these integration tests run under the
// existing CI job without duplicating the secret, while keeping the
// shorter name working for local runs.
//
// An explicit URL is required either way: a local DB with an
// incompatible schema must not silently make the default unit-test
// command fail.
func getTestDBURL() string {
	if v := os.Getenv("TEST_DB_URL"); v != "" {
		return v
	}
	return os.Getenv("TEST_DATABASE_URL")
}
