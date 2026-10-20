package sanitize

import (
	"context"
	"strings"
	"testing"
)

// r1039（D6，2026-10-20）—— `medical:` 族此前**完全没被加载**，而且**就算
// 加载也会炸**。
//
// 缺陷本体（两层，缺一不可）：
//  1. `parsePatternConfig` 只遍历 pii / secret / financial 三族 ⇒ medical 族
//     的三条规则（medical_record / diagnosis / medication）从未进入规则集。
//  2. 就算有人把 medical 加进遍历列表，它的正则里的 `\u4e00` 是 **JS 风格**
//     的 Unicode 转义，Go 的 RE2 **不支持**（`invalid escape sequence: \u`）。
//     而加载器是「整份文件全编译、全通过才替换」的语义 ⇒ 一条正则编译不过
//     ⇒ **整份 yaml 加载失败** ⇒ 网关回落到内置兜底规则集，连 pii/secret/
//     financial 也一起没了。
//
// ⇒ 本文件钉住「加载 + 生效」，通用门禁（yaml 规则全覆盖）在
// r1039_yaml_rule_coverage_test.go。
//
// ★ 判据纪律：判据用**真实规则文件**（NewPatternDetectorFromFile）——
// 内置 fallback 里没有任何医疗规则，用它写这类判据恒绿（AGENTS 判据纪律
// 第 2 条，r1010 教训）。
func TestR1039MedicalFamilyIsLoadedAndEnforced(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	detector.mu.RLock()
	loaded := map[string]bool{}
	for _, p := range detector.patterns {
		loaded[p.id] = true
	}
	detector.mu.RUnlock()

	for _, id := range []string{"medical.medical_record", "medical.diagnosis", "medical.medication"} {
		if !loaded[id] {
			t.Errorf("医疗规则 %s 没有被加载 ⇒ 这条规则是死的", id)
		}
	}

	sanitizer, err := NewSanitizer(detector)
	if err != nil {
		t.Fatalf("NewSanitizer: %v", err)
	}

	// 正向：真实的医疗声明必须被遮蔽。
	for _, tc := range []struct{ name, in, leak string }{
		{"病历号", "病历号 A123456", "A123456"},
		{"就诊号", "就诊号 ZY20240101", "ZY20240101"},
		{"住院号", "住院号 RY778899", "RY778899"},
		{"诊断", "诊断 高血压", "高血压"},
		{"确诊", "确诊 糖尿病", "糖尿病"},
		{"用药", "用药 阿司匹林", "阿司匹林"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := sanitizer.SanitizeInput(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(result.SanitizedText, tc.leak) {
				t.Errorf("医疗信息未脱敏：%q -> %q", tc.in, result.SanitizedText)
			}
			if !strings.Contains(result.SanitizedText, "{SENSITIVE:") {
				t.Errorf("医疗信息没有被占位符替换：%q -> %q", tc.in, result.SanitizedText)
			}
			restored, err := sanitizer.RestoreOutput(context.Background(), result.SanitizedText, result.SanitizeMap)
			if err != nil || restored != tc.in {
				t.Errorf("还原失败（原文被改写）：%q -> %q (err=%v)", tc.in, restored, err)
			}
		})
	}
}

// TestR1039MedicalRulesDoNotFireOnOrdinaryProse 是**允许侧**判据，且带
// 「前提在场」对照：同一批普通散文里**没有任何医疗词**，医疗规则不该命中。
//
// 这条与上面的正向用例成对：只有正向用例时，「规则被加载但永远不触发」
// 也会全绿（假绿）。这里用「医疗词在别处确实会触发」把前提钉住。
func TestR1039MedicalRulesDoNotFireOnOrdinaryProse(t *testing.T) {
	detector, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load production detector: %v", err)
	}
	for _, in := range []string{
		"今天下午的发布窗口已经确认，运维同学会提前到场。",
		"本次变更只涉及网关的路由配置，不影响上游模型的计费口径。",
		"请在下一个迭代开始前把评审意见同步到设计文档。",
		"灰度期间先观察错误率，再决定是否扩大流量比例。",
	} {
		fragments, err := detector.DetectScope(context.Background(), in, ScopeInputDeclarations)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fragments {
			if strings.HasPrefix(f.RuleID, "medical.") {
				t.Errorf("医疗规则在普通散文上误报：%q -> %+v", in, f)
			}
		}
	}
}
