package attachments

import (
	"context"
	"net/http/httptest"
	"testing"
)

// mockKeyVerifier 模拟 KeyVerifier 用于测试。
type mockKeyVerifier struct {
	verifyFunc func(ctx context.Context, rawKey string) (*KeyInfo, error)
}

func (m *mockKeyVerifier) Verify(ctx context.Context, rawKey string) (*KeyInfo, error) {
	if m.verifyFunc != nil {
		return m.verifyFunc(ctx, rawKey)
	}
	return nil, nil
}

func (m *mockKeyVerifier) Enabled() bool {
	return true
}

func TestAuthenticator_APIKey_Success(t *testing.T) {
	mockVerifier := &mockKeyVerifier{
		verifyFunc: func(ctx context.Context, rawKey string) (*KeyInfo, error) {
			if rawKey == "sk-valid-key-12345" {
				return &KeyInfo{
					ID:        123,
					KeyPrefix: "sk-valid",
					TenantID:  "tenant-001",
					Status:    "active",
					Enabled:   true,
				}, nil
			}
			return nil, ErrInvalidKey
		},
	}

	auth := NewAuthenticator(AuthModeAPIKey, mockVerifier)

	req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
	req.Header.Set("Authorization", "Bearer sk-valid-key-12345")

	authCtx, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if !authCtx.Authenticated {
		t.Error("expected authenticated=true")
	}
	if authCtx.TenantID != "tenant-001" {
		t.Errorf("expected tenant_id=tenant-001, got %s", authCtx.TenantID)
	}
	if authCtx.KeyID != 123 {
		t.Errorf("expected key_id=123, got %d", authCtx.KeyID)
	}
}

func TestAuthenticator_APIKey_MissingHeader(t *testing.T) {
	auth := NewAuthenticator(AuthModeAPIKey, &mockKeyVerifier{})

	req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
	// 没有 Authorization header

	_, err := auth.Authenticate(req)
	if err == nil {
		t.Error("expected error for missing Authorization header")
	}
	if err.Error() != "missing Authorization header" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAuthenticator_APIKey_InvalidFormat(t *testing.T) {
	auth := NewAuthenticator(AuthModeAPIKey, &mockKeyVerifier{})

	req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
	req.Header.Set("Authorization", "InvalidFormat")

	_, err := auth.Authenticate(req)
	if err == nil {
		t.Error("expected error for invalid Authorization format")
	}
}

func TestAuthenticator_APIKey_InvalidKey(t *testing.T) {
	mockVerifier := &mockKeyVerifier{
		verifyFunc: func(ctx context.Context, rawKey string) (*KeyInfo, error) {
			return nil, ErrInvalidKey
		},
	}

	auth := NewAuthenticator(AuthModeAPIKey, mockVerifier)

	req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
	req.Header.Set("Authorization", "Bearer sk-invalid-key")

	_, err := auth.Authenticate(req)
	if err == nil {
		t.Error("expected error for invalid key")
	}
}

func TestAuthenticator_None(t *testing.T) {
	auth := NewAuthenticator(AuthModeNone, nil)

	req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
	// 无需 Authorization header

	authCtx, err := auth.Authenticate(req)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}

	if !authCtx.Authenticated {
		t.Error("expected authenticated=true for AuthModeNone")
	}
}

// ErrInvalidKey 模拟无效 key 错误。
var ErrInvalidKey = context.DeadlineExceeded // 用已有 error 模拟
