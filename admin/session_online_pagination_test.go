package admin

import (
	"encoding/base64"
	"os"
	"testing"
	"time"
)

func TestCursorHMACSignature(t *testing.T) {
	// 设置测试密钥
	os.Setenv("CURSOR_HMAC_SECRET", "test-secret-key")
	defer os.Unsetenv("CURSOR_HMAC_SECRET")

	now := time.Now()

	// 测试编码 + 解码（正常流程）
	cursor := EncodeCursor(now)
	if cursor == "" {
		t.Fatal("EncodeCursor returned empty string")
	}

	decoded, err := ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor failed: %v", err)
	}

	// 时间戳应匹配（允许纳秒级精度误差）
	if decoded.Sub(now).Abs() > time.Microsecond {
		t.Errorf("timestamp mismatch: got %v, want %v", decoded, now)
	}

	// 测试篡改 cursor（伪造签名）
	// 构造一个有效时间戳但错误签名的 cursor
	fakePayload := now.Format(time.RFC3339Nano) + "|fakesignature"
	tampered := base64.URLEncoding.EncodeToString([]byte(fakePayload))
	_, err = ParseCursor(tampered)
	if err == nil {
		t.Error("ParseCursor should reject tampered cursor")
	}
	if err != nil && err.Error() != "cursor signature verification failed" {
		t.Logf("Got expected error: %v", err)
	}

	// 测试兼容旧格式（无签名）
	oldFormatCursor := "MjAyNi0wOC0xNFQxMjozNDo1Ni4xMjM0NTZa" // base64("2026-08-14T12:34:56.123456Z")
	_, err = ParseCursor(oldFormatCursor)
	if err != nil {
		t.Errorf("ParseCursor should accept old format: %v", err)
	}
}

func TestCursorDefaultSecret(t *testing.T) {
	// 清除环境变量，测试默认密钥
	os.Unsetenv("CURSOR_HMAC_SECRET")

	now := time.Now()
	cursor := EncodeCursor(now)
	decoded, err := ParseCursor(cursor)
	if err != nil {
		t.Fatalf("ParseCursor with default secret failed: %v", err)
	}

	if decoded.Sub(now).Abs() > time.Microsecond {
		t.Errorf("timestamp mismatch: got %v, want %v", decoded, now)
	}
}

func TestEmptyCursor(t *testing.T) {
	decoded, err := ParseCursor("")
	if err != nil {
		t.Errorf("ParseCursor('') should not error: %v", err)
	}
	if !decoded.IsZero() {
		t.Errorf("ParseCursor('') should return zero time, got %v", decoded)
	}

	cursor := EncodeCursor(time.Time{})
	if cursor != "" {
		t.Errorf("EncodeCursor(zero) should return empty string, got %q", cursor)
	}
}
