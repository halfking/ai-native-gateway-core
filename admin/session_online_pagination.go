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

// encodeOnlineSessionCursor 将时间戳（可选会话 ID）编码为带 HMAC 签名的
// base64 游标。格式：base64(payload|hmac)，payload 为 timestamp 或
// timestamp|sessionID。
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
// Prefer CURSOR_HMAC_SECRET；未配置时回落 LLM_GATEWAY_SECRET_KEY（网关必配、≥32）。
// 2026-10-15：D2 把 online 从空结果修成真实行后，缺专用密钥会让 hasMore 页整页 503
// （pagination cursor unavailable）——回落避免「修好慢查询却打不开列表」。
func computeCursorHMAC(data string) (string, error) {
	secret := strings.TrimSpace(os.Getenv("CURSOR_HMAC_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(os.Getenv("LLM_GATEWAY_SECRET_KEY"))
	}
	if len(secret) < 32 {
		return "", fmt.Errorf("cursor HMAC secret must be at least 32 bytes (CURSOR_HMAC_SECRET or LLM_GATEWAY_SECRET_KEY)")
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
