package center

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitorInstances(t *testing.T) {
	dbURL := getEnvForTest("TEST_DATABASE_URL", "postgres://postgres:postgres@localhost:5432/llm_gateway_test?sslmode=disable")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	store := NewPgxStore(pool)

	// Create test instances with different last_heartbeat values
	now := time.Now()
	instances := []struct {
		id             string
		lastHeartbeat  time.Time
		expectedStatus string
	}{
		{"online-instance", now.Add(-60 * time.Second), StatusOnline},      // 60s ago → online
		{"degraded-instance", now.Add(-180 * time.Second), StatusDegraded}, // 180s ago → degraded
		{"offline-instance", now.Add(-400 * time.Second), StatusOffline},   // 400s ago → offline
	}

	// Insert test instances
	for _, inst := range instances {
		instance := &InstanceInfo{
			InstanceID:     inst.id + "-" + time.Now().Format("20060102150405"),
			Hostname:       "test-host",
			IPAddress:      "192.168.1.1",
			Version:        "v1.0.0",
			BuildSeq:       1,
			Status:         "online",
			StartedAt:      now,
			LicenseKeyHash: "test-license",
			HardwareHash:   "test-hardware",
		}

		// Insert instance
		require.NoError(t, store.RegisterInstance(ctx, instance))

		// Manually update last_heartbeat to simulate aging
		_, err := store.db.Exec(ctx, "UPDATE gateway_instances SET last_heartbeat = $1 WHERE instance_id = $2",
			inst.lastHeartbeat, instance.InstanceID)
		require.NoError(t, err)
	}

	// Run status update
	err = updateInstanceStatuses(ctx, store)
	require.NoError(t, err)

	// Verify statuses
	for i, inst := range instances {
		retrieved, err := store.GetInstance(ctx, instances[i].id+"-"+time.Now().Format("20060102150405"))
		if err != nil {
			// Instance might have been created with timestamp, query by pattern
			rows, err := store.db.Query(ctx, "SELECT status FROM gateway_instances WHERE instance_id LIKE $1 ORDER BY started_at DESC LIMIT 1", inst.id+"%")
			require.NoError(t, err)
			defer rows.Close()

			if rows.Next() {
				var status string
				require.NoError(t, rows.Scan(&status))
				assert.Equal(t, inst.expectedStatus, status, "instance %s should be %s", inst.id, inst.expectedStatus)
			}
		} else {
			assert.Equal(t, inst.expectedStatus, retrieved.Status, "instance %s should be %s", inst.id, inst.expectedStatus)
		}
	}
}

func getEnvForTest(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
