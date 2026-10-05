package catalog

import "testing"

// 本文件钉住 EffectiveModality 的出处闸门。
//
// ★ 为什么这是承重的（而不是「多写一个函数而已」）：
//
// 迁移 825 的注释把病灶写在明处 —— 按名字推断模态，与语义核实，对
// models_canonical.modality 这一列提出**互斥**的要求：核实判负要把模型
// 降级成 text，而按名推断看到 stored='text' + 名字像多模态，就要把它推回去。
// 写侧（discovery/discovery.go 的 upsert、canonical_match.go 的重连判定）
// 因此加了 `modality_source NOT IN ('semantic','manual')` 这道闸门。
//
// 读侧一直没跟上：本函数原先只有 (name, stored) 两个入参，**结构上就拿不到
// source**，于是走的是下面这四行纯按名推断。后果有两处，都是「库里和页面上
// 不是同一个值」：
//
//   - admin 列表接口 GET /api/models
//   - 租户目录 maas（EffectiveModality 的 doc 原文就是 "shown to tenants"）
//
// 其中最刺眼的一条是**人工覆盖不可见**：PATCH /api/models/:id/modality 盖了
// modality_source='manual' 章，但凡 stored 不是 multimodal/vision/audio/
// embedding，一律被按名推断翻回去 —— 运维明明把某模型设成了 text，列表里
// 仍然是 multimodal。
//
// 真库当前 960 行 modality_source **全是 'inferred'**，所以这条缺陷今天还看
// 不出来；它在核实 worker 第一次写出 semantic 的那一刻变成活的。这也是为什么
// 它必须在这一轮修：修接线的那次改动正好就是让它变活的那次。
func TestEffectiveModality_annotatedSourceWins(t *testing.T) {
	cases := []struct {
		name         string
		canon        string
		stored       string
		source       string
		want         string
		whyItMatters string
	}{
		{
			// 承重的那一条：名字说多模态，核实说只是 text。核实赢。
			name:  "semantic downgrade survives a multimodal-looking name",
			canon: "minimax-m3", stored: "text", source: ModalitySourceSemantic,
			want: "text",
		},
		{
			// 同上，但出处是人工覆盖。这两条必须分开测：
			// 只测 semantic 的话，把 manual 从闸门里删掉判据不会红。
			name:  "manual override to text survives a multimodal-looking name",
			canon: "minimax-m3", stored: "text", source: ModalitySourceManual,
			want: "text",
		},
		{
			name:  "semantic upgrade is not walked back by name inference",
			canon: "qwen3-32b", stored: "vision", source: ModalitySourceSemantic,
			want: "vision",
		},
		{
			// 空格与大小写：DB 里是 enum-ish 文本，COALESCE 之后仍可能有
			// 大小写/空白差异，闸门不能因此失效（失效即静默退回按名推断）。
			name:  "source match tolerates case and padding",
			canon: "minimax-m3", stored: "text", source: "  SeMaNtIc  ",
			want: "text",
		},
		{
			name:  "annotated empty stored falls back to text, never to name inference",
			canon: "minimax-m3", stored: "", source: ModalitySourceManual,
			want: "text",
		},
	}
	for _, tc := range cases {
		if got := EffectiveModality(tc.canon, tc.stored, tc.source); got != tc.want {
			t.Errorf("%s: EffectiveModality(%q,%q,%q)=%q want %q",
				tc.name, tc.canon, tc.stored, tc.source, got, tc.want)
		}
	}
}

// TestEffectiveModality_unannotatedKeepsNameInference 钉住**没有被标注**的那条
// 行为没被顺手改掉：inferred / 空出处的行仍然按名字推断。
//
// 这条是防止「修复过度」的那一半。只测上面的闸门的话，把闸门写成
// 「一律返回 stored」也能全绿 —— 那会把 960 行 inferred 里所有
// stored='text' 的多模态模型一次性打成 text，是比原缺陷严重得多的回归。
func TestEffectiveModality_unannotatedKeepsNameInference(t *testing.T) {
	cases := []struct {
		name   string
		canon  string
		stored string
		source string
		want   string
	}{
		{"inferred source still infers", "minimax-m3", "text", ModalitySourceInferred, "multimodal"},
		{"empty source still infers", "minimax-m3", "text", "", "multimodal"},
		{"unknown source string still infers", "minimax-m3", "text", "whatever", "multimodal"},
		{"vision name inference for inferred rows", "claude-3-5-sonnet", "text", ModalitySourceInferred, "multimodal"},
		{"embedding is not overridden", "bge-m3", "text", ModalitySourceInferred, "embedding"},
	}
	for _, tc := range cases {
		if got := EffectiveModality(tc.canon, tc.stored, tc.source); got != tc.want {
			t.Errorf("%s: EffectiveModality(%q,%q,%q)=%q want %q",
				tc.name, tc.canon, tc.stored, tc.source, got, tc.want)
		}
	}
}

// TestEffectiveModality_videoIsNotOverridden 钉住顺带修掉的第二个洞：
// 'video' 曾经不在 stored 的早退名单里，于是 stored='video' 的模型会掉进按名
// 推断分支 —— 名字里带 gemini-/claude- 的就被报成 multimodal，video 丢失。
//
// 这条与出处无关（inferred 行也一样中招），所以单独成例：它承重的是
// 早退名单，不是出处闸门。
func TestEffectiveModality_videoIsNotOverridden(t *testing.T) {
	if got := EffectiveModality("gemini-3-pro", "video", ModalitySourceInferred); got != "video" {
		t.Errorf("stored video overridden by name inference: got %q want video", got)
	}
}

// TestModalityIsAnnotated_只认那两个出处 钉住闸门的白名单本身。
//
// 单独测是因为它是一条独立的判据：把 'inferred' 或任意字符串加进
// modalityIsAnnotated 的白名单，会让上面两组判据同时变红 —— 但根因在
// 这里，读失败信息时不必翻到 EffectiveModality。
func TestModalityIsAnnotated_onlySemanticAndManual(t *testing.T) {
	for _, s := range []string{ModalitySourceSemantic, ModalitySourceManual, " Semantic ", "MANUAL"} {
		if !modalityIsAnnotated(s) {
			t.Errorf("modalityIsAnnotated(%q)=false, want true", s)
		}
	}
	for _, s := range []string{"", ModalitySourceInferred, "unknown", "guess", "semantic_v2"} {
		if modalityIsAnnotated(s) {
			t.Errorf("modalityIsAnnotated(%q)=true, want false", s)
		}
	}
}
