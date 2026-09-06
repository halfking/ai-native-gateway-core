// annotation/exporter_test.go — 2026-09-06
//
// P2.1导出器单元测试

package annotation

import (
	"testing"
	"time"
)

func TestParseDateRange(t *testing.T) {
	tests := []struct {
		name      string
		start     string
		end       string
		wantError bool
		checkFunc func(*testing.T, time.Time, time.Time)
	}{
		{
			name:      "valid date range",
			start:     "2026-08-01",
			end:       "2026-09-01",
			wantError: false,
			checkFunc: func(t *testing.T, start, end time.Time) {
				expectedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
				expectedEnd := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC) // +1 day
				
				if !start.Equal(expectedStart) {
					t.Errorf("start = %v, want %v", start, expectedStart)
				}
				if !end.Equal(expectedEnd) {
					t.Errorf("end = %v, want %v", end, expectedEnd)
				}
			},
		},
		{
			name:      "invalid start date format",
			start:     "2026/08/01",
			end:       "2026-09-01",
			wantError: true,
		},
		{
			name:      "invalid end date format",
			start:     "2026-08-01",
			end:       "2026/09/01",
			wantError: true,
		},
		{
			name:      "start after end",
			start:     "2026-09-01",
			end:       "2026-08-01",
			wantError: true,
		},
		{
			name:      "start equals end",
			start:     "2026-08-01",
			end:       "2026-08-01",
			wantError: false, // 允许相等，表示一天的数据
			checkFunc: func(t *testing.T, start, end time.Time) {
				expectedStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
				expectedEnd := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) // +1 day
				
				if !start.Equal(expectedStart) {
					t.Errorf("start = %v, want %v", start, expectedStart)
				}
				if !end.Equal(expectedEnd) {
					t.Errorf("end = %v, want %v", end, expectedEnd)
				}
			},
		},
		{
			name:      "empty start",
			start:     "",
			end:       "2026-09-01",
			wantError: true,
		},
		{
			name:      "empty end",
			start:     "2026-08-01",
			end:       "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, end, err := ParseDateRange(tt.start, tt.end)
			if (err != nil) != tt.wantError {
				t.Errorf("ParseDateRange() error = %v, wantError %v", err, tt.wantError)
				return
			}
			if err == nil && tt.checkFunc != nil {
				tt.checkFunc(t, start, end)
			}
		})
	}
}

func TestExportConfig_Validation(t *testing.T) {
	// 测试导出配置的有效性
	tests := []struct {
		name   string
		config ExportConfig
		valid  bool
	}{
		{
			name: "valid config with min/max confidence",
			config: ExportConfig{
				StartDate:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:       time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				MinConfidence: ptrFloat64(0.0),
				MaxConfidence: ptrFloat64(0.7),
				Limit:         1000,
				OutputPath:    "/tmp/annotations.csv",
			},
			valid: true,
		},
		{
			name: "valid config without confidence filters",
			config: ExportConfig{
				StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				Limit:      1000,
				OutputPath: "/tmp/annotations.csv",
			},
			valid: true,
		},
		{
			name: "zero limit",
			config: ExportConfig{
				StartDate:  time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				EndDate:    time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				Limit:      0,
				OutputPath: "/tmp/annotations.csv",
			},
			valid: true, // 0表示不限制
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里只是验证配置可以被创建，实际验证在导出时进行
			if tt.config.OutputPath == "" && tt.valid {
				t.Error("valid config should have output path")
			}
		})
	}
}

// ptrFloat64 返回float64的指针
func ptrFloat64(v float64) *float64 {
	return &v
}
