package main

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// InstanceClaims extends jwt.RegisteredClaims with custom fields
type InstanceClaims struct {
	jwt.RegisteredClaims
	LicenseKeyHash string `json:"license_key_hash"`
}

// SignInstanceToken signs an instance JWT using EdDSA (Ed25519).
// Token expires in 7 days (604800 seconds).
func SignInstanceToken(instanceID, licenseKeyHash string, serverPrivKey ed25519.PrivateKey) (string, error) {
	now := time.Now()
	claims := InstanceClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   fmt.Sprintf("instance:%s", instanceID),
			Issuer:    "llm.kxpms.cn",
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(7 * 24 * time.Hour)),
		},
		LicenseKeyHash: licenseKeyHash,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	signedToken, err := token.SignedString(serverPrivKey)
	if err != nil {
		return "", fmt.Errorf("failed to sign JWT: %w", err)
	}

	return signedToken, nil
}

// VerifyInstanceToken verifies an instance JWT using EdDSA (Ed25519) and returns claims.
func VerifyInstanceToken(tokenString string, serverPubKey ed25519.PublicKey) (*InstanceClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &InstanceClaims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return serverPubKey, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to parse JWT: %w", err)
	}

	claims, ok := token.Claims.(*InstanceClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid JWT claims")
	}

	return claims, nil
}
