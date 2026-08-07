package freeresource

import (
	"testing"
	"time"
)

func TestQuotaTracker_ComputeWindows(t *testing.T) {
	qt := &QuotaTracker{}
	ts := time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC)

	windows := qt.computeWindows(ts, []WindowType{
		WindowTypeHour5,
		WindowTypeDay1,
		WindowTypeDay7,
		WindowTypeMonth1,
	})

	if len(windows) != 4 {
		t.Errorf("expected 4 windows, got %d", len(windows))
	}

	// 验证 day-1 窗口
	for _, w := range windows {
		if w.Type == WindowTypeDay1 {
			expectedStart := time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC)
			if !w.Start.Equal(expectedStart) {
				t.Errorf("day-1 window start: expected %v, got %v", expectedStart, w.Start)
			}
		}
		if w.Type == WindowTypeMonth1 {
			expectedStart := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
			if !w.Start.Equal(expectedStart) {
				t.Errorf("month-1 window start: expected %v, got %v", expectedStart, w.Start)
			}
		}
	}
}

func TestQuotaTracker_Record(t *testing.T) {
	// 这是集成测试，需要真实数据库
	// 在 CI 环境中，应使用 Docker PostgreSQL 容器
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 1. 创建测试数据库连接
	// 2. 插入测试凭据
	// 3. 调用 Record
	// 4. 验证 UPSERT 结果
}

func TestQuotaTracker_Preflight(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 1. 准备测试数据
	// 2. 测试无记录场景（应返回 true）
	// 3. 测试配额充足场景（应返回 true）
	// 4. 测试配额耗尽场景（应返回 false）
	// 5. 测试过期自动重置场景
}

func TestQuotaTracker_CorrectFromHeaders(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// TODO: 实现完整的集成测试
	// 测试各种 429 响应头组合
}

// 单元测试：验证窗口边界计算
func TestWindowBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		ts        time.Time
		winType   WindowType
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "day-1 mid-day",
			ts:        time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC),
			winType:   WindowTypeDay1,
			wantStart: time.Date(2024, 8, 15, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 8, 16, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "month-1 mid-month",
			ts:        time.Date(2024, 8, 15, 14, 30, 0, 0, time.UTC),
			winType:   WindowTypeMonth1,
			wantStart: time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}

	qt := &QuotaTracker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			windows := qt.computeWindows(tt.ts, []WindowType{tt.winType})
			if len(windows) != 1 {
				t.Fatalf("expected 1 window, got %d", len(windows))
			}

			w := windows[0]
			if !w.Start.Equal(tt.wantStart) {
				t.Errorf("start: expected %v, got %v", tt.wantStart, w.Start)
			}
			if !w.End.Equal(tt.wantEnd) {
				t.Errorf("end: expected %v, got %v", tt.wantEnd, w.End)
			}
		})
	}
}
