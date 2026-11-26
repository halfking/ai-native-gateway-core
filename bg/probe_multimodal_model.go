// Package bg — probe_multimodal_model.go
//
// 图形类 / 视频类模型识别（自动探测的成本门）。
//
// 用途：自动探测作用域门在「多模态自动探测」关闭时（默认）拒绝探测
// 图形/视频类模型 —— 一次出图/出片的成本远高于一次文本探针，2026-11-26
// 那场 gpt-image 风暴就是这类模型被自动探测反复打出来的。
//
// ★★ 为什么不能用现成的模态口径（三条都对着真库量过，不是推断）：
//
//	① provider_models.modality —— 生产库实测：gpt-image-2 / sora-2 /
//	   wan3.0-video-cn **全部记成 'text'**；vision=0 行、video=0 行
//	   ⇒ ⇒ 这个列根本区分不出「图像生成」⇒ 不可用。
//	② models_canonical.modality —— 有 'multimodal'(216 行) / 'vision'(15 行)，
//	   但那说的是「文本 + 图片输入的对话模型」（gemini-3-pro-image 在
//	   models_canonical 里就记成 'multimodal'）⇒ ⇒ **不等于**图像生成
//	   ⇒ ⇒ 同样不可用。
//	③ modelname.InferModality —— 规则集只有 audio、没有 image/video
//	   ⇒ gpt-image-2 会落回 "text" ⇒ 不可用；且**不得改它**：它同时是
//	   第四处修正（bg/probe_gateway_plan.go 的 gatewayPlanModalityFor）
//	   的模态参数来源 ⇒ ⇒ 改它会漂移**路由行为**（超出一个探测门该有的
//	   影响面）⇒ ⇒ 本文件另立专用识别器。
//
// ★判据形态：**整词（token）匹配**，不是朴素子串匹配。
//   朴素子串会把这些**文本模型**误伤（生产库实测存在）：
//     nvidia/ai-synthetic-video-detector   ← 名字里有 "video"，实为分类器
//     google/diffusiongemma-26b-a4b-it     ← 名字里有 "diffusion"，实为文本 LM
//     thinkingmachines/inkling*            ← 名字里有 "inkling"，实为文本模型
//   ⇒ 「diffusion」**不**是标记词（DiffusionGemma 是文本扩散 LM）；
//   ⇒ 「detector」命中即**排除**（分类器不是生成器，拿生成探针去探它
//     本来就是探错对象）。
//
// 整份真实生产清单（154 生产库 provider_models，2026-11-26 取）逐条钉在
// 同包测试 TestProbeGraphicVideoModelMatchesProductionInventory 里 ——
// 识别器改了就当场红。
package bg

import "strings"

// probeGraphicVideoTokens 是「模型名里出现即判为图形/视频类」的整词标记。
//
// 全部来自生产库实测清单（见文件头与同包测试），不是拍脑袋的规则表。
var probeGraphicVideoTokens = map[string]bool{
	// ── 图像生成 ───────────────────────────────────────────────
	"image": true, // gpt-image-* / gemini-*-image* / qwen-image-* / wan2.7-image-*
	"t2i":   true, // text-to-image：doubao-seedream-*-t2i-* / wan2.6-t2i-*
	"i2i":   true, // image-to-image

	// ── 视频生成 ───────────────────────────────────────────────
	"video":     true, // wan3.0-video* / wan2.7-videoedit 的 video 词素
	"videoedit": true, // wan2.7-videoedit-*（编辑类视频模型）
	"t2v":       true, // text-to-video：wan2.7-t2v-* / doubao-seedance-*-t2v-*
	"i2v":       true, // image-to-video：wan2.7-i2v-* / doubao-seedance-*-i2v-*
	"r2v":       true, // reference-to-video：wan2.7-r2v-*
	"flf2v":     true, // first-last-frame-to-video：wan2-1-14b-flf2v-*
}

// probeGraphicVideoFamilies 是「模型族名」标记 —— 整族都是媒体生成模型，
// 但名字里不一定带 image/video 字样（sora / kling / seedance / seedream）。
var probeGraphicVideoFamilies = map[string]bool{
	"sora":     true, // OpenAI 视频族
	"kling":    true, // 快手可灵视频族
	"seedance": true, // 豆包视频族
	"seedream": true, // 豆包图像族
}

// probeGraphicVideoExemptTokens 命中即**排除**（不是生成模型）。
//
// 生产库实测：`nvidia/ai-synthetic-video-detector` 是**视频真伪检测器**
// （文本分类模型），它的名字里带 "video" 整词 ⇒ 若不做豁免，整词匹配会
// 把它误判成视频生成模型 ⇒ ⇒ 图形/视频门会拦掉一个文本模型的探测。
//
// 判据取「整词 detector」而不是钉死某一个模型名：凡是名字里声明自己是
// 检测器的，都不是被生成探针探测的对象 —— 这条规则对未来的新模型同样成立。
var probeGraphicVideoExemptTokens = map[string]bool{
	"detector": true,
}

// probeGraphicVideoModel reports whether rawModel is a graphics-generation
// (image) or video-generation model, i.e. a model whose automatic probing
// costs orders of magnitude more than a text probe.
//
// False means "not a media-generation model" — it does NOT mean the model is
// cheap in every other sense (an embedding model is cheap too, and is not
// matched here either; that is fine, this predicate only answers
// "media generation or not").
func probeGraphicVideoModel(rawModel string) bool {
	tokens := probeModelNameTokens(rawModel)
	if len(tokens) == 0 {
		return false
	}
	// 豁免优先：分类器/检测器不是生成器（生产库 nvidia/ai-synthetic-video-detector）。
	for _, t := range tokens {
		if probeGraphicVideoExemptTokens[t] {
			return false
		}
	}
	for _, t := range tokens {
		if probeGraphicVideoTokens[t] || probeGraphicVideoFamilies[t] {
			return true
		}
	}
	return false
}

// probeModelNameTokens lowercases the model name and splits it into
// alphanumeric tokens. Separators are every character that is not a-z0-9 —
// so '-', '_', '.', '/', ':', '+' all split.
//
// Why split on '.' too: "gpt-image-1.5" must yield the token "image"
// (not "image1"), and "openai/gpt-5.4-image-2" must yield "image".
// Why split on ':' too: "thinkingmachines/inkling-small:free" must NOT
// produce a token that looks like a media marker.
//
// Casing is normalized here so the caller never has to pre-lowercase.
func probeModelNameTokens(rawModel string) []string {
	s := strings.ToLower(strings.TrimSpace(rawModel))
	if s == "" {
		return nil
	}
	tokens := make([]string, 0, 8)
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			if start < 0 {
				start = i
			}
		default:
			if start >= 0 {
				tokens = append(tokens, s[start:i])
				start = -1
			}
		}
	}
	if start >= 0 {
		tokens = append(tokens, s[start:])
	}
	return tokens
}
