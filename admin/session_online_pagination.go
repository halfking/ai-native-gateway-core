// Package admin — session_online_pagination.go
//
// Cursor-based 分页工具（V3.2 LP6）。
//
// Cursor 格式：base64(timestamp|hmac_signature)，用于时间倒序分页。
// HMAC 签名防止 cursor 伪造（P1 安全增强，2026-08-14）。
//
// 契约（13号文档§6）：
//   - cursor 可选，首次请求不传
//   - limit ∈ [1, 100]，默认 20
//   - 响应含 next_cursor、has_more
//   - cursor 解密失败返 400 session.pagination_invalid_cursor
package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"
)

// PaginationParams 是分页请求参数。
type PaginationParams struct {
	Cursor string // base64(timestamp|hmac)，空表示首次请求
	Limit  int    // ∈ [1, 100]，默认 20
}

// PaginationResponse 是分页响应元数据。
type PaginationResponse struct {
	NextCursor string `json:"next_cursor,omitempty"` // 下一页游标，空表示无更多
	HasMore    bool   `json:"has_more"`              // 是否有下一页
}

type onlineSessionCursor struct {
	UpdatedAt time.Time
	SessionID string
}

// ParseCursor 解析 base64 游标为时间戳并验证 HMAC 签名。
func ParseCursor(cursor string) (time.Time, error) {
	parsed, err := parseOnlineSessionCursor(cursor)
	return parsed.UpdatedAt, err
}

func parseOnlineSessionCursor(cursor string) (onlineSessionCursor, error) {
	if cursor == "" {
		return onlineSessionCursor{}, nil
	}
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return onlineSessionCursor{}, err
	}

	// Unsigned cursors are rejected so callers cannot bypass tamper protection.
	parts := strings.Split(string(decoded), "|")
	if len(parts) != 2 && len(parts) != 3 {
		return onlineSessionCursor{}, fmt.Errorf("invalid cursor format")
	}

	timestampStr := parts[0]
	sessionID := ""
	providedMAC := parts[1]
	if len(parts) == 3 {
		sessionID = parts[1]
		providedMAC = parts[2]
		if sessionID == "" {
			return onlineSessionCursor{}, fmt.Errorf("invalid cursor format")
		}
	}

	// 验证 HMAC
	signedPayload := timestampStr
	if sessionID != "" {
		signedPayload += "|" + sessionID
	}
	expectedMAC, err := computeCursorHMAC(signedPayload)
	if err != nil {
		return onlineSessionCursor{}, err
	}
	if !hmac.Equal([]byte(providedMAC), []byte(expectedMAC)) {
		return onlineSessionCursor{}, fmt.Errorf("cursor signature verification failed")
	}

	// 解析时间戳
	ts, err := time.Parse(time.RFC3339Nano, timestampStr)
	if err != nil {
		return onlineSessionCursor{}, fmt.Errorf("invalid timestamp in cursor: %w", err)
	}
	return onlineSessionCursor{UpdatedAt: ts, SessionID: sessionID}, nil
}

// EncodeCursor 将时间戳编码为 base64 游标（带 HMAC 签名）。
// 格式：base64(timestamp|hmac)。
func EncodeCursor(ts time.Time) (string, error) {
	return encodeOnlineSessionCursor(ts, "")
}

func encodeOnlineSessionCursor(ts time.Time, sessionID string) (string, error) {
	if ts.IsZero() {
		return "", nil
	}
	timestampStr := ts.Format(time.RFC3339Nano)
	payload := timestampStr
	if sessionID != "" {
		payload += "|" + sessionID
	}
	mac, err := computeCursorHMAC(payload)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString([]byte(fmt.Sprintf("%s|%s", payload, mac))), nil
}

// computeCursorHMAC 计算 cursor 的 HMAC-SHA256 签名。
// The secret is mandatory so deployments cannot silently share a public key.
func computeCursorHMAC(data string) (string, error) {
	secret := strings.TrimSpace(os.Getenv("CURSOR_HMAC_SECRET"))
	if len(secret) < 32 {
		return "", fmt.Errorf("cursor HMAC secret must be at least 32 bytes")
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NormalizePaginationParams 规范化分页参数（限制范围，设默认值）。
func NormalizePaginationParams(params PaginationParams) PaginationParams {
	if params.Limit <= 0 {
		params.Limit = 20
	}
	if params.Limit > 100 {
		params.Limit = 100
	}
	return params
}

// BuildPaginationResponse 构建分页响应元数据。
// items: 本次返回的条目数
// lastTS: 本次最后一条的时间戳（用于生成 next_cursor）
// limit: 请求的 limit
func BuildPaginationResponse(items int, lastTS time.Time, limit int) (PaginationResponse, error) {
	if items < limit {
		// 返回条目少于 limit，说明无更多数据
		return PaginationResponse{HasMore: false}, nil
	}
	if lastTS.IsZero() {
		return PaginationResponse{}, fmt.Errorf("cannot build cursor without a last timestamp")
	}
	nextCursor, err := EncodeCursor(lastTS)
	if err != nil {
		return PaginationResponse{}, err
	}
	// 有更多数据，生成 next_cursor
	return PaginationResponse{
		NextCursor: nextCursor,
		HasMore:    true,
	}, nil
}

func buildOnlineSessionPaginationResponse(hasMore bool, lastTS time.Time, lastSessionID string) (PaginationResponse, error) {
	if !hasMore {
		return PaginationResponse{HasMore: false}, nil
	}
	if lastTS.IsZero() || lastSessionID == "" {
		return PaginationResponse{}, fmt.Errorf("cannot build cursor without a session sort key")
	}
	nextCursor, err := encodeOnlineSessionCursor(lastTS, lastSessionID)
	if err != nil {
		return PaginationResponse{}, err
	}
	return PaginationResponse{NextCursor: nextCursor, HasMore: true}, nil
}
