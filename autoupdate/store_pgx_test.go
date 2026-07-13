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

func getTestDBURL() string {
	if url := os.Getenv("TEST_DATABASE_URL"); url != "" {
		return url
	}
	return "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable"
}

func setupTestStore(t *testing.T) (*PgxStore, context.Context, func()) {
	ctx := context.Background()
	dbURL := getTestDBURL()

	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err, "failed to connect to test database")
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("integration database unavailable: %v", err)
	}

	store := NewPgxStore(pool)

	cleanup := func() {
		// Clean up test data
		pool.Exec(ctx, "DELETE FROM instance_release_status WHERE instance_id LIKE 'test-%'")
		pool.Exec(ctx, "DELETE FROM upgrade_logs WHERE instance_id LIKE 'test-%'")
		pool.Exec(ctx, "DELETE FROM releases WHERE version LIKE 'test-%'")
		pool.Close()
	}

	return store, ctx, cleanup
}

func TestPgxStore_CreateRelease(t *testing.T) {
	store, ctx, cleanup := setupTestStore(t)
	defer cleanup()

	tests := []struct {
		name    string
		release *Release
		wantErr bool
	}{
		{
			name: "create stable release",
			release: &Release{
				Version:     "test-v1.0.0-" + time.Now().Format("20060102150405"),
				BuildSeq:    1000,
				Channel:     ChannelStable,
				Title:       "Test Release 1.0.0",
				Description: "Test release for unit tests",
				Changelog:   "- Added test feature",
				ImageTag:    "v1.0.0",
				ImageDigest: "sha256:abc123",
				MinVersion:  "v0.9.0",
				Mandatory:   false,
				CreatedBy:   "test-user",
			},
			wantErr: false,
		},
		{
			name: "create beta release",
			release: &Release{
				Version:     "test-v1.1.0-beta-" + time.Now().Format("20060102150405"),
				BuildSeq:    1100,
				Channel:     ChannelBeta,
				Title:       "Test Beta Release",
				Description: "Beta test release",
				Changelog:   "- Beta features",
				ImageTag:    "v1.1.0-beta",
				ImageDigest: "sha256:def456",
				Mandatory:   true,
				CreatedBy:   "test-user",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.CreateRelease(ctx, tt.release)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Greater(t, tt.release.ID, int64(0), "release ID should be assigned")
				assert.False(t, tt.release.CreatedAt.IsZero(), "created_at should be set")
			}
		})
	}
}

func TestPgxStore_GetLatestReleaseAfter(t *testing.T) {
	store, ctx, cleanup := setupTestStore(t)
	defer cleanup()

	// Create test releases
	baseTime := time.Now().Format("20060102150405")
	releases := []*Release{
		{
			Version:     "test-v1.0.0-" + baseTime,
			BuildSeq:    1000,
			Channel:     ChannelStable,
			Title:       "Release 1.0.0",
			ImageTag:    "v1.0.0",
			ImageDigest: "sha256:aaa",
			CreatedBy:   "test",
		},
		{
			Version:     "test-v1.1.0-" + baseTime,
			BuildSeq:    1100,
			Channel:     ChannelStable,
			Title:       "Release 1.1.0",
			ImageTag:    "v1.1.0",
			ImageDigest: "sha256:bbb",
			CreatedBy:   "test",
		},
		{
			Version:     "test-v1.2.0-" + baseTime,
			BuildSeq:    1200,
			Channel:     ChannelStable,
			Title:       "Release 1.2.0",
			ImageTag:    "v1.2.0",
			ImageDigest: "sha256:ccc",
			CreatedBy:   "test",
		},
	}

	for _, rel := range releases {
		err := store.CreateRelease(ctx, rel)
		require.NoError(t, err)

		// Publish the release
		err = store.UpdateReleaseStatus(ctx, rel.ID, true)
		require.NoError(t, err)
	}

	tests := []struct {
		name            string
		channel         Channel
		currentBuildSeq int
		expectVersion   string
		expectError     bool
	}{
		{
			name:            "get latest after 1000",
			channel:         ChannelStable,
			currentBuildSeq: 1000,
			expectVersion:   "test-v1.2.0-" + baseTime, // Should return latest (1200)
			expectError:     false,
		},
		{
			name:            "get latest after 1100",
			channel:         ChannelStable,
			currentBuildSeq: 1100,
			expectVersion:   "test-v1.2.0-" + baseTime, // Should return 1200
			expectError:     false,
		},
		{
			name:            "no newer version",
			channel:         ChannelStable,
			currentBuildSeq: 1200,
			expectVersion:   "",
			expectError:     true, // No newer version available
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release, err := store.GetLatestReleaseAfter(ctx, tt.channel, tt.currentBuildSeq)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, release)
				assert.Equal(t, tt.expectVersion, release.Version)
			}
		})
	}
}

func TestPgxStore_RecordUpdateReport(t *testing.T) {
	store, ctx, cleanup := setupTestStore(t)
	defer cleanup()

	// Create a test release
	baseTime := time.Now().Format("20060102150405")
	release := &Release{
		Version:     "test-v1.5.0-" + baseTime,
		BuildSeq:    1500,
		Channel:     ChannelStable,
		Title:       "Test Release 1.5.0",
		ImageTag:    "v1.5.0",
		ImageDigest: "sha256:test",
		CreatedBy:   "test",
	}
	err := store.CreateRelease(ctx, release)
	require.NoError(t, err)

	tests := []struct {
		name    string
		report  *UpdateReportData
		wantErr bool
	}{
		{
			name: "success report",
			report: &UpdateReportData{
				InstanceID:  "test-instance-success-" + baseTime,
				FromVersion: "v1.4.0",
				ToVersion:   release.Version,
				Status:      StatusSuccess,
				DurationMS:  5000,
				Error:       "",
			},
			wantErr: false,
		},
		{
			name: "failed report",
			report: &UpdateReportData{
				InstanceID:  "test-instance-failed-" + baseTime,
				FromVersion: "v1.4.0",
				ToVersion:   release.Version,
				Status:      StatusFailed,
				DurationMS:  2000,
				Error:       "download failed",
			},
			wantErr: false,
		},
		{
			name: "rollback report",
			report: &UpdateReportData{
				InstanceID:  "test-instance-rollback-" + baseTime,
				FromVersion: release.Version,
				ToVersion:   "v1.4.0",
				Status:      StatusRollback,
				DurationMS:  1000,
				Error:       "health check failed",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := store.RecordUpdateReport(ctx, tt.report)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify instance_release_status was updated
				status, err := store.GetInstanceStatus(ctx, tt.report.InstanceID)
				assert.NoError(t, err)
				assert.Equal(t, tt.report.Status, status.Status)
				assert.Equal(t, tt.report.ToVersion, status.Version)
			}
		})
	}
}

func TestPgxStore_GetUpgradeHistory(t *testing.T) {
	store, ctx, cleanup := setupTestStore(t)
	defer cleanup()

	instanceID := "test-instance-history-" + time.Now().Format("20060102150405")

	// Create multiple upgrade logs
	versions := []struct {
		from   string
		to     string
		status string
	}{
		{"v1.0.0", "v1.1.0", StatusSuccess},
		{"v1.1.0", "v1.2.0", StatusSuccess},
		{"v1.2.0", "v1.3.0", StatusFailed},
		{"v1.3.0", "v1.2.0", StatusRollback},
	}

	for _, v := range versions {
		logID, err := store.CreateUpgradeLog(ctx, instanceID, v.from, v.to)
		require.NoError(t, err)

		err = store.UpdateUpgradeLog(ctx, logID, v.status, "", time.Now())
		require.NoError(t, err)
	}

	// Get history
	history, _, err := store.GetUpgradeHistory(ctx, instanceID, 0, 10)
	assert.NoError(t, err)
	assert.Len(t, history, 4, "should retrieve all 4 logs")

	// Verify order (most recent first)
	assert.Equal(t, StatusRollback, history[0].Status)
	assert.Equal(t, StatusFailed, history[1].Status)
	assert.Equal(t, StatusSuccess, history[2].Status)
}
