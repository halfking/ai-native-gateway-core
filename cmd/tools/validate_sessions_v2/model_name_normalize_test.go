package main

import "testing"

// TestNormalizeModelNameEquatesRealWorldSpellings（§9.32，2026-10-02）
//
// `normalizeModelName` 决定 parity 门会不会被 8 万条**命名噪声**喂成永远红。
// 真库实测：模型原始串「不一致」81,531 行（10.7%），归一化后只剩 2,913 行（0.38%）。
// 也就是说：**86%+ 的"不一致"根本不是缺陷，是同一模型的两种写法**。
//
// 所以这个纯函数必须自己被守住 —— 它不需要数据库，**不依赖真库就能跑**，
// 也不该只靠集成测试间接覆盖。
//
// 样本全部取自真库实际出现过的成对写法（§9.32 实测输出），不是编的。
func TestNormalizeModelNameEquatesRealWorldSpellings(t *testing.T) {
	cases := []struct{ v1, session, why string }{
		{"MiniMax-M3", "minimax-m3", "大小写"},
		{"minimaxai/minimax-m3", "minimax-m3", "厂商前缀"},
		{"nvidia/riva-translate-4b-instruct-v2", "riva-translate-4b-instruct-v2", "厂商前缀 + 分隔符"},
		{"google/diffusiongemma-26b-a4b-it", "diffusiongemma-26b-a4b-it", "厂商前缀 + 变体后缀"},
		{"glm-5-2-260617", "glm-5.2", "版本后缀 + 分隔符"},
		{"glm-5-3-flash-260828", "glm-5.3-flashx", "变体后缀叠在版本后缀之后"},
		{"glm-5.3-flash", "glm-5.3", "变体后缀"},
		{"minimax-m2.7-highspeed", "minimax-m2.7", "能力档位后缀"},
		{"claude-opus-4-5", "claude-opus-4-5-20251101", "日期后缀"},
		{"doubao-seed-2-0-code-preview-260215", "doubao-seed-2-0-code", "变体后缀 + 日期后缀叠加"},
	}
	for _, c := range cases {
		a, b := normalizeModelName(c.v1), normalizeModelName(c.session)
		if a != b {
			t.Errorf("同一模型的两种写法没有被归一化（%s）：\n"+
				"  v1=%q      -> %q\n  session=%q -> %q\n"+
				"这类差异在真库占模型「不一致」的 86%%+，归一化失效会让 parity 门被\n"+
				"命名噪声喂成永远红 —— 而那不是缺陷。", c.why, c.v1, a, c.session, b)
		}
	}
}

// TestNormalizeModelNameKeepsGenuineDivergence 钉住归一化**不能过度**：
// 真分歧必须仍然不等，否则门会把真缺陷洗掉。
func TestNormalizeModelNameKeepsGenuineDivergence(t *testing.T) {
	distinct := [][2]string{
		{"glm-5-2-260617", "glm-5.1"}, // 真库实测的最大一类残余：不同的模型
		{"claude-opus-4-5", "claude-sonnet-4-5"},
		{"gpt-4o", "gpt-4o-mini"},
		{"gemini-3-pro", "gemini-2.5-pro"},
	}
	for _, p := range distinct {
		if normalizeModelName(p[0]) == normalizeModelName(p[1]) {
			t.Errorf("归一化把**真分歧**洗成了相等：%q vs %q\n"+
				"过度归一化比不归一化更坏 —— 它会把真缺陷洗成一致。", p[0], p[1])
		}
	}
}
