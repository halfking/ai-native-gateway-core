package autoupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// CompareVersions 比较两个语义化版本号
// 返回: -1 (v1 < v2), 0 (v1 == v2), 1 (v1 > v2)
func CompareVersions(v1, v2 string) int {
	parts1 := parseVersion(v1)
	parts2 := parseVersion(v2)

	for i := 0; i < 3; i++ {
		if parts1[i] < parts2[i] {
			return -1
		}
		if parts1[i] > parts2[i] {
			return 1
		}
	}
	return 0
}

// parseVersion 解析版本号为 [major, minor, patch]
func parseVersion(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}

	for i := 0; i < len(parts) && i < 3; i++ {
		num, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err == nil {
			result[i] = num
		}
	}
	return result
}

// IsNewer 判断 newer 是否比 current 新
func IsNewer(current, newer string) bool {
	return CompareVersions(current, newer) < 0
}

// IsCompatible 判断当前版本是否满足最低版本要求
func IsCompatible(current, minRequired string) bool {
	if minRequired == "" {
		return true
	}
	return CompareVersions(current, minRequired) >= 0
}

// ValidateVersion 验证版本号格式（语义化版本）
func ValidateVersion(v string) error {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid version format: %s (expected major.minor.patch)", v)
	}

	for i, part := range parts {
		if _, err := strconv.Atoi(strings.TrimSpace(part)); err != nil {
			return fmt.Errorf("invalid version part[%d]: %s", i, part)
		}
	}
	return nil
}

// FormatVersion 规范化版本号（去除 v 前缀）
func FormatVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}
