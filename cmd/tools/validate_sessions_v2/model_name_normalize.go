package main

import (
	"regexp"
	"strings"
)

var (
	reSep     = regexp.MustCompile(`[-_.]`)
	reVersion = regexp.MustCompile(`-[0-9]{4,8}$`)
	// 变体后缀：同一模型的能力档位命名（highspeed / flash / preview / code …）。
	// 归一化只用于**比较**，不用于写回存储值。
	// 变体后缀：**只剥「格式/阶段标记」，不剥「区分真实模型的能力档位」**。
	//
	// 这条边界是被自己的测试逼出来的：`mini`/`pro`/`max` 一旦剥掉，
	// `gpt-4o` 与 `gpt-4o-mini` 就被洗成相等 —— 而它们**是不同模型**。
	// 过度归一化比不归一化更坏：它把真缺陷洗成一致。
	// （TestNormalizeModelNameKeepsGenuineDivergence 钉住这条。）
	reVariant = regexp.MustCompile(`-(highspeed|flashx|flash|preview|code|reasoning|thinking|instruct|it|lite|chat|search|vision|omni)$`)
)

// normalizeModelName 把厂商前缀、日期/版本后缀、分隔符差异、大小写差异归一，
// 使「同一个模型的两种写法」在比较时相等。**只在比较时使用，不改写存储值。**
func normalizeModelName(m string) string {
	s := strings.ToLower(strings.TrimSpace(m))
	if i := strings.IndexByte(s, '/'); i >= 0 { // 去掉 provider/ 前缀
		s = s[i+1:]
	}
	s = reSep.ReplaceAllString(s, "-")
	// 反复剥：变体后缀可能叠加在版本后缀之后（glm-5-3-flash-260828）
	for i := 0; i < 3; i++ {
		s = reVersion.ReplaceAllString(s, "")
		s = reVariant.ReplaceAllString(s, "")
	}
	return strings.Trim(s, "-")
}
