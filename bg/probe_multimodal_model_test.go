package bg

import "testing"

// TestProbeGraphicVideoModelMatchesProductionInventory pins the graphics /
// video classifier against the **real production model inventory**.
//
// ★ 证据来源：154 生产库 provider_models 全表
//   （SELECT DISTINCT raw_model_name WHERE raw_model_name ~ '(image|video|
//   sora|kling|t2v|i2v|r2v|t2i|flf2v|diffusion|inkling|detector)'），
//   2026-11-26 取。⇒ 清单里的每一条都真实存在于生产。
//
// ★ 为什么必须钉全量清单而不是钉几条样例：这条识别器决定「哪些模型不
//   自动探测」⇒ 判错的代价是双向的（漏判 ⇒ 媒体生成模型被反复探测＝
//   2026-11-26 那场风暴；误判 ⇒ 一个文本模型永远不被自动探测＝恢复状态
//   不再被自动更正）。
//
// ★ 这条清单同时是**反例清单**的来源：nvidia/ai-synthetic-video-detector /
//   google/diffusiongemma-26b-a4b-it / thinkingmachines/inkling* 三族名字里
//   带 video / diffusion / inkling 字样，但它们是**文本模型** ⇒ 朴素子串
//   匹配会误伤 ⇒ ⇒ 判据必须是整词匹配 + 检测器豁免。
func TestProbeGraphicVideoModelMatchesProductionInventory(t *testing.T) {
	// ── 图形类 / 视频类（应判 true）────────────────────────────────
	mediaModels := []string{
		// OpenAI 图像族
		"gpt-image-1", "gpt-image-1-mini", "gpt-image-1.5",
		"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst",
		"gpt-image-2.6", "gpt-image-2.6-flare", "gpt-image-2.6-sunburst",
		"codex-gpt-image-2", "plus-codex-gpt-image-2",
		"openai/gpt-5-image", "openai/gpt-5-image-mini", "openai/gpt-5.4-image-2",
		// Gemini 图像族
		"gemini-2.5-flash-image", "gemini-3-pro-image", "gemini-3-pro-image-preview",
		"gemini-3.1-flash-image", "gemini-3.1-flash-image-preview",
		"gemini-3.1-flash-lite-image",
		"google/gemini-2.5-flash-image", "google/gemini-3-pro-image",
		"google/gemini-3-pro-image-preview", "google/gemini-3.1-flash-image",
		"google/gemini-3.1-flash-image-preview", "google/gemini-3.1-flash-lite-image",
		// 通义 / 阿里图像族
		"qwen-image-2.0-cn", "qwen-image-2.0-glb",
		"qwen-image-2.0-pro-cn", "qwen-image-2.0-pro-glb",
		// 豆包图像 / 视频族
		"doubao-seedream-3-0-t2i-250415",
		"doubao-seedance-1-0-lite-t2v-250428",
		"doubao-seedance-1-0-lite-i2v-250428",
		// 可灵 / Sora 视频族
		"kling-v2-1-cn", "kling-v2-6-cn", "kling-v3-cn", "sora-2",
		// 通义万相图像 / 视频族
		"wan2.6-t2i-cn", "wan2.6-t2i-glb",
		"wan2.7-image-cn", "wan2.7-image-glb", "wan2.7-image-pro-cn", "wan2.7-image-pro-glb",
		"wan2.7-i2v-cn", "wan2.7-i2v-glb",
		"wan2.7-t2v-cn", "wan2.7-t2v-glb",
		"wan2.7-r2v-cn", "wan2.7-r2v-glb",
		"wan2.7-videoedit-cn", "wan2.7-videoedit-glb",
		"wan2-1-14b-i2v-250225", "wan2-1-14b-t2v-250225", "wan2-1-14b-flf2v-250417",
		"wan3.0-video", "wan3.0-video-cn", "wan3.0-video-glb",
		"wan3.0-video-prime-cn", "wan3.0-video-prime-glb",
	}
	for _, m := range mediaModels {
		if !probeGraphicVideoModel(m) {
			t.Errorf("probeGraphicVideoModel(%q) = false, want true (production media-generation model)", m)
		}
	}

	// ── 名字带 media 字样但**不是**媒体生成模型（应判 false）───────
	textModels := []string{
		// 生产库实测：名字里有 "video"，实为真伪**检测器**（文本分类模型）
		"nvidia/ai-synthetic-video-detector",
		// 生产库实测：名字里有 "diffusion"，实为**文本**扩散 LM
		"google/diffusiongemma-26b-a4b-it",
		// 生产库实测：inkling 是文本模型族
		"thinkingmachines/inkling",
		"thinkingmachines/inkling-small",
		"thinkingmachines/inkling-small:free",
		"thinkingmachines/inkling:batch",
		"thinkingmachines/inkling:free",
	}
	for _, m := range textModels {
		if probeGraphicVideoModel(m) {
			t.Errorf("probeGraphicVideoModel(%q) = true, want false (production TEXT model — media gate must not swallow it)", m)
		}
	}
}

// TestProbeGraphicVideoModelIgnoresOrdinaryTextModels guards the everyday
// path: the vast majority of models are plain text/chat models and must
// never be treated as media-generation.
func TestProbeGraphicVideoModelIgnoresOrdinaryTextModels(t *testing.T) {
	for _, m := range []string{
		"", "   ", "claude-sonnet-5", "gpt-4o", "gpt-5.4", "minimax-m3",
		"glm-5.3-flash", "z-ai/glm-5.3-flash", "gemini-2.5-flash", "gemini-1.5-pro",
		"qwen-vl-max", "deepseek-chat", "doubao-seed-1-6", "kimi-k2",
		"moonshot-v1-128k", "ernie-4.5-turbo", "text-embedding-3-large",
		// "image" 只是更长词的一部分 —— 整词匹配不应命中
		"imagenet-classifier", "my-imagenet-embedder",
		// "video" 只是更长词的一部分
		"videogen-text", "videogenm",
	} {
		if probeGraphicVideoModel(m) {
			t.Errorf("probeGraphicVideoModel(%q) = true, want false (ordinary text model)", m)
		}
	}
}

// TestProbeGraphicVideoModelIsCaseInsensitive pins the normalization: model
// names arrive in mixed case from providers and clients.
func TestProbeGraphicVideoModelIsCaseInsensitive(t *testing.T) {
	for _, m := range []string{"GPT-Image-2", "SORA-2", "KLING-V3-CN", "OpenAI/GPT-5-Image"} {
		if !probeGraphicVideoModel(m) {
			t.Errorf("probeGraphicVideoModel(%q) = false, want true (case must not change the verdict)", m)
		}
	}
}

// TestProbeModelNameTokens pins the tokenizer: the whole verdict rests on
// token boundaries, so the tokenizer itself needs a direct contract.
func TestProbeModelNameTokens(t *testing.T) {
	cases := map[string][]string{
		"gpt-image-2":            {"gpt", "image", "2"},
		"gpt-image-1.5":          {"gpt", "image", "1", "5"},
		"openai/gpt-5.4-image-2": {"openai", "gpt", "5", "4", "image", "2"},
		"wan2.7-videoedit-cn":    {"wan2", "7", "videoedit", "cn"},
		"thinkingmachines/inkling-small:free": {
			"thinkingmachines", "inkling", "small", "free",
		},
		"  ":        {},
		"": {},
		"no-seps":   {"no", "seps"},
		"a.b_c-d:e": {"a", "b", "c", "d", "e"},
	}
	for in, want := range cases {
		got := probeModelNameTokens(in)
		if len(got) != len(want) {
			t.Errorf("probeModelNameTokens(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("probeModelNameTokens(%q) = %v, want %v", in, got, want)
				break
			}
		}
	}
}
