package main

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSignAndVerifyInstanceToken(t *testing.T) {
	// Generate test key pair
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	instanceID := "test-instance-123"
	licenseKeyHash := "abc123def456"

	// Sign token
	token, err := SignInstanceToken(instanceID, licenseKeyHash, privKey)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// Verify token
	claims, err := VerifyInstanceToken(token, pubKey)
	require.NoError(t, err)
	assert.Equal(t, "instance:"+instanceID, claims.Subject)
	assert.Equal(t, "llm.kxpms.cn", claims.Issuer)
	assert.Equal(t, licenseKeyHash, claims.LicenseKeyHash)

	// Check expiration (7 days)
	expiresIn := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	assert.Equal(t, 7*24*time.Hour, expiresIn)
}

func TestVerifyInstanceToken_InvalidSignature(t *testing.T) {
	// Generate two different key pairs
	_, privKey1, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	pubKey2, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// Sign with privKey1
	token, err := SignInstanceToken("test-instance", "hash", privKey1)
	require.NoError(t, err)

	// Try to verify with pubKey2 (should fail)
	_, err = VerifyInstanceToken(token, pubKey2)
	assert.Error(t, err)
}

func TestVerifyInstanceToken_ExpiredToken(t *testing.T) {
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	// Create expired token manually
	now := time.Now().Add(-8 * 24 * time.Hour) // 8 days ago
	claims := InstanceClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "instance:expired",
			Issuer:    "llm.kxpms.cn",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
		},
		LicenseKeyHash: "hash",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signedToken, err := token.SignedString(privKey)
	require.NoError(t, err)

	// Verify should fail due to expiration
	_, err = VerifyInstanceToken(signedToken, pubKey)
	assert.Error(t, err)
}

func TestGenerateRefreshToken(t *testing.T) {
	token1, err := GenerateRefreshToken()
	require.NoError(t, err)
	assert.Len(t, token1, 64) // 32 bytes = 64 hex chars

	token2, err := GenerateRefreshToken()
	require.NoError(t, err)
	assert.Len(t, token2, 64)

	// Tokens should be different
	assert.NotEqual(t, token1, token2)
}
