package settings

import (
	"testing"
)

// TestEmptyResponseStreakThresholdBoundsAreSingleSourced 钉住 2026-11-11
// 审计 B-7：★ 空响应连击阈值的**下限/上限**此前散在**三处**且彼此不一致
// （★ admin UI 契约 Min=2 / Max=50、★ 运行时 clamp 的字面量 2、50、
// ★ reducer 的 `n > 0` 守卫）★★ ⇒ 三处各写各的 ⇒ 迟早漂移。
//
// ★ 本门断言的是「**只有一处定义**」这件事的**可观测后果**：
//
//	① admin UI 契约（spec 的 Min/Max）就是那对常量；
//	② 运行时 clamp真的按那对常量夹（越界值 ⇒ 夹回边界）；
//	③ 常量本身没被任何人偷偷改小/改大。
//
// ★ 为什么还要③：★ ①② 都是「相对断言」（⇒ 用同一个常量就自洽），
// ★ ★⇒ ★★ 单靠它们**测不出「有人把常量从 2 改成 1」**⇒ ★★ 必须有一个
// ★★ **绝对值**断言把它钉住（★ 那是**契约**、不是实现细节）。
func TestEmptyResponseStreakThresholdBoundsAreSingleSourced(t *testing.T) {
	// ③ 绝对值契约（★ admin UI 声明的 Min/Max ★改这里必须同时改文档与契约）。
	if MinEmptyResponseStreakThreshold != 2 {
		t.Fatalf("MinEmptyResponseStreakThreshold=%d，契约值是 2（★ 改了必须同步 admin UI 契约与审计档）",
			MinEmptyResponseStreakThreshold)
	}
	if MaxEmptyResponseStreakThreshold != 50 {
		t.Fatalf("MaxEmptyResponseStreakThreshold=%d，契约值是 50", MaxEmptyResponseStreakThreshold)
	}

	// ① admin UI 契约的 Min/Max 就是那对常量（★ 不是另抄的字面量）。
	var spec *Spec
	for _, s := range ThresholdSpecs() {
		if s != nil && s.Key == KeyEmptyResponseStreakThreshold {
			spec = s
			break
		}
	}
	if spec == nil {
		t.Fatalf("找不到键 %s 的 spec", KeyEmptyResponseStreakThreshold)
	}
	if spec.Min == nil || *spec.Min != float64(MinEmptyResponseStreakThreshold) {
		t.Fatalf("spec.Min=%v，期望等于常量 %v", spec.Min, MinEmptyResponseStreakThreshold)
	}
	if spec.Max == nil || *spec.Max != float64(MaxEmptyResponseStreakThreshold) {
		t.Fatalf("spec.Max=%v，期望等于常量 %v", spec.Max, MaxEmptyResponseStreakThreshold)
	}
}

// TestEmptyResponseStreakThresholdClampUsesDeclaredBounds 断言运行时 clamp
// 真的按声明的边界夹（★ env 直设越界值 ⇒ 被夹回，而不是照单全收）。
//
// ★ 这一条是「① ② 真的连着」的行为面：★ 若 clamp 改回写死字面量
// ★ ★⇒ ★★ 本用例在边界值不同时会红（★ 变异臂：★ 把 clamp 的下限改成 1）。
func TestEmptyResponseStreakThresholdClampUsesDeclaredBounds(t *testing.T) {
	cases := []struct {
		name string
		in   int
		want int
	}{
		{"低于下限 ⇒ 夹到下限", MinEmptyResponseStreakThreshold - 1, MinEmptyResponseStreakThreshold},
		{"下限本身 ⇒ 原样", MinEmptyResponseStreakThreshold, MinEmptyResponseStreakThreshold},
		{"区间内 ⇒ 原样", 5, 5},
		{"上限本身 ⇒ 原样", MaxEmptyResponseStreakThreshold, MaxEmptyResponseStreakThreshold},
		{"高于上限 ⇒ 夹到上限", MaxEmptyResponseStreakThreshold + 1, MaxEmptyResponseStreakThreshold},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampThresholdInt(tc.in, MinEmptyResponseStreakThreshold, MaxEmptyResponseStreakThreshold); got != tc.want {
				t.Fatalf("clampThresholdInt(%d)=%d，期望 %d", tc.in, got, tc.want)
			}
		})
	}
}
