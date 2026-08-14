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

// ParseCursor 解析 base64 游标为时间戳并验证 HMAC 签名。
func ParseCursor(cursor string) (time.Time, error) {
	if cursor == "" {
		return time.Time{}, nil // 首次请求，返回 zero time（表示从最新开始）
	}
	decoded, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, err
	}

	// Unsigned cursors are rejected so callers cannot bypass tamper protection.
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid cursor format: signature missing")
	}

	timestampStr := parts[0]
	providedMAC := parts[1]

	// 验证 HMAC
	expectedMAC, err := computeCursorHMAC(timestampStr)
	if err != nil {
		return time.Time{}, err
	}
	if !hmac.Equal([]byte(providedMAC), []byte(expectedMAC)) {
		return time.Time{}, fmt.Errorf("cursor signature verification failed")
	}

	// 解析时间戳
	ts, err := time.Parse(time.RFC3339Nano, timestampStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp in cursor: %w", err)
	}
	return ts, nil
}

// EncodeCursor 将时间戳编码为 base64 游标（带 HMAC 签名）。
// 格式：base64(timestamp|hmac)。
func EncodeCursor(ts time.Time) (string, error) {
	if ts.IsZero() {
		return "", nil
	}
	timestampStr := ts.Format(time.RFC3339Nano)
	mac, err := computeCursorHMAC(timestampStr)
	if err != nil {
		return "", err
	}
	payload := fmt.Sprintf("%s|%s", timestampStr, mac)
	return base64.URLEncoding.EncodeToString([]byte(payload)), nil
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
