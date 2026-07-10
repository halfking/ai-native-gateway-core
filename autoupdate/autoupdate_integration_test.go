//go:build integration

package autoupdate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutoUpdateIntegration tests the complete auto-update flow:
// Create Release → Publish Version → Create Gray Rule → Upgrade Instance → Rollback
//
// To run:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn>
//	go test -tags=integration ./autoupdate -v -run TestAutoUpdateIntegration
func TestAutoUpdateIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set, skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err, "failed to connect to database")
	defer pool.Close()

	store := NewPgxStore(pool)

	// Clean up test data
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_upgrade_logs WHERE release_id IN (SELECT id FROM autoupdate_releases WHERE version LIKE 'v0.0.0-test-%')")
		_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_gray_rules WHERE release_id IN (SELECT id FROM autoupdate_releases WHERE version LIKE 'v0.0.0-test-%')")
		_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_releases WHERE version LIKE 'v0.0.0-test-%'")
	}()

	t.Run("CompleteReleaseFlow", func(t *testing.T) {
		// Step 1: Create Release
		version := "v0.0.0-test-" + time.Now().Format("20060102-150405")
		release := &Release{
			Version:     version,
			BuildSeq:    12345,
			Channel:     ChannelBeta,
			Title:       "Test Release",
			Description: "Integration test release",
			Changelog:   "- Test feature 1\n- Test feature 2",
			ImageTag:    "registry.example.com/app:" + version,
			ImageDigest: "sha256:abcdef1234567890",
			MinVersion:  "v1.0.0",
			Mandatory:   false,
			CreatedBy:   "integration-test",
			CreatedAt:   time.Now(),
		}

		err := store.CreateRelease(ctx, release)
		require.NoError(t, err, "CreateRelease should succeed")
		assert.Greater(t, release.ID, int64(0), "Release ID should be assigned")

		// Step 2: Publish Release
		err = store.UpdateReleaseStatus(ctx, release.ID, true)
		require.NoError(t, err, "UpdateReleaseStatus should succeed")

		// Verify release is published
		publishedRelease, err := store.GetRelease(ctx, version)
		require.NoError(t, err)
		assert.NotNil(t, publishedRelease.PublishedAt, "PublishedAt should be set")

		// Step 3: Create Gray Release Rule (Canary Phase)
		grayRule := &GrayReleaseRule{
			ReleaseID: release.ID,
			Phase:     PhaseCanary,
			Percent:   5,
			Status:    "active",
			CreatedAt: time.Now(),
		}

		err = store.CreateGrayRule(ctx, grayRule)
		require.NoError(t, err, "CreateGrayRule should succeed")

		// Step 4: Simulate Instance Upgrade
		instanceID := "test-instance-" + time.Now().Format("150405")
		logID, err := store.CreateUpgradeLog(ctx, instanceID, "v1.0.0", version)
		require.NoError(t, err, "CreateUpgradeLog should succeed")
		assert.Greater(t, logID, int64(0))

		// Update instance status through upgrade stages
		statuses := []string{StatusPending, StatusDownload, StatusReady, StatusUpgrading, StatusSuccess}

		for _, status := range statuses {
			releaseStatus := &ReleaseStatus{
				ReleaseID:  release.ID,
				InstanceID: instanceID,
				Status:     status,
				Version:    version,
				StartedAt:  time.Now(),
			}

			if status == StatusSuccess {
				now := time.Now()
				releaseStatus.CompletedAt = &now
			}

			err = store.UpdateInstanceStatus(ctx, releaseStatus)
			require.NoError(t, err, "UpdateInstanceStatus should succeed for status: %s", status)
			time.Sleep(10 * time.Millisecond)
		}

		// Verify final status
		finalStatus, err := store.GetInstanceStatus(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, StatusSuccess, finalStatus.Status)
		assert.NotNil(t, finalStatus.CompletedAt)

		// Complete upgrade log
		err = store.UpdateUpgradeLog(ctx, logID, StatusSuccess, "", time.Now())
		require.NoError(t, err)

		// Step 5: Progress Gray Release to Batch1
		err = store.UpdateGrayPhase(ctx, release.ID, PhaseBatch1, 25)
		require.NoError(t, err, "UpdateGrayPhase should succeed")

		updatedRule, err := store.GetGrayRule(ctx, release.ID)
		require.NoError(t, err)
		assert.Equal(t, PhaseBatch1, updatedRule.Phase)
		assert.Equal(t, 25, updatedRule.Percent)

		// Step 6: Get Upgrade History
		history, err := store.GetUpgradeHistory(ctx, instanceID, 10)
		require.NoError(t, err)
		assert.NotEmpty(t, history, "Should have upgrade history")
	})

	t.Run("VersionComparison", func(t *testing.T) {
		// Create multiple releases with different versions
		versions := []string{
			"v0.0.0-test-1.0.0-" + time.Now().Format("150405"),
			"v0.0.0-test-1.1.0-" + time.Now().Format("150405"),
			"v0.0.0-test-2.0.0-" + time.Now().Format("150405"),
		}

		for i, ver := range versions {
			release := &Release{
				Version:     ver,
				BuildSeq:    10000 + i,
				Channel:     ChannelStable,
				Title:       "Version Test " + ver,
				Description: "Test release for version comparison",
				Changelog:   "Test changelog",
				ImageTag:    "registry.example.com/app:" + ver,
				Mandatory:   false,
				CreatedBy:   "integration-test",
				CreatedAt:   time.Now(),
			}

			err := store.CreateRelease(ctx, release)
			require.NoError(t, err, "CreateRelease should succeed for %s", ver)
			time.Sleep(10 * time.Millisecond)
		}

		// Get latest release
		latest, err := store.GetLatestRelease(ctx, ChannelStable)
		require.NoError(t, err, "GetLatestRelease should succeed")
		assert.NotNil(t, latest)
		t.Logf("Latest release: %s (build %d)", latest.Version, latest.BuildSeq)

		// List all releases
		releases, total, err := store.ListReleases(ctx, ChannelStable, 0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(releases), 3, "Should have at least 3 test releases")
		assert.GreaterOrEqual(t, total, 3)
	})

	t.Run("RollbackScenario", func(t *testing.T) {
		// Create a release
		version := "v0.0.0-test-rollback-" + time.Now().Format("20060102-150405")
		release := &Release{
			Version:     version,
			BuildSeq:    99999,
			Channel:     ChannelBeta,
			Title:       "Rollback Test Release",
			Description: "This release will be rolled back",
			Changelog:   "- Breaking change",
			ImageTag:    "registry.example.com/app:" + version,
			Mandatory:   false,
			CreatedBy:   "integration-test",
			CreatedAt:   time.Now(),
		}

		err := store.CreateRelease(ctx, release)
		require.NoError(t, err)

		// Publish it
		err = store.UpdateReleaseStatus(ctx, release.ID, true)
		require.NoError(t, err)

		// Start upgrade on instance
		instanceID := "rollback-instance-" + time.Now().Format("150405")
		logID, err := store.CreateUpgradeLog(ctx, instanceID, "v1.0.0", version)
		require.NoError(t, err)

		// Simulate upgrade failure
		err = store.UpdateUpgradeLog(ctx, logID, StatusFailed, "Compatibility check failed", time.Now())
		require.NoError(t, err, "UpdateUpgradeLog with failure should succeed")

		// Mark instance as rolled back
		rollbackStatus := &ReleaseStatus{
			ReleaseID:  release.ID,
			InstanceID: instanceID,
			Status:     StatusRollback,
			Version:    "v1.0.0", // Rolled back to previous version
			StartedAt:  time.Now(),
			Error:      "Upgrade failed, rolled back to v1.0.0",
		}

		err = store.UpdateInstanceStatus(ctx, rollbackStatus)
		require.NoError(t, err, "UpdateInstanceStatus for rollback should succeed")

		// Verify rollback status
		status, err := store.GetInstanceStatus(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, StatusRollback, status.Status)
		assert.Equal(t, "v1.0.0", status.Version)

		// Get upgrade history to see failed attempt
		history, err := store.GetUpgradeHistory(ctx, instanceID, 5)
		require.NoError(t, err)
		assert.NotEmpty(t, history)

		var foundFailed bool
		for _, h := range history {
			if h.Status == StatusFailed {
				foundFailed = true
				break
			}
		}
		assert.True(t, foundFailed, "Should find failed upgrade in history")
	})

	t.Run("GrayReleaseProgression", func(t *testing.T) {
		// Create release
		version := "v0.0.0-test-gray-" + time.Now().Format("20060102-150405")
		release := &Release{
			Version:     version,
			BuildSeq:    88888,
			Channel:     ChannelStable,
			Title:       "Gray Release Test",
			Description: "Testing gray release progression",
			Changelog:   "- Gradual rollout",
			ImageTag:    "registry.example.com/app:" + version,
			Mandatory:   false,
			CreatedBy:   "integration-test",
			CreatedAt:   time.Now(),
		}

		err := store.CreateRelease(ctx, release)
		require.NoError(t, err)

		err = store.UpdateReleaseStatus(ctx, release.ID, true)
		require.NoError(t, err)

		// Progress through phases
		phases := []struct {
			phase   Phase
			percent int
		}{
			{PhaseCanary, 5},
			{PhaseBatch1, 25},
			{PhaseBatch2, 50},
			{PhaseBatch3, 75},
			{PhaseFull, 100},
		}

		for _, p := range phases {
			grayRule := &GrayReleaseRule{
				ReleaseID: release.ID,
				Phase:     p.phase,
				Percent:   p.percent,
				Status:    "active",
				CreatedAt: time.Now(),
			}

			// For first phase, create; for others, update
			if p.phase == PhaseCanary {
				err = store.CreateGrayRule(ctx, grayRule)
				require.NoError(t, err, "CreateGrayRule should succeed")
			} else {
				err = store.UpdateGrayPhase(ctx, release.ID, p.phase, p.percent)
				require.NoError(t, err, "UpdateGrayPhase should succeed for phase %s", p.phase)
			}

			// Verify phase update
			currentRule, err := store.GetGrayRule(ctx, release.ID)
			require.NoError(t, err)
			assert.Equal(t, p.phase, currentRule.Phase)
			assert.Equal(t, p.percent, currentRule.Percent)

			t.Logf("Phase %s: %d%% rollout", p.phase, p.percent)
			time.Sleep(10 * time.Millisecond)
		}
	})

	t.Run("MandatoryUpdate", func(t *testing.T) {
		// Create mandatory release
		version := "v0.0.0-test-mandatory-" + time.Now().Format("20060102-150405")
		release := &Release{
			Version:     version,
			BuildSeq:    77777,
			Channel:     ChannelStable,
			Title:       "Critical Security Update",
			Description: "This update must be applied immediately",
			Changelog:   "- Critical security patch\n- Must update",
			ImageTag:    "registry.example.com/app:" + version,
			MinVersion:  "v1.0.0",
			Mandatory:   true, // Mandatory flag
			CreatedBy:   "security-team",
			CreatedAt:   time.Now(),
		}

		err := store.CreateRelease(ctx, release)
		require.NoError(t, err)

		err = store.UpdateReleaseStatus(ctx, release.ID, true)
		require.NoError(t, err)

		// Verify mandatory flag
		retrieved, err := store.GetRelease(ctx, version)
		require.NoError(t, err)
		assert.True(t, retrieved.Mandatory, "Mandatory flag should be true")
		assert.Equal(t, "v1.0.0", retrieved.MinVersion)

		t.Log("Mandatory update created successfully")
	})

	t.Run("MultiChannelReleases", func(t *testing.T) {
		timestamp := time.Now().Format("150405")
		channels := []Channel{ChannelCanary, ChannelBeta, ChannelStable}

		for i, channel := range channels {
			version := "v0.0.0-test-" + string(channel) + "-" + timestamp
			release := &Release{
				Version:     version,
				BuildSeq:    60000 + i,
				Channel:     channel,
				Title:       "Release for " + string(channel),
				Description: "Testing multi-channel releases",
				Changelog:   "Channel-specific changelog",
				ImageTag:    "registry.example.com/app:" + version,
				Mandatory:   false,
				CreatedBy:   "integration-test",
				CreatedAt:   time.Now(),
			}

			err := store.CreateRelease(ctx, release)
			require.NoError(t, err, "CreateRelease should succeed for channel %s", channel)

			err = store.UpdateReleaseStatus(ctx, release.ID, true)
			require.NoError(t, err)

			time.Sleep(10 * time.Millisecond)
		}

		// Verify each channel has releases
		for _, channel := range channels {
			releases, total, err := store.ListReleases(ctx, channel, 0, 5)
			require.NoError(t, err, "ListReleases should succeed for channel %s", channel)
			assert.GreaterOrEqual(t, len(releases), 1, "Should have releases for channel %s", channel)
			assert.GreaterOrEqual(t, total, 1)

			latest, err := store.GetLatestRelease(ctx, channel)
			require.NoError(t, err, "GetLatestRelease should succeed for channel %s", channel)
			assert.NotNil(t, latest)
			assert.Equal(t, channel, latest.Channel)
		}
	})
}

// TestUpgradeRetry tests retry mechanism for failed upgrades
func TestUpgradeRetry(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	pgURL := os.Getenv("LLM_GATEWAY_PG_URL")
	if pgURL == "" {
		t.Skip("LLM_GATEWAY_PG_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgURL)
	require.NoError(t, err)
	defer pool.Close()

	store := NewPgxStore(pool)

	t.Run("RetryMechanism", func(t *testing.T) {
		version := "v0.0.0-test-retry-" + time.Now().Format("20060102-150405")
		instanceID := "retry-instance-" + time.Now().Format("150405")

		// Create upgrade log with retries
		logID, err := store.CreateUpgradeLog(ctx, instanceID, "v1.0.0", version)
		require.NoError(t, err)

		// Simulate multiple retry attempts
		maxRetries := 3
		for retry := 1; retry <= maxRetries; retry++ {
			status := &ReleaseStatus{
				ReleaseID:  1, // Dummy release ID
				InstanceID: instanceID,
				Status:     StatusFailed,
				Version:    version,
				StartedAt:  time.Now(),
				Error:      "Network timeout",
				RetryCount: retry,
			}

			err = store.UpdateInstanceStatus(ctx, status)
			require.NoError(t, err, "Retry %d should be recorded", retry)
			time.Sleep(10 * time.Millisecond)
		}

		// Final successful attempt
		successStatus := &ReleaseStatus{
			ReleaseID:  1,
			InstanceID: instanceID,
			Status:     StatusSuccess,
			Version:    version,
			StartedAt:  time.Now(),
			RetryCount: maxRetries + 1,
		}

		now := time.Now()
		successStatus.CompletedAt = &now

		err = store.UpdateInstanceStatus(ctx, successStatus)
		require.NoError(t, err)

		// Update log with final status
		err = store.UpdateUpgradeLog(ctx, logID, StatusSuccess, "", time.Now())
		require.NoError(t, err)

		// Verify final status
		finalStatus, err := store.GetInstanceStatus(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, StatusSuccess, finalStatus.Status)
		assert.Equal(t, maxRetries+1, finalStatus.RetryCount)

		// Clean up
		_, _ = pool.Exec(ctx, "DELETE FROM autoupdate_upgrade_logs WHERE id = $1", logID)
	})
}
