package autoupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// PreReleaseSuffixes P2 修复：预发布版本标签优先级（数值越大优先级越低）
// alpha < beta < rc < （正式版）
var PreReleaseSuffixes = map[string]int{
	"alpha": 1,
	"beta":  2,
	"rc":    3,
	"":      100, // 正式版
}

// CompareVersions 比较两个语义化版本号（P2 修复：支持 alpha/beta/rc 预发布标签）
// 返回: -1 (v1 < v2), 0 (v1 == v2), 1 (v1 > v2)
// 支持格式: 2.4.2, 2.4.2-alpha, 2.4.2-alpha.1, 2.4.2-beta.2, 2.4.2-rc.1
func CompareVersions(v1, v2 string) int {
	parts1, pre1 := parseVersionWithPreRelease(v1)
	parts2, pre2 := parseVersionWithPreRelease(v2)

	// 比较主版本号
	for i := 0; i < 3; i++ {
		if parts1[i] < parts2[i] {
			return -1
		}
		if parts1[i] > parts2[i] {
			return 1
		}
	}

	// 主版本号相同，比较预发布标签
	return comparePreRelease(pre1, pre2)
}

// parseVersion 解析版本号为 [major, minor, patch]
func parseVersion(v string) [3]int {
	parts, _ := parseVersionWithPreRelease(v)
	return parts
}

// parseVersionWithPreRelease 解析版本号和预发布标签
// 返回: [major, minor, patch], 预发布标签字符串（如 "alpha.1", "beta.2", ""）
func parseVersionWithPreRelease(v string) ([3]int, string) {
	v = strings.TrimPrefix(v, "v")

	// 分离预发布标签（用 - 分隔）
	var preRelease string
	if idx := strings.Index(v, "-"); idx != -1 {
		preRelease = v[idx+1:]
		v = v[:idx]
	}

	parts := strings.Split(v, ".")
	result := [3]int{0, 0, 0}

	for i := 0; i < len(parts) && i < 3; i++ {
		num, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err == nil {
			result[i] = num
		}
	}
	return result, preRelease
}

// comparePreRelease 比较预发布标签
// 规则：
//   - 没有预发布标签的版本 > 有预发布标签的版本（如 2.4.2 > 2.4.2-rc.1）
//   - alpha < beta < rc（按字母/优先级排序）
//   - 数字后缀大的更新（如 alpha.2 > alpha.1）
func comparePreRelease(pre1, pre2 string) int {
	// 两者都没有预发布标签
	if pre1 == "" && pre2 == "" {
		return 0
	}
	// 有预发布标签的小于没有预发布标签的（正式版 > 所有预发布版）
	if pre1 == "" {
		return 1
	}
	if pre2 == "" {
		return -1
	}

	// 解析预发布标签：<name>[.<number>]
	name1, num1 := parsePreReleasePart(pre1)
	name2, num2 := parsePreReleasePart(pre2)

	// 比较预发布阶段（alpha < beta < rc）
	priority1, ok1 := PreReleaseSuffixes[name1]
	if !ok1 {
		priority1 = 0 // 未知阶段视为最低优先级
	}
	priority2, ok2 := PreReleaseSuffixes[name2]
	if !ok2 {
		priority2 = 0
	}

	if priority1 != priority2 {
		if priority1 < priority2 {
			return -1
		}
		return 1
	}

	// 同一阶段，比较数字后缀
	if num1 < num2 {
		return -1
	}
	if num1 > num2 {
		return 1
	}
	return 0
}

// parsePreReleasePart 解析预发布部分，如 "alpha.2" → ("alpha", 2)
func parsePreReleasePart(pre string) (string, int) {
	parts := strings.SplitN(pre, ".", 2)
	name := parts[0]
	num := 0
	if len(parts) > 1 {
		n, err := strconv.Atoi(parts[1])
		if err == nil {
			num = n
		}
	}
	return name, num
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

// IsPreRelease 判断是否为预发布版本
func IsPreRelease(v string) bool {
	_, pre := parseVersionWithPreRelease(v)
	return pre != ""
}

// ValidateVersion 验证版本号格式（P2 修复：支持预发布标签）
// 支持格式: 2.4.2, 2.4.2-alpha, 2.4.2-alpha.1, 2.4.2-beta.2, 2.4.2-rc.1
func ValidateVersion(v string) error {
	v = strings.TrimPrefix(v, "v")

	// 分离预发布标签
	coreVersion := v
	if idx := strings.Index(v, "-"); idx != -1 {
		coreVersion = v[:idx]
		preRelease := v[idx+1:]
		if err := validatePreRelease(preRelease); err != nil {
			return err
		}
	}

	parts := strings.Split(coreVersion, ".")
	if len(parts) != 3 {
		return fmt.Errorf("invalid version format: %s (expected major.minor.patch[-preRelease])", v)
	}

	for i, part := range parts {
		if _, err := strconv.Atoi(strings.TrimSpace(part)); err != nil {
			return fmt.Errorf("invalid version part[%d]: %s", i, part)
		}
	}
	return nil
}

// validatePreRelease 验证预发布标签格式
func validatePreRelease(pre string) error {
	if pre == "" {
		return nil
	}
	parts := strings.SplitN(pre, ".", 2)
	validNames := map[string]bool{"alpha": true, "beta": true, "rc": true}
	if !validNames[parts[0]] {
		return fmt.Errorf("invalid pre-release name: %s (expected alpha/beta/rc)", parts[0])
	}
	if len(parts) > 1 {
		if _, err := strconv.Atoi(parts[1]); err != nil {
			return fmt.Errorf("invalid pre-release number: %s", parts[1])
		}
	}
	return nil
}

// FormatVersion 规范化版本号（去除 v 前缀）
func FormatVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}
