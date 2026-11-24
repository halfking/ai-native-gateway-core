package pluginruntime

import (
	"strconv"
	"strings"
)

// HostAPI is the host pluginrt API version plugins' caret ranges are checked against.
// Kept at major 1 so existing process plugins (empty API → "^1") stay compatible.
const HostAPI = "1.0"

// Compatible reports whether a plugin written for caret range want runs on HostAPI.
// An empty range is "^1". Same major, and a minor no newer than the host's.
func Compatible(want string) bool {
	return compatibleWith(want, HostAPI)
}

// compatibleWith 是 Compatible 的宿主版本参数化形态。
//
// ★ 为什么拆出来（2026-11-25 12h 修订审计轮 P1-1）：
//
//	宿主 API 版本在本仓是**编译期常量**（HostAPI = "1.0"）⇒ 只按「插件 API 范围」
//	判定兼容性的 Compatible(want) **无法验证「宿主升级后旧插件是否仍兼容」**。
//	⇒ 宿主 API 演化轮的钉测（api_compat_test.go 的 TestAPIEvolution）原本
//	  写了 host="1.5" 的场景，但 Compatible 内部只读常量 HostAPI ⇒ 该场景
//	  **永远不可达** ⇒ 门绿而覆盖为零（典型的「字符串/常量门不算门」）。
//	⇒ 这里抽出宿主版本形参，让演化场景**真的能被执行**；Compatible(want)
//	  退化为 compatibleWith(want, HostAPI)，**行为逐字不变**。
func compatibleWith(want, host string) bool {
	if want == "" {
		want = "^1"
	}
	want, ok := strings.CutPrefix(want, "^")
	if !ok {
		return false
	}
	wMaj, wMin, ok1 := parseAPIVersion(want)
	hMaj, hMin, ok2 := parseAPIVersion(host)
	return ok1 && ok2 && wMaj == hMaj && wMin <= hMin
}

func parseAPIVersion(v string) (int, int, bool) {
	maj, min, _ := strings.Cut(v, ".")
	a, err := strconv.Atoi(maj)
	if err != nil {
		return 0, 0, false
	}
	if min == "" {
		return a, 0, true
	}
	min, _, _ = strings.Cut(min, ".")
	b, err := strconv.Atoi(min)
	return a, b, err == nil
}
