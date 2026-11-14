package native

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/pkg/authplugin"
)

func TestNativeAuthProvider_Name(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	p, _ := NewNativeAuthProvider(&pgxpool.Pool{}, "test-secret")
	if got := p.Name(); got != "native" {
		t.Errorf("Name() = %q, want %q", got, "native")
	}
}

func TestNativeAuthProvider_SupportsFeature(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	p, _ := NewNativeAuthProvider(&pgxpool.Pool{}, "test-secret")

	tests := []struct {
		feature authplugin.AuthFeature
		want    bool
	}{
		{authplugin.FeaturePasswordAuth, true},
		{authplugin.FeatureTokenValidation, true},
		{authplugin.FeatureSessionRevocation, true},
		{authplugin.FeatureRefreshToken, false},
		{authplugin.FeatureMFA, false},
	}

	for _, tt := range tests {
		if got := p.SupportsFeature(tt.feature); got != tt.want {
			t.Errorf("SupportsFeature(%q) = %v, want %v", tt.feature, got, tt.want)
		}
	}
}

func TestSignAndVerifyToken(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	secret := "test-jwt-secret"

	token, expiresAt, err := signToken(1, "default", "testuser", "admin", secret, false)
	if err != nil {
		t.Fatalf("signToken() error = %v", err)
	}
	if token == "" {
		t.Fatal("signToken() returned empty token")
	}
	if expiresAt.Before(time.Now()) {
		t.Fatal("signToken() returned past expiration time")
	}

	claims, err := verifyToken(token, secret)
	if err != nil {
		t.Fatalf("verifyToken() error = %v", err)
	}
	if claims.UserID != 1 {
		t.Errorf("claims.UserID = %d, want 1", claims.UserID)
	}
	if claims.Username != "testuser" {
		t.Errorf("claims.Username = %q, want %q", claims.Username, "testuser")
	}
	if claims.TenantID != "default" {
		t.Errorf("claims.TenantID = %q, want %q", claims.TenantID, "default")
	}
	if claims.Role != "admin" {
		t.Errorf("claims.Role = %q, want %q", claims.Role, "admin")
	}
}

func TestVerifyToken_InvalidToken(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	_, err := verifyToken("invalid.jwt.token", "test-secret")
	if err == nil {
		t.Error("verifyToken() with invalid token should fail")
	}
}

func TestVerifyToken_WrongSecret(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	token, _, _ := signToken(1, "default", "testuser", "admin", "correct-secret", false)
	_, err := verifyToken(token, "wrong-secret")
	if err == nil {
		t.Error("verifyToken() with wrong secret should fail")
	}
}

func TestNativeAuthProvider_RefreshTokenNotSupported(t *testing.T) {
	t.Setenv("LLM_GATEWAY_JWT_SECRET", "")
	p, _ := NewNativeAuthProvider(&pgxpool.Pool{}, "test-secret")
	_, err := p.RefreshToken(context.Background(), "any-token")
	if err == nil {
		t.Error("RefreshToken() should return error (not supported)")
	}
}
