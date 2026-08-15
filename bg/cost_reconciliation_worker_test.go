package bg

import (
	"testing"
	"time"
)

// TestCostReconciliationMonthsToAggregate 验证每次定时聚合覆盖的月份：
// 当前月（保持热数据最新）+ 上一月（月初后补齐最终账）。
func TestCostReconciliationMonthsToAggregate(t *testing.T) {
	tests := []struct {
		name string
		now  time.Time
		want []time.Time
	}{
		{
			name: "月中：当月+上月",
			now:  time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC),
			want: []time.Time{
				time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "1月跨年：1月+去年12月",
			now:  time.Date(2026, 1, 10, 3, 0, 0, 0, time.UTC),
			want: []time.Time{
				time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC),
			},
		},
		{
			name: "月初1日：当月+上月",
			now:  time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC),
			want: []time.Time{
				time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CostReconciliationMonthsToAggregate(tt.now)
			if len(got) != len(tt.want) {
				t.Fatalf("months = %v, want %v", got, tt.want)
			}
			for i := range got {
				if !got[i].Equal(tt.want[i]) {
					t.Errorf("months[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
