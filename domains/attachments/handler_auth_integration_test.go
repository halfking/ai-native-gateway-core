package attachments

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// TestHandler_WithAPIKeyAuth 测试 Handler 的 API Key 认证集成。
func TestHandler_WithAPIKeyAuth(t *testing.T) {
	// 1. 创建临时存储
	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	if err != nil {
		t.Fatalf("NewStorage failed: %v", err)
	}

	// 2. 写入测试附件
	testFile := filepath.Join(tmpDir, "test.png")
	if err := os.WriteFile(testFile, []byte("fake-image-data"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// 3. 创建 mock KeyVerifier
	mockVerifier := &mockKeyVerifier{
		verifyFunc: func(ctx context.Context, rawKey string) (*KeyInfo, error) {
			if rawKey == "sk-valid-12345" {
				return &KeyInfo{
					ID:        100,
					KeyPrefix: "sk-valid",
					TenantID:  "tenant-test",
					Status:    "active",
					Enabled:   true,
				}, nil
			}
			return nil, context.DeadlineExceeded // 模拟 invalid key
		},
	}

	// 4. 创建 Handler 并配置 API Key 认证
	handler := NewHandler(storage, nil)
	authenticator := NewAuthenticator(AuthModeAPIKey, mockVerifier)
	handler.SetAuthenticator(authenticator)

	// 5. 测试：有效的 API Key
	t.Run("valid_api_key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
		req.Header.Set("Authorization", "Bearer sk-valid-12345")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d, body: %s", rec.Code, rec.Body.String())
		}
		if rec.Body.String() != "fake-image-data" {
			t.Errorf("unexpected body: %s", rec.Body.String())
		}
	})

	// 6. 测试：无效的 API Key
	t.Run("invalid_api_key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
		req.Header.Set("Authorization", "Bearer sk-invalid-99999")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	// 7. 测试：缺少 Authorization header
	t.Run("missing_auth_header", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/attachments/test.png", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})
}

// TestHandler_NoAuth 测试无认证模式（默认行为）。
func TestHandler_NoAuth(t *testing.T) {
	tmpDir := t.TempDir()
	storage, err := NewStorage(tmpDir)
	if err != nil {
		t.Fatalf("NewStorage failed: %v", err)
	}

	testFile := filepath.Join(tmpDir, "public.txt")
	if err := os.WriteFile(testFile, []byte("public-content"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Handler 不设置 authenticator，默认无认证
	handler := NewHandler(storage, nil)

	req := httptest.NewRequest("GET", "/api/attachments/public.txt", nil)
	// 无需 Authorization header
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != "public-content" {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}
