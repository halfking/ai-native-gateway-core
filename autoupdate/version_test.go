package autoupdate

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCompareVersions 测试 P2 修复后的版本比较（支持 alpha/beta/rc）
func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name     string
		v1       string
		v2       string
		expected int
	}{
		// 基础语义化版本
		{"equal", "2.4.2", "2.4.2", 0},
		{"v1 < v2", "2.4.1", "2.4.2", -1},
		{"v1 > v2", "2.4.3", "2.4.2", 1},
		{"major diff", "1.0.0", "2.0.0", -1},
		{"minor diff", "2.3.0", "2.4.0", -1},
		{"with v prefix", "v2.4.2", "v2.4.3", -1},

		// 预发布版本（P2 修复）
		{"alpha < release", "2.4.2-alpha", "2.4.2", -1},
		{"release > alpha", "2.4.2", "2.4.2-alpha", 1},
		{"alpha < beta", "2.4.2-alpha", "2.4.2-beta", -1},
		{"beta < rc", "2.4.2-beta", "2.4.2-rc", -1},
		{"rc < release", "2.4.2-rc", "2.4.2", -1},
		{"alpha.1 < alpha.2", "2.4.2-alpha.1", "2.4.2-alpha.2", -1},
		{"alpha.2 > alpha.1", "2.4.2-alpha.2", "2.4.2-alpha.1", 1},
		{"alpha.5 = alpha.5", "2.4.2-alpha.5", "2.4.2-alpha.5", 0},
		{"alpha.10 > alpha.9", "2.4.2-alpha.10", "2.4.2-alpha.9", 1}, // 数值比较非字典序
		{"beta.1 > alpha.99", "2.4.2-beta.1", "2.4.2-alpha.99", 1},
		{"rc.1 > beta.99", "2.4.2-rc.1", "2.4.2-beta.99", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CompareVersions(tt.v1, tt.v2)
			assert.Equal(t, tt.expected, result, "CompareVersions(%q, %q) = %d, want %d", tt.v1, tt.v2, result, tt.expected)
		})
	}
}

func TestIsNewer(t *testing.T) {
	tests := []struct {
		current  string
		newer    string
		expected bool
	}{
		{"2.4.2", "2.4.3", true},
		{"2.4.2", "2.4.2", false},
		{"2.4.2", "2.4.1", false},
		{"2.4.2-alpha", "2.4.2", true},
		{"2.4.2", "2.4.2-alpha", false},
		{"2.4.2-alpha.1", "2.4.2-alpha.2", true},
		{"2.4.2-beta", "2.4.2-rc", true},
	}

	for _, tt := range tests {
		t.Run(tt.current+"->"+tt.newer, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsNewer(tt.current, tt.newer))
		})
	}
}

func TestIsPreRelease(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{"2.4.2", false},
		{"2.4.2-alpha", true},
		{"2.4.2-beta", true},
		{"2.4.2-rc", true},
		{"2.4.2-alpha.1", true},
		{"v2.4.2", false},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsPreRelease(tt.version))
		})
	}
}

func TestValidateVersion_PreRelease(t *testing.T) {
	tests := []struct {
		version  string
		expected bool
	}{
		{"2.4.2", true},
		{"v2.4.2", true},
		{"2.4.2-alpha", true},
		{"2.4.2-beta", true},
		{"2.4.2-rc.1", true},
		{"2.4.2-alpha.99", true},
		{"invalid", false},
		{"2.4", false},
		{"2.4.2-invalid", false},   // 预发布名无效
		{"2.4.2-alpha.abc", false}, // 预发布数字无效
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			err := ValidateVersion(tt.version)
			if tt.expected {
				assert.NoError(t, err, "expected %s to be valid", tt.version)
			} else {
				assert.Error(t, err, "expected %s to be invalid", tt.version)
			}
		})
	}
}
