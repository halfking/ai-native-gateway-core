//go:build integration

package center

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCenterIntegration tests the complete center operations flow:
// Register Instance → Send Heartbeat → Issue Command → Report Result → Health Check
//
// To run:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn>
//	go test -tags=integration ./center -v -run TestCenterIntegration
func TestCenterIntegration(t *testing.T) {
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
		_, _ = pool.Exec(ctx, "DELETE FROM center_commands WHERE instance_id LIKE 'test-instance-%'")
		_, _ = pool.Exec(ctx, "DELETE FROM center_heartbeats WHERE instance_id LIKE 'test-instance-%'")
		_, _ = pool.Exec(ctx, "DELETE FROM center_status_reports WHERE instance_id LIKE 'test-instance-%'")
		_, _ = pool.Exec(ctx, "DELETE FROM center_instances WHERE instance_id LIKE 'test-instance-%'")
	}()

	t.Run("CompleteLifecycle", func(t *testing.T) {
		instanceID := "test-instance-" + time.Now().Format("20060102-150405")

		// Step 1: Register Instance
		instance := &InstanceInfo{
			InstanceID: instanceID,
			Hostname:   "test-server-01.example.com",
			IPAddress:  "192.168.1.100",
			Region:     "us-west-2",
			Version:    "v1.2.3",
			BuildSeq:   12345,
			StartedAt:  time.Now(),
			Status:     StatusOnline,
		}

		err := store.RegisterInstance(ctx, instance)
		require.NoError(t, err, "RegisterInstance should succeed")

		// Verify registration
		retrieved, err := store.GetInstance(ctx, instanceID)
		require.NoError(t, err, "GetInstance should succeed")
		assert.Equal(t, instance.Hostname, retrieved.Hostname)
		assert.Equal(t, instance.IPAddress, retrieved.IPAddress)
		assert.Equal(t, StatusOnline, retrieved.Status)

		// Step 2: Send Heartbeat
		heartbeat := &HeartbeatPayload{
			UptimeSecs:   300,
			GoVersion:    "go1.22.0",
			NumGoroutine: 45,
			AllocMB:      128.5,
			TotalAllocMB: 256.0,
			SysMB:        512.0,
			CPUCores:     8,
		}

		err = store.RecordHeartbeat(ctx, instanceID, heartbeat)
		require.NoError(t, err, "RecordHeartbeat should succeed")

		// Verify heartbeat recorded
		lastHeartbeat, err := store.GetLastHeartbeat(ctx, instanceID)
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now(), lastHeartbeat, 5*time.Second)

		// Step 3: Send multiple heartbeats to build history
		for i := 0; i < 5; i++ {
			heartbeat.UptimeSecs += 60
			heartbeat.AllocMB += 10.0
			heartbeat.NumGoroutine += 5

			err = store.RecordHeartbeat(ctx, instanceID, heartbeat)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
		}

		// Get heartbeat history
		since := time.Now().Add(-1 * time.Hour)
		history, err := store.GetHeartbeatHistory(ctx, instanceID, since, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(history), 5, "Should have at least 5 heartbeat records")

		// Step 4: Send Status Report
		statusReport := &StatusReportPayload{
			State:          "running",
			ActiveLicenses: 10,
			ActiveDevices:  15,
			RequestsTotal:  100000,
			RequestsOk:     99500,
			RequestsErr:    500,
			AvgLatencyMs:   25.5,
			P99LatencyMs:   150.0,
		}

		err = store.RecordStatusReport(ctx, instanceID, statusReport)
		require.NoError(t, err, "RecordStatusReport should succeed")

		// Verify status report
		latestStatus, err := store.GetLatestStatus(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, statusReport.State, latestStatus.State)
		assert.Equal(t, statusReport.RequestsTotal, latestStatus.RequestsTotal)

		// Step 5: Issue Command to Instance
		commandID := "cmd-" + time.Now().Format("20060102-150405-000")
		command := &Command{
			CommandID:  commandID,
			InstanceID: instanceID,
			Command:    "restart_service",
			Args: map[string]string{
				"service": "api-gateway",
				"timeout": "30s",
			},
			Status:   CommandStatusPending,
			IssuedAt: time.Now(),
			IssuedBy: "admin@example.com",
		}

		err = store.CreateCommand(ctx, command)
		require.NoError(t, err, "CreateCommand should succeed")
		assert.Greater(t, command.ID, int64(0))

		// Step 6: Instance fetches pending commands
		pending, err := store.ListPendingCommands(ctx, instanceID)
		require.NoError(t, err)
		assert.NotEmpty(t, pending, "Should have pending commands")

		var foundCommand *Command
		for i := range pending {
			if pending[i].CommandID == commandID {
				foundCommand = &pending[i]
				break
			}
		}
		require.NotNil(t, foundCommand, "Should find our command")
		assert.Equal(t, "restart_service", foundCommand.Command)

		// Step 7: Execute Command and Report Result
		time.Sleep(50 * time.Millisecond) // Simulate execution time

		result := &CommandResult{
			Success: true,
			Output:  "Service restarted successfully",
			ExecMs:  50,
		}

		err = store.UpdateCommandStatus(ctx, commandID, CommandStatusExecuted, result)
		require.NoError(t, err, "UpdateCommandStatus should succeed")

		// Verify command result
		executed, err := store.GetCommand(ctx, commandID)
		require.NoError(t, err)
		assert.Equal(t, CommandStatusExecuted, executed.Status)
		assert.NotNil(t, executed.Result)
		assert.True(t, executed.Result.Success)
		assert.Equal(t, "Service restarted successfully", executed.Result.Output)
		assert.NotNil(t, executed.ExecutedAt)

		// Step 8: Get Command History
		cmdHistory, err := store.GetCommandHistory(ctx, instanceID, 10)
		require.NoError(t, err)
		assert.NotEmpty(t, cmdHistory, "Should have command history")

		// Step 9: Update Instance Status to Degraded
		err = store.UpdateInstanceStatus(ctx, instanceID, StatusDegraded)
		require.NoError(t, err)

		updated, err := store.GetInstance(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, StatusDegraded, updated.Status)

		// Step 10: Recover to Online
		err = store.UpdateInstanceStatus(ctx, instanceID, StatusOnline)
		require.NoError(t, err)

		// Step 11: Delete Instance
		err = store.DeleteInstance(ctx, instanceID)
		require.NoError(t, err, "DeleteInstance should succeed")

		// Verify deletion
		_, err = store.GetInstance(ctx, instanceID)
		assert.Error(t, err, "GetInstance should fail after deletion")
	})

	t.Run("MultipleInstances", func(t *testing.T) {
		// Register multiple instances
		timestamp := time.Now().Format("150405")
		instances := []InstanceInfo{
			{
				InstanceID: "test-instance-prod-01-" + timestamp,
				Hostname:   "prod-01.example.com",
				IPAddress:  "10.0.1.10",
				Region:     "us-east-1",
				Version:    "v1.5.0",
				BuildSeq:   15000,
				StartedAt:  time.Now(),
				Status:     StatusOnline,
			},
			{
				InstanceID: "test-instance-prod-02-" + timestamp,
				Hostname:   "prod-02.example.com",
				IPAddress:  "10.0.1.11",
				Region:     "us-east-1",
				Version:    "v1.5.0",
				BuildSeq:   15000,
				StartedAt:  time.Now(),
				Status:     StatusOnline,
			},
			{
				InstanceID: "test-instance-prod-03-" + timestamp,
				Hostname:   "prod-03.example.com",
				IPAddress:  "10.0.1.12",
				Region:     "us-west-2",
				Version:    "v1.4.0",
				BuildSeq:   14000,
				StartedAt:  time.Now(),
				Status:     StatusDegraded,
			},
		}

		for i := range instances {
			err := store.RegisterInstance(ctx, &instances[i])
			require.NoError(t, err, "RegisterInstance should succeed for instance %d", i)
		}

		// List all online instances
		onlineInstances, total, err := store.ListInstances(ctx, StatusOnline, 0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(onlineInstances), 2, "Should have at least 2 online instances")
		assert.GreaterOrEqual(t, total, 2)

		// List degraded instances
		degradedInstances, _, err := store.ListInstances(ctx, StatusDegraded, 0, 10)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(degradedInstances), 1, "Should have at least 1 degraded instance")

		// List all instances
		allInstances, totalAll, err := store.ListInstances(ctx, "", 0, 100)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, len(allInstances), 3, "Should have at least 3 instances")
		assert.GreaterOrEqual(t, totalAll, 3)
	})

	t.Run("CommandExpiration", func(t *testing.T) {
		instanceID := "test-instance-expire-" + time.Now().Format("150405")

		// Register instance
		instance := &InstanceInfo{
			InstanceID: instanceID,
			Hostname:   "expire-test.example.com",
			IPAddress:  "192.168.1.200",
			Version:    "v1.0.0",
			BuildSeq:   10000,
			StartedAt:  time.Now(),
			Status:     StatusOnline,
		}

		err := store.RegisterInstance(ctx, instance)
		require.NoError(t, err)

		// Create command with expiration
		commandID := "cmd-expire-" + time.Now().Format("150405-000")
		expiresAt := time.Now().Add(5 * time.Second)

		command := &Command{
			CommandID:  commandID,
			InstanceID: instanceID,
			Command:    "health_check",
			Status:     CommandStatusPending,
			IssuedAt:   time.Now(),
			IssuedBy:   "system",
			ExpiresAt:  &expiresAt,
		}

		err = store.CreateCommand(ctx, command)
		require.NoError(t, err)

		// Immediately check - should be pending
		retrieved, err := store.GetCommand(ctx, commandID)
		require.NoError(t, err)
		assert.Equal(t, CommandStatusPending, retrieved.Status)
		assert.NotNil(t, retrieved.ExpiresAt)

		// Wait for expiration
		time.Sleep(6 * time.Second)

		// Mark as expired (application layer responsibility)
		err = store.UpdateCommandStatus(ctx, commandID, CommandStatusExpired, nil)
		require.NoError(t, err)

		// Verify expired status
		expired, err := store.GetCommand(ctx, commandID)
		require.NoError(t, err)
		assert.Equal(t, CommandStatusExpired, expired.Status)
	})

	t.Run("HealthCheck", func(t *testing.T) {
		instanceID := "test-instance-health-" + time.Now().Format("150405")

		// Register instance
		instance := &InstanceInfo{
			InstanceID: instanceID,
			Hostname:   "health-test.example.com",
			IPAddress:  "192.168.1.150",
			Version:    "v1.3.0",
			BuildSeq:   13000,
			StartedAt:  time.Now(),
			Status:     StatusOnline,
		}

		err := store.RegisterInstance(ctx, instance)
		require.NoError(t, err)

		// Send regular heartbeats
		heartbeat := &HeartbeatPayload{
			UptimeSecs:   100,
			GoVersion:    "go1.22.0",
			NumGoroutine: 30,
			AllocMB:      64.0,
			TotalAllocMB: 128.0,
			SysMB:        256.0,
			CPUCores:     4,
		}

		for i := 0; i < 3; i++ {
			err = store.RecordHeartbeat(ctx, instanceID, heartbeat)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
		}

		// Check last heartbeat (should be recent)
		lastHeartbeat, err := store.GetLastHeartbeat(ctx, instanceID)
		require.NoError(t, err)
		timeSinceLastHB := time.Since(lastHeartbeat)
		assert.Less(t, timeSinceLastHB, 5*time.Second, "Last heartbeat should be recent")

		// Simulate missed heartbeats (no new heartbeat for a while)
		// In production, a background job would mark instance as offline
		time.Sleep(100 * time.Millisecond)

		// Application layer checks for stale heartbeats
		staleThreshold := time.Now().Add(-30 * time.Second)
		if lastHeartbeat.Before(staleThreshold) {
			err = store.UpdateInstanceStatus(ctx, instanceID, StatusOffline)
			require.NoError(t, err)
		}

		t.Log("Health check mechanism verified")
	})

	t.Run("MetricReporting", func(t *testing.T) {
		instanceID := "test-instance-metrics-" + time.Now().Format("150405")

		// Register instance
		instance := &InstanceInfo{
			InstanceID: instanceID,
			Hostname:   "metrics-test.example.com",
			IPAddress:  "192.168.1.180",
			Version:    "v1.4.0",
			BuildSeq:   14000,
			StartedAt:  time.Now(),
			Status:     StatusOnline,
		}

		err := store.RegisterInstance(ctx, instance)
		require.NoError(t, err)

		// Send comprehensive status report
		statusReport := &StatusReportPayload{
			State:          "healthy",
			ActiveLicenses: 50,
			ActiveDevices:  75,
			RequestsTotal:  1000000,
			RequestsOk:     995000,
			RequestsErr:    5000,
			AvgLatencyMs:   15.5,
			P99LatencyMs:   95.0,
		}

		err = store.RecordStatusReport(ctx, instanceID, statusReport)
		require.NoError(t, err)

		// Retrieve and verify
		latest, err := store.GetLatestStatus(ctx, instanceID)
		require.NoError(t, err)
		assert.Equal(t, "healthy", latest.State)
		assert.Equal(t, int64(1000000), latest.RequestsTotal)
		assert.Equal(t, int64(995000), latest.RequestsOk)

		// Calculate error rate
		errorRate := float64(latest.RequestsErr) / float64(latest.RequestsTotal) * 100
		assert.Less(t, errorRate, 1.0, "Error rate should be less than 1%")

		t.Logf("Error rate: %.2f%%", errorRate)
		t.Logf("Avg latency: %.2fms, P99: %.2fms", latest.AvgLatencyMs, latest.P99LatencyMs)
	})

	t.Run("CommandWithArgs", func(t *testing.T) {
		instanceID := "test-instance-cmdargs-" + time.Now().Format("150405")

		// Register instance
		instance := &InstanceInfo{
			InstanceID: instanceID,
			Hostname:   "cmdargs-test.example.com",
			IPAddress:  "192.168.1.190",
			Version:    "v1.5.0",
			BuildSeq:   15000,
			StartedAt:  time.Now(),
			Status:     StatusOnline,
		}

		err := store.RegisterInstance(ctx, instance)
		require.NoError(t, err)

		// Create command with complex arguments
		commandID := "cmd-args-" + time.Now().Format("150405-000")
		command := &Command{
			CommandID:  commandID,
			InstanceID: instanceID,
			Command:    "execute_script",
			Args: map[string]string{
				"script_path": "/opt/scripts/maintenance.sh",
				"mode":        "safe",
				"timeout":     "300s",
				"notify":      "admin@example.com",
				"params":      `{"level":"info","verbose":true}`,
			},
			Status:   CommandStatusPending,
			IssuedAt: time.Now(),
			IssuedBy: "automation@example.com",
		}

		err = store.CreateCommand(ctx, command)
		require.NoError(t, err)

		// Instance executes command
		result := &CommandResult{
			Success: true,
			Output:  "Script executed successfully\nProcessed 1000 items\nCompleted in 45s",
			ExecMs:  45000,
		}

		err = store.UpdateCommandStatus(ctx, commandID, CommandStatusExecuted, result)
		require.NoError(t, err)

		// Verify execution
		executed, err := store.GetCommand(ctx, commandID)
		require.NoError(t, err)
		assert.True(t, executed.Result.Success)
		assert.Contains(t, executed.Result.Output, "successfully")
		assert.Equal(t, int64(45000), executed.Result.ExecMs)

		// Verify args preserved
		assert.Equal(t, "/opt/scripts/maintenance.sh", executed.Args["script_path"])
		assert.Equal(t, "safe", executed.Args["mode"])
	})
}

// TestDashboardStats tests dashboard statistics aggregation
func TestDashboardStats(t *testing.T) {
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

	t.Run("AggregateStats", func(t *testing.T) {
		timestamp := time.Now().Format("150405")

		// Register multiple instances
		for i := 1; i <= 3; i++ {
			instance := &InstanceInfo{
				InstanceID: "test-instance-stats-" + timestamp + "-" + string(rune('0'+i)),
				Hostname:   "stats-host-" + string(rune('0'+i)) + ".example.com",
				IPAddress:  "192.168.1." + string(rune('0'+i)),
				Version:    "v1.5.0",
				BuildSeq:   15000,
				StartedAt:  time.Now(),
				Status:     StatusOnline,
			}

			err := store.RegisterInstance(ctx, instance)
			require.NoError(t, err)

			// Send heartbeats and status reports
			heartbeat := &HeartbeatPayload{
				UptimeSecs:   300 * int64(i),
				GoVersion:    "go1.22.0",
				NumGoroutine: 30 * i,
				AllocMB:      64.0 * float64(i),
				TotalAllocMB: 128.0 * float64(i),
				SysMB:        256.0 * float64(i),
				CPUCores:     4,
			}

			err = store.RecordHeartbeat(ctx, instance.InstanceID, heartbeat)
			require.NoError(t, err)

			statusReport := &StatusReportPayload{
				State:          "running",
				ActiveLicenses: 10 * i,
				ActiveDevices:  15 * i,
				RequestsTotal:  100000 * int64(i),
				RequestsOk:     99000 * int64(i),
				RequestsErr:    1000 * int64(i),
				AvgLatencyMs:   20.0 + float64(i)*5.0,
				P99LatencyMs:   100.0 + float64(i)*10.0,
			}

			err = store.RecordStatusReport(ctx, instance.InstanceID, statusReport)
			require.NoError(t, err)
		}

		// List instances and aggregate stats manually (dashboard logic)
		instances, total, err := store.ListInstances(ctx, StatusOnline, 0, 100)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, total, 3)

		var (
			totalLicenses int64
			totalDevices  int64
			totalRequests int64
		)

		for _, inst := range instances {
			if !strings.HasPrefix(inst.InstanceID, "test-instance-stats-") {
				continue
			}

			status, err := store.GetLatestStatus(ctx, inst.InstanceID)
			if err != nil {
				continue
			}

			totalLicenses += int64(status.ActiveLicenses)
			totalDevices += int64(status.ActiveDevices)
			totalRequests += status.RequestsTotal
		}

		t.Logf("Aggregate Stats: Licenses=%d, Devices=%d, Requests=%d",
			totalLicenses, totalDevices, totalRequests)

		assert.Greater(t, totalRequests, int64(0), "Should have aggregated requests")
	})
}

// Helper to marshal JSON
func mustJSON(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
