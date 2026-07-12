package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateRefreshToken generates a cryptographically secure 32-byte hex refresh token.
func GenerateRefreshToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate refresh token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
