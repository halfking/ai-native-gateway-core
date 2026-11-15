package casdoor

import (
	"context"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMFAManager(t *testing.T) {
	t.Run("WithIssuer", func(t *testing.T) {
		manager := NewMFAManager("Test Issuer")
		assert.NotNil(t, manager)
		assert.Equal(t, "Test Issuer", manager.issuer)
	})

	t.Run("WithoutIssuer", func(t *testing.T) {
		manager := NewMFAManager("")
		assert.NotNil(t, manager)
		assert.Equal(t, "LLM Gateway", manager.issuer)
	})
}

func TestGenerateMFASecret(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("GenerateSuccess", func(t *testing.T) {
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)
		assert.NotNil(t, secret)
		assert.NotEmpty(t, secret.Secret)
		assert.NotEmpty(t, secret.QRCodeURL)
		assert.Equal(t, "Test App", secret.Issuer)
		assert.Equal(t, "testuser", secret.Account)
		assert.False(t, secret.CreatedAt.IsZero())
	})

	t.Run("GenerateMultiple", func(t *testing.T) {
		secret1, err := manager.GenerateMFASecret(ctx, "user1")
		require.NoError(t, err)

		secret2, err := manager.GenerateMFASecret(ctx, "user2")
		require.NoError(t, err)

		// Secrets should be different
		assert.NotEqual(t, secret1.Secret, secret2.Secret)
		assert.NotEqual(t, secret1.QRCodeURL, secret2.QRCodeURL)
	})
}

func TestVerifyMFACode(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("ValidCode", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Generate current code
		code, err := totp.GenerateCode(secret.Secret, time.Now())
		require.NoError(t, err)

		// Verify code
		valid, err := manager.VerifyMFACode(ctx, secret.Secret, code)
		require.NoError(t, err)
		assert.True(t, valid)
	})

	t.Run("InvalidCode", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Try invalid code
		valid, err := manager.VerifyMFACode(ctx, secret.Secret, "000000")
		require.NoError(t, err)
		assert.False(t, valid)
	})

	t.Run("ExpiredCode", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Generate code from 2 minutes ago
		pastTime := time.Now().Add(-2 * time.Minute)
		code, err := totp.GenerateCode(secret.Secret, pastTime)
		require.NoError(t, err)

		// Should be invalid (outside window)
		valid, err := manager.VerifyMFACode(ctx, secret.Secret, code)
		require.NoError(t, err)
		assert.False(t, valid)
	})
}

func TestVerifyMFACodeWithWindow(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("WithinWindow", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Generate code from 30 seconds ago
		pastTime := time.Now().Add(-30 * time.Second)
		code, err := totp.GenerateCode(secret.Secret, pastTime)
		require.NoError(t, err)

		// Should be valid with window=1 (allows +/- 30s)
		valid, err := manager.VerifyMFACodeWithWindow(ctx, secret.Secret, code, 1)
		require.NoError(t, err)
		assert.True(t, valid)
	})

	t.Run("OutsideWindow", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Generate code from 2 minutes ago
		pastTime := time.Now().Add(-2 * time.Minute)
		code, err := totp.GenerateCode(secret.Secret, pastTime)
		require.NoError(t, err)

		// Should be invalid even with window=1
		valid, err := manager.VerifyMFACodeWithWindow(ctx, secret.Secret, code, 1)
		require.NoError(t, err)
		assert.False(t, valid)
	})

	t.Run("LargerWindow", func(t *testing.T) {
		// Generate secret
		secret, err := manager.GenerateMFASecret(ctx, "testuser")
		require.NoError(t, err)

		// Generate code from 90 seconds ago
		pastTime := time.Now().Add(-90 * time.Second)
		code, err := totp.GenerateCode(secret.Secret, pastTime)
		require.NoError(t, err)

		// Should be valid with window=3 (allows +/- 90s)
		valid, err := manager.VerifyMFACodeWithWindow(ctx, secret.Secret, code, 3)
		require.NoError(t, err)
		assert.True(t, valid)
	})
}

func TestGenerateBackupCodes(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("DefaultCount", func(t *testing.T) {
		codes, err := manager.GenerateBackupCodes(ctx, 10)
		require.NoError(t, err)
		assert.Len(t, codes, 10)

		// All codes should be unique
		seen := make(map[string]bool)
		for _, code := range codes {
			assert.NotEmpty(t, code)
			assert.False(t, seen[code], "duplicate code found")
			seen[code] = true
		}
	})

	t.Run("CustomCount", func(t *testing.T) {
		codes, err := manager.GenerateBackupCodes(ctx, 5)
		require.NoError(t, err)
		assert.Len(t, codes, 5)
	})

	t.Run("ZeroCount", func(t *testing.T) {
		codes, err := manager.GenerateBackupCodes(ctx, 0)
		require.NoError(t, err)
		assert.Len(t, codes, 10) // Should default to 10
	})

	t.Run("UniqueCodes", func(t *testing.T) {
		codes1, err := manager.GenerateBackupCodes(ctx, 10)
		require.NoError(t, err)

		codes2, err := manager.GenerateBackupCodes(ctx, 10)
		require.NoError(t, err)

		// Two sets of codes should be different
		assert.NotEqual(t, codes1, codes2)
	})
}

func TestEnableMFA(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("EnableSuccess", func(t *testing.T) {
		secret, backupCodes, err := manager.EnableMFA(ctx, "testuser")
		require.NoError(t, err)
		assert.NotNil(t, secret)
		assert.NotEmpty(t, secret.Secret)
		assert.Len(t, backupCodes, 10)
	})

	t.Run("VerifyGeneratedSecret", func(t *testing.T) {
		secret, _, err := manager.EnableMFA(ctx, "testuser")
		require.NoError(t, err)

		// Generate code with the secret
		code, err := totp.GenerateCode(secret.Secret, time.Now())
		require.NoError(t, err)

		// Verify the code works
		valid, err := manager.VerifyMFACode(ctx, secret.Secret, code)
		require.NoError(t, err)
		assert.True(t, valid)
	})
}

func TestDisableMFA(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("DisableSuccess", func(t *testing.T) {
		err := manager.DisableMFA(ctx, "testuser")
		assert.NoError(t, err)
	})
}

func TestGetMFAStatus(t *testing.T) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	t.Run("GetStatus", func(t *testing.T) {
		status, err := manager.GetMFAStatus(ctx, "testuser")
		require.NoError(t, err)
		assert.NotNil(t, status)
		// Default status is disabled
		assert.False(t, status.Enabled)
	})
}

func TestMFAConfig(t *testing.T) {
	t.Run("DisabledConfig", func(t *testing.T) {
		config := &MFAConfig{
			Enabled: false,
		}
		assert.False(t, config.Enabled)
		assert.Empty(t, config.Secret)
		assert.Empty(t, config.BackupCodes)
	})

	t.Run("EnabledConfig", func(t *testing.T) {
		config := &MFAConfig{
			Enabled:      true,
			Secret:       "test-secret",
			BackupCodes:  []string{"code1", "code2"},
			LastVerified: time.Now(),
		}
		assert.True(t, config.Enabled)
		assert.NotEmpty(t, config.Secret)
		assert.Len(t, config.BackupCodes, 2)
		assert.False(t, config.LastVerified.IsZero())
	})
}

// BenchmarkGenerateMFASecret benchmarks MFA secret generation.
func BenchmarkGenerateMFASecret(b *testing.B) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := manager.GenerateMFASecret(ctx, "testuser")
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVerifyMFACode benchmarks MFA code verification.
func BenchmarkVerifyMFACode(b *testing.B) {
	manager := NewMFAManager("Test App")
	ctx := context.Background()

	// Setup: generate secret and code
	secret, err := manager.GenerateMFASecret(ctx, "testuser")
	if err != nil {
		b.Fatal(err)
	}
	code, err := totp.GenerateCode(secret.Secret, time.Now())
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := manager.VerifyMFACode(ctx, secret.Secret, code)
		if err != nil {
			b.Fatal(err)
		}
	}
}
