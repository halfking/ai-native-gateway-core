package catalog

import "testing"

func TestResolveVendor(t *testing.T) {
	cases := []struct {
		name, family, dbVendor, want string
	}{
		{"gpt-5.4", "gpt", "", "OpenAI"},
		{"minimax-m3", "minimax", "", "MiniMax"},
		{"glm-4.7", "glm", "", "Zhipu AI"},
	}
	for _, tc := range cases {
		if got := ResolveVendor(tc.name, tc.family, tc.dbVendor); got != tc.want {
			t.Errorf("ResolveVendor(%q,%q,%q)=%q want %q", tc.name, tc.family, tc.dbVendor, got, tc.want)
		}
	}
}

func TestInferVendor(t *testing.T) {
	// 675: family 行缺失（如 qwen3.8）或 vendor 为空时按名推断，不落「其他」。
	// 676: 默认厂商识别扩展——family id / 名称前缀双表兜底。
	cases := []struct {
		name, family, want string
	}{
		{"qwen3.8-27b", "qwen3.8", "Alibaba"},
		{"qwen3.8-max", "", "Alibaba"},
		{"qwen2.5-vl-72b-instruct", "qwen2.5", "Alibaba"},
		{"qwen3.7-flash", "qwen3.7", "Alibaba"},
		{"qwq-32b", "qwq", "Alibaba"},
		{"glm5.2-ultraspeed", "glm5.2", "Zhipu AI"},
		{"glm-4.7", "", "Zhipu AI"},
		{"hunyuan-a13b-instruct", "hunyuan", "Tencent"},
		{"ernie-3.5-8k", "ernie", "Baidu"},
		{"seed-1.6", "seed", "ByteDance"},
		{"longcat-2.0", "longcat", "Meituan"},
		{"spark-3.5", "spark", "iFlytek"},
		{"pangu-chat", "pangu", "Huawei"},
		{"ling-3.0-flash", "ling", "InclusionAI"},
		{"bge-m3", "bge", "BAAI"},
		{"dots-3-note-preview:free", "dots", "rednote"},
		{"abab5.5-chat", "abab5.5", "MiniMax"},
		{"grok-1", "xai-grok", "xAI"},
		{"grok-5", "xai", "xAI"},
		{"mimo-v2.5-pro", "xiaomi-mimo", "小米"},
		{"nvidia-nemotron-nano-9b-v2", "nvidia-nemotron", "NVIDIA"},
		{"nemotron-3-content-safety", "nvidia-nemotron", "NVIDIA"},
		{"nemoretriever-parse", "nemoretriever", "NVIDIA"},
		{"nv-embed-v1", "nv", "NVIDIA"},
		{"starcoder2-15b", "starcoder2", "BigCode"},
		{"bloom-176b", "bigscience-bloom", "BigScience"},
		{"codegemma-1.1-7b", "codegemma", "Google"},
		{"palm-2", "google-palm", "Google"},
		{"sonar-deep-research", "perplexity-sonar", "Perplexity"},
		{"tts-1", "openai-audio", "OpenAI"},
		{"dall-e-3", "openai-image", "OpenAI"},
		{"codex-auto-review", "codex", "OpenAI"},
		{"sora-2", "sora", "OpenAI"},
		{"claude-sonnet-3.5", "unknown", "Anthropic"},
		{"ui-tars-1.5-7b", "ui", "ByteDance"},
		{"devstral-2512", "devstral", "Mistral AI"},
		{"voxtral-small-24b-2507", "voxtral", "Mistral AI"},
		{"wizardlm-2-8x22b", "wizardlm", "Microsoft"},
		{"granite-3.0-3b-a800m-instruct", "granite", "IBM"},
		{"jamba-1.5-large-instruct", "jamba", "AI21"},
		{"falcon-180b", "falcon", "TII"},
		{"dbrx-instruct", "dbrx", "Databricks"},
		{"olmo-3-32b-think", "olmo", "AI2"},
		{"solar-10.7b-instruct", "solar", "Upstage"},
		{"hermes-3-llama-3.1-405b", "hermes", "Nous Research"},
		{"palmyra-creative-122b", "palmyra", "Writer"},
		{"mercury-2", "mercury", "Inception"},
		{"lfm-2.5-2.6b:free", "lfm", "Liquid AI"},
		{"fuyu-8b", "fuyu", "Adept"},
		{"arctic-embed-l", "arctic", "Snowflake"},
		{"stable-diffusion-3", "stability", "Stability AI"},
		{"hyperclova-x", "naver-hyperclova", "Naver"},
		{"sea-lion-7b-instruct", "sea", "AI Singapore"},
		{"zamba2-7b-instruct", "zamba2", "Zyphra"},
		{"sakana-namazu", "sakana", "Sakana AI"},
		{"sarvam-m", "sarvam", "Sarvam AI"},
		{"stockmark-2-100b-instruct", "stockmark", "Stockmark"},
		{"reka-edge", "reka", "Reka"},
		{"rinna-3.6b", "rinna", "rinna"},
		{"youdao-translate", "youdao", "Youdao"},
		{"l3.1-euryale-70b", "l3.1", "Community"},
		{"mythomax-l2-13b", "mythomax", "Community"},
		{"dolphin-mistral-24b-venice-edition", "dolphin", "Community"},
		// 顺序敏感：palmyra 必须先于 palm 命中，否则被并成 Google。
		{"palmyra-creative-122b", "", "Writer"},
		// 明确无法归属的保持空串（由调用方决定落「其他」）。
		{"totally-unknown-model", "unclassified", ""},
		{"fugu-ultra", "fugu", ""},
		{"nova-2-lite-v1", "nova", ""},
	}
	for _, tc := range cases {
		if got := InferVendor(tc.name, tc.family); got != tc.want {
			t.Errorf("InferVendor(%q,%q)=%q want %q", tc.name, tc.family, got, tc.want)
		}
	}
}

func TestEffectiveModality_minimaxM3(t *testing.T) {
	if got := EffectiveModality("minimax-m3", "text"); got != "multimodal" {
		t.Fatalf("got %q want multimodal", got)
	}
}
