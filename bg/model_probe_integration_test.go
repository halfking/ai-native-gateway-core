//go:build integration

package bg

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func TestNonfeaturedWatchdogIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pgContainer, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("testdb"),
		postgres.WithUsername("testuser"),
		postgres.WithPassword("testpass"))
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	connStr, err := pgContainer.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("get connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Create minimal schema for watchdog query
	schema := `
		CREATE TABLE IF NOT EXISTS providers (
			id BIGINT PRIMARY KEY,
			enabled BOOLEAN DEFAULT TRUE,
			manual_disabled BOOLEAN DEFAULT FALSE
		);
		CREATE TABLE IF NOT EXISTS credentials (
			id BIGINT PRIMARY KEY,
			provider_id BIGINT,
			status TEXT DEFAULT 'active',
			lifecycle_status TEXT DEFAULT 'active',
			manual_disabled BOOLEAN DEFAULT FALSE
		);
		CREATE TABLE IF NOT EXISTS provider_models (
			id BIGINT PRIMARY KEY,
			provider_id BIGINT,
			raw_model_name TEXT
		);
		CREATE TABLE IF NOT EXISTS credential_model_bindings (
			id BIGINT PRIMARY KEY,
			credential_id BIGINT,
			provider_model_id BIGINT,
			unavailable_reason TEXT
		);
		CREATE TABLE IF NOT EXISTS model_probe_state (
			credential_id BIGINT,
			raw_model_name TEXT,
			state TEXT DEFAULT 'unknown',
			next_retry_at TIMESTAMPTZ DEFAULT NOW(),
			PRIMARY KEY (credential_id, raw_model_name)
		);
		CREATE TABLE IF NOT EXISTS routing_policy (
			id SMALLINT PRIMARY KEY DEFAULT 1,
			tenant_id TEXT DEFAULT 'default',
			featured_models TEXT[]
		);
		CREATE TABLE IF NOT EXISTS request_logs_hot (
			id BIGINT PRIMARY KEY,
			ts TIMESTAMPTZ,
			success BOOLEAN,
			client_model TEXT,
			outbound_model TEXT
		);
		INSERT INTO routing_policy (id, tenant_id, featured_models)
		VALUES (1, 'default', ARRAY['gpt-4o']) ON CONFLICT DO NOTHING;
	`
	if _, err := pool.Exec(ctx, schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// Insert test fixtures
	_, err = pool.Exec(ctx, `
		INSERT INTO providers (id, enabled, manual_disabled) VALUES (1, TRUE, FALSE), (2, FALSE, FALSE);
		INSERT INTO credentials (id, provider_id, status, lifecycle_status, manual_disabled)
		VALUES (10, 1, 'active', 'active', FALSE), (20, 1, 'active', 'suspended', FALSE), (30, 2, 'active', 'active', FALSE);
		INSERT INTO provider_models (id, provider_id, raw_model_name) VALUES (100, 1, 'claude-3-5-sonnet'), (200, 2, 'gpt-4o-mini');
		INSERT INTO credential_model_bindings (id, credential_id, provider_model_id, unavailable_reason)
		VALUES (1000, 10, 100, NULL), (2000, 20, 100, NULL), (3000, 30, 200, 'manual_test');
		INSERT INTO model_probe_state (credential_id, raw_model_name, state, next_retry_at)
		VALUES (10, 'claude-3-5-sonnet', 'healthy_confirmed', NOW() - INTERVAL '1 hour'),
		       (20, 'claude-3-5-sonnet', 'healthy_confirmed', NOW() - INTERVAL '1 hour'),
		       (30, 'gpt-4o-mini', 'healthy_confirmed', NOW() - INTERVAL '1 hour');
	`)
	if err != nil {
		t.Fatalf("insert fixtures: %v", err)
	}

	// Apply watchdog index migration
	_, err = pool.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_mps_healthy_confirmed_next_retry
		    ON model_probe_state (next_retry_at)
		    WHERE state = 'healthy_confirmed';
	`)
	if err != nil {
		t.Fatalf("create index: %v", err)
	}

	// Execute watchdog update with eligibility filters
	tag, err := pool.Exec(ctx, `
		WITH static AS (
		    SELECT lower(unnest(COALESCE(
		        (SELECT featured_models FROM routing_policy WHERE tenant_id = $1 LIMIT 1),
		        ARRAY[]::TEXT[]
		    ))) AS model
		), usage AS (
		    SELECT lower(raw_model) AS raw_model FROM (
		        SELECT COALESCE(rl.outbound_model, rl.client_model) AS raw_model,
		               count(*) AS calls
		        FROM request_logs_hot rl
		        WHERE rl.success
		          AND rl.ts > now() - make_interval(hours => $2)
		          AND COALESCE(rl.outbound_model, rl.client_model) <> ''
		        GROUP BY raw_model
		    ) t
		    ORDER BY calls DESC LIMIT $3
		)
		UPDATE model_probe_state mps
		SET next_retry_at = now() + ($4 * interval '1 second')
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		JOIN credentials c ON c.id = cmb.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE mps.credential_id = cmb.credential_id
		  AND mps.raw_model_name = pm.raw_model_name
		  AND mps.state = 'healthy_confirmed'
		  AND (mps.next_retry_at IS NULL OR mps.next_retry_at < now() + ($4 * interval '1 second'))
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.enabled, FALSE) = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%'
		  AND lower(mps.raw_model_name) NOT IN (SELECT model FROM static)
		  AND lower(mps.raw_model_name) NOT IN (SELECT raw_model FROM usage)
	`, "default", 24, 10, 3600)
	if err != nil {
		t.Fatalf("watchdog update: %v", err)
	}

	// Assert only credential 10 (active+enabled+no manual flags) was updated
	if tag.RowsAffected() != 1 {
		t.Errorf("expected 1 updated row, got %d", tag.RowsAffected())
	}

	var updatedCreds []int64
	rows, err := pool.Query(ctx, `
		SELECT credential_id FROM model_probe_state
		WHERE next_retry_at > NOW() + INTERVAL '30 minutes'
		ORDER BY credential_id
	`)
	if err != nil {
		t.Fatalf("query updated: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		updatedCreds = append(updatedCreds, id)
	}
	if len(updatedCreds) != 1 || updatedCreds[0] != 10 {
		t.Errorf("expected only credential 10 updated, got %v", updatedCreds)
	}
}
