package providerprofile_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skipIfNoDB skips tests that need the real pg17.
func skipIfNoDB(t *testing.T) {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB test")
	}
}

// dbPoolFromTestURL builds a pool from TEST_DATABASE_URL.
func dbPoolFromTestURL(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// seedCredential inserts a throwaway active credential and returns its id.
func seedCredential(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	err := pool.QueryRow(context.Background(), `
		INSERT INTO credentials (provider_id, label, status, lifecycle_status, manual_disabled, fp_slot_limit)
		VALUES ($1, $2, 'active', 'active', false, 0) RETURNING id`,
		99990001, "pp-test-cred").Scan(&id)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM credentials WHERE id=$1`, id)
	})
	return id
}

func TestPGCredentialActor_DisableThenEnable(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)

	// Disable
	err := actor.Disable(ctx, credID, "总分连续3天低于40")
	require.NoError(t, err)

	var lifecycle, avail, reason string
	var disabledAt *time.Time
	err = pool.QueryRow(ctx, `
		SELECT lifecycle_status, availability_state, auto_disabled_reason, auto_disabled_at
		FROM credentials WHERE id=$1`, credID).
		Scan(&lifecycle, &avail, &reason, &disabledAt)
	require.NoError(t, err)
	assert.Equal(t, "disabled", lifecycle)
	assert.Equal(t, "suspended", avail)
	assert.Equal(t, "总分连续3天低于40", reason)
	require.NotNil(t, disabledAt)

	// Enable (manual_disabled is false → should succeed)
	err = actor.Enable(ctx, credID, "总分连续3天达到70")
	require.NoError(t, err)

	err = pool.QueryRow(ctx, `
		SELECT lifecycle_status, availability_state, auto_enabled_reason, auto_disabled_at
		FROM credentials WHERE id=$1`, credID).
		Scan(&lifecycle, &avail, &reason, &disabledAt)
	require.NoError(t, err)
	assert.Equal(t, "active", lifecycle)
	assert.Equal(t, "ready", avail)
	assert.Equal(t, "总分连续3天达到70", reason)
	assert.Nil(t, disabledAt, "auto_disabled_at must be cleared on enable")
}

func TestPGCredentialActor_EnableRefusesManualDisabled(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)
	// admin manually disables
	_, err := pool.Exec(ctx, `UPDATE credentials SET manual_disabled=true, lifecycle_status='disabled' WHERE id=$1`, credID)
	require.NoError(t, err)

	// auto-enable must refuse
	err = actor.Enable(ctx, credID, "should not apply")
	assert.ErrorIs(t, err, providerprofile.ErrManualDisabled)
}

func TestPGCredentialActor_Whitelist(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	const provID int64 = 99990002
	// add to whitelist
	_, err := pool.Exec(ctx, `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO provider_profile_whitelist (provider_id, reason) VALUES ($1, 'test') ON CONFLICT (provider_id) DO NOTHING`, provID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	})

	whitelisted, err := actor.IsWhitelisted(ctx, provID)
	require.NoError(t, err)
	assert.True(t, whitelisted, "provider should be whitelisted")

	whitelisted, err = actor.IsWhitelisted(ctx, 99999999)
	require.NoError(t, err)
	assert.False(t, whitelisted)
}

func TestPGCredentialActor_CurrentLifecycle(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)

	lc, err := actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Status)
	assert.False(t, lc.ManualDisabled)

	_, err = pool.Exec(ctx, `UPDATE credentials SET manual_disabled=true WHERE id=$1`, credID)
	require.NoError(t, err)
	lc, err = actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.True(t, lc.ManualDisabled)
}

func TestPGCredentialActor_RecordEvent(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	actor := providerprofile.NewPGCredentialActor(pool)
	ctx := context.Background()

	credID := seedCredential(t, pool)

	err := actor.RecordEvent(ctx, credID, "profile_auto_disabled", map[string]interface{}{"reason": "test", "score": 30.0})
	require.NoError(t, err)

	var kind string
	var payload map[string]interface{}
	err = pool.QueryRow(ctx, `SELECT event_kind, payload_json FROM provider_events WHERE credential_id=$1 ORDER BY ts DESC LIMIT 1`, credID).Scan(&kind, &payload)
	require.NoError(t, err)
	assert.Equal(t, "profile_auto_disabled", kind)
}
