package licensing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateFreeLicense(t *testing.T) {
	h := &BootstrapHandler{}

	t.Run("normal instance ID", func(t *testing.T) {
		instanceID := "test-instance-1234567890"
		hardwareHash := "hw-hash-abc"

		lic := h.generateFreeLicense(instanceID, hardwareHash)

		require.NotNil(t, lic)
		assert.Equal(t, "FREE-test-instanc", lic.LicenseKey)
		assert.Equal(t, "Free User", lic.CustomerName)
		assert.Equal(t, 1, lic.MaxDevices)
		assert.Equal(t, "free", lic.SubscriptionTier)
		assert.Contains(t, lic.Features, "basic_ai_coding")
		assert.Equal(t, hardwareHash, lic.HardwareHash)

		// 验证有效期为 10 年
		expectedExpiry := time.Now().AddDate(10, 0, 0)
		assert.WithinDuration(t, expectedExpiry, lic.ExpiresAt, 5*time.Second)
	})

	t.Run("short instance ID", func(t *testing.T) {
		instanceID := "short-id"
		hardwareHash := "hw-hash-xyz"

		lic := h.generateFreeLicense(instanceID, hardwareHash)

		require.NotNil(t, lic)
		assert.Equal(t, "FREE-short-id", lic.LicenseKey)
		assert.Equal(t, "free-short-id@local", lic.CustomerEmail)
	})

	t.Run("very short instance ID", func(t *testing.T) {
		instanceID := "abc"
		hardwareHash := "hw-hash-123"

		lic := h.generateFreeLicense(instanceID, hardwareHash)

		require.NotNil(t, lic)
		assert.Equal(t, "FREE-abc", lic.LicenseKey)
		assert.Equal(t, "free-abc@local", lic.CustomerEmail)
		assert.Equal(t, hardwareHash, lic.HardwareHash)
	})

	t.Run("license key uniqueness", func(t *testing.T) {
		id1 := "instance-1-with-long-id"
		id2 := "instance-2-with-long-id"

		lic1 := h.generateFreeLicense(id1, "hw1")
		lic2 := h.generateFreeLicense(id2, "hw2")

		assert.NotEqual(t, lic1.LicenseKey, lic2.LicenseKey, "different instances should have different license keys")
	})

	t.Run("device limit is enforced", func(t *testing.T) {
		instanceID := "test-device-limit"
		hardwareHash := "hw-hash-limit"

		lic := h.generateFreeLicense(instanceID, hardwareHash)

		assert.Equal(t, 1, lic.MaxDevices, "free license should limit to 1 device")
	})
}
