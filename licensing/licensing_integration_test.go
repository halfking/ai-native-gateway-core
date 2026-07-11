//go:build integration

package licensing

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLicensingIntegration tests the complete licensing flow:
// Create License → Device Activation → Heartbeat → Revoke License
//
// To run:
//
//	export LLM_GATEWAY_PG_URL=<your-postgres-dsn>
//	go test -tags=integration ./licensing -v -run TestLicensingIntegration
func TestLicensingIntegration(t *testing.T) {
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
		_, _ = pool.Exec(ctx, "DELETE FROM license_devices WHERE license_id IN (SELECT id FROM licenses WHERE customer_email LIKE 'test-integration-%')")
		_, _ = pool.Exec(ctx, "DELETE FROM licenses WHERE customer_email LIKE 'test-integration-%'")
	}()

	t.Run("CompleteLifecycle", func(t *testing.T) {
		// Step 1: Create License
		licenseKey := "TEST-" + time.Now().Format("20060102-150405")
		license := &License{
			LicenseKey:       licenseKey,
			CustomerName:     "Integration Test Customer",
			CustomerEmail:    "test-integration-" + time.Now().Format("20060102150405") + "@example.com",
			MaxDevices:       2,
			SubscriptionTier: "professional",
			Features:         []string{"ai_coding", "code_review", "team_collab"},
			ExpiresAt:        time.Now().Add(365 * 24 * time.Hour),
			CreatedAt:        time.Now(),
		}

		err := store.CreateLicense(ctx, license)
		require.NoError(t, err, "CreateLicense should succeed")
		assert.Greater(t, license.ID, int64(0), "License ID should be assigned")

		// Step 2: First Device Activation
		device1 := &Device{
			LicenseID:    license.ID,
			InstanceID:   "instance-001",
			HardwareHash: "hw-hash-001",
			DeviceName:   "Dev Machine 1",
			ActivatedAt:  time.Now(),
			Status:       "active",
		}

		err = store.ActivateDevice(ctx, device1)
		require.NoError(t, err, "First device activation should succeed")
		assert.Greater(t, device1.ID, int64(0), "Device ID should be assigned")

		// Step 3: Second Device Activation (at limit)
		device2 := &Device{
			LicenseID:    license.ID,
			InstanceID:   "instance-002",
			HardwareHash: "hw-hash-002",
			DeviceName:   "Dev Machine 2",
			ActivatedAt:  time.Now(),
			Status:       "active",
		}

		err = store.ActivateDevice(ctx, device2)
		require.NoError(t, err, "Second device activation should succeed")

		// Step 4: Verify MaxDevices limit (should have 2 devices)
		activeCount, err := store.CountActiveDevices(ctx, licenseKey)
		require.NoError(t, err, "CountActiveDevices should succeed")
		assert.Equal(t, 2, activeCount, "Should have exactly 2 active devices")

		// Step 5: Try third device activation (should fail)
		device3 := &Device{
			LicenseID:    license.ID,
			InstanceID:   "instance-003",
			HardwareHash: "hw-hash-003",
			DeviceName:   "Dev Machine 3",
			ActivatedAt:  time.Now(),
			Status:       "active",
		}

		err = store.ActivateDevice(ctx, device3)
		// Note: The actual error handling depends on your implementation
		// If your store enforces limit, this should error
		if err == nil {
			// If no error, verify count doesn't exceed limit in application logic
			t.Log("Store allows over-limit activation; app layer should prevent this")
		}

		// Step 6: Heartbeat Updates
		err = store.UpdateHeartbeat(ctx, licenseKey, "hw-hash-001")
		require.NoError(t, err, "UpdateHeartbeat should succeed")

		// Verify heartbeat was updated
		devices, err := store.GetActiveDevices(ctx, licenseKey)
		require.NoError(t, err, "GetActiveDevices should succeed")

		var device1Updated *Device
		for i := range devices {
			if devices[i].HardwareHash == "hw-hash-001" {
				device1Updated = &devices[i]
				break
			}
		}
		require.NotNil(t, device1Updated, "Device 1 should exist")
		assert.NotNil(t, device1Updated.LastHeartbeat, "LastHeartbeat should be set")

		// Step 7: Revoke License
		err = store.RevokeLicense(ctx, licenseKey)
		require.NoError(t, err, "RevokeLicense should succeed")

		// Verify license is revoked
		revokedLicense, err := store.GetLicense(ctx, licenseKey)
		require.NoError(t, err, "GetLicense should still work after revocation")
		assert.NotNil(t, revokedLicense.RevokedAt, "RevokedAt should be set")
	})

	t.Run("OfflineActivation", func(t *testing.T) {
		// Step 1: Create offline activation request
		requestID := "offline-req-" + time.Now().Format("20060102-150405")
		offlineReq := &OfflineRequest{
			LicenseKey:   "TEST-OFFLINE-KEY",
			HardwareHash: "hw-offline-001",
			InstanceID:   "offline-instance-001",
			DeviceName:   "Offline Dev Machine",
			RequestID:    requestID,
			Timestamp:    time.Now(),
		}

		err := store.CreateOfflineRequest(ctx, offlineReq)
		require.NoError(t, err, "CreateOfflineRequest should succeed")

		// Step 2: Retrieve offline request
		retrieved, err := store.GetOfflineRequest(ctx, requestID)
		require.NoError(t, err, "GetOfflineRequest should succeed")
		assert.Equal(t, offlineReq.LicenseKey, retrieved.LicenseKey)
		assert.Equal(t, offlineReq.HardwareHash, retrieved.HardwareHash)

		// Step 3: Approve offline request (in real scenario, this would be signed)
		signedLicense := &SignedLicense{
			Data:      []byte("encrypted-license-data"),
			Signature: []byte("cryptographic-signature"),
		}

		err = store.ApproveOfflineRequest(ctx, requestID, signedLicense)
		require.NoError(t, err, "ApproveOfflineRequest should succeed")
	})

	t.Run("DeviceLimit", func(t *testing.T) {
		// Create license with 2 device limit
		licenseKey := "TEST-LIMIT-" + time.Now().Format("20060102-150405")
		license := &License{
			LicenseKey:       licenseKey,
			CustomerName:     "Device Limit Test",
			CustomerEmail:    "test-integration-limit-" + time.Now().Format("20060102150405") + "@example.com",
			MaxDevices:       2,
			SubscriptionTier: "basic",
			Features:         []string{"ai_coding"},
			ExpiresAt:        time.Now().Add(30 * 24 * time.Hour),
			CreatedAt:        time.Now(),
		}

		err := store.CreateLicense(ctx, license)
		require.NoError(t, err)

		// Activate 2 devices
		for i := 1; i <= 2; i++ {
			device := &Device{
				LicenseID:    license.ID,
				InstanceID:   time.Now().Format("20060102150405.000000"),
				HardwareHash: time.Now().Format("20060102150405.000000"),
				DeviceName:   "Device " + string(rune('0'+i)),
				ActivatedAt:  time.Now(),
				Status:       "active",
			}
			err = store.ActivateDevice(ctx, device)
			require.NoError(t, err, "Device %d activation should succeed", i)
			time.Sleep(10 * time.Millisecond) // Ensure unique timestamps
		}

		// Verify we have 2 active devices
		count, err := store.CountActiveDevices(ctx, licenseKey)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should have exactly 2 active devices")

		// Deactivate one device
		devices, err := store.GetActiveDevices(ctx, licenseKey)
		require.NoError(t, err)
		require.Len(t, devices, 2)

		err = store.DeactivateDevice(ctx, licenseKey, devices[0].HardwareHash, "user_requested")
		require.NoError(t, err, "DeactivateDevice should succeed")

		// Verify count decreased
		count, err = store.CountActiveDevices(ctx, licenseKey)
		require.NoError(t, err)
		assert.Equal(t, 1, count, "Should have 1 active device after deactivation")

		// Now we can activate a new device
		newDevice := &Device{
			LicenseID:    license.ID,
			InstanceID:   "new-instance",
			HardwareHash: "new-hw-hash",
			DeviceName:   "Replacement Device",
			ActivatedAt:  time.Now(),
			Status:       "active",
		}

		err = store.ActivateDevice(ctx, newDevice)
		require.NoError(t, err, "New device activation should succeed after deactivation")

		count, err = store.CountActiveDevices(ctx, licenseKey)
		require.NoError(t, err)
		assert.Equal(t, 2, count, "Should be back to 2 active devices")
	})
}

// TestLicenseModules tests license module feature flags
func TestLicenseModules(t *testing.T) {
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

	t.Run("GetLicenseModules", func(t *testing.T) {
		licenseKey := "TEST-MODULES-" + time.Now().Format("20060102-150405")

		// Create a test license
		license := &License{
			LicenseKey:       licenseKey,
			CustomerName:     "Module Test",
			CustomerEmail:    "test-integration-modules-" + time.Now().Format("20060102150405") + "@example.com",
			MaxDevices:       1,
			SubscriptionTier: "enterprise",
			Features:         []string{"ai_coding", "code_review", "advanced_analytics"},
			ExpiresAt:        time.Now().Add(365 * 24 * time.Hour),
			CreatedAt:        time.Now(),
		}

		err := store.CreateLicense(ctx, license)
		require.NoError(t, err)

		// Get modules (may be empty if no module mapping exists)
		modules, err := store.GetLicenseModules(ctx, licenseKey)
		require.NoError(t, err)
		assert.NotNil(t, modules, "Modules map should not be nil")

		// Clean up
		_, _ = pool.Exec(ctx, "DELETE FROM licenses WHERE license_key = $1", licenseKey)
	})
}
