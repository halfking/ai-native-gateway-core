package admin

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestCursorHMACSignature(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")

	now := time.Now()

	// 测试编码 + 解码（正常流程）
	cursor, err := encodeOnlineSessionCursor(now, "")
	if cursor == "" {
		t.Fatal("encodeOnlineSessionCursor returned empty string")
	}

	decoded, err := parseOnlineSessionCursor(cursor)
	if err != nil {
		t.Fatalf("parseOnlineSessionCursor failed: %v", err)
	}

	// 时间戳应匹配（允许纳秒级精度误差）
	if decoded.UpdatedAt.Sub(now).Abs() > time.Microsecond {
		t.Errorf("timestamp mismatch: got %v, want %v", decoded.UpdatedAt, now)
	}

	// 测试篡改 cursor（伪造签名）
	// 构造一个有效时间戳但错误签名的 cursor
	fakePayload := now.Format(time.RFC3339Nano) + "|fakesignature"
	tampered := base64.URLEncoding.EncodeToString([]byte(fakePayload))
	_, err = parseOnlineSessionCursor(tampered)
	if err == nil {
		t.Error("parseOnlineSessionCursor should reject tampered cursor")
	}
	if err != nil && err.Error() != "cursor signature verification failed" {
		t.Logf("Got expected error: %v", err)
	}

	// Unsigned cursors must be rejected.
	oldFormatCursor := "MjAyNi0wOC0xNFQxMjozNDo1Ni4xMjM0NTZa" // base64("2026-08-14T12:34:56.123456Z")
	_, err = parseOnlineSessionCursor(oldFormatCursor)
	if err == nil {
		t.Error("parseOnlineSessionCursor should reject unsigned cursor")
	}
}

func TestCursorRequiresSecret(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "")
	now := time.Now()
	if _, err := encodeOnlineSessionCursor(now, ""); err == nil {
		t.Fatal("encodeOnlineSessionCursor should fail without a configured secret")
	}
}

func TestCursorRejectsShortSecret(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "too-short")
	if _, err := encodeOnlineSessionCursor(time.Now(), ""); err == nil {
		t.Fatal("encodeOnlineSessionCursor should reject a short secret")
	}
}

func TestOnlineSessionCursorRoundTrip(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	want := onlineSessionCursor{
		UpdatedAt: time.Date(2026, 8, 14, 12, 34, 56, 123456789, time.UTC),
		SessionID: "session-123",
	}

	cursor, err := encodeOnlineSessionCursor(want.UpdatedAt, want.SessionID)
	if err != nil {
		t.Fatalf("encodeOnlineSessionCursor() error = %v", err)
	}
	got, err := parseOnlineSessionCursor(cursor)
	if err != nil {
		t.Fatalf("parseOnlineSessionCursor() error = %v", err)
	}
	if got != want {
		t.Errorf("cursor round-trip = %+v, want %+v", got, want)
	}

	tamperedPayload := want.UpdatedAt.Format(time.RFC3339Nano) + "|session-456|invalid-signature"
	tamperedCursor := base64.URLEncoding.EncodeToString([]byte(tamperedPayload))
	if _, err := parseOnlineSessionCursor(tamperedCursor); err == nil {
		t.Fatal("parseOnlineSessionCursor should reject a tampered session ID")
	}
}

func TestEmptyCursor(t *testing.T) {
	decoded, err := parseOnlineSessionCursor("")
	if err != nil {
		t.Errorf("parseOnlineSessionCursor('') should not error: %v", err)
	}
	if !decoded.UpdatedAt.IsZero() {
		t.Errorf("parseOnlineSessionCursor('') should return zero time, got %v", decoded.UpdatedAt)
	}

	cursor, err := encodeOnlineSessionCursor(time.Time{}, "")
	if err != nil {
		t.Fatalf("encodeOnlineSessionCursor(zero) failed: %v", err)
	}
	if cursor != "" {
		t.Errorf("encodeOnlineSessionCursor(zero) should return empty string, got %q", cursor)
	}
}
