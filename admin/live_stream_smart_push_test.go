package admin

import (
	"testing"
)

func TestNeedsFullRefresh(t *testing.T) {
	tests := []struct {
		name     string
		cached   *LiveStreamSnapshot
		fresh    *LiveStreamSnapshot
		expected bool
		reason   string
	}{
		{
			name:     "首次推送（缓存为空）",
			cached:   nil,
			fresh:    &LiveStreamSnapshot{Summary: LiveStreamStats{Total: 100}},
			expected: true,
			reason:   "首次推送必须推送",
		},
		{
			name:     "总请求数变化 < 20%（不推送）",
			cached:   &LiveStreamSnapshot{Summary: LiveStreamStats{Total: 100}},
			fresh:    &LiveStreamSnapshot{Summary: LiveStreamStats{Total: 115}},
			expected: false,
			reason:   "变化 15%，低于 20% 阈值",
		},
		{
			name:     "总请求数变化 > 20%（推送）",
			cached:   &LiveStreamSnapshot{Summary: LiveStreamStats{Total: 100}},
			fresh:    &LiveStreamSnapshot{Summary: LiveStreamStats{Total: 130}},
			expected: true,
			reason:   "变化 30%，超过 20% 阈值",
		},
		{
			name: "泳道数量变化（推送）",
			cached: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 100},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "openai"},
						{ID: "anthropic"},
					},
				},
			},
			fresh: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 100},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "openai"},
						{ID: "anthropic"},
						{ID: "google"},
					},
				},
			},
			expected: true,
			reason:   "泳道数量从 2 增加到 3",
		},
		{
			name: "Top 5 顺序变化（推送）",
			cached: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 100},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "openai"},
						{ID: "anthropic"},
					},
				},
			},
			fresh: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 100},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "anthropic"},
						{ID: "openai"},
					},
				},
			},
			expected: true,
			reason:   "Top 1 位置从 openai 变为 anthropic",
		},
		{
			name: "数据无显著变化（不推送）",
			cached: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 100},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "openai"},
						{ID: "anthropic"},
					},
				},
			},
			fresh: &LiveStreamSnapshot{
				Summary: LiveStreamStats{Total: 105},
				Dimensions: map[string][]LiveStreamLane{
					"vendor": {
						{ID: "openai"},
						{ID: "anthropic"},
					},
				},
			},
			expected: false,
			reason:   "总数变化 5%，泳道数量和顺序不变",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := needsFullRefresh(tt.cached, tt.fresh)
			if result != tt.expected {
				t.Errorf("needsFullRefresh() = %v, expected %v\n理由: %s", result, tt.expected, tt.reason)
			}
		})
	}
}

func TestAbsInt(t *testing.T) {
	tests := []struct {
		input    int
		expected int
	}{
		{0, 0},
		{5, 5},
		{-5, 5},
		{100, 100},
		{-100, 100},
	}

	for _, tt := range tests {
		result := absInt(tt.input)
		if result != tt.expected {
			t.Errorf("absInt(%d) = %d, expected %d", tt.input, result, tt.expected)
		}
	}
}
