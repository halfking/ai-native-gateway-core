package providerprofile_test

import (
	"context"
	"testing"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/providerprofile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPGProfileSource_Recent verifies the adapter returns value-typed
// []DailyProfile (not []*DailyProfile) ordered by date DESC, satisfying the
// AlertEngine's ProfileSource interface.
func TestPGProfileSource_Recent(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	ctx := context.Background()
	credID := seedCredential(t, pool)
	provID := int64(99990030)

	// seed 3 daily profiles on distinct dates (descending insertion order doesn't matter)
	dates := []time.Time{
		time.Date(2026, 7, 26, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 25, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC),
	}
	for i, d := range dates {
		_, err := pool.Exec(ctx, `
			INSERT INTO provider_profile_daily
			  (credential_id, provider_id, profile_date, total_score, availability_score, stability_score)
			VALUES ($1, $2, $3, $4, 90, 90)
			ON CONFLICT (credential_id, profile_date) DO UPDATE SET total_score=EXCLUDED.total_score`,
			credID, provID, d, float64(62-i))
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_profile_daily WHERE credential_id=$1`, credID)
	})

	src := providerprofile.NewPGProfileSource(providerprofile.NewPGProfileStore(pool))
	got, err := src.Recent(ctx, credID, 8)
	require.NoError(t, err)

	require.Len(t, got, 3, "should return all 3 seeded profiles")
	// ordered DESC (most recent first)
	assert.Equal(t, dates[0], got[0].ProfileDate, "most recent first")
	assert.Equal(t, dates[2], got[2].ProfileDate, "oldest last")
	// scores preserved
	assert.Equal(t, 60.0, got[2].TotalScore)
	assert.Equal(t, 62.0, got[0].TotalScore)
	// returns values, and providerID is populated
	assert.Equal(t, provID, got[0].ProviderID)
}

// TestPGProfileSource_RecentEmpty verifies no rows → empty slice (not nil) and no error.
func TestPGProfileSource_RecentEmpty(t *testing.T) {
	skipIfNoDB(t)
	pool := dbPoolFromTestURL(t)
	src := providerprofile.NewPGProfileSource(providerprofile.NewPGProfileStore(pool))

	got, err := src.Recent(context.Background(), 99999998, 8)
	require.NoError(t, err)
	assert.Empty(t, got)
}
