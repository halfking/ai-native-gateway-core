package v2

import (
	"context"
	"os"
	"strings"
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
		t.Skip("Test database not configured (set TEST_SESSION_V2_DATABASE_URL, TEST_DB_URL, or TEST_DATABASE_URL)")
	}
	if os.Getenv("TEST_SESSION_V2_ISOLATED") != "1" {
		t.Skip("TEST_SESSION_V2_ISOLATED=1 is required for mutating V2 database tests")
	}

	ctx := context.Background()
	db, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	t.Cleanup(db.Close)

	require.NoError(t, db.Ping(ctx))
	var database string
	require.NoError(t, db.QueryRow(ctx, `SELECT current_database()`).Scan(&database))
	if !strings.HasSuffix(database, "_test") {
		t.Fatalf("refusing mutating V2 database test against database %q; use a dedicated *_test database", database)
	}
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
// TEST_SESSION_V2_DATABASE_URL is the dedicated CI override. It takes
// precedence over legacy variables so runner residue cannot redirect a
// mutating test away from the database validated by the workflow.
//
// Local runs may still use TEST_DB_URL or TEST_DATABASE_URL. An explicit URL
// is required either way: a local DB with an incompatible schema must not
// silently make the default unit-test command fail.
func getTestDBURL() string {
	for _, key := range []string{"TEST_SESSION_V2_DATABASE_URL", "TEST_DB_URL", "TEST_DATABASE_URL"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}
