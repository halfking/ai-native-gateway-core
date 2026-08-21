package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_AlertEngineReportsWithoutChangingCredentialLifecycle verifies
// profile alerts are advisory. Model-level probe state is responsible for
// changing individual credential_model_bindings; profile scoring must not take
// the whole credential offline.
func TestIntegration_AlertEngineReportsWithoutChangingCredentialLifecycle(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()

	credID := seedCredential(t, pool)
	now := time.Now().UTC()
	day := func(offset int) time.Time { return now.AddDate(0, 0, offset).Truncate(24 * time.Hour) }

	seedDaily := func(score float64) {
		for _, off := range []int{0, -1, -2} {
			_, err := pool.Exec(ctx, `
				INSERT INTO provider_profile_daily
				  (credential_id, provider_id, profile_date, total_score, availability_score, stability_score)
				VALUES ($1, 99990001, $2, $3, 90, 90)
				ON CONFLICT (credential_id, profile_date) DO UPDATE
				  SET total_score = EXCLUDED.total_score,
				      availability_score = EXCLUDED.availability_score`,
				credID, day(off), score)
			require.NoError(t, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_daily WHERE credential_id=$1`, credID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_events WHERE credential_id=$1`, credID)
	})

	profileStore := providerprofile.NewPGProfileStore(pool)
	src := providerprofile.NewPGProfileSource(profileStore)
	alertStore := providerprofile.NewPGAlertStore(pool)
	actor := providerprofile.NewPGCredentialActor(pool)
	eng := providerprofile.NewAlertEngine(src, alertStore, actor, providerprofile.DefaultAlertConfig())

	// --- Phase 1: 3 low-score days → alert only ---
	seedDaily(30)
	res, err := eng.EvaluateCredential(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "none", res.Action, "low-score profile must not disable the credential")

	lc, err := actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Lifecycle, "credential lifecycle must stay active")

	// an auto_disabled alert must have been persisted
	var alertCount int
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM provider_profile_alerts WHERE credential_id=$1 AND alert_type='auto_disabled'`,
		credID).Scan(&alertCount)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, alertCount, 1, "auto_disabled alert persisted")

	// --- Phase 2: 3 high-score days → alert only ---
	seedDaily(75)
	res, err = eng.EvaluateCredential(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "none", res.Action, "recovery profile must not enable the credential")

	lc, err = actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Lifecycle, "credential lifecycle must remain active")

	// an auto_enabled alert must have been persisted
	err = pool.QueryRow(ctx,
		`SELECT count(*) FROM provider_profile_alerts WHERE credential_id=$1 AND alert_type='auto_enabled'`,
		credID).Scan(&alertCount)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, alertCount, 1, "auto_enabled alert persisted")
}

// TestIntegration_AlertEngineWhitelistBlocksDisable verifies a whitelisted
// provider records the alert but does NOT flip lifecycle.
func TestIntegration_AlertEngineWhitelistBlocksDisable(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()

	// Use a distinct provider_id and whitelist it.
	const provID int64 = 99990050
	_, err := pool.Exec(ctx, `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO provider_profile_whitelist (provider_id, reason) VALUES ($1,'test') ON CONFLICT (provider_id) DO NOTHING`, provID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_whitelist WHERE provider_id=$1`, provID)
	})

	credID := seedCredentialWithProvider(t, pool, provID)
	now := time.Now().UTC()
	for _, off := range []int{0, -1, -2} {
		_, err := pool.Exec(ctx, `
			INSERT INTO provider_profile_daily (credential_id, provider_id, profile_date, total_score, availability_score, stability_score)
			VALUES ($1, $2, $3, 30, 90, 90)
			ON CONFLICT (credential_id, profile_date) DO UPDATE SET total_score=EXCLUDED.total_score`,
			credID, provID, now.AddDate(0, 0, off).Truncate(24*time.Hour))
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_alerts WHERE credential_id=$1`, credID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_daily WHERE credential_id=$1`, credID)
	})

	profileStore := providerprofile.NewPGProfileStore(pool)
	src := providerprofile.NewPGProfileSource(profileStore)
	alertStore := providerprofile.NewPGAlertStore(pool)
	actor := providerprofile.NewPGCredentialActor(pool)
	eng := providerprofile.NewAlertEngine(src, alertStore, actor, providerprofile.DefaultAlertConfig())

	res, err := eng.EvaluateCredential(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "none", res.Action, "whitelist must suppress auto-disable")

	lc, err := actor.CurrentLifecycle(ctx, credID)
	require.NoError(t, err)
	assert.Equal(t, "active", lc.Lifecycle, "lifecycle must stay active for whitelisted provider")
}
