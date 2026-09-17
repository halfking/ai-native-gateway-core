// Package admin — session_online_test.go
//
// 单元测试：tenant 隔离、cursor 分页、freshness 计算。
package admin

import (
	"encoding/base64"
	"testing"
	"time"
)

// TestParseCursor 测试 cursor 解析。
func TestParseCursor(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	tests := []struct {
		name     string
		cursor   string
		wantErr  bool
		wantZero bool
	}{
		{
			name:     "empty cursor",
			cursor:   "",
			wantZero: true,
		},
		{
			name:   "valid cursor",
			cursor: mustEncodeCursor(t, time.Date(2026, 8, 13, 10, 30, 0, 123456789, time.UTC)),
		},
		{
			name:    "invalid base64",
			cursor:  "not-base64!!!",
			wantErr: true,
		},
		{
			name:    "invalid timestamp",
			cursor:  base64.URLEncoding.EncodeToString([]byte("not-a-timestamp")),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := parseOnlineSessionCursor(tt.cursor)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOnlineSessionCursor() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantZero && !parsed.UpdatedAt.IsZero() {
				t.Errorf("parseOnlineSessionCursor() expected zero time, got %v", parsed.UpdatedAt)
			}
			if !tt.wantZero && !tt.wantErr && parsed.UpdatedAt.IsZero() {
				t.Errorf("parseOnlineSessionCursor() got zero time, expected valid timestamp")
			}
		})
	}
}

func mustEncodeCursor(t *testing.T, ts time.Time) string {
	t.Helper()
	cursor, err := encodeOnlineSessionCursor(ts, "")
	if err != nil {
		t.Fatalf("encodeOnlineSessionCursor() error = %v", err)
	}
	return cursor
}

// TestEncodeCursor 测试 cursor 编码。
func TestEncodeCursor(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	ts := time.Date(2026, 8, 13, 10, 30, 0, 123456789, time.UTC)
	cursor, err := encodeOnlineSessionCursor(ts, "")
	if cursor == "" {
		t.Errorf("encodeOnlineSessionCursor() returned empty string")
	}

	// 验证往返一致性
	decoded, err := parseOnlineSessionCursor(cursor)
	if err != nil {
		t.Errorf("parseOnlineSessionCursor(encodeOnlineSessionCursor()) error = %v", err)
	}
	if !decoded.UpdatedAt.Equal(ts) {
		t.Errorf("round-trip failed: got %v, want %v", decoded.UpdatedAt, ts)
	}
}

// TestNormalizePaginationParams 测试分页参数规范化。
func TestNormalizePaginationParams(t *testing.T) {
	tests := []struct {
		name      string
		input     PaginationParams
		wantLimit int
	}{
		{
			name:      "zero limit → default 20",
			input:     PaginationParams{Limit: 0},
			wantLimit: 20,
		},
		{
			name:      "negative limit → default 20",
			input:     PaginationParams{Limit: -5},
			wantLimit: 20,
		},
		{
			name:      "limit > 100 → cap at 100",
			input:     PaginationParams{Limit: 200},
			wantLimit: 100,
		},
		{
			name:      "valid limit",
			input:     PaginationParams{Limit: 50},
			wantLimit: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizePaginationParams(tt.input)
			if got.Limit != tt.wantLimit {
				t.Errorf("NormalizePaginationParams() limit = %v, want %v", got.Limit, tt.wantLimit)
			}
		})
	}
}

// TestBuildOnlineSessionPaginationResponse 测试分页响应构建。
func TestBuildOnlineSessionPaginationResponse(t *testing.T) {
	t.Setenv("CURSOR_HMAC_SECRET", "test-secret-key-0123456789012345")
	ts := time.Date(2026, 8, 13, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name          string
		hasMore       bool
		lastTS        time.Time
		lastSessionID string
		wantHasMore   bool
		wantErr       bool
	}{
		{
			name:        "no more → no cursor",
			hasMore:     false,
			lastTS:      ts,
			wantHasMore: false,
		},
		{
			name:          "has more → cursor emitted",
			hasMore:       true,
			lastTS:        ts,
			lastSessionID: "session-abc",
			wantHasMore:   true,
		},
		{
			name:          "zero lastTS → error",
			hasMore:       true,
			lastSessionID: "session-abc",
			wantErr:       true,
		},
		{
			name:    "empty sessionID → error",
			hasMore: true,
			lastTS:  ts,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := buildOnlineSessionPaginationResponse(tt.hasMore, tt.lastTS, tt.lastSessionID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildOnlineSessionPaginationResponse() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if resp.HasMore != tt.wantHasMore {
				t.Errorf("buildOnlineSessionPaginationResponse() has_more = %v, want %v", resp.HasMore, tt.wantHasMore)
			}
			if resp.HasMore && resp.NextCursor == "" {
				t.Errorf("buildOnlineSessionPaginationResponse() has_more=true but next_cursor is empty")
			}
		})
	}
}

// TestCalculateFreshness 测试 freshness 计算。
func TestCalculateFreshness(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		dataSource DataSource
		updatedAt  time.Time
		wantStale  bool
	}{
		{
			name:       "hot: 2 min ago → fresh",
			dataSource: DataSourceHot,
			updatedAt:  now.Add(-2 * time.Minute),
			wantStale:  false,
		},
		{
			name:       "hot: 10 min ago → stale",
			dataSource: DataSourceHot,
			updatedAt:  now.Add(-10 * time.Minute),
			wantStale:  true,
		},
		{
			name:       "merged: 30 min ago → fresh",
			dataSource: DataSourceMerged,
			updatedAt:  now.Add(-30 * time.Minute),
			wantStale:  false,
		},
		{
			name:       "merged: 2 hours ago → stale",
			dataSource: DataSourceMerged,
			updatedAt:  now.Add(-2 * time.Hour),
			wantStale:  true,
		},
		{
			name:       "v2_archive: always stale",
			dataSource: DataSourceV2Archive,
			updatedAt:  now.Add(-1 * time.Minute),
			wantStale:  true,
		},
		{
			name:       "zero time → stale",
			dataSource: DataSourceHot,
			updatedAt:  time.Time{},
			wantStale:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := CalculateFreshness(tt.dataSource, tt.updatedAt, now)
			if info.Stale != tt.wantStale {
				t.Errorf("CalculateFreshness() stale = %v, want %v", info.Stale, tt.wantStale)
			}
			if info.DataSource != tt.dataSource {
				t.Errorf("CalculateFreshness() data_source = %v, want %v", info.DataSource, tt.dataSource)
			}
			if info.FreshnessMs < 0 {
				t.Errorf("CalculateFreshness() freshness_ms = %v, should be >= 0", info.FreshnessMs)
			}
		})
	}
}

// TestCalculateFreshness_ClockSkew 测试时钟回拨保护。
func TestCalculateFreshness_ClockSkew(t *testing.T) {
	now := time.Date(2026, 8, 13, 10, 30, 0, 0, time.UTC)
	future := now.Add(5 * time.Minute) // 未来时间（时钟回拨）

	info := CalculateFreshness(DataSourceHot, future, now)
	if info.FreshnessMs != 0 {
		t.Errorf("CalculateFreshness() with future updatedAt: freshness_ms = %v, want 0 (clock skew protection)", info.FreshnessMs)
	}
}
