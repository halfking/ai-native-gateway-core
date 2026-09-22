package summary

// 压缩 fallback 链"同厂优先"重排（Wave 3 B9，2026-09-22 设计差距审计）。
//
// 设计差距：摘要模型链是全局静态顺序，目标模型是 claude-* 时 fallback
// 却可能先砸向 gpt-*/glm-*，厂商间无谓的失败重试拉长压缩耗时。设计
// 要求链首插入"目标大模型同厂优先"档。
//
// 落地方式：不改链内容、不改配置语义——目标模型已知时对链做稳定重排
// （同厂段前置、异厂段后置，两段内部保持原相对顺序）。"同厂"用模型名
// 首 token 近似（claude-*/glm-*/gpt-*…），与 discovery.InferFamily 的
// 厂商归组在常见命名上一致且零额外依赖。

import "strings"

// vendorKey extracts the lowercase vendor token of a model name: the part
// before the first '-' or '.' ("claude-sonnet-4-5" → "claude",
// "z-ai/glm-5.2" → "z-ai/glm" — vendor-prefixed names keep enough entropy
// to be distinct, which is fine for a stable reordering).
func vendorKey(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.IndexAny(n, "-._"); idx > 0 {
		return n[:idx]
	}
	return n
}

// ReorderSameVendorFirst returns the chain with entries whose vendor token
// matches the target model's moved to the front (stable). Empty/unknown
// target or empty chain returns the input unchanged.
func ReorderSameVendorFirst(models []string, targetModel string) []string {
	if len(models) <= 1 || strings.TrimSpace(targetModel) == "" {
		return models
	}
	target := vendorKey(targetModel)
	if target == "" {
		return models
	}
	same := make([]string, 0, len(models))
	rest := make([]string, 0, len(models))
	for _, m := range models {
		if vendorKey(m) == target {
			same = append(same, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(same) == 0 {
		return models
	}
	out := append(same, rest...)
	return out
}
